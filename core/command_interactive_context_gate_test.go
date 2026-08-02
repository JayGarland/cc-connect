package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// G1 — command path and interactive path must land on the same session.
//
// A slash command that manipulates the session (/new, /cancel, /stop) resolves
// its target through commandContextWithWorkspace; the message it is supposed to
// affect resolved through handleMessage. When those two disagree, /new "succeeds"
// against an empty SessionManager and an interactive key nothing is stored under,
// the live agent process is never torn down, and the next message resumes the old
// conversation (is_resume=true) — silently, with no error anywhere.
//
// The contract asserted here, for every seat configuration and every channel
// shape:
//
//   - when the seat resolves a workspace, the command path must NOT return the
//     global SessionManager, and
//   - the interactive key must be "<effective work dir>:<sessionKey>" — the same
//     path-prefixed form handleMessage files live state under (and the same form
//     interactiveKeyForSessionKey / sendWorkDirForSession use). The pool key
//     ("general", "L-855") is NOT a valid prefix: it is what the command path
//     used to return, and it matches nothing in e.interactiveStates.
//
// Red proof (pre-fix): every threadless row of the dispatch_isolation_only and
// pattern_and_isolation seats fails — commandContextWithWorkspace returned the
// global manager and a bare sessionKey, because it only consulted
// resolveWorkspacePattern when the channel had a Telegram thread. The threaded
// rows of those seats fail on the prefix — "L-855:" instead of the work dir.
//
// Coverage declaration (L-0697): the table is the cartesian product of
// {workspace_pattern set/unset} × {dispatch_topic_isolation on/off} ×
// {private DM, group General topic, group letter topic}. No row is excluded;
// the neither-flag rows stay in and assert the intentional global fallback, and
// the pattern-seat threadless rows assert the init-flow fallback. Rows are
// produced by the product, so a new flag combination cannot quietly skip.
func TestCommandContext_MatchesInteractiveSessionTarget(t *testing.T) {
	const (
		uid      = "7664413698"
		groupID  = "-1003917051393"
		threadID = "855"
	)

	channels := []struct {
		name       string
		sessionKey string
		channelKey string
		threaded   bool
	}{
		{"private_dm", "telegram:" + uid + ":" + uid, uid, false},
		{"group_general_topic", "telegram:" + groupID + ":" + uid, groupID, false},
		{"group_letter_topic", "telegram:" + groupID + ":" + threadID + ":" + uid, groupID + ":" + threadID, true},
	}
	seats := []struct {
		name    string
		pattern bool
		iso     bool
	}{
		{"pattern_and_isolation", true, true},
		{"pattern_only", true, false},
		{"dispatch_isolation_only", false, true},
		{"neither_flag", false, false},
	}

	for _, seat := range seats {
		for _, ch := range channels {
			t.Run(seat.name+"/"+ch.name, func(t *testing.T) {
				root := t.TempDir()
				workDir := filepath.Join(root, "workdir")
				if err := os.MkdirAll(workDir, 0o755); err != nil {
					t.Fatal(err)
				}

				agent := &dummyAgentWithWorkDir{stubAgent: stubAgent{}, workDir: workDir}
				const regName = "g1-command-context-agent"
				agent.name = regName
				RegisterAgent(regName, func(opts map[string]any) (Agent, error) { return agent, nil })

				p := &stubPlatformEngine{n: "telegram"}
				e := NewEngine("test-seat", agent, []Platform{p}, filepath.Join(root, "sessions.json"), LangEnglish)
				e.SetDataDir(root)
				if seat.pattern {
					if err := os.MkdirAll(filepath.Join(root, "task-"+threadID), 0o755); err != nil {
						t.Fatal(err)
					}
					e.SetWorkspacePattern(filepath.Join(root, "task-{{THREAD_ID}}"))
				}
				if seat.iso {
					e.SetDispatchTopicIsolation(true)
				}

				msg := &Message{
					Platform:   "telegram",
					SessionKey: ch.sessionKey,
					ChannelKey: ch.channelKey,
					Content:    "/new",
				}
				_, sessions, interactiveKey, _, err := e.commandContextWithWorkspace(p, msg)
				if err != nil {
					t.Fatalf("commandContextWithWorkspace() error = %v", err)
				}

				// Which rows resolve a workspace at all:
				//   - a threaded channel resolves one whenever either flag is set;
				//   - a threadless channel (DM, General) resolves one only under
				//     dispatch_topic_isolation, via the "general" sentinel.
				resolves := (ch.threaded && (seat.pattern || seat.iso)) || (!ch.threaded && seat.iso)

				if !resolves {
					if sessions != e.sessions {
						t.Fatalf("resolved a workspace SessionManager for a seat/channel that has none; want the global fallback")
					}
					if interactiveKey != ch.sessionKey {
						t.Fatalf("interactiveKey = %q, want bare sessionKey %q", interactiveKey, ch.sessionKey)
					}
					return
				}

				if sessions == e.sessions {
					t.Fatalf("command path resolved the GLOBAL SessionManager while the interactive path resolves a workspace one; /new would clear a session nobody is using")
				}

				wantPrefix := workDir
				if seat.pattern && ch.threaded {
					// Pattern seats route to an absolute worktree path, which is
					// its own effective dir.
					wantPrefix = filepath.Join(root, "task-"+threadID)
				}
				want := wantPrefix + ":" + ch.sessionKey
				if interactiveKey != want {
					t.Fatalf("interactiveKey = %q, want %q — the command path must key live state exactly as handleMessage does, or cleanupInteractiveState/stopInteractiveSession address nothing",
						interactiveKey, want)
				}
			})
		}
	}
}

