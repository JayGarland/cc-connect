package core

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeResultFile(t *testing.T, threadsDir, thread, letter, body string) string {
	t.Helper()
	dir := filepath.Join(threadsDir, thread)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, letter+".result.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractOpenPointsUsesExactHeadingsOnly(t *testing.T) {
	body := "## Conclusion\nready\n\n## Open Points\n- ship it\n- test it\n\n## Notes\ntext: open points are elsewhere\n"
	if got := extractOpenPoints(body); !reflect.DeepEqual(got, []string{"ship it", "test it"}) {
		t.Fatalf("open points = %#v", got)
	}
	legacy := "## Open Questions\n- legacy item\n"
	if got := extractOpenPoints(legacy); !reflect.DeepEqual(got, []string{"legacy item"}) {
		t.Fatalf("legacy open points = %#v", got)
	}
}

func TestDiffResultSectionsReturnsCurrentChangedTextOnly(t *testing.T) {
	previous := "## Conclusion\nold\n\n## Open Points\n- keep\n"
	current := "## Conclusion\nnew\n\n## Open Points\n- keep\n- decide\n"
	got := diffResultSections(previous, current)
	want := []receiptSection{{Heading: "Conclusion", Body: "new"}, {Heading: "Open Points", Body: "- keep\n- decide"}}
	if !reflect.DeepEqual(got.Sections, want) {
		t.Fatalf("update = %#v, want %#v", got, want)
	}
}

func TestNotifyStoreUpdateDiffBaseKeepsOnlyPreviousGeneration(t *testing.T) {
	store := newNotifyStore(filepath.Join(t.TempDir(), "data"))
	if got, err := store.updateDiffBase("L-0430", []byte("## Conclusion\nfirst\n")); err != nil || len(got.Sections) != 0 {
		t.Fatalf("first base = %#v, %v", got, err)
	}
	got, err := store.updateDiffBase("L-0430", []byte("## Conclusion\nsecond\n"))
	if err != nil || !reflect.DeepEqual(got.Sections, []receiptSection{{Heading: "Conclusion", Body: "second"}}) {
		t.Fatalf("second base = %#v, %v", got, err)
	}
	data, err := os.ReadFile(store.diffBasePath("L-0430"))
	if err != nil || string(data) != "## Conclusion\nsecond\n" {
		t.Fatalf("rolling base = %q, %v", data, err)
	}
}

