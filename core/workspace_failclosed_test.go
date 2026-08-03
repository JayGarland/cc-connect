package core

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// L-0767: pattern-seat fail-closed on an unauthorized topic.
//
// The defect: when a {{LETTER_ID}} pattern seat resolved a topic that had no
// dispatch-ledger entry and no sticky binding, resolveWorkspacePattern
// fabricated "L-"+threadID, persisted it, and provisioned a worktree for a
// letter that does not exist. Observed 2026-08-03: topic 10565 produced
// letter "L-10565" and worktree F:\foundry\product\worktrees\L-10565, while
// the work actually belonged to L-0765. The fabricated worktree had never
// hosted a session, so the Claude project dir for it was never created, and
// ValidateSessionID discarded a healthy 5.75-hour session as "not belonging
// to this project" (cc-connect-stdout.log 2026-08-03T05:47:06.840).
//
// Boss ruling (2026-08-03): fail CLOSED, and never silently — refuse the
// topic and say so out loud, rather than fabricating an identity or silently
// degrading into the seat's own work_dir (for dev-pro that work_dir is
// F:\GitHub\resonova, the base repo's main worktree, which L-0564 forbids
// writing in; a silent degrade there is still a silent failure, just bleeding
// somewhere else).
// ---------------------------------------------------------------------------

// TestPatternSeatRefusesUnauthorizedTopicInsteadOfFabricating is the core
// regression: no ledger entry, no sticky binding => refusal, not fabrication.
func TestPatternSeatRefusesUnauthorizedTopicInsteadOfFabricating(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "{{LETTER_ID}}"))

	const threadID = "10565"

	workspace, err := e.resolveWorkspacePatternChecked(threadID, "")
	if err == nil {
		t.Fatalf("resolveWorkspacePatternChecked() returned no error; want refusal (got workspace %q)", workspace)
	}
	if workspace != "" {
		t.Fatalf("refused resolution must yield an empty workspace, got %q", workspace)
	}

	var refusal *unauthorizedTopicError
	if !errors.As(err, &refusal) {
		t.Fatalf("error is %T (%v); want *unauthorizedTopicError so the message path can refuse loudly", err, err)
	}
	if refusal.threadID != threadID {
		t.Fatalf("refusal.threadID = %q, want %q", refusal.threadID, threadID)
	}
	if refusal.project != "dev-pro" {
		t.Fatalf("refusal.project = %q, want %q", refusal.project, "dev-pro")
	}

	// Loud, not silent: the operator-facing text must name the topic and the
	// sanctioned way forward (the dispatch card), so nobody has to read logs
	// to find out why the seat did nothing.
	msg := refusal.Error()
	if !strings.Contains(msg, threadID) {
		t.Errorf("refusal message %q does not name the thread id %q", msg, threadID)
	}
	if !strings.Contains(strings.ToLower(msg), "dispatch") {
		t.Errorf("refusal message %q does not point at the dispatch flow", msg)
	}

	// Fail-closed means nothing is written: a fabricated identity must not be
	// persisted as this topic's binding, or the next call would "succeed" by
	// reading back its own fabrication.
	if bound := e.ensureTopicLetterBindingStore().lookup("dev-pro", threadID); bound != "" {
		t.Fatalf("refusal persisted a binding %q; fail-closed must write nothing", bound)
	}
}

// TestPatternSeatRefusalIsStableAcrossRetries guards the specific way this
// defect class regresses: a refusal that quietly remembers something turns
// into an accepted fabrication on the second message.
func TestPatternSeatRefusalIsStableAcrossRetries(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "{{LETTER_ID}}"))

	const threadID = "10565"
	for i := range 3 {
		workspace, err := e.resolveWorkspacePatternChecked(threadID, "still working on it")
		if err == nil {
			t.Fatalf("attempt %d: got workspace %q, want a persistent refusal", i+1, workspace)
		}
		var refusal *unauthorizedTopicError
		if !errors.As(err, &refusal) {
			t.Fatalf("attempt %d: error is %T, want *unauthorizedTopicError", i+1, err)
		}
	}
}

// TestPatternSeatStillAcceptsLedgerAuthorizedTopic is the other half of
// fail-closed: the authorized path must be untouched. A ledger entry is the
// stable key this binding is allowed to come from.
func TestPatternSeatStillAcceptsLedgerAuthorizedTopic(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "{{LETTER_ID}}"))

	const threadID = "10565"
	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:          "L-0765",
		To:              "dev-pro",
		TopicID:         threadID,
		TopicSessionKey: "telegram:-1003917051393:" + threadID + ":7664413698",
		State:           dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert dispatch expectation: %v", err)
	}

	want := filepath.Join(root, "worktrees", "L-0765")
	got, err := e.resolveWorkspacePatternChecked(threadID, "")
	if err != nil {
		t.Fatalf("ledger-authorized topic was refused: %v", err)
	}
	if got != want {
		t.Fatalf("resolveWorkspacePatternChecked() = %q, want %q", got, want)
	}
}

// TestPatternSeatHonorsExistingStickyBinding confirms a topic already bound
// by an earlier authorized dispatch keeps resolving after the ledger entry
// ages out — the binding store is a stable key too, unlike message text.
func TestPatternSeatHonorsExistingStickyBinding(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "{{LETTER_ID}}"))

	const threadID = "10565"
	e.ensureTopicLetterBindingStore().bind("dev-pro", threadID, "L-0765")

	want := filepath.Join(root, "worktrees", "L-0765")
	got, err := e.resolveWorkspacePatternChecked(threadID, "")
	if err != nil {
		t.Fatalf("topic with a sticky binding was refused: %v", err)
	}
	if got != want {
		t.Fatalf("resolveWorkspacePatternChecked() = %q, want %q", got, want)
	}
}

// TestDispatchTopicIsolationSeatKeepsShardForUnauthorizedTopic documents the
// deliberate BOUNDARY of the L-0767 rule, and why the sibling branch is not
// treated the same way.
//
// The L-0767 QUERY listed both fabrication sites as in scope. Reading the
// consumer proved that wrong for this one: with no workspace_pattern, the value
// returned is a session SHARD KEY, not a workspace path. It is not absolute, so
// getOrCreateWorkspaceAgent runs it against the seat's own static work_dir and
// never provisions a worktree (defaultDispatchWorkspaceKey, engine.go:18242-18247).
// "L-"+threadID here is a per-topic shard name derived from threadID — itself a
// stable key — not a fabricated letter identity pointing at a disconnected
// worktree. Refusing here would deny ad-hoc chat its own session and buy no
// safety. This test pins that boundary so a later reading of the letter does not
// "finish the job" and break threadless chat.
func TestDispatchTopicIsolationSeatKeepsShardForUnauthorizedTopic(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetDispatchTopicIsolation(true)

	const threadID = "10565"
	shard, err := e.resolveWorkspacePatternChecked(threadID, "")
	if err != nil {
		t.Fatalf("shard-only seat was refused: %v; the fail-closed rule applies to worktree paths, not session shards", err)
	}
	if shard != "L-"+threadID {
		t.Fatalf("shard = %q, want %q", shard, "L-"+threadID)
	}
	if filepath.IsAbs(shard) {
		t.Fatalf("shard %q is an absolute path; it must stay a shard key so no worktree is provisioned", shard)
	}
}
