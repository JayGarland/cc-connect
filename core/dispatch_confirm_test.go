package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPendingDispatchStore_UpsertAndTakeByLetter(t *testing.T) {
	dir := t.TempDir()
	s := newPendingDispatchStore(dir)

	entry := PendingDispatch{
		Request:          dispatchRequest{To: "dev-pro", Letter: "L-0667", Thread: "cc-connect-dispatch-architecture-redesign", Path: "x.query.md"},
		SourceSessionKey: "telegram:1:1",
		SourcePlatform:   "telegram",
		Card:             MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3},
	}
	if err := s.upsert(entry); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, found, err := s.takeByLetter("L-0667")
	if err != nil {
		t.Fatalf("takeByLetter: %v", err)
	}
	if !found {
		t.Fatal("expected pending entry to be found")
	}
	if got.Request.To != "dev-pro" || got.SourceSessionKey != "telegram:1:1" || got.Card.MessageID != 3 {
		t.Fatalf("unexpected entry: %+v", got)
	}

	// Idempotent: a second take finds nothing.
	_, found2, err := s.takeByLetter("L-0667")
	if err != nil {
		t.Fatalf("second takeByLetter: %v", err)
	}
	if found2 {
		t.Fatal("expected second takeByLetter to find nothing (already consumed)")
	}
}