func TestNotifyStorePruneDiffBasesRemovesOnlyAbsentResults(t *testing.T) {
	store := newNotifyStore(filepath.Join(t.TempDir(), "data"))
	if _, err := store.updateDiffBase("L-0430", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.updateDiffBase("L-0431", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := store.pruneDiffBases([]resultFileInfo{{Letter: "L-0430"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.diffBasePath("L-0430")); err != nil {
		t.Fatalf("active diff base was removed: %v", err)
	}
	if _, err := os.Stat(store.diffBasePath("L-0431")); !os.IsNotExist(err) {
		t.Fatalf("stale diff base still exists: %v", err)
	}
}

func TestNotifyStorePersistsOpenPointsAndUpdateForNewGeneration(t *testing.T) {
	store := newNotifyStore(filepath.Join(t.TempDir(), "data"))
	row := indexResultRow{Letter: "L-0430", Thread: "alpha", Generation: "g1", OpenPoints: []string{"decide"}, Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: "new"}}}}
	if _, err := store.recordArrivalTransition(row); err != nil {
		t.Fatal(err)
	}
	record, err := store.receipt("L-0430")
	if err != nil || !reflect.DeepEqual(record.OpenPoints, row.OpenPoints) || !reflect.DeepEqual(record.Update, row.Update) {
		t.Fatalf("receipt = %#v, %v", record, err)
	}
}

func TestDeclaredSourceSessionPathReadsOnlyFrontMatter(t *testing.T) {
	if got := declaredSourceSessionPath("---\nSource-Session-Path: F:\\external\\session.jsonl\n---\nbody"); got != `F:\external\session.jsonl` {
		t.Fatalf("declaredSourceSessionPath() = %q", got)
	}
	if got := declaredSourceSessionPath("body\nSource-Session-Path: F:\\must-not-match.jsonl"); got != "" {
		t.Fatalf("body text must not be treated as a declaration: %q", got)
	}
}

func TestScanResultFiles(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	writeResultFile(t, threadsDir, "alpha", "L-0100", "---\nID: L-0100\n---\n\n## Conclusion\nfirst answer.\n")
	writeResultFile(t, threadsDir, "alpha", "L-0101", "---\nID: L-0101\nStatus: STUCK\n---\n\n## Blocker\nbudget exhausted.\n")
	// Non-result files must be ignored.
	if err := os.WriteFile(filepath.Join(threadsDir, "alpha", "L-0100.query.md"), []byte("query"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := scanResultFiles(threadsDir)
	if err != nil {
		t.Fatalf("scanResultFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 result files, got %d: %+v", len(files), files)
	}
	byLetter := map[string]resultFileInfo{}
	for _, f := range files {
		byLetter[f.Letter] = f
	}
	if byLetter["L-0100"].Thread != "alpha" {
		t.Errorf("L-0100 thread mismatch: %+v", byLetter["L-0100"])
	}
	if byLetter["L-0101"].Thread != "alpha" {
		t.Errorf("L-0101 thread mismatch: %+v", byLetter["L-0101"])
	}
}

func TestScanResultFilesMissingThreadsDir(t *testing.T) {
	files, err := scanResultFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error for missing threads dir, got %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no files, got %+v", files)
	}
}

func TestResolveLetterResultRequiresOneExactResult(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	path := writeResultFile(t, threadsDir, "alpha", "L-0430", "## Conclusion\nexact source\n")
	got, body, err := resolveLetterResult(threadsDir, "L-0430")
	if err != nil || got.Path != path || string(body) != "## Conclusion\nexact source\n" {
		t.Fatalf("exact result = (%+v, %q, %v)", got, body, err)
	}
	if _, _, err := resolveLetterResult(threadsDir, "0430"); err == nil {
		t.Fatal("accepted an invalid L-ID")
	}
	if _, _, err := resolveLetterResult(threadsDir, "L-9999"); err == nil {
		t.Fatal("resolved a missing RESULT")
	}
	writeResultFile(t, threadsDir, "beta", "L-0430", "duplicate")
	if _, _, err := resolveLetterResult(threadsDir, "L-0430"); err == nil {
		t.Fatal("resolved an ambiguous RESULT")
	}
}

func TestFormatLetterSourceEnvelopeIncludesOnlySuppliedQuery(t *testing.T) {
	withoutQuery := formatLetterSourceEnvelope("L-0430", "F:\\archive\\L-0430.result.md", "", []byte("source"), "")
	if !strings.Contains(withoutQuery, "[LETTER SOURCE]") || strings.Contains(withoutQuery, "[Boss query]") {
		t.Fatalf("envelope without query = %q", withoutQuery)
	}
	withQuery := formatLetterSourceEnvelope("L-0430", "F:\\archive\\L-0430.result.md", "", []byte("source"), "what changed?")
	if !strings.Contains(withQuery, "[Boss query]\nwhat changed?") {
		t.Fatalf("envelope with query = %q", withQuery)
	}
}

func TestExtractResultSummary(t *testing.T) {
	root := t.TempDir()
	donePath := writeResultFile(t, root, "alpha", "L-0100", "---\nID: L-0100\n---\n\n## Conclusion\nfirst answer.\n\n## Options for Boss\n...\n")
	if got := extractResultSummary(donePath); got != "first answer." {
		t.Errorf("DONE summary = %q, want %q", got, "first answer.")
	}
	if got := extractResultStatus(donePath); got != "" {
		t.Errorf("missing status = %q, want empty", got)
	}

	stuckPath := writeResultFile(t, root, "alpha", "L-0101", "---\nID: L-0101\nStatus: STUCK\n---\n\n## Partial Work\nsome\n\n## Blocker\nbudget exhausted.\n")
	if got := extractResultSummary(stuckPath); got != "budget exhausted." {
		t.Errorf("STUCK summary = %q, want %q", got, "budget exhausted.")
	}
	if got := extractResultStatus(stuckPath); got != "STUCK" {
		t.Errorf("STUCK status = %q, want STUCK", got)
	}

	bodyStatusPath := writeResultFile(t, root, "alpha", "L-0102", "ID: L-0102\nStatus: DONE\n---\n\n## Conclusion\nready\n\nStatus: STUCK\n")
	if got := extractResultStatus(bodyStatusPath); got != "DONE" {
		t.Errorf("header status = %q, want DONE", got)
	}
	noHeaderStatusPath := writeResultFile(t, root, "alpha", "L-0103", "ID: L-0103\n---\n\n## Conclusion\nready\n\nStatus: STUCK\n")
	if got := extractResultStatus(noHeaderStatusPath); got != "" {
		t.Errorf("body status = %q, want empty", got)
	}
}

func TestScanNewResultFilesDeliversDispatchedResultsToInbox(t *testing.T) {
	now := time.Now()
	files := []resultFileInfo{
		{Letter: "L-0100", Thread: "alpha", Path: "L-0100.result.md", ModTime: now},
		{Letter: "L-0101", Thread: "alpha", Path: "L-0101.result.md", ModTime: now},
	}
	ledger := notifyLedger{Notified: map[string]string{}}

	// Dispatched and manual RESULTS share the Inbox delivery path.
	fresh := scanNewResultFiles(files, &ledger)
	if len(fresh) != 2 || fresh[0].Letter != "L-0100" || fresh[1].Letter != "L-0101" {
		t.Fatalf("expected both dispatched and manual RESULTS, got %+v", fresh)
	}
	// Both deliveries must be recorded so unchanged generations never re-trigger.
	if _, ok := ledger.Notified["L-0100"]; !ok {
		t.Error("dispatch-covered letter not recorded in ledger")
	}

	// Second scan with unchanged mtimes: nothing new.
	fresh = scanNewResultFiles(files, &ledger)
	if len(fresh) != 0 {
		t.Fatalf("expected no fresh files on rescan, got %+v", fresh)
	}

	// A pursuit-mode edit bumps mtime: must re-fire even though the letter
	// was seen before (L-0429 requires "created or changed").
	files[1].ModTime = now.Add(1 * time.Hour)
	fresh = scanNewResultFiles(files, &ledger)
	if len(fresh) != 1 || fresh[0].Letter != "L-0101" {
		t.Fatalf("expected L-0101 to re-fire after modification, got %+v", fresh)
	}
}

func TestCheckNewResultsEndToEnd(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	indexPath := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# Archive INDEX\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.configureNotify(NotifyConfig{
		Enabled:   true,
		IndexPath: indexPath,
	})

	// First scan seeds an already-existing result without notifying.
	writeResultFile(t, threadsDir, "alpha", "L-0100", "---\nID: L-0100\n---\n\n## Conclusion\npre-existing.\n")
	e.checkNewResults()
	ledger, err := e.notifyStore.load()
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if !ledger.Seeded {
		t.Fatal("expected ledger to be seeded after first scan")
	}
	if _, seen := ledger.Notified["L-0100"]; !seen {
		t.Fatal("pre-existing result must be recorded during seed")
	}

	// A new result written after seeding must be picked up on the next scan
	// with no dependency on INDEX.md ever being touched (L-0429).
	writeResultFile(t, threadsDir, "alpha", "L-0101", "---\nID: L-0101\n---\n\n## Conclusion\nbrand new.\n")
	e.checkNewResults()
	ledger, _ = e.notifyStore.load()
	if _, seen := ledger.Notified["L-0101"]; !seen {
		t.Fatal("new result was not recorded as notified")
	}
}

