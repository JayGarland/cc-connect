package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- stubs -------------------------------------------------------------

// startCountingAgent counts StartSession calls so a test can assert that a
// relay started no agent process of its own.
type startCountingAgent struct {
	stubAgent
	mu      sync.Mutex
	starts  int
	session AgentSession
}

func (a *startCountingAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
	if a.session != nil {
		return a.session, nil
	}
	return &stubAgentSession{}, nil
}

func (a *startCountingAgent) StartCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.starts
}

func (a *startCountingAgent) Name() string { return "counting" }

// closeTrackingSession records Close calls so a test can assert that a relay
// did not tear down the interactive conversation's live process.
type closeTrackingSession struct {
	*resultAgentSession
	mu     sync.Mutex
	closes int
}

func newCloseTrackingSession(result string) *closeTrackingSession {
	return &closeTrackingSession{resultAgentSession: newResultAgentSession(result)}
}

func (s *closeTrackingSession) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return nil
}

func (s *closeTrackingSession) CloseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// topicRelayFixture is a workspace-pattern seat with one existing topic
// conversation, matching the production shape: a per-workspace SessionManager
// holding the topic's session key, and a live interactive state serving it.
type topicRelayFixture struct {
	engine         *Engine
	workspace      string
	topicKey       string
	interactiveKey string
	wsAgent        *startCountingAgent
	liveSession    *closeTrackingSession
	wsSessions     *SessionManager
}

