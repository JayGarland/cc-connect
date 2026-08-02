package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Bot-to-bot relay, delivered as a turn on the topic's own conversation.
//
// A relay used to be a second, parallel delivery path into a seat: it started
// its own agent process against its own `relay:<from>:<chat>` session key and
// closed it when the answer came back. That meant a relay arriving in a topic
// could not see anything the topic had already worked out — it cold-started
// beside the real conversation, in the same workspace, and the two never met.
//
// Here a relay instead submits a turn to the conversation that already exists
// for that topic, and waits for that turn's text. The relay owns no process.
// The machinery it rides on — the pending-message queue, the permission
// handling, the staleness watermark, the drain loop — is the same machinery a
// human message uses; the only thing added for the relay is a per-turn response
// sink (see deliverTurnResponse).
//
// Two consequences are deliberate. A relay that arrives mid-turn is queued
// behind that turn rather than executed beside it, because a seat should finish
// what it is doing and read its messages in order. And because delivery is now
// an ordinary turn, both the relay's prompt and the seat's answer appear in the
// Telegram topic instead of being visible only to `/sessionboard`.

// topicTurnResult is one completed turn handed back to a synchronous caller.
type topicTurnResult struct {
	Response string
	Err      error
}

var (
	// errNoTopicSession means this relay has no existing topic conversation to
	// continue. It is not a failure: the caller falls back to the independent
	// relay session, which is still the right shape for a seat that is not
	// topic-scoped at all, or for a DM where no topic exists.
	errNoTopicSession = errors.New("relay: no existing topic session")

	// errTurnEndedWithoutResult reports a turn that stopped without producing a
	// final result — agent error, event channel closed, shutdown.
	errTurnEndedWithoutResult = errors.New("turn ended without a result")

	// errTurnDroppedAsStale reports a queued turn discarded by the drain loop's
	// staleness filter before it ever ran.
	errTurnDroppedAsStale = errors.New("queued turn dropped as stale")
)

// relayTopicTarget is a resolved topic conversation a relay can be delivered to.
type relayTopicTarget struct {
	sessions *SessionManager
	// sessionKey is the topic's own key, e.g. "telegram:-100…:855:7664413698".
	// It is discovered, never constructed — see resolveRelayTopicTarget.
	sessionKey string
	// interactiveKey is how the engine indexes the live turn state for that
	// conversation. It is not assembled here: it comes from the same resolver
	// the human-message path uses, because a relay that keys the state
	// differently starts a second agent process beside the conversation it was
	// supposed to join.
	interactiveKey string
	workspace      string
	platform       Platform
}