func TestCheckNewResultsDeliversWhenDiffCacheIsUnavailable(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	indexPath := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# Archive INDEX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.dataDir = root
	e.configureNotify(NotifyConfig{Enabled: true, TelegramEnabled: true, Platform: "telegram", SessionKey: "telegram:123:123", IndexPath: indexPath})

	writeResultFile(t, threadsDir, "alpha", "L-0100", "## Conclusion\nseed\n")
	e.checkNewResults()
	cacheDir := filepath.Join(root, "notify_diff_cache")
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeResultFile(t, threadsDir, "alpha", "L-0101", "## Conclusion\narrived despite cache failure\n")
	e.checkNewResults()
	if got := p.receiptCardsSent; got != 1 {
		t.Fatalf("receipt cards sent = %d, want 1 when diff cache fails", got)
	}
}

func TestCheckNewResultsStoresParsedOpenPointsAndGenerationUpdate(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	indexPath := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# Archive INDEX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.configureNotify(NotifyConfig{Enabled: true, IndexPath: indexPath})
	path := writeResultFile(t, threadsDir, "alpha", "L-0430", "## Conclusion\nfirst\n\n## Open Points\n- decide\n")
	e.checkNewResults()
	if err := os.WriteFile(path, []byte("## Conclusion\nsecond\n\n## Open Points\n- decide\n- ship\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatal(err)
	}
	e.checkNewResults()
	record, err := e.notifyStore.receipt("L-0430")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.OpenPoints, []string{"decide", "ship"}) {
		t.Fatalf("open points = %#v", record.OpenPoints)
	}
	if !reflect.DeepEqual(record.Update.Sections, []receiptSection{{Heading: "Conclusion", Body: "second"}, {Heading: "Open Points", Body: "- decide\n- ship"}}) {
		t.Fatalf("update = %#v", record.Update)
	}
}