// setupConfirmDispatchFixture mirrors TestExecuteDispatch_LedgerOrdering's
// minimal shape but WITHOUT a workspace pattern on the target, so
// executeDispatch takes its non-TaskTopicCreator branch and the
// receiptActionPlatform stub (which does not implement TaskTopicCreator)
// suffices for both the source (card rendering) and target roles.
func setupConfirmDispatchFixture(t *testing.T) (sourceEngine *Engine, p *receiptActionPlatform, req dispatchRequest, queryPath string) {
	t.Helper()
	root := t.TempDir()
	threadDir := filepath.Join(root, "threads", "cc-connect-dispatch-architecture-redesign")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queryPath = filepath.Join(threadDir, "L-0667.query.md")
	query := `---
ID: L-0667
Thread: cc-connect-dispatch-architecture-redesign
Type: QUERY
---

## Query
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}

	p = &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}

	targetEngine := NewEngine("dev-pro", &stubAgent{}, []Platform{p}, "", LangEnglish)

	sourceEngine = NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sourceEngine.dataDir = root
	sourceEngine.relayManager = NewRelayManager(root)
	sourceEngine.relayManager.RegisterEngine("dev-pro", targetEngine)
	sourceEngine.relayManager.RegisterEngine("secretary-seat", sourceEngine)
	sourceEngine.configureDispatch(DispatchConfig{
		Enabled:             true,
		SourceProject:       "secretary-seat",
		DashboardSessionKey: "telegram:-1003917051393:7664413698",
	})

	req = dispatchRequest{To: "dev-pro", Letter: "L-0667", Thread: "cc-connect-dispatch-architecture-redesign", Path: queryPath}
	return sourceEngine, p, req, queryPath
}

// TestMaybeHandleDispatchBlock_RendersConfirmCardInsteadOfAutoExecuting is
// the L-0667 acceptance test: a parsed [DISPATCH] block must render a
// confirm card and record a pending proposal, WITHOUT calling
// ControlPlaneDispatch/executeDispatch at all — no dispatch ledger entry,
// no relay message, until the confirm button is pressed.
func TestMaybeHandleDispatchBlock_RendersConfirmCardInsteadOfAutoExecuting(t *testing.T) {
	e, p, req, queryPath := setupConfirmDispatchFixture(t)

	block := "[DISPATCH]\nTo: " + req.To + "\nLetter: " + req.Letter + "\nThread: " + req.Thread + "\nPath: " + queryPath

	handled, replacement := e.maybeHandleDispatchBlock(p, "telegram:-1003917051393:7664413698", block)
	if !handled {
		t.Fatal("expected the [DISPATCH] block to be handled")
	}
	if !strings.Contains(replacement, "awaiting confirmation") {
		t.Fatalf("replacement = %q, want an awaiting-confirmation message, not an executed receipt", replacement)
	}

	if p.receiptCardsSent != 1 {
		t.Fatalf("expected exactly 1 confirm card sent, got %d", p.receiptCardsSent)
	}
	if !strings.Contains(p.buttonContent, req.Letter) || !strings.Contains(p.buttonContent, req.To) {
		t.Fatalf("card content = %q, want it to mention letter and target seat", p.buttonContent)
	}
	if len(p.buttonRows) != 1 || len(p.buttonRows[0]) != 1 || p.buttonRows[0][0].Data != "dispatch_confirm:"+req.Letter {
		t.Fatalf("unexpected card buttons: %+v", p.buttonRows)
	}

	// No dispatch actually happened yet.
	open, err := e.dispatchStore.listOpen()
	if err != nil {
		t.Fatalf("listOpen: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("expected zero open dispatch expectations before confirmation, got %d: %+v", len(open), open)
	}

	// The pending proposal was recorded and is retrievable.
	pending, found, err := e.pendingDispatchStore.takeByLetter(req.Letter)
	if err != nil {
		t.Fatalf("takeByLetter: %v", err)
	}
	if !found {
		t.Fatal("expected a pending proposal to be recorded")
	}
	if pending.Request.To != req.To || pending.Card.MessageID == 0 {
		t.Fatalf("unexpected pending entry: %+v", pending)
	}
}

// TestConfirmDispatch_Success proves the confirm button is the sole
// actuator: pressing it (ConfirmDispatch) is what finally calls
// ControlPlaneDispatch, producing a real dispatch ledger entry and audit
// log entry, and updates the card to the real receipt.
func TestConfirmDispatch_Success(t *testing.T) {
	e, p, req, queryPath := setupConfirmDispatchFixture(t)
	_ = queryPath

	block := "[DISPATCH]\nTo: " + req.To + "\nLetter: " + req.Letter + "\nThread: " + req.Thread + "\nPath: " + queryPath
	if handled, _ := e.maybeHandleDispatchBlock(p, "telegram:-1003917051393:7664413698", block); !handled {
		t.Fatal("setup: expected [DISPATCH] block to be handled")
	}

	receipt, ok, err := e.ConfirmDispatch(p, req.Letter)
	if err != nil {
		t.Fatalf("ConfirmDispatch: %v", err)
	}
	if !ok {
		t.Fatal("expected a pending proposal to be found and confirmed")
	}
	if !strings.Contains(receipt, "Dispatched") {
		t.Fatalf("receipt = %q, want a real dispatch receipt", receipt)
	}

	open, err := e.dispatchStore.listOpen()
	if err != nil {
		t.Fatalf("listOpen: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected exactly 1 open dispatch expectation after confirmation, got %d", len(open))
	}

	auditEntries, err := e.controlPlaneAudit.list()
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(auditEntries) != 1 || auditEntries[0].Letter != req.Letter {
		t.Fatalf("expected 1 audit entry for %s, got %+v", req.Letter, auditEntries)
	}

	if p.receiptCardsUpdated != 1 {
		t.Fatalf("expected the confirm card to be updated exactly once, got %d", p.receiptCardsUpdated)
	}
	if !strings.Contains(p.updatedContent, "Dispatched") {
		t.Fatalf("updated card content = %q, want the real receipt", p.updatedContent)
	}
	if p.updatedButtons != nil {
		t.Fatalf("expected buttons cleared after confirmation, got %+v", p.updatedButtons)
	}

	// Idempotent: pressing confirm again finds nothing pending.
	_, ok2, err := e.ConfirmDispatch(p, req.Letter)
	if err != nil {
		t.Fatalf("second ConfirmDispatch: %v", err)
	}
	if ok2 {
		t.Fatal("expected second confirm press to find no pending proposal")
	}
	if len(mustListOpen(t, e)) != 1 {
		t.Fatal("a double-press must not dispatch twice")
	}
}

// TestConfirmDispatch_MissingPending confirms a stale or unknown letter ID
// is handled gracefully: no panic, no error escalation, nothing dispatched.
func TestConfirmDispatch_MissingPending(t *testing.T) {
	e, p, _, _ := setupConfirmDispatchFixture(t)

	receipt, ok, err := e.ConfirmDispatch(p, "L-9999")
	if err != nil {
		t.Fatalf("ConfirmDispatch: %v", err)
	}
	if ok {
		t.Fatal("expected no pending proposal to be found for an unknown letter")
	}
	if receipt != "" {
		t.Fatalf("expected empty receipt, got %q", receipt)
	}
	if p.receiptCardsUpdated != 0 {
		t.Fatalf("expected no card update for a missing pending proposal, got %d", p.receiptCardsUpdated)
	}
}

func mustListOpen(t *testing.T, e *Engine) []DispatchExpectation {
	t.Helper()
	open, err := e.dispatchStore.listOpen()
	if err != nil {
		t.Fatalf("listOpen: %v", err)
	}
	return open
}
