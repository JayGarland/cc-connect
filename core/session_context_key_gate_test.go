package core

import (
	"os"
	"path/filepath"
	"testing"
)

// G2 — resolving from a bare session key must give the same answer as resolving
// from a Message.
//
// Card callbacks carry no Message, only a session key, so the card renderers
// (/list, /model, /delete, /dir, /history, /current, /ps, executeCardAction)
// resolve through sessionContextForKey while the typed command resolves through
// resolveMessageWorkspaceContext. When the two disagree, the same command shows
// one store when typed and another when clicked — and on a
// dispatch_topic_isolation seat they disagreed everywhere:
//
//	iso seat, DM/General/topic, no live state → sessionContextForKey = GLOBAL
//	iso seat, DM/General/topic, live state    → a manager keyed by the work-dir
//	                                            path, while the conversation
//	                                            lives in "general" / "L-<thread>"
//
// The cause was a hand-copied gate: sessionContextForKey required
// `workspacePattern != ""` AND a thread id, so isolation-only seats never
// entered the branch at all.
//
// Coverage declaration (L-0697): the table is the cartesian product of
//
//	seat    ∈ {pattern+iso, pattern only, iso only, binding only, single}
//	channel ∈ {private DM, group General, group letter topic}
//	live    ∈ {no interactive state, interactive state present}
//
// = 30 rows, none excluded. `live` is a dimension rather than a fixture detail
// because sessionContextForKey has a live-state recovery path that answers
// differently from the cold path, and the pre-fix code was wrong in both — one
// row shape alone would have proved too little. Seats that resolve no workspace
// (binding only, single) stay in and assert both paths agree on the global
// fallback.
//
// Known and deliberate non-coverage: a {{LETTER_ID}}-only pattern seat in a
// threadless channel, where the message path can derive a workspace from an
// L-XXXX in the message *text* and a bare session key has no text to read. That
// asymmetry is not repairable from a session key and is left to the live-state
// recovery path; it is not silently skipped here — the pattern-only rows below
// carry no letter mention, so both paths agree on "no topic workspace".
func TestSessionContextForKey_MatchesDecider(t *testing.T) {
	const (
		uid      = "7664413698"
		groupID  = "-1003917051393"
		threadID = "855"
	)
	channels := []struct {
		name       string
		sessionKey string
		channelKey string
	}{
		{"private_dm", "telegram:" + uid + ":" + uid, uid},
		{"group_general", "telegram:" + groupID + ":" + uid, groupID},
		{"group_letter_topic", "telegram:" + groupID + ":" + threadID + ":" + uid, groupID + ":" + threadID},
	}
	seats := []struct {
		name     string
		pattern  bool
		iso      bool
		multiCfg bool
	}{
		{"pattern_and_isolation", true, true, true},
		{"pattern_only", true, false, true},
		{"dispatch_isolation_only", false, true, true},
		{"binding_only", false, false, true},
		{"single_workspace", false, false, false},
	}

	for _, seat := range seats {
		for _, ch := range channels {
			for _, live := range []bool{false, true} {
				name := seat.name + "/" + ch.name
				if live {
					name += "/live_state"
				} else {
					name += "/cold"
				}
				t.Run(name, func(t *testing.T) {
					root := t.TempDir()
					workDir := filepath.Join(root, "workdir")
					if err := os.MkdirAll(workDir, 0o755); err != nil {
						t.Fatal(err)
					}
					agent := &dummyAgentWithWorkDir{stubAgent: stubAgent{}, workDir: workDir}
					const regName = "g2-session-context-agent"
					agent.name = regName
					RegisterAgent(regName, func(opts map[string]any) (Agent, error) { return agent, nil })

					p := &stubPlatformEngine{n: "telegram"}
					e := NewEngine("seat", agent, []Platform{p}, filepath.Join(root, "sessions.json"), LangEnglish)
					e.SetDataDir(root)
					// main.go turns multi-workspace on for any of the three flags.
					if seat.multiCfg {
						e.SetMultiWorkspace("", filepath.Join(root, "bindings.json"))
					}
					if seat.pattern {
						if err := os.MkdirAll(filepath.Join(root, "task-"+threadID), 0o755); err != nil {
							t.Fatal(err)
						}
						e.SetWorkspacePattern(filepath.Join(root, "task-{{THREAD_ID}}"))
					}
					if seat.iso {
						e.SetDispatchTopicIsolation(true)
					}

					wsCtx, err := e.resolveMessageWorkspaceContext(p, &Message{
						Platform:   "telegram",
						SessionKey: ch.sessionKey,
						ChannelKey: ch.channelKey,
					})
					if err != nil {
						t.Fatalf("resolveMessageWorkspaceContext() error = %v", err)
					}

					if live {
						// Filed exactly as handleMessage files it.
						e.interactiveMu.Lock()
						e.interactiveStates[wsCtx.interactiveKey] = &interactiveState{
							platform:     p,
							replyCtx:     ch.sessionKey,
							workspaceDir: wsCtx.workspace,
						}
						e.interactiveMu.Unlock()
					}

					_, gotSessions := e.sessionContextForKey(ch.sessionKey)
					if gotSessions != wsCtx.sessions {
						t.Fatalf("sessionContextForKey resolved store %q, message path resolved %q — a card callback would read a different store than the typed command",
							storeName(gotSessions), storeName(wsCtx.sessions))
					}
				})
			}
		}
	}
}

func storeName(sm *SessionManager) string {
	if sm == nil {
		return "<nil>"
	}
	if p := sm.StorePath(); p != "" {
		return filepath.Base(p)
	}
	return "<anonymous>"
}