// TestCheckNewResultsSkipsMtimeOnlyTouchWithUnchangedContent is a regression
// test for L-0478: scanNewResultFiles' freshness check is mtime-only, so a
// non-substantive filesystem touch (git checkout, an archive script
// rewrite, an editor autosave) that leaves file content byte-identical used
// to be treated exactly like a real RESULT revision — including reopening
// an already-acknowledged/pending-close receipt. checkNewResults must skip
// notifying when the touched file's content matches its cached diff base,
// while still notifying on a genuine content change.
func TestCheckNewResultsSkipsMtimeOnlyTouchWithUnchangedContent(t *testing.T) {
	root := t.TempDir()
	threadsDir := filepath.Join(root, "threads")
	indexPath := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# Archive INDEX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.dataDir = root
	e.configureNotify(NotifyConfig{Enabled: true, TelegramEnabled: true, Platform: "telegram", SessionKey: "telegram:123:123", IndexPath: indexPath})

	// Seed with an empty threads dir so the letter written next is a genuine
	// first arrival, not swallowed by the seed-without-notify path.
	e.checkNewResults()

	body := "## Conclusion\nfirst\n"
	path := writeResultFile(t, threadsDir, "alpha", "L-0500", body)
	e.checkNewResults()
	if p.receiptCardsSent != 1 {
		t.Fatalf("first arrival sent = %d, want 1", p.receiptCardsSent)
	}
	before, err := e.notifyStore.receipt("L-0500")
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}

	// Touch mtime forward without changing a single byte of content.
	touched := time.Now().Add(1 * time.Hour)
	if err := os.Chtimes(path, touched, touched); err != nil {
		t.Fatal(err)
	}
	e.checkNewResults()
	if p.receiptCardsSent != 1 || p.receiptCardsUpdated != 0 {
		t.Fatalf("mtime-only touch must not notify: sent=%d updated=%d, want 1/0", p.receiptCardsSent, p.receiptCardsUpdated)
	}
	after, err := e.notifyStore.receipt("L-0500")
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if after.Generation != before.Generation {
		t.Fatalf("mtime-only touch must not advance the receipt generation: before=%q after=%q", before.Generation, after.Generation)
	}

	// A genuine content change must still notify.
	changed := time.Now().Add(2 * time.Hour)
	if err := os.WriteFile(path, []byte("## Conclusion\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	e.checkNewResults()
	if p.receiptCardsSent != 1 || p.receiptCardsUpdated != 1 {
		t.Fatalf("genuine content change must notify: sent=%d updated=%d, want 1/1", p.receiptCardsSent, p.receiptCardsUpdated)
	}
}

func TestNotifyStoreRoundTrip(t *testing.T) {
	store := newNotifyStore(t.TempDir())
	ledger, err := store.load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if ledger.Seeded {
		t.Fatal("fresh ledger must not be seeded")
	}
	ledger.Seeded = true
	ledger.Notified["L-0042"] = "2026-07-07T00:00:00Z"
	if err := store.save(ledger); err != nil {
		t.Fatalf("save: %v", err)
	}
	back, err := store.load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !back.Seeded || back.Notified["L-0042"] == "" {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestDispatchStoreLetters(t *testing.T) {
	var s *dispatchStore
	if got := s.letters(); len(got) != 0 {
		t.Fatalf("nil store must return empty set, got %v", got)
	}
	s = newDispatchStore(t.TempDir())
	if err := s.upsert(DispatchExpectation{Letter: "L-0200", Thread: "x", To: "dev-pro", State: dispatchStateDispatched}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got := s.letters()
	if !got["L-0200"] {
		t.Fatalf("expected L-0200 in letters set, got %v", got)
	}
}

func TestPsToastEscape(t *testing.T) {
	if got := psToastEscape("Boss's letter"); got != "Boss''s letter" {
		t.Errorf("escape failed: %q", got)
	}
	if got := psToastEscape("no quotes"); got != "no quotes" {
		t.Errorf("no-op case failed: %q", got)
	}
}

func TestNotifyStoreReceiptPersistsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "cc-connect-maintenance", "L-0430", "ID: L-0430\nStatus: DONE\n---\n\nbody\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{
		Letter: "L-0430", Thread: "cc-connect-maintenance", Summary: "ready",
		Path: resultPath, Status: "DONE",
	}); err != nil {
		t.Fatalf("recordArrival: %v", err)
	}
	first, changed, err := store.acknowledge("L-0430", "jay")
	if err != nil || !changed {
		t.Fatalf("first acknowledge = (%+v, %v, %v), want changed receipt", first, changed, err)
	}
	if first.AcknowledgedBy != "jay" || first.AcknowledgedAt == "" || first.ResultPath == "" || first.Status != "DONE" {
		t.Fatalf("first receipt = %+v", first)
	}
	second, changed, err := store.acknowledge("L-0430", "other")
	if err != nil || changed {
		t.Fatalf("second acknowledge = (%+v, %v, %v), want idempotent", second, changed, err)
	}
	if second.AcknowledgedBy != "jay" || second.AcknowledgedAt != first.AcknowledgedAt {
		t.Fatalf("idempotent receipt = %+v, want %+v", second, first)
	}
}

// TestNotifyStoreMarkClosedIsIndependentOfAcknowledge verifies the L-0455
// fix at the storage layer: markClosed must succeed regardless of whether
// acknowledge ran first (closed via a pending-close card after 收件/交主秘书)
// or never ran at all (closed directly from the still-open original card),
// and it must not itself set AcknowledgedAt — the two states are independent.
func TestNotifyStoreMarkClosedIsIndependentOfAcknowledge(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "cc-connect-maintenance", "L-0455", "ID: L-0455\nStatus: DONE\n---\n\nbody\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{Letter: "L-0455", Thread: "cc-connect-maintenance", Summary: "ready", Path: resultPath, Status: "DONE"}); err != nil {
		t.Fatalf("recordArrival: %v", err)
	}

	// Close reachable without ever acknowledging first.
	closed, changed, err := store.markClosed("L-0455")
	if err != nil || !changed {
		t.Fatalf("markClosed = (%+v, %v, %v), want changed", closed, changed, err)
	}
	if closed.AcknowledgedAt != "" {
		t.Fatalf("markClosed must not set AcknowledgedAt: %+v", closed)
	}
	if closed.ClosedAt == "" {
		t.Fatalf("markClosed did not record ClosedAt: %+v", closed)
	}

	// Idempotent: a retry (e.g. after a push-failure retry button) must not
	// error and must not move ClosedAt.
	again, changed, err := store.markClosed("L-0455")
	if err != nil || changed {
		t.Fatalf("second markClosed = (%+v, %v, %v), want idempotent no-op", again, changed, err)
	}
	if again.ClosedAt != closed.ClosedAt {
		t.Fatalf("idempotent markClosed moved ClosedAt: %q -> %q", closed.ClosedAt, again.ClosedAt)
	}
}

// TestNotifyStoreMarkClosedAfterAcknowledgeLeavesAcknowledgedAtIntact covers
// the other order: 收件/交主秘书 first, then close from the resulting
// pending-close card. Closing must not disturb the existing acknowledgment.
func TestNotifyStoreMarkClosedAfterAcknowledgeLeavesAcknowledgedAtIntact(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "cc-connect-maintenance", "L-0455", "ID: L-0455\nStatus: DONE\n---\n\nbody\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{Letter: "L-0455", Thread: "cc-connect-maintenance", Summary: "ready", Path: resultPath, Status: "DONE"}); err != nil {
		t.Fatalf("recordArrival: %v", err)
	}
	acked, _, err := store.acknowledge("L-0455", "jay")
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	closed, changed, err := store.markClosed("L-0455")
	if err != nil || !changed {
		t.Fatalf("markClosed after acknowledge = (%+v, %v, %v), want changed", closed, changed, err)
	}
	if closed.AcknowledgedAt != acked.AcknowledgedAt || closed.AcknowledgedBy != "jay" {
		t.Fatalf("close disturbed the existing acknowledgment: %+v", closed)
	}
}

