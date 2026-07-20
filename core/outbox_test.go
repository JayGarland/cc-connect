package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetryOutboxCleanupKeepsDispatchedCardUntilDeleteSucceeds(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}, deleteErr: errors.New("telegram unavailable")}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.platforms = []Platform{p}
	e.outboxConfig = OutboxConfig{Platform: "telegram"}
	e.outboxRecords = map[string]outboxRecord{
		"L-0100": {Dispatched: true, Card: &MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3}},
	}

	e.retryOutboxCleanup()
	if !e.outboxRecords["L-0100"].Dispatched {
		t.Fatal("failed delete must retain the dispatched card for retry")
	}

	p.deleteErr = nil
	e.retryOutboxCleanup()
	if _, ok := e.outboxRecords["L-0100"]; ok {
		t.Fatal("successful retry must remove the dispatched card record")
	}
}

func TestMarkOutboxDispatchedMarksCardWhenDeleteFails(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}, deleteErr: errors.New("telegram unavailable")}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.outboxRecords = map[string]outboxRecord{
		"L-0100": {Card: &MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3}},
	}

	e.markOutboxDispatched(p, "L-0100", "callback-card")
	record, ok := e.outboxRecords["L-0100"]
	if !ok || !record.Dispatched {
		t.Fatal("failed delete must preserve a dispatched cleanup record")
	}
	if !strings.Contains(p.updatedContent, "已分发") || len(p.updatedButtons) != 0 {
		t.Fatalf("fallback card = content:%q buttons:%#v", p.updatedContent, p.updatedButtons)
	}
}

func TestHandleOutboxCommandExcludesDispatchedRecords(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.outboxRecords = map[string]outboxRecord{
		"L-0100": {Dispatched: true, To: "dev-pro", Route: "heavy", Thread: "alpha"},
		"L-0101": {To: "dev-pro", Route: "heavy", Thread: "alpha"},
	}

	e.handleOutboxCommand(p, &Message{ReplyCtx: "chat"}, nil)
	got := strings.Join(p.getSent(), "\n")
	if strings.Contains(got, "L-0100") || !strings.Contains(got, "L-0101") {
		t.Fatalf("pending outbox = %q; dispatched records must be excluded", got)
	}
}

func writeQueryFile(t *testing.T, threadsDir, thread, letter, body string) string {
	t.Helper()
	dir := filepath.Join(threadsDir, thread)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, letter+".query.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanOutboxQueriesRequiresRegisteredUndispatchedQuery(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nShip it\n")
	writeQueryFile(t, threads, "alpha", "L-0101", "---\nID: L-0101\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n")
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n| L-0101 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanOutboxQueries(threads, index, map[string]bool{"L-0101": true})
	if err != nil || len(got) != 1 || got[0].Letter != "L-0100" {
		t.Fatalf("outbox = %#v, %v", got, err)
	}
}

func TestScanOutboxQueriesExcludesTerminalLetters(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	for _, letter := range []string{"L-0100", "L-0101", "L-0102"} {
		writeQueryFile(t, threads, "alpha", letter, "---\nID: "+letter+"\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nShip it\n")
	}
	indexRows := "| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n" +
		"| L-0100 | RESULT | alpha | ROOT | delivered | 2026-07-18 |\n" +
		"| L-0101 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n" +
		"| L-0101 | CLOSED | alpha | ROOT | accepted | 2026-07-18 |\n" +
		"| L-0102 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"
	if err := os.WriteFile(index, []byte(indexRows), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanOutboxQueries(threads, index, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Letter != "L-0102" {
		t.Fatalf("outbox = %#v; terminal letters must be excluded", got)
	}
}

func TestScanOutboxQueriesExcludesWrittenResultWithoutIndexResult(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nShip it\n")
	if err := os.WriteFile(filepath.Join(threads, "alpha", "L-0100.result.md"), []byte("---\nID: L-0100\nType: RESULT\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanOutboxQueries(threads, index, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("outbox = %#v; written RESULT must be terminal even without INDEX RESULT", got)
	}
}

func TestFormatOutboxCardShowsMetadataAndReadOnlyButtons(t *testing.T) {
	content, buttons := formatOutboxCard(NewI18n(LangEnglish), outboxRecord{Thread: "alpha", To: "dev-pro", Route: "heavy", QueryPath: "F:\\archive\\L-0100.query.md", Generation: "g1", Summary: "Ship it"}, "L-0100", "", 0, 0)
	for _, want := range []string{"📤 L-0100", "To: dev-pro", "Route: heavy", "Ship it"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %q", want, content)
		}
	}
	if len(buttons) != 1 || len(buttons[0]) != 3 || buttons[0][0].Data != "cmd:/outbox page L-0100 g1 0" || buttons[0][1].Data != "cmd:/outbox manual L-0100 g1" || buttons[0][2].Data != "cmd:/outbox secretary L-0100 g1" {
		t.Fatalf("buttons = %#v", buttons)
	}
}

func TestOutboxManualStatePersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.outboxManual = map[string]bool{"L-0100": true}
	if err := e.saveOutboxManual(); err != nil {
		t.Fatal(err)
	}
	restarted := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	restarted.dataDir = root
	if !restarted.loadOutboxManual()["L-0100"] {
		t.Fatal("manual outbox state was not persisted")
	}
}

func TestOutboxLedgerPersistsCardAndCleanupState(t *testing.T) {
	root := t.TempDir()
	store := newOutboxStore(root)
	want := outboxRecord{Thread: "alpha", QueryPath: "query.md", Generation: "digest", Dispatched: true, Card: &MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3}}
	if err := store.save(outboxLedger{Records: map[string]outboxRecord{"L-0100": want}}); err != nil {
		t.Fatal(err)
	}
	got, err := newOutboxStore(root).load()
	if err != nil {
		t.Fatal(err)
	}
	record := got.Records["L-0100"]
	if record.Generation != want.Generation || !record.Dispatched || record.Card == nil || record.Card.MessageID != 3 {
		t.Fatalf("reloaded record = %#v", record)
	}
}

