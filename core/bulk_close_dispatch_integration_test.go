package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupBulkCloseDispatchFixture builds a dispatch-capable Engine (unlike
// bulk_close_test.go's unit-level fixtures, this one drives
// maybeHandleDispatchBlock itself) so the dispatch-card integration —
// offering the bulk-close list/button in the first place, and restoring the
// card on cancel — is exercised end to end, not just the underlying
// execute/compute helpers already covered in bulk_close_test.go.
func setupBulkCloseDispatchFixture(t *testing.T) (e *Engine, p *receiptActionPlatform, root string) {
	t.Helper()
	root = t.TempDir()
	p = &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	targetEngine := NewEngine("dev-pro", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e = NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.dataDir = root
	e.relayManager = NewRelayManager(root)
	e.relayManager.RegisterEngine("dev-pro", targetEngine)
	e.relayManager.RegisterEngine("secretary-seat", e)
	e.configureDispatch(DispatchConfig{
		Enabled:             true,
		SourceProject:       "secretary-seat",
		DashboardSessionKey: "telegram:-1:1",
	})
	e.notifyConfig.IndexPath = filepath.Join(root, "INDEX.md")
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	e.SetAdminFrom("boss-id")
	return e, p, root
}

func writeBulkCloseDispatchQuery(t *testing.T, root, thread, letter string) string {
	t.Helper()
	dir := filepath.Join(root, "threads", thread)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, letter+".query.md")
	body := "---\nID: " + letter + "\nThread: " + thread + "\nType: QUERY\n---\n\n## Query\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeBulkCloseDispatchIndex(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMaybeHandleDispatchBlockOffersBulkCloseWhenUnclosedLettersExist proves
// the dispatch confirm card (L-0667) is extended, not replaced: it still
// carries the original Confirm Dispatch button plus a read-only list and a
// new bulk-close button when same-thread unclosed letters exist.
func TestMaybeHandleDispatchBlockOffersBulkCloseWhenUnclosedLettersExist(t *testing.T) {
	e, p, root := setupBulkCloseDispatchFixture(t)
	queryPath := writeBulkCloseDispatchQuery(t, root, "alpha", "L-0300")
	resultPath := writeResultFile(t, root, "alpha", "L-0100", "ID: L-0100\nStatus: DONE\n---\n")
	writeBulkCloseDispatchIndex(t, e.notifyConfig.IndexPath, "| L-0100 | RESULT | alpha | ROOT | earlier work | 2026-07-01 |\n")
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0100", Thread: "alpha", Path: resultPath, Status: "DONE", Summary: "earlier work"}); err != nil {
		t.Fatal(err)
	}

	block := "[DISPATCH]\nTo: dev-pro\nLetter: L-0300\nThread: alpha\nPath: " + queryPath
	handled, replacement := e.maybeHandleDispatchBlock(p, "telegram:-1:1", block)
	if !handled {
		t.Fatal("expected the [DISPATCH] block to be handled")
	}
	if !strings.Contains(replacement, "awaiting confirmation") {
		t.Fatalf("replacement = %q, want an awaiting-confirmation message", replacement)
	}
	if !strings.Contains(p.buttonContent, "L-0100") {
		t.Fatalf("dispatch card missing the unclosed-letter list: %q", p.buttonContent)
	}
	foundConfirm, foundBulk := false, false
	for _, row := range p.buttonRows {
		for _, b := range row {
			if b.Data == "dispatch_confirm:L-0300" {
				foundConfirm = true
			}
			if b.Data == "cmd:/receipt bulkclose L-0300" {
				foundBulk = true
			}
		}
	}
	if !foundConfirm {
		t.Fatalf("dispatch card lost its original Confirm Dispatch button: %#v", p.buttonRows)
	}
	if !foundBulk {
		t.Fatalf("dispatch card missing the bulk-close button: %#v", p.buttonRows)
	}
}

// TestBulkCloseCancelRestoresDispatchCard proves cancel is non-destructive:
// the original dispatch proposal card (list + both buttons) comes back, and
// nothing is closed.
func TestBulkCloseCancelRestoresDispatchCard(t *testing.T) {
	e, p, root := setupBulkCloseDispatchFixture(t)
	queryPath := writeBulkCloseDispatchQuery(t, root, "alpha", "L-0700")
	resultPath := writeResultFile(t, root, "alpha", "L-0100", "ID: L-0100\nStatus: DONE\n---\n")
	writeBulkCloseDispatchIndex(t, e.notifyConfig.IndexPath, "| L-0100 | RESULT | alpha | ROOT | earlier work | 2026-07-01 |\n")
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0100", Thread: "alpha", Path: resultPath, Status: "DONE", Summary: "earlier work"}); err != nil {
		t.Fatal(err)
	}
	block := "[DISPATCH]\nTo: dev-pro\nLetter: L-0700\nThread: alpha\nPath: " + queryPath
	if handled, _ := e.maybeHandleDispatchBlock(p, "telegram:-1:1", block); !handled {
		t.Fatal("setup: dispatch block must be handled")
	}
	msg := &Message{UserID: "boss-id", UserName: "boss", ReplyCtx: "inbox"}
	if handled := e.handleCommand(p, msg, "/receipt bulkclose L-0700"); !handled {
		t.Fatal("review must not start an agent turn")
	}
	if handled := e.handleCommand(p, msg, "/receipt bulkclosecancel L-0700"); !handled {
		t.Fatal("cancel must not start an agent turn")
	}
	if !strings.Contains(p.updatedContent, "L-0700") || !strings.Contains(p.updatedContent, "L-0100") {
		t.Fatalf("cancel must restore the dispatch card with its unclosed-letter list: %q", p.updatedContent)
	}
	dispatchFound, bulkFound := false, false
	for _, row := range p.updatedButtons {
		for _, b := range row {
			if b.Data == "dispatch_confirm:L-0700" {
				dispatchFound = true
			}
			if b.Data == "cmd:/receipt bulkclose L-0700" {
				bulkFound = true
			}
		}
	}
	if !dispatchFound || !bulkFound {
		t.Fatalf("cancel did not restore both original buttons: %#v", p.updatedButtons)
	}
	record, err := e.notifyStore.receipt("L-0100")
	if err != nil {
		t.Fatal(err)
	}
	if record.ClosedAt != "" {
		t.Fatal("cancel must not close anything")
	}
}