func newTopicRelayFixture(t *testing.T, chatID, threadID, userID, reply string) *topicRelayFixture {
	t.Helper()
	root := t.TempDir()
	pattern := filepath.Join(root, "task-{{THREAD_ID}}")
	workspace := strings.ReplaceAll(pattern, "{{THREAD_ID}}", threadID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	globalAgent := &startCountingAgent{}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("dev-pro", globalAgent, []Platform{p}, "", LangEnglish)
	e.SetWorkspacePattern(pattern)
	e.SetMultiWorkspace(root, filepath.Join(t.TempDir(), "bindings.json"))

	live := newCloseTrackingSession(reply)
	wsAgent := &startCountingAgent{session: live}
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.agent = wsAgent
	ws.sessions = NewSessionManager("")

	// The topic conversation already exists — this is what a relay must join.
	topicKey := "telegram:" + chatID + ":" + threadID + ":" + userID
	session := ws.sessions.GetOrCreateActive(topicKey)

	interactiveKey := workspace + ":" + topicKey
	e.interactiveMu.Lock()
	e.interactiveStates[interactiveKey] = &interactiveState{
		platform:        p,
		replyCtx:        topicKey,
		agent:           wsAgent,
		agentSession:    live,
		servedSessionID: session.ID,
		workspaceDir:    workspace,
	}
	e.interactiveMu.Unlock()

	return &topicRelayFixture{
		engine:         e,
		workspace:      workspace,
		topicKey:       topicKey,
		interactiveKey: interactiveKey,
		wsAgent:        wsAgent,
		liveSession:    live,
		wsSessions:     ws.sessions,
	}
}

// --- G2 ---------------------------------------------------------------

// G2 (class gate; spec from L-0714 Gate Deposited) — no second process.
//
// What it refuses: a relay that, when the target topic already has a live
// interactive agent session, starts an agent process of its own or closes the
// interactive one. That is the pre-L-0718 shape — HandleRelay called
// agent.StartSession() with the session's agent id and agentSession.Close() on
// the way out — and it is what forks a resumed CLI session in two and drops the
// conversation the relay was supposed to continue.
//
// Coverage declaration (L-0697): scan surface = every agent-lifecycle call made
// on the relay delivery path, observed at the only place they can occur — the
// Agent and AgentSession interfaces, counted by the stubs above; the relay is
// driven end-to-end through the exported HandleRelay rather than through an
// internal helper, so no intermediate layer is bypassed.
// Exclusions: NONE. The fallback branch (no topic conversation, independent
// relay session) is not excluded from the gate — it is asserted by the sibling
// test below, which requires that it DOES start a process. Both branches carry
// an assertion; neither is left unmeasured.
func TestHandleRelay_TopicTurnStartsNoSecondProcess(t *testing.T) {
	f := newTopicRelayFixture(t, "-1003917051393", "855", "7664413698", "continued in topic")

	startsBefore := f.wsAgent.StartCount()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := f.engine.HandleRelay(ctx, "secretary-seat", f.topicKey, "please continue")
	if err != nil {
		t.Fatalf("HandleRelay() error = %v", err)
	}
	if resp != "continued in topic" {
		t.Fatalf("HandleRelay() response = %q, want %q", resp, "continued in topic")
	}

	if got := f.wsAgent.StartCount() - startsBefore; got != 0 {
		t.Fatalf("relay started %d agent process(es); want 0 — the topic's live session must be reused, not forked", got)
	}
	if got := f.liveSession.CloseCount(); got != 0 {
		t.Fatalf("relay closed the interactive agent session %d time(s); want 0 — the relay does not own that process", got)
	}
}

// G2, other branch: with no existing topic conversation the relay falls back to
// its own dedicated session, and there it MUST start a process. Asserting this
// keeps the gate from being satisfiable by a relay that simply never runs.
func TestHandleRelay_FallbackBranchStillStartsItsOwnProcess(t *testing.T) {
	root := t.TempDir()
	pattern := filepath.Join(root, "task-{{THREAD_ID}}")
	threadID := "900"
	workspace := strings.ReplaceAll(pattern, "{{THREAD_ID}}", threadID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("dev-pro", &startCountingAgent{}, []Platform{p}, "", LangEnglish)
	e.SetWorkspacePattern(pattern)
	e.SetMultiWorkspace(root, filepath.Join(t.TempDir(), "bindings.json"))

	wsAgent := &startCountingAgent{session: newResultAgentSession("fresh relay")}
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.agent = wsAgent
	ws.sessions = NewSessionManager("") // no topic conversation on record

	sourceKey := "telegram:-1003917051393:" + threadID + ":7664413698"
	resp, err := e.HandleRelay(context.Background(), "secretary-seat", sourceKey, "hello")
	if err != nil {
		t.Fatalf("HandleRelay() error = %v", err)
	}
	if resp != "fresh relay" {
		t.Fatalf("HandleRelay() response = %q, want %q", resp, "fresh relay")
	}
	if got := wsAgent.StartCount(); got != 1 {
		t.Fatalf("fallback relay started %d process(es); want exactly 1", got)
	}
	relayKey := relayConversationKey("secretary-seat", "telegram", "-1003917051393:"+threadID)
	if ws.sessions.ActiveSessionID(relayKey) == "" {
		t.Fatal("fallback relay did not record its own dedicated session")
	}
}

// --- G3' --------------------------------------------------------------

// G3' (class gate) — relay resolution discovers, never fabricates.
//
// The gate specified as G3 in L-0714 guards a `--topic` flag; cross-topic
// addressing is not in this letter, so that flag has no surface here. The same
// defect class within THIS letter's surface is the one gated instead: relay
// target resolution must never bring a persistent binding into existence. If it
// could, a relay into a topic that has no conversation would mint a session key
// (and with it a workspace binding) out of a guess — the L-0587 shard-hopping
// failure arriving through a new door.
//
// What it refuses: a resolver that creates a session, a session key, or a
// topic→letter binding when the topic has none.
//
// Coverage declaration (L-0697): scan surface = resolveRelayTopicTarget, over
// the three "nothing on record" shapes it can meet — empty manager, populated
// manager with no matching topic, and a source key with no thread at all. State
// is compared before and after by value (session-key set and topic→letter
// binding store), not by inspecting the returned error, because "returned an
// error but bound something anyway" is precisely the failure this must catch.
// Exclusions: NONE of the resolver's outcomes are skipped; the ambiguity
// outcome is asserted by TestResolveRelayTopicTarget_AmbiguousTopicFailsClosed.
func TestResolveRelayTopicTarget_NeverFabricatesABinding(t *testing.T) {
	cases := []struct {
		name     string
		seed     []string
		sourceID string
	}{
		{"empty_manager", nil, "telegram:-100999:855:7664413698"},
		{"no_matching_topic", []string{"telegram:-100999:111:7664413698"}, "telegram:-100999:855:7664413698"},
		{"source_has_no_thread", []string{"telegram:-100999:855:7664413698"}, "telegram:-100999:7664413698"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "task-855")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			e := NewEngine("dev-pro", &startCountingAgent{}, []Platform{&stubPlatformEngine{n: "telegram"}}, "", LangEnglish)
			e.SetDataDir(root)
			e.SetWorkspacePattern(filepath.Join(root, "task-{{THREAD_ID}}"))

			sessions := NewSessionManager("")
			for _, k := range tc.seed {
				sessions.GetOrCreateActive(k)
			}

			keysBefore := sessions.UserKeysWithPrefix("")
			bindingsBefore := topicLetterBindingSnapshot(t, root)

			target, err := e.resolveRelayTopicTarget(tc.sourceID, workspace, sessions)
			if err == nil {
				t.Fatalf("resolveRelayTopicTarget() unexpectedly resolved to %+v; nothing was on record", target)
			}

			// The assertion that matters is on the state, not the error: an
			// error return with a side effect is the defect.
			if got := sessions.UserKeysWithPrefix(""); !equalStringSlices(keysBefore, got) {
				t.Fatalf("resolution changed the session-key set: before=%v after=%v", keysBefore, got)
			}
			if got := topicLetterBindingSnapshot(t, root); got != bindingsBefore {
				t.Fatalf("resolution mutated the topic→letter binding store: before=%q after=%q", bindingsBefore, got)
			}
		})
	}
}