func TestNotifyStoreKeepsOriginalResultPathAtArrival(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0431", "ID: L-0431\nStatus: DONE\n---\n\n## Conclusion\noriginal\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{Letter: "L-0431", Thread: "alpha", Path: resultPath, Status: "DONE"}); err != nil {
		t.Fatalf("record arrival: %v", err)
	}
	ledger, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	record := ledger.Receipts["L-0431"]
	if got, want := record.ResultPath, resultPath; got != want {
		t.Fatalf("result path = %q, want %q", got, want)
	}
}

func TestNotifyStoreReceiptGenerationReplacesPendingAndReopensAcknowledged(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0430", "body")
	store := newNotifyStore(filepath.Join(root, "data"))
	first := indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Summary: "first", Status: "DONE", Generation: "2026-07-16T20:00:00Z"}
	if _, err := store.recordArrivalTransition(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Summary, second.Generation = "second", "2026-07-16T20:01:00Z"
	arrival, err := store.recordArrivalTransition(second)
	if err != nil || !arrival.Replaced || arrival.Receipt.Summary != "second" || arrival.Receipt.AcknowledgedAt != "" {
		t.Fatalf("pending replacement = %+v, %v", arrival, err)
	}
	if _, changed, err := store.acknowledge("L-0430", "jay"); err != nil || !changed {
		t.Fatalf("acknowledge = (%v, %v)", changed, err)
	}
	third := second
	third.Summary, third.Generation = "third", "2026-07-16T20:02:00Z"
	arrival, err = store.recordArrivalTransition(third)
	if err != nil || !arrival.Replaced || arrival.Receipt.AcknowledgedAt != "" || arrival.Receipt.Summary != "third" {
		t.Fatalf("acknowledged re-entry = %+v, %v", arrival, err)
	}
}

// TestNotifyStoreNewGenerationResetsClosedAt mirrors the existing
// AcknowledgedAt-reset-on-new-generation behavior for ClosedAt (L-0455): a
// freshly-arrived RESULT update supersedes a prior close the same way it
// already supersedes a prior acknowledgment, since the archived CLOSED row
// belongs to the old content, not the new one.
func TestNotifyStoreNewGenerationResetsClosedAt(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0430", "body")
	store := newNotifyStore(filepath.Join(root, "data"))
	first := indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Summary: "first", Status: "DONE", Generation: "2026-07-16T20:00:00Z"}
	if _, err := store.recordArrivalTransition(first); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := store.markClosed("L-0430"); err != nil || !changed {
		t.Fatalf("markClosed = (%v, %v)", changed, err)
	}
	second := first
	second.Summary, second.Generation = "second", "2026-07-16T20:01:00Z"
	arrival, err := store.recordArrivalTransition(second)
	if err != nil || !arrival.Replaced || arrival.Receipt.ClosedAt != "" {
		t.Fatalf("new generation must reset ClosedAt = %+v, %v", arrival, err)
	}
}