// TestBulkCloseConfirmRequiresAdmin mirrors
// TestEngineReceiptCloseRequiresAdmin for the batch path: a non-admin
// pressing confirm must not close anything.
func TestBulkCloseConfirmRequiresAdmin(t *testing.T) {
	e, p, root := setupBulkCloseDispatchFixture(t)
	queryPath := writeBulkCloseDispatchQuery(t, root, "alpha", "L-0800")
	resultPath := writeResultFile(t, root, "alpha", "L-0100", "ID: L-0100\nStatus: DONE\n---\n")
	writeBulkCloseDispatchIndex(t, e.notifyConfig.IndexPath, "| L-0100 | RESULT | alpha | ROOT | earlier work | 2026-07-01 |\n")
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0100", Thread: "alpha", Path: resultPath, Status: "DONE", Summary: "earlier work"}); err != nil {
		t.Fatal(err)
	}
	block := "[DISPATCH]\nTo: dev-pro\nLetter: L-0800\nThread: alpha\nPath: " + queryPath
	if handled, _ := e.maybeHandleDispatchBlock(p, "telegram:-1:1", block); !handled {
		t.Fatal("setup: dispatch block must be handled")
	}
	msg := &Message{UserID: "not-boss", UserName: "someone", ReplyCtx: "inbox"}
	if handled := e.handleCommand(p, msg, "/receipt bulkcloseconfirm L-0800"); !handled {
		t.Fatal("non-admin confirm must still be handled (with a rejection), not start an agent turn")
	}
	record, err := e.notifyStore.receipt("L-0100")
	if err != nil {
		t.Fatal(err)
	}
	if record.ClosedAt != "" {
		t.Fatal("a non-admin must never be able to execute a bulk close")
	}
}
