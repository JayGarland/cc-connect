package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestControlPlaneDispatch_RecordsAuditEntryAndPreservesBehavior proves the
// L-0662 wrapper is behavior-neutral: it reruns the exact scenario from
// TestExecuteDispatchFallback (dispatch.go's existing regression test) but
// through ControlPlaneDispatch instead of calling executeDispatch directly,
// and asserts the pre-existing effects (receipt text, dispatch ledger) are
// unchanged while a new audit entry is additionally recorded.
func TestControlPlaneDispatch_RecordsAuditEntryAndPreservesBehavior(t *testing.T) {
	root := t.TempDir()
	threadDir := filepath.Join(root, "threads", "topology-reframe")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(threadDir, "L-0154.query.md")
	query := `---
ID: L-0154
Thread: topology-reframe
Type: QUERY
---

## Query
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &mockTaskTopicPlatform{
		stubMediaPlatform: stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}},
		createTaskTopicFunc: func(ctx context.Context, dashboardSessionKey, title, content string) (*TaskTopic, error) {
			return nil, fmt.Errorf("not enough rights to create a topic")
		},
		reconstructFunc: func(sessionKey string) (any, error) {
			return "reconstructed-ctx", nil
		},
	}

	targetEngine := NewEngine("dev-pro", &stubAgent{}, []Platform{p}, "", LangEnglish)
	targetEngine.SetWorkspacePattern(filepath.Join(root, "worktrees", "task-{{THREAD_ID}}"))

	sourceEngine := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sourceEngine.dataDir = root
	sourceEngine.relayManager = NewRelayManager(root)
	sourceEngine.relayManager.RegisterEngine("dev-pro", targetEngine)
	sourceEngine.relayManager.RegisterEngine("secretary-seat", sourceEngine)

	sourceEngine.configureDispatch(DispatchConfig{
		Enabled:             true,
		SourceProject:       "secretary-seat",
		DashboardSessionKey: "telegram:-1003917051393:7664413698:0",
		PollInterval:        1 * time.Second,
	})

	req := dispatchRequest{
		To:     "dev-pro",
		Letter: "L-0154",
		Thread: "topology-reframe",
		Path:   queryPath,
	}

	receipt, err := sourceEngine.ControlPlaneDispatch(p, "telegram:-1003917051393:7664413698:0", req)
	if err != nil {
		t.Fatalf("ControlPlaneDispatch failed: %v", err)
	}
	if receipt != "✅ Dispatched L-0154 to dev-pro" {
		t.Errorf("unexpected receipt: %q", receipt)
	}

	// Pre-existing behavior (executeDispatch's own effect) must be unchanged.
	open, err := sourceEngine.dispatchStore.listOpen()
	if err != nil {
		t.Fatalf("listOpen failed: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open expectation, got %d", len(open))
	}

	// New behavior: exactly one audit entry recorded for this decision.
	entries, err := sourceEngine.controlPlaneAudit.list()
	if err != nil {
		t.Fatalf("audit list failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Action != "dispatch" || got.Letter != "L-0154" || got.Thread != "topology-reframe" ||
		got.To != "dev-pro" || got.SourceProject != "secretary-seat" || got.Outcome != "ok" {
		t.Errorf("unexpected audit entry: %+v", got)
	}
	if got.At.IsZero() {
		t.Errorf("expected non-zero audit timestamp")
	}
}

// TestControlPlaneDispatch_RecordsAuditEntryOnFailure confirms a rejected
// dispatch (e.g. unknown target seat) is also recorded, with its outcome —
// the audit log is a record of decisions attempted, not just successes.
func TestControlPlaneDispatch_RecordsAuditEntryOnFailure(t *testing.T) {
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = t.TempDir()
	e.relayManager = NewRelayManager(e.dataDir)
	// No target registered — executeDispatch fails fast with "target seat not running".

	req := dispatchRequest{To: "nobody", Letter: "L-0001", Thread: "t", Path: "nonexistent"}
	if _, err := e.ControlPlaneDispatch(nil, "telegram:1:1", req); err == nil {
		t.Fatalf("expected error for missing target seat")
	}

	entries, err := e.controlPlaneAudit.list()
	if err != nil {
		t.Fatalf("audit list failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Outcome == "ok" {
		t.Errorf("expected a failure outcome to be recorded, got %q", entries[0].Outcome)
	}
}