func TestNotifyStorePreservesFullReceiptSummaryWithoutCreatingSnapshot(t *testing.T) {
	root := t.TempDir()
	body := "ID: L-0430\nStatus: DONE\n---\n\nimmutable body\n"
	resultPath := writeResultFile(t, root, "alpha", "L-0430", body)
	store := newNotifyStore(filepath.Join(root, "data"))
	longSummary := strings.Repeat("legacy summary ", 40)
	if err := store.save(notifyLedger{Receipts: map[string]receiptRecord{
		"L-0430": {Thread: "alpha", ResultPath: resultPath, Summary: longSummary, Status: "DONE", ArrivedAt: "2026-07-16T16:15:01Z"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.recordArrival(indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Summary: longSummary, Status: "DONE"}); err != nil {
		t.Fatal(err)
	}
	record, err := store.receipt("L-0430")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := record.ResultPath, resultPath; got != want {
		t.Fatalf("legacy result path = %q, want %q", got, want)
	}
	if got, want := record.Summary, longSummary; got != want {
		t.Fatalf("summary = %q, want full %q", got, want)
	}
}

func TestFormatInboxBoardCompactQueue(t *testing.T) {
	entries := []inboxQueueEntry{
		{Letter: "L-0448", Thread: "agent-memory-evolution", To: "architect", Date: "2026-07-18", Summary: "评估单体 Agent 记忆沉淀与进化设计"},
		{Letter: "L-0447", Thread: "cc-connect-maintenance", To: "architect", Date: "2026-07-18", Summary: "设计 Telegram Outbox 发件箱卡片"},
	}
	got := formatInboxBoard(NewI18n(LangChinese), entries, nil)
	want := "📥 收件箱待处理队列 (2)\n" +
		"• [L-0448] (agent-memory-evolution) To: architect — 评估单体 Agent 记忆沉淀与进化设计 (2026-07-18)\n" +
		"• [L-0447] (cc-connect-maintenance) To: architect — 设计 Telegram Outbox 发件箱卡片 (2026-07-18)"
	if got != want {
		t.Fatalf("board =\n%s\nwant\n%s", got, want)
	}
	if empty := formatInboxBoard(NewI18n(LangEnglish), nil, nil); empty != "📥 Inbox is empty — no pending RESULT cards." {
		t.Fatalf("empty board = %q", empty)
	}
}

func TestFormatInboxBoardIncludesPendingCloseSection(t *testing.T) {
	entries := []inboxQueueEntry{
		{Letter: "L-0448", Thread: "agent-memory-evolution", To: "architect", Date: "2026-07-18", Summary: "评估单体 Agent 记忆沉淀与进化设计"},
	}
	pendingClose := []inboxQueueEntry{
		{Letter: "L-0449", Thread: "cc-connect-maintenance", To: "architect", Date: "2026-07-17", Summary: "一键封信按钮设计交付"},
	}
	got := formatInboxBoard(NewI18n(LangChinese), entries, pendingClose)
	want := "📥 收件箱待处理队列 (1)\n" +
		"• [L-0448] (agent-memory-evolution) To: architect — 评估单体 Agent 记忆沉淀与进化设计 (2026-07-18)\n\n" +
		"🔒 待封信 (1)\n" +
		"• [L-0449] (cc-connect-maintenance) To: architect — 一键封信按钮设计交付 (2026-07-17)"
	if got != want {
		t.Fatalf("board =\n%s\nwant\n%s", got, want)
	}
	pendingOnly := formatInboxBoard(NewI18n(LangChinese), nil, pendingClose)
	wantPendingOnly := "🔒 待封信 (1)\n" +
		"• [L-0449] (cc-connect-maintenance) To: architect — 一键封信按钮设计交付 (2026-07-17)"
	if pendingOnly != wantPendingOnly {
		t.Fatalf("pending-only board =\n%s\nwant\n%s", pendingOnly, wantPendingOnly)
	}
}

func TestCollectPendingInboxEntriesSkipsAcknowledged(t *testing.T) {
	root := t.TempDir()
	path := writeResultFile(t, root, "alpha", "L-0451", "ID: L-0451\nTo: architect\nFrom: secretary-seat\nStatus: DONE\n---\n\n## Conclusion\nready\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{
		Letter: "L-0451", Thread: "alpha", Path: path, To: "architect", From: "secretary-seat",
		Summary: "ready", Status: "DONE", Generation: "2026-07-18T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.recordArrival(indexResultRow{
		Letter: "L-0449", Thread: "alpha", Path: path, Summary: "done", Status: "DONE", Generation: "2026-07-17T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.acknowledge("L-0449", "jay"); err != nil {
		t.Fatal(err)
	}
	ledger, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	entries := collectPendingInboxEntries(ledger)
	if len(entries) != 1 || entries[0].Letter != "L-0451" || entries[0].To != "architect" {
		t.Fatalf("entries = %#v", entries)
	}
}

// TestCollectPendingInboxEntriesSkipsClosedButUnacknowledged covers a gap
// L-0455's own decoupling opened: a letter can now be closed directly from
// the still-open original card without ever being acknowledged, so
// AcknowledgedAt alone is no longer sufficient to detect "still pending".
// Without also excluding ClosedAt, such a letter would keep showing up in
// /inbox with a card whose every button now replies MsgReceiptUnavailable.
func TestCollectPendingInboxEntriesSkipsClosedButUnacknowledged(t *testing.T) {
	root := t.TempDir()
	path := writeResultFile(t, root, "alpha", "L-0455", "ID: L-0455\nTo: architect\nStatus: DONE\n---\n\n## Conclusion\nready\n")
	store := newNotifyStore(filepath.Join(root, "data"))
	if err := store.recordArrival(indexResultRow{
		Letter: "L-0455", Thread: "alpha", Path: path, To: "architect",
		Summary: "ready", Status: "DONE", Generation: "2026-07-18T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := store.markClosed("L-0455"); err != nil || !changed {
		t.Fatalf("markClosed = (%v, %v)", changed, err)
	}
	ledger, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if entries := collectPendingInboxEntries(ledger); len(entries) != 0 {
		t.Fatalf("closed-but-unacknowledged letter must not appear as pending: %#v", entries)
	}
	if entries := collectPendingCloseEntries(ledger); len(entries) != 0 {
		t.Fatalf("closed letter must not appear in the pending-close section either: %#v", entries)
	}
}

func TestReceiptEnvelopeGivesAgentOriginalResultPath(t *testing.T) {
	got := formatReceiptEnvelope("L-0430", receiptRecord{
		Thread:     "cc-connect-maintenance",
		Status:     "DONE",
		ResultPath: "F:\\nexus\\docs\\archive\\threads\\cc-connect-maintenance\\L-0430.result.md",
	})
	want := "[RECEIPT L-0430]\n原信文件：F:\\nexus\\docs\\archive\\threads\\cc-connect-maintenance\\L-0430.result.md\n线程：cc-connect-maintenance\n状态：DONE\n\n请直接读取上述 RESULT 原信，并按正常译信流程处理。"
	if got != want {
		t.Errorf("receipt envelope = %q, want %q", got, want)
	}
}

func TestReceiptInboxCardRendersOpenPointsAndShortUpdateInline(t *testing.T) {
	record := receiptRecord{
		Thread: "alpha", Status: "DONE", Summary: "ready", ArrivedAt: "2026-07-16T20:00:00Z", ResultPath: "C:\\x.md", Generation: "g1",
		OpenPoints: []string{"decide retention"}, Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: "new text"}}},
	}
	content, buttons := formatReceiptInboxCard(NewI18n(LangEnglish), "L-0430", record, "", 0, 0)
	for _, want := range []string{"📬 L-0430 · Updated", "Open points:", "• decide retention", "Changes:", "Conclusion\nnew text"} {
		if !strings.Contains(content, want) {
			t.Fatalf("card missing %q: %s", want, content)
		}
	}
	if len(buttons) != 2 || len(buttons[0]) != 3 || len(buttons[1]) != 1 {
		t.Fatalf("short update buttons = %#v", buttons)
	}
	if got := buttons[1][0].Data; got != "cmd:/receipt close L-0430 g1" {
		t.Fatalf("close button = %q", got)
	}
}

func TestReceiptInboxCardAddsUpdateButtonOnlyForLongUpdate(t *testing.T) {
	record := receiptRecord{Generation: "g1", Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: strings.Repeat("x", receiptCompactUpdateLimit)}}}}
	content, buttons := formatReceiptInboxCard(NewI18n(LangEnglish), "L-0430", record, "", 0, 0)
	if strings.Contains(content, strings.Repeat("x", receiptCompactUpdateLimit)) {
		t.Fatalf("long update leaked into compact card")
	}
	found := false
	for _, row := range buttons {
		for _, button := range row {
			if button.Data == "cmd:/receipt update L-0430 g1 0" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("missing conditional update button: %#v", buttons)
	}
}