// G1b — the relay path must address the same live state as the human path.
//
// A relay is delivered as a turn on the topic's own conversation (L-0718), which
// only works if it indexes that conversation the way the conversation is
// indexed. resolveRelayTopicTarget used to assemble "<workspace>:<sessionKey>"
// from the relay context's pool key, so on an isolation-only seat it addressed
// "L-855:telegram:…" while the topic's own live state sat under
// "<work dir>:telegram:…" — two keys, two agent processes, one topic.
//
// Observed 2026-08-02 14:16:04 (architect-codex, dispatch_topic_isolation, no
// workspace_pattern): "relay: continuing topic session
// interactive_key=L-5214:telegram:-1003917051393:5214:7664413698", against an
// interactive path that spawns "F:\nexus:telegram:…".
//
// Coverage declaration (L-0697): the seat matrix is {pattern, iso, both} — the
// three configurations in which a topic resolves a workspace at all; the
// no-flag seat is excluded because it resolves no topic workspace and
// resolveRelayTopicTarget returns errNoTopicSession before any key is built,
// which the "resolves" branch of
// TestCommandContext_MatchesInteractiveSessionTarget already covers. Both key
// forms coincide for absolute-path (pattern) workspaces; the iso rows are the
// ones that can drift, and they are in.
func TestRelayTopicTarget_MatchesInteractiveSessionTarget(t *testing.T) {
	const (
		chatID   = "-1003917051393"
		threadID = "855"
		uid      = "7664413698"
	)
	topicKey := "telegram:" + chatID + ":" + threadID + ":" + uid

	seats := []struct {
		name    string
		pattern bool
		iso     bool
	}{
		{"pattern_and_isolation", true, true},
		{"pattern_only", true, false},
		{"dispatch_isolation_only", false, true},
	}

	for _, seat := range seats {
		t.Run(seat.name, func(t *testing.T) {
			root := t.TempDir()
			workDir := filepath.Join(root, "workdir")
			if err := os.MkdirAll(workDir, 0o755); err != nil {
				t.Fatal(err)
			}

			agent := &dummyAgentWithWorkDir{stubAgent: stubAgent{}, workDir: workDir}
			const regName = "g1b-relay-context-agent"
			agent.name = regName
			RegisterAgent(regName, func(opts map[string]any) (Agent, error) { return agent, nil })

			p := &stubPlatformEngine{n: "telegram"}
			e := NewEngine("test-seat", agent, []Platform{p}, filepath.Join(root, "sessions.json"), LangEnglish)
			e.SetDataDir(root)
			if seat.pattern {
				if err := os.MkdirAll(filepath.Join(root, "task-"+threadID), 0o755); err != nil {
					t.Fatal(err)
				}
				e.SetWorkspacePattern(filepath.Join(root, "task-{{THREAD_ID}}"))
			}
			if seat.iso {
				e.SetDispatchTopicIsolation(true)
			}

			// What a human message in this topic resolves to.
			_, wantSessions, wantKey, _, err := e.commandContextWithWorkspace(p, &Message{
				Platform:   "telegram",
				SessionKey: topicKey,
				ChannelKey: chatID + ":" + threadID,
			})
			if err != nil {
				t.Fatalf("commandContextWithWorkspace() error = %v", err)
			}

			// The topic conversation a relay must join.
			relayAgent, relaySessions, _, relayWorkspace, err := e.relayContextForSourceSessionKey("secretary-seat", topicKey)
			if err != nil {
				t.Fatalf("relayContextForSourceSessionKey() error = %v", err)
			}
			_ = relayAgent
			relaySessions.GetOrCreateActive(topicKey)

			target, err := e.resolveRelayTopicTarget(topicKey, relayWorkspace, relaySessions)
			if err != nil {
				t.Fatalf("resolveRelayTopicTarget() error = %v", err)
			}
			if target.interactiveKey != wantKey {
				t.Fatalf("relay interactiveKey = %q, want %q — a relay keyed differently from the topic's own live state runs beside the conversation instead of joining it",
					target.interactiveKey, wantKey)
			}
			if target.sessions != wantSessions {
				t.Fatalf("relay resolved a different SessionManager than the message path")
			}
		})
	}
}