// The ambiguity outcome: several people hold their own session in one topic.
// Picking one would attach the relay to a guess permanently, so resolution must
// refuse and name the candidates.
func TestResolveRelayTopicTarget_AmbiguousTopicFailsClosed(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "task-855")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	e := NewEngine("dev-pro", &startCountingAgent{}, []Platform{&stubPlatformEngine{n: "telegram"}}, "", LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "task-{{THREAD_ID}}"))

	sessions := NewSessionManager("")
	sessions.GetOrCreateActive("telegram:-100999:855:1111")
	sessions.GetOrCreateActive("telegram:-100999:855:2222")

	target, err := e.resolveRelayTopicTarget("telegram:-100999:855:1111", workspace, sessions)
	if err == nil {
		t.Fatalf("ambiguous topic resolved to %+v; want a refusal", target)
	}
	for _, want := range []string{"1111", "2222", "candidate sessions"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not name the candidates (%q missing): %v", want, err)
		}
	}
}

// --- G4 ---------------------------------------------------------------

// G4 (instance gate) — the 2026-07-31 15:08 incident.
//
// secretary-seat relayed into dev-pro's L-0710 topic. The workspace was already
// correct; the relay still opened a second session inside it (agent 080a5850,
// history null) next to the real conversation (agent 339d34d3), and answered
// from the empty one. This pins the two observable facts a person can check on
// disk: the relay resolves to the SAME session key the topic already uses, and
// the workspace's session count does not grow.
func TestHandleRelay_L0710Incident_NoNewSessionInShard(t *testing.T) {
	f := newTopicRelayFixture(t, "-1003917051393", "710", "7664413698", "ack")

	before := len(f.wsSessions.UserKeysWithPrefix(""))

	target, err := f.engine.resolveRelayTopicTarget(f.topicKey, f.workspace, f.wsSessions)
	if err != nil {
		t.Fatalf("resolveRelayTopicTarget() error = %v", err)
	}
	if target.sessionKey != f.topicKey {
		t.Fatalf("relay resolved session key %q, want the topic's own key %q", target.sessionKey, f.topicKey)
	}
	if target.interactiveKey != f.interactiveKey {
		t.Fatalf("relay resolved interactive key %q, want %q", target.interactiveKey, f.interactiveKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.engine.HandleRelay(ctx, "secretary-seat", f.topicKey, "continue L-0710"); err != nil {
		t.Fatalf("HandleRelay() error = %v", err)
	}

	after := f.wsSessions.UserKeysWithPrefix("")
	if len(after) != before {
		t.Fatalf("workspace shard gained a session: before=%d after=%d (%v)", before, len(after), after)
	}
	for _, k := range after {
		if strings.HasPrefix(k, "relay:") {
			t.Fatalf("relay opened a dedicated session %q beside the topic conversation", k)
		}
	}
}

// --- helpers ----------------------------------------------------------

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// topicLetterBindingSnapshot reads the topic→letter binding ledger straight off
// disk so a test compares the durable artifact byte-for-byte, not an in-memory
// view that a buggy resolver could leave looking clean.
func topicLetterBindingSnapshot(t *testing.T, dataDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "topic_letter_bindings.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read topic_letter_bindings.json: %v", err)
	}
	return string(data)
}

// --- turn-start failure ------------------------------------------------

// failingStartAgent never yields a usable session.
type failingStartAgent struct {
	stubAgent
	mu     sync.Mutex
	starts int
}

func (a *failingStartAgent) Name() string { return "failing" }

func (a *failingStartAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
	return nil, errors.New("agent unavailable")
}

// A relay whose turn cannot start must be told so, not left to time out.
//
// The turn-start paths return before the event loop that normally hands back a
// result, so without an explicit release the caller would sit on the relay
// deadline — 600s in the running fleet (config.toml [relay] timeout_secs) —
// waiting for a turn that never ran.
func TestSubmitTopicTurn_TurnThatCannotStartReleasesTheCaller(t *testing.T) {
	f := newTopicRelayFixture(t, "-1003917051393", "861", "7664413698", "unused")

	// Replace the live conversation with one whose agent cannot start.
	f.engine.interactiveMu.Lock()
	delete(f.engine.interactiveStates, f.interactiveKey)
	f.engine.interactiveMu.Unlock()
	ws := f.engine.workspacePool.GetOrCreate(f.workspace)
	ws.agent = &failingStartAgent{}

	target, err := f.engine.resolveRelayTopicTarget(f.topicKey, f.workspace, f.wsSessions)
	if err != nil {
		t.Fatalf("resolveRelayTopicTarget() error = %v", err)
	}

	respCh, err := f.engine.SubmitTopicTurn(target, "secretary-seat", "are you there?")
	if err != nil {
		return // refused up front — the caller was released synchronously
	}

	select {
	case res := <-respCh:
		if res.Err == nil {
			t.Fatalf("turn could not start yet reported success: %q", res.Response)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("caller was never released; a relay whose turn cannot start would block until its relay deadline")
	}
}
