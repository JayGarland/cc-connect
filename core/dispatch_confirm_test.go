package core

import (
	"context"
	"errors"
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

// warningDispatchPlatform is a platform with Send but NO ReceiptCardManager
// capability. Under the L-0759 fail-closed pursuit (Boss ruling), a [DISPATCH]
// block on it is refused — there is no way to obtain Boss's confirmation.
type warningDispatchPlatform struct {
	stubPlatformEngine
}

// failingCardDispatchPlatform implements ReceiptCardManager but its
// SendReceiptCard always fails. Under the L-0759 fail-closed pursuit, the
// [DISPATCH] is refused rather than auto-executed past the card.
type failingCardDispatchPlatform struct {
	warningDispatchPlatform
}

func (p *failingCardDispatchPlatform) SendReceiptCard(_ context.Context, _ any, _ string, _ [][]ButtonOption) (MessageLocator, error) {
	return MessageLocator{}, errors.New("card unavailable")
}

func (p *failingCardDispatchPlatform) UpdateReceiptCard(context.Context, MessageLocator, string, [][]ButtonOption) error {
	return nil
}

func TestMaybeHandleDispatchBlock_NoCardCapabilityRejectsFailClosed(t *testing.T) {
	e, _, req, queryPath := setupConfirmDispatchFixture(t)
	p := &warningDispatchPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}

	block := "[DISPATCH]\nTo: " + req.To + "\nLetter: " + req.Letter + "\nThread: " + req.Thread + "\nPath: " + queryPath
	handled, replacement := e.maybeHandleDispatchBlock(p, "telegram:-1003917051393:7664413698", block)

	if !handled || !strings.Contains(replacement, "Dispatch rejected") || !strings.Contains(replacement, "fail-closed") {
		t.Fatalf("maybeHandleDispatchBlock = handled:%v replacement:%q, want fail-closed rejection", handled, replacement)
	}
	if open := mustListOpen(t, e); len(open) != 0 {
		t.Fatalf("no-card platform must not dispatch, got open expectations: %+v", open)
	}
	entries, err := e.controlPlaneAudit.list()
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "dispatch_rejected" || entries[0].Letter != req.Letter {
		t.Fatalf("audit entries = %+v, want rejected audit for %s", entries, req.Letter)
	}
}

func TestMaybeHandleDispatchBlock_CardSendFailureRejectsFailClosed(t *testing.T) {
	e, _, req, queryPath := setupConfirmDispatchFixture(t)
	p := &failingCardDispatchPlatform{warningDispatchPlatform: warningDispatchPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}}

	block := "[DISPATCH]\nTo: " + req.To + "\nLetter: " + req.Letter + "\nThread: " + req.Thread + "\nPath: " + queryPath
	handled, replacement := e.maybeHandleDispatchBlock(p, "telegram:-1003917051393:7664413698", block)

	if !handled || !strings.Contains(replacement, "Dispatch rejected") || !strings.Contains(replacement, "confirmation card") {
		t.Fatalf("maybeHandleDispatchBlock = handled:%v replacement:%q, want card-failure rejection", handled, replacement)
	}
	if open := mustListOpen(t, e); len(open) != 0 {
		t.Fatalf("card-send failure must not dispatch, got open expectations: %+v", open)
	}
	entries, err := e.controlPlaneAudit.list()
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "dispatch_rejected" || entries[0].Letter != req.Letter {
		t.Fatalf("audit entries = %+v, want rejected audit for %s", entries, req.Letter)
	}
}

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