func TestPublishOutboxRetriesSameGenerationWithoutCard(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.outboxConfig = OutboxConfig{Platform: "telegram", SessionKey: "telegram:123:123"}
	e.outboxRecords = map[string]outboxRecord{"L-0100": {Generation: "digest"}}
	e.publishOutbox(queryFileInfo{Letter: "L-0100", Thread: "alpha", To: "dev-pro", Route: "heavy", Path: "L-0100.query.md", Summary: "queued", Digest: "digest"})
	if p.receiptCardsSent != 1 {
		t.Fatalf("card sends = %d, want retry for a record without a card", p.receiptCardsSent)
	}
}

func TestOutboxFailedSendPersistsRetryBackoff(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}, sendErr: errors.New("unavailable")}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.outboxConfig = OutboxConfig{Platform: "telegram", SessionKey: "telegram:123:123"}
	e.outboxRecords = map[string]outboxRecord{}
	e.publishOutbox(queryFileInfo{Letter: "L-0100", Thread: "alpha", To: "dev-pro", Route: "heavy", Path: "L-0100.query.md", Summary: "queued", Digest: "digest"})
	record := e.outboxRecords["L-0100"]
	if record.Attempts != 1 || record.RetryAt.IsZero() { t.Fatalf("retry state = %#v", record) }
	e.publishOutbox(queryFileInfo{Letter: "L-0100", Thread: "alpha", To: "dev-pro", Route: "heavy", Path: "L-0100.query.md", Summary: "queued", Digest: "digest"})
	if p.receiptCardsSent != 1 { t.Fatalf("backoff sends = %d, want 1", p.receiptCardsSent) }
}

func TestCheckOutboxPublishesAfterPlanningLockIsReleased(t *testing.T) {
	// publishOutbox owns its own brief state locks. This regression calls the
	// watcher path, which used to hold outboxMu across SendReceiptCard.
	root := t.TempDir()
	threads, index := filepath.Join(root, "threads"), filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil { t.Fatal(err) }
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nqueued\n")
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.dataDir, e.outboxConfig, e.outboxRecords, e.outboxManual, e.outboxSeeded = root, OutboxConfig{Enabled: true, IndexPath: index, Platform: "telegram", SessionKey: "telegram:123:123"}, map[string]outboxRecord{}, map[string]bool{}, true
	e.checkOutbox()
	if p.receiptCardsSent != 1 { t.Fatalf("send count = %d, want 1", p.receiptCardsSent) }
}

func TestMarkOutboxDispatchedPersistsCleanupRecord(t *testing.T) {
	root := t.TempDir()
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}, deleteErr: errors.New("telegram unavailable")}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.outboxStore = newOutboxStore(root)
	e.outboxRecords = map[string]outboxRecord{"L-0100": {Card: &MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3}}}
	e.markOutboxDispatched(p, "L-0100", "callback-card")
	ledger, err := newOutboxStore(root).load()
	if err != nil || !ledger.Records["L-0100"].Dispatched {
		t.Fatalf("durable cleanup record = %#v, %v", ledger, err)
	}
}

func TestContentDigestIgnoresMtimeAndChangesWithContent(t *testing.T) {
	first := contentDigest([]byte("first"))
	if first == "" || first != contentDigest([]byte("first")) {
		t.Fatalf("digest must be stable: %q", first)
	}
	if first == contentDigest([]byte("second")) {
		t.Fatal("different content must have a distinct digest")
	}
}

func TestOutboxCallbackDataFitsTelegramLimit(t *testing.T) {
	record := outboxRecord{Thread: "alpha", To: "dev-pro", Route: "heavy", QueryPath: "L-0100.query.md", Generation: contentDigest([]byte("query"))}
	_, buttons := formatOutboxCard(NewI18n(LangEnglish), record, "L-0100", "", 0, 0)
	for _, row := range buttons {
		for _, button := range row {
			if len([]byte(button.Data)) > 64 {
				t.Fatalf("callback payload exceeds Telegram 64-byte limit: %d %q", len([]byte(button.Data)), button.Data)
			}
		}
	}
}

func TestScanOutboxQueriesCarriesContentDigest(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	body := "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nShip it\n"
	writeQueryFile(t, threads, "alpha", "L-0100", body)
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil { t.Fatal(err) }
	got, err := scanOutboxQueries(threads, index, nil)
	if err != nil || len(got) != 1 || got[0].Digest != contentDigest([]byte(body)) { t.Fatalf("query = %#v, %v", got, err) }
}

func TestOutboxFirstScanIsQuietBaseline(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nold\n")
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index}
	e.outboxRecords = map[string]outboxRecord{}
	e.outboxManual = map[string]bool{}
	e.checkOutbox()
	if !e.outboxSeeded || len(e.outboxRecords) != 1 {
		t.Fatalf("baseline = seeded:%v records:%#v", e.outboxSeeded, e.outboxRecords)
	}
	ledger, err := newOutboxStore(root).load()
	if err != nil || !ledger.Seeded || len(ledger.Records) != 1 {
		t.Fatalf("durable baseline = %#v, %v", ledger, err)
	}
}