func TestReceiptInboxCardUsesTotalCompactBudget(t *testing.T) {
	record := receiptRecord{Summary: strings.Repeat("s", receiptCompactTextLimit), Generation: "g1", Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: "short update"}}}}
	content, buttons := formatReceiptInboxCard(NewI18n(LangEnglish), "L-0430", record, "", 0, 0)
	if strings.Contains(content, "short update") {
		t.Fatalf("update exceeded total compact budget: %q", content)
	}
	found := false
	for _, row := range buttons {
		for _, button := range row {
			if button.Data == "cmd:/receipt update L-0430 g1 0" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing update button after base card consumed budget")
	}
}

func TestReceiptInboxCardLocalizesUpdateLabels(t *testing.T) {
	record := receiptRecord{Generation: "g1", OpenPoints: []string{"决定"}, Update: receiptUpdate{Sections: []receiptSection{{Heading: "Conclusion", Body: "新内容"}}}}
	content, _ := formatReceiptInboxCard(NewI18n(LangChinese), "L-0430", record, "", 0, 0)
	for _, want := range []string{"📬 L-0430 · 已更新", "开放点：", "本次更新："} {
		if !strings.Contains(content, want) {
			t.Fatalf("localized card missing %q: %s", want, content)
		}
	}
}

// TestReceiptInboxCardPaginatesOriginalResultWithoutHash also guards the
// L-0460 follow-up fix: a page must use the short "letter · thread" header,
// not the full compact preamble (Summary/OpenPoints/Result path), since that
// preamble is unbounded in length and, repeated on every page, could push a
// long RESULT's rendered page past Telegram's message length limit.
func TestReceiptInboxCardPaginatesOriginalResultWithoutHash(t *testing.T) {
	record := receiptRecord{
		Thread: "alpha", Status: "DONE", Summary: "ready", ArrivedAt: "2026-07-16T16:20:00Z",
		ResultPath: "F:\\nexus\\docs\\archive\\threads\\alpha\\L-0431.result.md",
	}
	content, buttons := formatReceiptInboxCard(NewI18n(LangEnglish), "L-0431", record, "first page\nsecond page", 0, 2)
	if !strings.Contains(content, "📬 L-0431") || !strings.Contains(content, "alpha") || strings.Contains(content, "Result path:") || strings.Contains(content, "SHA-256") || !strings.Contains(content, "Page 1/2") {
		t.Fatalf("inbox card content = %q", content)
	}
	if got := buttons[0][0].Text; got != "Next →" {
		t.Fatalf("next button = %q", got)
	}
	if got := buttons[0][0].Data; got != "cmd:/receipt page L-0431 2026-07-16T16:20:00Z 1" {
		t.Fatalf("next button = %q", got)
	}
	if got := buttons[len(buttons)-1][0].Data; got != "cmd:/receipt close L-0431 2026-07-16T16:20:00Z" {
		t.Fatalf("close button = %q", got)
	}
	actionRow := buttons[len(buttons)-2]
	if got := actionRow[0].Data; got != "cmd:/receipt collapse L-0431 2026-07-16T16:20:00Z" {
		t.Fatalf("collapse button = %q", got)
	}
	if got := actionRow[1].Data; got != "cmd:/receipt receive L-0431 2026-07-16T16:20:00Z" {
		t.Fatalf("receive button = %q", got)
	}
	if got := actionRow[2].Data; got != "cmd:/receipt primary L-0431 2026-07-16T16:20:00Z" {
		t.Fatalf("primary button = %q", got)
	}

	_, buttons = formatReceiptInboxCard(NewI18n(LangEnglish), "L-0431", record, "", 0, 0)
	if got := buttons[0][0].Text; got != "View original" {
		t.Fatalf("view-original button = %q", got)
	}
}

// TestReceiptInboxCardPageStaysUnderTelegramLimitWithLongSummary is a
// regression test for L-0460's follow-up bug (reported live against L-0458):
// a RESULT with a long Summary and several long OpenPoints, combined with a
// near-full-size original page, used to push the rendered page past
// Telegram's ~4096-character message limit, so UpdateMessageWithButtons
// failed with MESSAGE_TOO_LONG and Boss saw "Receipt is unavailable"
// instead of the original letter.
func TestReceiptInboxCardPageStaysUnderTelegramLimitWithLongSummary(t *testing.T) {
	longSummary := strings.Repeat("架构审查结论很长，包含多个子系统的详细描述。", 20) // ~460 runes
	longOpenPoints := []string{
		strings.Repeat("第一个未决问题的详细说明，包含背景和后续建议。", 10),
		strings.Repeat("第二个未决问题的详细说明，包含背景和后续建议。", 10),
		strings.Repeat("第三个未决问题的详细说明，包含背景和后续建议。", 10),
	}
	record := receiptRecord{
		Thread: "resonova-architecture", Status: "DONE", Summary: longSummary, ArrivedAt: "2026-07-19T06:44:15Z",
		ResultPath: "F:\\nexus-archive\\threads\\resonova-architecture\\L-0458.result.md",
		OpenPoints: longOpenPoints,
	}
	page := strings.Repeat("x", 3000) // receiptOriginalPages' pageSize
	content, _ := formatReceiptInboxCard(NewI18n(LangChinese), "L-0458", record, page, 0, 5)
	if n := len([]rune(content)); n > 4096 {
		t.Fatalf("paginated card content is %d runes, exceeds Telegram's message limit: a long Summary/OpenPoints must not leak into a page render", n)
	}
}

