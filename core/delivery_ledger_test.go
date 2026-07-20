package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDeliveryStoreUsesLocalDataDirectory(t *testing.T) {
	store := newDeliveryStore(t.TempDir())
	if store == nil || store.path == "" {
		t.Fatal("unified delivery store must be created under the local data directory")
	}
	if got := store.path; len(got) < len("delivery_ledger.json") || got[len(got)-len("delivery_ledger.json"):] != "delivery_ledger.json" {
		t.Fatalf("path = %q, want local delivery_ledger.json", got)
	}
}

func TestDeliveryMigrationMergesLegacyLedgersOnce(t *testing.T) {
	root := t.TempDir()
	notify := newNotifyStore(root)
	if err := notify.save(notifyLedger{Receipts: map[string]receiptRecord{"L-0100": {Thread: "alpha", ResultPath: "result.md", Generation: "rd", Card: &MessageLocator{MessageID: 1}}}}); err != nil {
		t.Fatal(err)
	}
	outbox := newOutboxStore(root)
	if err := outbox.save(outboxLedger{Records: map[string]outboxRecord{"L-0100": {QueryPath: "query.md", Generation: "qd", Card: &MessageLocator{MessageID: 2}, Attempts: 2}}}); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(filepath.Join(root, "outbox_manual.json"), []byte(`{"L-0101":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newDeliveryStore(root)
	if err := store.migrateLegacyOnce(root); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Records["L-0100"]; r.Inbox.Card == nil || r.Outbox.Card == nil || r.Outbox.Attempts != 2 {
		t.Fatalf("merged record = %#v", r)
	}
	if got.Records["L-0101"].Outbox.Status != "manual" {
		t.Fatalf("manual state = %#v", got.Records["L-0101"])
	}
	if err := store.migrateLegacyOnce(root); err != nil {
		t.Fatal(err)
	}
	again, _ := store.load()
	if len(again.Records) != 2 {
		t.Fatalf("migration duplicated records: %#v", again)
	}
}

func TestDesiredDeliveryStateTreatsResultOrDispatchAsOutboxTerminal(t *testing.T) {
	query := queryFileInfo{Letter: "L-0100", Thread: "alpha", Path: "q.md", Digest: "q"}
	got := desiredDeliveryState(query, resultFileInfo{Letter: "L-0100", Path: "r.md"}, true)
	if got.Outbox.Status != "terminal" || got.Inbox.Status != "pending" || got.ResultPath != "r.md" {
		t.Fatalf("desired = %#v", got)
	}
}

func TestChangedDeliveryInputsReturnsOnlyAffectedLetterIDs(t *testing.T) {
	prior := deliveryLedger{Records: map[string]deliveryRecord{"L-0100": {Scanner: deliveryScannerState{QueryFingerprint: "old"}}, "L-0101": {Scanner: deliveryScannerState{QueryFingerprint: "same"}}}}
	changed := changedDeliveryInputs(prior, map[string]deliveryScannerState{"L-0100": {QueryFingerprint: "new"}, "L-0101": {QueryFingerprint: "same"}})
	if len(changed) != 1 || !changed["L-0100"] {
		t.Fatalf("changed = %#v", changed)
	}
}

func TestDeliveryLedgerFullAuditDueAfterInterval(t *testing.T) {
	now := time.Now().UTC()
	if !(deliveryLedger{}).fullAuditDue(now) || (deliveryLedger{LastFullAudit: now}).fullAuditDue(now) || !(deliveryLedger{LastFullAudit: now.Add(-deliveryFullAuditInterval)}).fullAuditDue(now) {
		t.Fatal("full audit schedule is incorrect")
	}
}

func TestDeliveryLedgerRoundTripsUnifiedRecord(t *testing.T) {
	store := newDeliveryStore(t.TempDir())
	want := deliveryLedger{Version: deliveryLedgerVersion, Records: map[string]deliveryRecord{
		"L-0482": {
			Thread: "product-manager-persona", QueryPath: "L-0482.query.md", ResultPath: "L-0482.result.md",
			QueryDigest: "query-digest", ResultDigest: "result-digest", Inbox: deliveryInboxState{Status: "pending", Card: &MessageLocator{Platform: "telegram", MessageID: 1}},
			Outbox:  deliveryOutboxState{Status: "dispatched", Card: &MessageLocator{Platform: "telegram", MessageID: 2}, Attempts: 3, RetryAt: time.Unix(100, 0).UTC()},
			Scanner: deliveryScannerState{QueryFingerprint: "q", ResultFingerprint: "r"},
		},
	}}
	if err := store.save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	r := got.Records["L-0482"]
	if got.Version != deliveryLedgerVersion || r.Inbox.Card == nil || r.Outbox.Card == nil || r.Outbox.Attempts != 3 || r.Scanner.ResultFingerprint != "r" {
		t.Fatalf("round trip = %#v", got)
	}
}