// resolveRelayTopicTarget finds the conversation a relay should continue.
//
// The persistent binding it returns is derived only from stable keys: the
// source channel/thread id, the dispatch ledger (via resolveWorkspacePattern),
// and session keys already on record. Nothing here reads the relay's message
// text — deriving a durable identity from message content is the defect class
// that produced the L-0587 shard-hopping bug, and this is exactly the kind of
// resolution path where it would reappear.
//
// sessions and workspace come from the caller's already-completed relay context
// resolution and are used only to decide whether this relay is workspace-scoped
// at all. Once the topic's own session key is discovered, the target is resolved
// through resolveMessageWorkspaceContext — the same single decider a human
// message in that topic goes through — so the relay joins that conversation
// instead of running beside it under a differently-shaped key.
func (e *Engine) resolveRelayTopicTarget(sourceSessionKey, workspace string, sessions *SessionManager) (*relayTopicTarget, error) {
	if sessions == nil || workspace == "" {
		return nil, errNoTopicSession
	}
	platformName, chatID, err := parseSessionKeyParts(sourceSessionKey)
	if err != nil {
		return nil, errNoTopicSession
	}
	threadID := extractThreadIDFromSessionKey(sourceSessionKey)
	if threadID == "" {
		// No forum topic (DM, General) — there is no topic conversation to join.
		return nil, errNoTopicSession
	}

	// A topic session key is "<platform>:<chatID>:<threadID>:<userID>". The
	// user id belongs to whoever spoke in the topic and is unknown here, so the
	// key is matched by prefix rather than assembled.
	prefix := platformName + ":" + normalizeChatID(chatID) + ":" + threadID + ":"
	candidates := sessions.UserKeysWithPrefix(prefix)

	switch len(candidates) {
	case 0:
		return nil, errNoTopicSession
	case 1:
	default:
		// Several people have their own session in this one topic. Picking any
		// of them would be a guess, and a guess here silently attaches the
		// relay to one person's conversation for good. Refuse and say who.
		return nil, fmt.Errorf(
			"relay: topic %s has %d candidate sessions (%s); cannot choose one — "+
				"address the target explicitly or set share_session_in_channel for this platform",
			threadID, len(candidates), strings.Join(candidates, ", "))
	}

	// Resolve the topic exactly as an incoming message to it would be resolved.
	// The synthetic Message carries only stable keys — the discovered session key
	// and the channel key the platform itself builds for that chat+thread
	// ("<chatID>:<threadID>", telegram.go buildChannelKey) — never the relay
	// text. Assembling the interactive key here instead ("<workspace>:<key>")
	// is what made a relay to an isolation-only seat address
	// "L-855:telegram:…" while the seat's own live state sits under
	// "<work dir>:telegram:…".
	p := e.platformForName(platformName)
	probe := &Message{
		Platform:   platformName,
		SessionKey: candidates[0],
		ChannelKey: normalizeChatID(chatID) + ":" + threadID,
	}
	wsCtx, err := e.resolveMessageWorkspaceContext(p, probe)
	if err != nil {
		return nil, fmt.Errorf("relay: resolve topic %s context: %w", threadID, err)
	}
	if wsCtx.needsInit || wsCtx.workspace == "" {
		// The topic resolves to no workspace of its own; there is nothing for a
		// relay to join. Fall back to the independent relay session.
		return nil, errNoTopicSession
	}
	if wsCtx.sessions != sessions {
		// The candidate was discovered in the relay context's SessionManager. If
		// the message-path resolver lands somewhere else, that manager may not
		// know this session at all and GetOrCreateActive would cold-start a new
		// one under an existing conversation's key. Refuse and take the
		// independent relay session instead of guessing.
		slog.Warn("relay: topic resolves to a different SessionManager than the relay context; using the independent relay session",
			"project", e.name, "topic", threadID, "relay_workspace", workspace, "message_workspace", wsCtx.workspace)
		return nil, errNoTopicSession
	}

	return &relayTopicTarget{
		sessions:       wsCtx.sessions,
		sessionKey:     candidates[0],
		interactiveKey: wsCtx.interactiveKey,
		workspace:      wsCtx.workspace,
		platform:       p,
	}, nil
}