// routingPrimitives are the calls that decide which agent, SessionManager and
// interactive key a message belongs to.
var routingPrimitives = map[string]bool{
	"resolveWorkspacePattern":   true,
	"workspaceContext":          true,
	"getOrCreateWorkspaceAgent": true,
}

// messageRoutingResolvers is the frozen baseline of functions that call a
// routing primitive directly. It is a declared inventory of existing debt, not
// an approval list for new code: the count may only decrease.
//
//   - workspaceContext / resolveMessageWorkspaceContext are the intended homes —
//     the primitive itself and the single decider both message paths call;
//   - the other six each re-derive routing for a non-message entry point
//     (cron, timer, relay, out-of-band send, chat transcript, key lookup). Each
//     one is a place where the next divergence can appear; they are listed so
//     that fixing one is a ratchet step, and adding a ninth is a test failure.
//
// G1 red proof: before this fix handleMessage and commandContextWithWorkspace
// each resolved independently — the set had 9 members, and the two extra names
// are exactly the pair that disagreed.
var messageRoutingResolvers = []string{
	"ExecuteCronJob",
	"ExecuteTimerJob",
	"SendToSessionInWorkDir",
	"observeChatMessage",
	"relayContextForSourceSessionKey",
	"resolveMessageWorkspaceContext",
	"sessionContextForKey",
	"workspaceContext",
}

// TestMessageRouting_HasOneDecider refuses a new function that resolves message
// routing on its own.
//
// Coverage declaration (L-0697): scans every non-test .go file in package core
// (filepath.Glob "core/*.go", minus _test.go), parsing each with go/parser and
// walking every function declaration — no file, function, or symbol whitelist
// narrows the scan. A call counts when its selector is one of routingPrimitives,
// regardless of receiver, so a helper on another type is caught too. Exclusion:
// _test.go files, because test fixtures legitimately drive these primitives
// directly to build state. To be caught, a new resolver only has to call one of
// the three primitives — the shape every past divergence had.
func TestMessageRouting_HasOneDecider(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	allowed := make(map[string]bool, len(messageRoutingResolvers))
	for _, fn := range messageRoutingResolvers {
		allowed[fn] = true
	}

	found := map[string]bool{}
	var offenders []string
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !routingPrimitives[sel.Sel.Name] {
					return true
				}
				found[fn.Name.Name] = true
				if !allowed[fn.Name.Name] {
					offenders = append(offenders, fn.Name.Name+" ("+fset.Position(call.Pos()).String()+")")
				}
				return true
			})
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("these functions resolve message routing on their own:\n  %s\n\nMessage routing has one decider: resolveMessageWorkspaceContext. Call it instead of resolveWorkspacePattern/workspaceContext/getOrCreateWorkspaceAgent directly — handleMessage and commandContextWithWorkspace each having their own copy is what let /new stop clearing DM sessions.",
			strings.Join(offenders, "\n  "))
	}

	// Frozen baseline: the inventory may shrink, never grow. A name that
	// disappears must be deleted from messageRoutingResolvers in the same change.
	if len(found) > len(messageRoutingResolvers) {
		t.Fatalf("routing resolvers = %d, frozen baseline = %d", len(found), len(messageRoutingResolvers))
	}
	for _, fn := range messageRoutingResolvers {
		if !found[fn] {
			t.Fatalf("%q is in the frozen baseline but no longer calls a routing primitive; remove it from messageRoutingResolvers so the baseline ratchets down to %d", fn, len(found))
		}
	}
}
