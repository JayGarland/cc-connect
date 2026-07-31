package core

import (
	"os"
	"path/filepath"
	"testing"
)

// G1 (L-0716; spec from L-0714 Gate Deposited) — topic-resolution peer gate.
//
// The relay path and the interactive path must resolve the SAME *SessionManager
// for the same (seat config, threadID). The table is the cartesian product of
// {workspace_pattern set/unset} × {dispatch_topic_isolation true/false} — rows
// are produced by the product, not hand-enumerated, so a future seat-flag
// combination enters the table automatically.
//
// No combination is excluded: the both-flags-off row stays in the table and
// asserts BOTH paths intentionally fall back to the global manager (L-0714
// component 3 fallback). Deleting that row would silently narrow the gate.
//
// Coverage declaration (L-0697): scan surface = relayContextForSourceSessionKey
// and commandContextWithWorkspace, the two functions under test, invoked
// directly. Exclusions: none.
func TestRelayTopicResolution_MatchesInteractiveGate(t *testing.T) {
	cases := []struct {
		name string
		wp   bool
		di   bool
	}{
		{"workspace_pattern_and_dispatch_isolation", true, true},
		{"workspace_pattern_only", true, false},
		{"dispatch_isolation_only", false, true}, // red before the engine.go:17298 gate fix (L-0714 O2, nine seats)
		{"neither_flag", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			threadID := "855"
			sourceSessionKey := "telegram:-1003917051393:" + threadID + ":7664413698"

			workDir := filepath.Join(root, "workdir")
			agent := &dummyAgentWithWorkDir{stubAgent: stubAgent{}, workDir: workDir}
			p := &stubPlatformEngine{n: "telegram"}
			e := NewEngine("test", agent, []Platform{p}, filepath.Join(root, "sessions.json"), LangEnglish)
			e.SetDataDir(root)

			if tc.wp {
				if err := os.MkdirAll(filepath.Join(root, "task-"+threadID), 0o755); err != nil {
					t.Fatal(err)
				}
				e.SetWorkspacePattern(filepath.Join(root, "task-{{THREAD_ID}}"))
			}
			if tc.di {
				e.SetDispatchTopicIsolation(true)
			}

			// Register the agent factory so getOrCreateWorkspaceAgent can lazily
			// create the per-workspace agent (mirrors
			// TestWorkspacePatternRouting_DispatchTopicIsolation).
			const regName = "g1-topic-gate-agent"
			agent.name = regName
			RegisterAgent(regName, func(opts map[string]any) (Agent, error) { return agent, nil })

			relayAgent, relaySessions, _, err := e.relayContextForSourceSessionKey("dev-pro", sourceSessionKey)
			if err != nil {
				t.Fatalf("relayContextForSourceSessionKey() error = %v", err)
			}
			_ = relayAgent

			msg := &Message{
				Platform:   "telegram",
				SessionKey: sourceSessionKey,
				ChannelKey: "-1003917051393:" + threadID,
			}
			_, interactiveSessions, interactiveKey, _, err := e.commandContextWithWorkspace(p, msg)
			if err != nil {
				t.Fatalf("commandContextWithWorkspace() error = %v", err)
			}

			if relaySessions != interactiveSessions {
				t.Fatalf("relay (%p) and interactive (%p) resolved different SessionManager for the same seat config + threadID; gate must be aligned (interactiveKey=%q)",
					relaySessions, interactiveSessions, interactiveKey)
			}

			if tc.wp || tc.di {
				if relaySessions == e.sessions {
					t.Fatalf("relay resolved the GLOBAL manager for a topic-resolving seat; want the workspace sessions (L-0714 O2 nine-seat gap)")
				}
			} else {
				if relaySessions != e.sessions {
					t.Fatalf("relay resolved a non-global manager for a no-flag seat; want the global fallback (L-0714 component 3)")
				}
			}
		})
	}
}