func TestNotifyLetterArrivedSendsShortPlatformMessageWithoutAgentTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.configureNotify(NotifyConfig{
		TelegramEnabled: true,
		ToastEnabled:    false,
		Platform:        "telegram",
		SessionKey:      "telegram:123:123",
	})

	e.notifyLetterArrived(indexResultRow{
		Letter:  "L-0430",
		Thread:  "cc-connect-maintenance",
		Summary: "notification context is decoupled.",
	})

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one direct notification", sent)
	}
	if got, want := sent[0], "📬 L-0430 到货"; got != want {
		t.Fatalf("notification = %q, want %q", got, want)
	}
	if strings.Contains(sent[0], "[LETTER_ARRIVED]") {
		t.Fatalf("notification must not use agent-injected marker: %q", sent[0])
	}
}

func TestNotifyLetterArrivedDoesNotAdvertiseReceiptWithoutStore(t *testing.T) {
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.configureNotify(NotifyConfig{
		TelegramEnabled: true,
		ToastEnabled:    false,
		Platform:        "telegram",
		SessionKey:      "telegram:123:123",
	})

	e.notifyLetterArrived(indexResultRow{Letter: "L-0430", Thread: "cc-connect-maintenance"})

	if len(p.buttonRows) != 0 {
		t.Fatalf("receipt button advertised without store: %#v", p.buttonRows)
	}
	if got, want := p.getSent(), []string{"📬 L-0430 到货"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plain notification = %#v, want %#v", got, want)
	}
}

func TestNotifyLetterArrivedUpdatesPendingCardForNewGeneration(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0430", "body")
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	e.notifyConfig = NotifyConfig{TelegramEnabled: true, Platform: "telegram", SessionKey: "telegram:123:123"}
	first := indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Status: "DONE", Summary: "first", Generation: "2026-07-16T20:00:00Z"}
	e.notifyLetterArrived(first)
	second := first
	second.Summary, second.Generation = "second", "2026-07-16T20:01:00Z"
	e.notifyLetterArrived(second)
	if p.receiptCardsSent != 1 || p.receiptCardsUpdated != 1 {
		t.Fatalf("card lifecycle = send %d update %d, want 1/1", p.receiptCardsSent, p.receiptCardsUpdated)
	}
	if !strings.Contains(p.updatedContent, "second") {
		t.Fatalf("updated card = %q", p.updatedContent)
	}
}

// TestNotifyLetterArrivedReopensPendingCloseCardInPlace is a regression test
// for L-0478: a letter acknowledged into pending-close, then hit by a new
// arrival generation (real revision or mtime-only touch that slipped past
// the L-0478 content-unchanged guard), used to orphan the pending-close card
// forever and mint a brand-new full inbox card next to it — the AcknowledgedAt
// == "" condition this branch used to require made it unreachable once a
// letter had been acknowledged, since recordArrivalTransition also nulls
// arrival.Receipt.Card on that same reset. Reopening must instead edit the
// existing pending-close card in place into the fresh full inbox card.
func TestNotifyLetterArrivedReopensPendingCloseCardInPlace(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0430", "body")
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	e.notifyConfig = NotifyConfig{TelegramEnabled: true, Platform: "telegram", SessionKey: "telegram:123:123"}

	first := indexResultRow{Letter: "L-0430", Thread: "alpha", Path: resultPath, Status: "DONE", Summary: "first", Generation: "2026-07-16T20:00:00Z"}
	e.notifyLetterArrived(first)
	if p.receiptCardsSent != 1 {
		t.Fatalf("initial arrival sent = %d, want 1", p.receiptCardsSent)
	}

	receipt, _, err := e.notifyStore.acknowledge("L-0430", "boss")
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	e.sendPendingCloseCard("L-0430", receipt)
	if p.receiptCardsSent != 2 {
		t.Fatalf("pending-close card sent = %d, want 2 (a new message, distinct from the original inbox card)", p.receiptCardsSent)
	}

	second := first
	second.Summary, second.Generation = "second", "2026-07-16T20:01:00Z"
	e.notifyLetterArrived(second)

	if p.receiptCardsSent != 2 {
		t.Fatalf("reopen must not mint a third card, sent = %d, want 2", p.receiptCardsSent)
	}
	if p.receiptCardsUpdated != 1 {
		t.Fatalf("reopen must edit the pending-close card in place, updated = %d, want 1", p.receiptCardsUpdated)
	}
	if !strings.Contains(p.updatedContent, "second") {
		t.Fatalf("reopened card content = %q, want it to carry the new generation's summary", p.updatedContent)
	}
	if strings.Contains(p.updatedContent, "pending close") {
		t.Fatalf("reopened card must become the full inbox card, not stay a pending-close card: %q", p.updatedContent)
	}
}