// SubmitTopicTurn delivers message as a turn on target's conversation and
// returns the channel that turn's result will arrive on.
//
// It does not start an agent process of its own. When the conversation is busy
// the turn is queued behind the running one; when it is idle the turn runs
// through the same processInteractiveMessageWith path a human message takes,
// which is what resumes the existing agent session rather than cold-starting a
// new one.
func (e *Engine) SubmitTopicTurn(target *relayTopicTarget, fromProject, message string) (<-chan topicTurnResult, error) {
	if target == nil {
		return nil, errNoTopicSession
	}

	respCh := make(chan topicTurnResult, 1)
	session := target.sessions.GetOrCreateActive(target.sessionKey)

	replyCtx := any(target.sessionKey)
	if rc, ok := target.platform.(ReplyContextReconstructor); ok {
		if reconstructed, rerr := rc.ReconstructReplyCtx(target.sessionKey); rerr == nil {
			replyCtx = reconstructed
		} else {
			slog.Warn("relay topic turn: failed to reconstruct reply ctx; using session key fallback",
				"project", e.name,
				"session", target.sessionKey,
				"error", rerr,
			)
		}
	}

	msg := &Message{
		Platform:          target.platform.Name(),
		SessionKey:        target.sessionKey,
		Content:           message,
		UserID:            relaySyntheticUserID,
		UserName:          "cc-connect relay (" + fromProject + ")",
		ReplyCtx:          replyCtx,
		UserMessageTimeMs: time.Now().UnixMilli(),
	}

	// A state entry must exist before the busy check so a relay arriving during
	// agent startup is queued rather than dropped (the reason handleMessage
	// does the same before taking the lock).
	e.ensureInteractiveStateForQueueing(target.interactiveKey, target.platform, replyCtx)

	if e.queueSyntheticTurnForBusySession(target.platform, msg, target.interactiveKey, session, respCh) {
		slog.Info("relay: queued behind running turn",
			"project", e.name, "from", fromProject,
			"interactive_key", target.interactiveKey)
		// The drain loop may have finished between the busy check and the
		// append, leaving the queued turn with no one to run it. Same race, and
		// same guard, as the human-message path in handleMessage.
		if session.TryLock() {
			agent, _ := e.sessionContextForKey(target.sessionKey)
			go e.drainOrphanedQueue(session, target.sessions, target.interactiveKey, agent, target.workspace)
		}
		return respCh, nil
	}

	if !session.TryLock() {
		// Busy, but the queue would not take it (queue full, or the agent
		// process is gone). Report rather than silently drop.
		return nil, fmt.Errorf("relay: target session %q is busy and its queue is unavailable", target.sessionKey)
	}

	agent, sessions := e.sessionContextForKey(target.sessionKey)
	if sessions == nil {
		sessions = target.sessions
	}
	e.setTurnResponseSink(target.interactiveKey, respCh)
	session.TouchUserActivity()
	e.noteUserMessageAccepted(target.interactiveKey, msg.UserMessageTimeMs)

	slog.Info("relay: continuing topic session",
		"project", e.name, "from", fromProject,
		"interactive_key", target.interactiveKey,
		"session", session.ID,
		"agent_session", session.GetAgentSessionID())

	go e.processInteractiveMessageWith(target.platform, msg, session, agent, sessions,
		target.interactiveKey, target.workspace, target.sessionKey)

	return respCh, nil
}

// relaySyntheticUserID marks a turn as machine-originated. It matches the id
// InjectRelayHandback already uses so both synthetic paths look the same to
// anything downstream that inspects the sender.
const relaySyntheticUserID = "cc-connect-relay"

// interactiveStateFor returns the live turn state for interactiveKey, or nil.
// The state object can be replaced while a turn is being set up, so callers
// that need it at cleanup time must look it up again rather than hold on to a
// pointer captured earlier.
func (e *Engine) interactiveStateFor(interactiveKey string) *interactiveState {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	return e.interactiveStates[interactiveKey]
}

// setTurnResponseSink attaches respCh to the state that will run the next turn
// for interactiveKey. getOrCreateInteractiveStateWith may replace that state
// when it spawns the agent; adoptPendingFromPlaceholder carries the sink across
// the replacement, the same way it already carries queued messages.
func (e *Engine) setTurnResponseSink(interactiveKey string, respCh chan topicTurnResult) {
	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	state.turnRespCh = respCh
	state.mu.Unlock()
}

// relayViaTopicTurn is the synchronous half: submit the turn, wait for it under
// the relay deadline.
func (e *Engine) relayViaTopicTurn(ctx context.Context, target *relayTopicTarget, fromProject, message string) (string, error) {
	respCh, err := e.SubmitTopicTurn(target, fromProject, message)
	if err != nil {
		return "", err
	}

	select {
	case res := <-respCh:
		if res.Err != nil {
			return "", res.Err
		}
		slog.Info("relay: turn complete",
			"from", fromProject, "to", e.name,
			"response_len", len(res.Response),
			"interactive_key", target.interactiveKey)
		return res.Response, nil
	case <-ctx.Done():
		// The relay caller stops waiting; the turn does not stop running. It
		// belongs to the topic conversation, so it finishes, replies into the
		// topic, and stays resumable — which is why this path needs none of the
		// background draining an independently-owned relay process required.
		slog.Warn("relay: deadline reached; turn continues in its topic",
			"from", fromProject, "to", e.name,
			"interactive_key", target.interactiveKey,
			"error", ctx.Err())
		return "", ctx.Err()
	case <-e.ctx.Done():
		return "", e.ctx.Err()
	}
}
