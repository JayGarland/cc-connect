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

// messageRoutingHomes are the functions allowed to touch a routing primitive
// because resolving is their job:
//
//   - workspaceContext — the primitive that turns a workspace into an agent +
//     SessionManager + effective dir;
//   - topicWorkspaceKey — the single gate in front of resolveWorkspacePattern
//     (pinned to exactly one caller by TestResolveWorkspacePattern_HasOneCaller);
//   - resolveMessageWorkspaceContext — the decider for a Message;
//   - sessionContextForKey — the decider for a bare session key, which card
//     callbacks need because they carry no Message. It is a second entry point,
//     not a second opinion: TestSessionContextForKey_MatchesDecider pins it to
//     the same answer across the whole seat × channel matrix.
var messageRoutingHomes = []string{
	"resolveMessageWorkspaceContext",
	"sessionContextForKey",
	"topicWorkspaceKey",
	"workspaceContext",
}

// messageRoutingDebt is the frozen inventory of functions that still re-derive
// routing for a non-message entry point. Each is a place the next divergence can
// appear. The count may only decrease.
//
// Baseline history — each step is a letter that cured one:
//
//	6 → handleMessage and commandContextWithWorkspace resolved independently
//	    (that pair is what let /new stop clearing DM sessions)
//	5 → both routed through resolveMessageWorkspaceContext
//	4 → observeChatMessage routed through topicWorkspaceKey
var messageRoutingDebt = []string{
	"ExecuteCronJob",
	"ExecuteTimerJob",
	"SendToSessionInWorkDir",
	"relayContextForSourceSessionKey",
}

// TestResolveWorkspacePattern_HasOneCaller pins the gate in front of
// resolveWorkspacePattern to a single site.
//
// Four call sites each wrote the gate out by hand and two of them wrote it
// differently: the message path tested `workspacePattern != "" ||
// dispatchTopicIsolation`, sessionContextForKey tested `workspacePattern != ""`
// and additionally required a thread id. That second copy is why every
// dispatch_topic_isolation seat resolved the wrong SessionManager from a bare
// session key, in a DM, in General and in a letter topic alike.
//
// Coverage declaration (L-0697): same AST scan and same scope as
// TestMessageRouting_HasOneDecider below — every non-test file in package core,
// no whitelist. Exclusion: _test.go, which may call it directly to assert
// shard behaviour.
func TestResolveWorkspacePattern_HasOneCaller(t *testing.T) {
	callers := routingPrimitiveCallers(t, map[string]bool{"resolveWorkspacePattern": true})
	delete(callers, "topicWorkspaceKey")
	if len(callers) > 0 {
		var names []string
		for fn, where := range callers {
			names = append(names, fn+" ("+where+")")
		}
		sort.Strings(names)
		t.Fatalf("resolveWorkspacePattern is called outside topicWorkspaceKey:\n  %s\n\nGo through topicWorkspaceKey, or the gate in front of it gets written out by hand again and the copies drift.",
			strings.Join(names, "\n  "))
	}
}

// routingPrimitiveCallers returns enclosing function name → first call position
// for every call to one of prims in package core's non-test files.
func routingPrimitiveCallers(t *testing.T, prims map[string]bool) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
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
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !prims[sel.Sel.Name] {
					return true
				}
				if _, seen := out[fn.Name.Name]; !seen {
					out[fn.Name.Name] = fset.Position(call.Pos()).String()
				}
				return true
			})
		}
	}
	return out
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
	callers := routingPrimitiveCallers(t, routingPrimitives)

	allowed := map[string]bool{}
	for _, fn := range messageRoutingHomes {
		allowed[fn] = true
	}
	for _, fn := range messageRoutingDebt {
		allowed[fn] = true
	}

	var offenders []string
	for fn, where := range callers {
		if !allowed[fn] {
			offenders = append(offenders, fn+" ("+where+")")
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("these functions resolve message routing on their own:\n  %s\n\nRouting is resolved in %v and nowhere else. A message resolves through resolveMessageWorkspaceContext, a bare session key through sessionContextForKey; both must agree. Each independent copy is a future divergence — that is how /new stopped clearing DM sessions, and how the card renderers ended up reading a different store than the typed command.",
			strings.Join(offenders, "\n  "), messageRoutingHomes)
	}

	// Frozen debt inventory: may shrink, never grow. A name that no longer
	// resolves must be removed in the same change that cured it, so the baseline
	// ratchets down instead of quietly holding a slot open.
	var stillDebt int
	for _, fn := range messageRoutingDebt {
		if _, ok := callers[fn]; ok {
			stillDebt++
			continue
		}
		t.Fatalf("%q no longer resolves routing on its own; drop it from messageRoutingDebt so the baseline ratchets %d → %d", fn, len(messageRoutingDebt), len(messageRoutingDebt)-1)
	}
	if stillDebt > len(messageRoutingDebt) {
		t.Fatalf("independent resolvers = %d, frozen baseline = %d", stillDebt, len(messageRoutingDebt))
	}
	for _, fn := range messageRoutingHomes {
		if _, ok := callers[fn]; !ok {
			t.Fatalf("%q is declared a routing home but resolves nothing; the declaration and the code have drifted", fn)
		}
	}
}