func TestConfirmDispatchRejectsAwaitingArchiveVerification(t *testing.T) {
	e, p, req, queryPath := setupConfirmDispatchFixture(t)
	e.pendingDispatchStore = newPendingDispatchStore(e.dataDir)
	if err := e.pendingDispatchStore.upsert(PendingDispatch{Request: req, SourceSessionKey: "telegram:1:1", Card: MessageLocator{Platform: "telegram", MessageID: 1}}); err != nil {
		t.Fatal(err)
	}
	e.outboxRecords = map[string]outboxRecord{req.Letter: {QueryPath: queryPath, Generation: "g", Verification: verificationAwaiting}}
	_, ok, err := e.ConfirmDispatch(p, req.Letter)
	if err != nil || ok {
		t.Fatalf("ConfirmDispatch = ok:%v err:%v", ok, err)
	}
	if len(mustListOpen(t, e)) != 0 {
		t.Fatal("awaiting verification dispatched")
	}
	if _, found, err := e.pendingDispatchStore.peekByLetter(req.Letter); err != nil || !found {
		t.Fatalf("pending proposal must remain: found:%v err:%v", found, err)
	}
}

// TestMaybeHandleDispatchBlock_OffersBulkCloseForUnclosedThreadLetters is
// the L-0694 Option B wiring test: when the dispatch's own thread has an
// earlier same-thread letter with a RESULT row but no CLOSED row, the
// confirm card must carry a read-only list plus a second "🔒 一并封信"
// button — without changing what "✅ Confirm Dispatch" does (still row 0,
// still exactly the same callback data).
func TestMaybeHandleDispatchBlock_OffersBulkCloseForUnclosedThreadLetters(t *testing.T) {
	e, p, req, queryPath := setupConfirmDispatchFixture(t)
	e.notifyConfig.IndexPath = filepath.Join(e.dataDir, "INDEX.md")
	e.notifyStore = newNotifyStore(filepath.Join(e.dataDir, "notify-data"))
	if err := os.WriteFile(e.notifyConfig.IndexPath, []byte(
		"# INDEX\n| L-0600 | RESULT | "+req.Thread+" | ROOT | prior result | 2026-07-01 |\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	priorResultPath := filepath.Join(e.dataDir, "threads", req.Thread, "L-0600.result.md")
	if err := os.MkdirAll(filepath.Dir(priorResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priorResultPath, []byte("ID: L-0600\nStatus: DONE\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0600", Thread: req.Thread, Path: priorResultPath, Status: "DONE", Summary: "prior result"}); err != nil {
		t.Fatal(err)
	}

	block := "[DISPATCH]\nTo: " + req.To + "\nLetter: " + req.Letter + "\nThread: " + req.Thread + "\nPath: " + queryPath
	handled, replacement := e.maybeHandleDispatchBlock(p, "telegram:-1003917051393:7664413698", block)
	if !handled {
		t.Fatal("expected the [DISPATCH] block to be handled")
	}
	if !strings.Contains(replacement, "awaiting confirmation") {
		t.Fatalf("replacement = %q", replacement)
	}

	if !strings.Contains(p.buttonContent, "L-0600") {
		t.Fatalf("card content must list the unclosed same-thread letter, got %q", p.buttonContent)
	}
	if len(p.buttonRows) != 2 {
		t.Fatalf("expected 2 button rows (confirm dispatch + bulk close), got %+v", p.buttonRows)
	}
	if p.buttonRows[0][0].Data != "dispatch_confirm:"+req.Letter {
		t.Fatalf("confirm-dispatch button must be unchanged, got %+v", p.buttonRows[0])
	}
	if len(p.buttonRows[1]) != 1 || !strings.Contains(p.buttonRows[1][0].Data, "bulkclose "+req.Letter) {
		t.Fatalf("expected a bulk-close button wired to this dispatch letter as token, got %+v", p.buttonRows[1])
	}

	entry, found, err := e.ensurePendingBulkCloseStore().peek(req.Letter)
	if err != nil || !found {
		t.Fatalf("expected a PendingBulkClose recorded under token %s, found=%v err=%v", req.Letter, found, err)
	}
	if len(entry.Letters) != 1 || entry.Letters[0] != "L-0600" {
		t.Fatalf("unexpected pending bulk-close entry: %+v", entry)
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
