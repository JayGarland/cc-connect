package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestHandleOutboxCommandShowsDefaultForRouteLessRecord(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.outboxRecords = map[string]outboxRecord{
		"L-0101": {To: "dev-pro", Thread: "alpha"},
	}

	e.handleOutboxCommand(p, &Message{ReplyCtx: "chat"}, nil)
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "L-0101 · dev-pro · default · alpha") {
		t.Fatalf("route-less list entry = %q", got)
	}
}

func TestScanOutboxQueriesAllowsRegisteredQueryWithoutRoute(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nDate: 2026-07-18\n---\n\n## Query\nShip it\n")
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanOutboxQueries(threads, index, nil)
	if err != nil || len(got) != 1 || got[0].Route != "" {
		t.Fatalf("outbox = %#v, %v", got, err)
	}
}

func TestScanOutboxQueriesRejectsIncompleteIdentityFrontMatter(t *testing.T) {
	tests := []struct {
		name   string
		letter string
		body   string
	}{
		{name: "wrong ID", letter: "L-0100", body: "---\nID: L-9999\nThread: alpha\nType: QUERY\nTo: dev-pro\nDate: 2026-07-18\n---\n"},
		{name: "missing ID", letter: "L-0101", body: "---\nThread: alpha\nType: QUERY\nTo: dev-pro\nDate: 2026-07-18\n---\n"},
		{name: "non-query type", letter: "L-0102", body: "---\nID: L-0102\nThread: alpha\nType: RESULT\nTo: dev-pro\nDate: 2026-07-18\n---\n"},
		{name: "missing thread", letter: "L-0103", body: "---\nID: L-0103\nType: QUERY\nTo: dev-pro\nDate: 2026-07-18\n---\n"},
		{name: "missing recipient", letter: "L-0104", body: "---\nID: L-0104\nThread: alpha\nType: QUERY\nDate: 2026-07-18\n---\n"},
		{name: "missing date", letter: "L-0105", body: "---\nID: L-0105\nThread: alpha\nType: QUERY\nTo: dev-pro\n---\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			threads := filepath.Join(root, "threads")
			index := filepath.Join(root, "INDEX.md")
			writeQueryFile(t, threads, "alpha", tt.letter, tt.body)
			if err := os.WriteFile(index, []byte("| "+tt.letter+" | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			got, err := scanOutboxQueries(threads, index, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("outbox = %#v; incomplete identity must be rejected", got)
			}
		})
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

func TestHandleOutboxManualStaleCardExplainsResultAlreadyArrived(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	writeResultFile(t, threads, "alpha", "L-0100", "---\nStatus: DONE\n---\n")
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.outboxConfig = OutboxConfig{Enabled: true, IndexPath: filepath.Join(root, "INDEX.md")}
	e.outboxRecords = map[string]outboxRecord{}
	if !e.handleOutboxCommand(p, &Message{ReplyCtx: "reply"}, []string{"manual", "L-0100", "old"}) {
		t.Fatal("command not handled")
	}
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "already completed") {
		t.Fatalf("reply = %q", got)
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

// TestFormatOutboxCardDirectStartButtonLabel is the L-0717 regression gate:
// the outbox card's dispatch-actuator button must be labeled "⚡ 直接开始"
// (not the misleading "交秘书发" — no secretary AI runs at click time) and
// must keep the unchanged cmd:/outbox secretary callback data.
//
// Coverage declaration (L-0697): scans formatOutboxCard's dispatch-actuator
// button for the label + callback pair. Excludes the manual button and the
// view-original button (unchanged, covered by the existing metadata test)
// and all verification-state branches (unchanged). Instance gate, not class:
// a single button label/content contract on an unchanged actuator.
func TestFormatOutboxCardDirectStartButtonLabel(t *testing.T) {
	_, buttons := formatOutboxCard(NewI18n(LangEnglish), outboxRecord{Thread: "alpha", To: "dev-pro", Route: "heavy", QueryPath: "F:\\archive\\L-0100.query.md", Generation: "g1", Summary: "Ship it"}, "L-0100", "", 0, 0)
	if len(buttons) != 1 || len(buttons[0]) != 3 {
		t.Fatalf("buttons = %#v, want one row of three", buttons)
	}
	actuator := buttons[0][2]
	if actuator.Text != "⚡ 直接开始" {
		t.Fatalf("dispatch actuator button label = %q, want %q", actuator.Text, "⚡ 直接开始")
	}
	if actuator.Data != "cmd:/outbox secretary L-0100 g1" {
		t.Fatalf("dispatch actuator button callback = %q, want unchanged cmd:/outbox secretary L-0100 g1", actuator.Data)
	}
}

func TestVerificationStateUsesArchiveTextOnly(t *testing.T) {
	cases := []struct {
		name, verify, verified string
		want                   verificationState
	}{
		{name: "legacy blank verify is ready", want: verificationReady},
		{name: "named verifier awaits empty result", verify: "architect-codex", want: verificationAwaiting},
		{name: "any nonempty verified text is ready", verify: "architect-codex", verified: "anything at all", want: verificationReady},
		{name: "protocol none exemption is ready", verify: "none — Boss 当场豁免预派发校验（2026-07-31 pursuit 直发）", want: verificationReady},
		{name: "protocol none exemption hyphen variant is ready", verify: "none - letter protocol standard", want: verificationReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyVerification(tc.verify, tc.verified); got != tc.want {
				t.Fatalf("classifyVerification(%q, %q) = %q, want %q", tc.verify, tc.verified, got, tc.want)
			}
		})
	}
}

func TestFormatOutboxCardAwaitingVerificationShowsRequestOnly(t *testing.T) {
	record := outboxRecord{Thread: "alpha", To: "dev-pro", Route: "heavy", QueryPath: "L-0100.query.md", Generation: "g1", Summary: "Ship it", Verify: "architect-codex", Verification: verificationAwaiting}
	content, buttons := formatOutboxCard(NewI18n(LangEnglish), record, "L-0100", "", 0, 0)
	if !strings.Contains(content, "Awaiting verification: architect-codex") {
		t.Fatalf("content = %q", content)
	}
	if len(buttons) != 1 || len(buttons[0]) != 2 || buttons[0][1].Data != "verification_request:"+verificationCallbackToken("L-0100", "g1") {
		t.Fatalf("buttons = %#v", buttons)
	}
	for _, row := range buttons {
		for _, button := range row {
			if len([]byte(button.Data)) > 64 {
				t.Fatalf("callback data exceeds Telegram limit: %d bytes: %q", len([]byte(button.Data)), button.Data)
			}
		}
	}
}

func TestFormatOutboxCardVerificationCallbackFitsTelegramLimit(t *testing.T) {
	letter := "L-999999999999999999999999999999999999999999999999999999999999"
	generation := strings.Repeat("g", 4096)
	_, buttons := formatOutboxCard(NewI18n(LangEnglish), outboxRecord{Thread: "alpha", To: "dev-pro", QueryPath: "L.query.md", Generation: generation, Verify: "verifier", Verification: verificationAwaiting}, letter, "", 0, 0)
	if got := len([]byte(buttons[0][1].Data)); got > 64 {
		t.Fatalf("verification callback is %d bytes, exceeds Telegram limit: %q", got, buttons[0][1].Data)
	}
}
func TestOutboxManualRejectsAwaitingVerification(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.outboxRecords = map[string]outboxRecord{"L-0100": {Generation: "g", Verification: verificationAwaiting}}
	if !e.handleOutboxCommand(p, &Message{ReplyCtx: "chat"}, []string{"manual", "L-0100", "g"}) {
		t.Fatal("command not handled")
	}
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "awaiting verification") {
		t.Fatalf("reply = %q", got)
	}
	if e.outboxManual["L-0100"] {
		t.Fatal("awaiting letter was marked manually dispatched")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRequestVerificationRequiresConfiguredRelayDelivery(t *testing.T) {
	root := t.TempDir()
	path := writeQueryFile(t, filepath.Join(root, "threads"), "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nDate: 2026-07-18\n---\n")
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	e.outboxRecords = map[string]outboxRecord{"L-0100": {QueryPath: path, Generation: contentDigest(mustReadFile(t, path)), Verify: "architect-codex", Verification: verificationAwaiting}}
	_, ok, err := e.RequestVerification(&stubPlatformEngine{n: "telegram"}, verificationCallbackToken("L-0100", e.outboxRecords["L-0100"].Generation))
	if err == nil || ok {
		t.Fatalf("unconfigured delivery = ok:%v err:%v", ok, err)
	}
	if _, found, getErr := newVerificationExpectationStore(root).get("L-0100", e.outboxRecords["L-0100"].Generation); getErr != nil || found {
		t.Fatalf("failed delivery must release expectation: found:%v err:%v", found, getErr)
	}
	if got := e.outboxRecords["L-0100"].Verification; got != verificationAwaiting {
		t.Fatalf("state after failed delivery = %q, want awaiting", got)
	}
}

func TestRequestVerificationReleasesFailedDeliveryForRetry(t *testing.T) {
	root := t.TempDir()
	path := writeQueryFile(t, filepath.Join(root, "threads"), "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: verifier-seat\nVerified:\nDate: 2026-07-18\n---\n")
	generation := contentDigest(mustReadFile(t, path))
	source := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	source.dataDir = root
	source.outboxConfig = OutboxConfig{SessionKey: "telegram:1:0"}
	source.outboxRecords = map[string]outboxRecord{"L-0100": {QueryPath: path, Generation: generation, Verify: "verifier-seat", Verification: verificationAwaiting}}
	source.relayManager = NewRelayManager(root)
	source.relayManager.RegisterEngine("secretary-seat", source)
	source.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "verifier-seat": ""})

	_, ok, err := source.RequestVerification(&stubPlatformEngine{n: "telegram"}, verificationCallbackToken("L-0100", generation))
	if err == nil || ok {
		t.Fatalf("failed relay = ok:%v err:%v", ok, err)
	}
	if got := source.outboxRecords["L-0100"].Verification; got != verificationAwaiting {
		t.Fatalf("state = %q, want awaiting", got)
	}
	if _, found, err := newVerificationExpectationStore(root).get("L-0100", generation); err != nil || found {
		t.Fatalf("failed relay left inflight expectation: found:%v err:%v", found, err)
	}
}

func TestRequestVerificationRelaysOneVerifierIntakeWithoutImplementationDispatch(t *testing.T) {
	root := t.TempDir()
	path := writeQueryFile(t, filepath.Join(root, "threads"), "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: verifier-seat\nVerified:\nDate: 2026-07-18\n---\n")
	generation := contentDigest(mustReadFile(t, path))
	source := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	source.dataDir = root
	source.outboxConfig = OutboxConfig{SessionKey: "telegram:1:0"}
	source.outboxRecords = map[string]outboxRecord{"L-0100": {QueryPath: path, Generation: generation, Verify: "verifier-seat", Verification: verificationAwaiting}}
	verifierSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	verifier := NewEngine("verifier-seat", &msgRecordingAgent{nextSession: verifierSession}, nil, "", LangEnglish)
	source.relayManager = NewRelayManager(root)
	source.relayManager.RegisterEngine("secretary-seat", source)
	source.relayManager.RegisterEngine("verifier-seat", verifier)
	source.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "verifier-seat": ""})
	_, ok, err := source.RequestVerification(&stubPlatformEngine{n: "telegram"}, verificationCallbackToken("L-0100", generation))
	if err != nil || !ok {
		t.Fatalf("request = ok:%v err:%v", ok, err)
	}
	_, ok, err = source.RequestVerification(&stubPlatformEngine{n: "telegram"}, verificationCallbackToken("L-0100", generation))
	if err != nil || ok {
		t.Fatalf("duplicate = ok:%v err:%v", ok, err)
	}
	if prompts := verifierSession.prompts(); len(prompts) != 1 || !strings.Contains(prompts[0], "Query: "+path) || strings.Contains(prompts[0], "Generation:") || !strings.Contains(prompts[0], "Do not create a RESULT or dispatch the implementation task.") {
		t.Fatalf("verifier intake = %#v", prompts)
	}
	if len(mustListOpen(t, source)) != 0 {
		t.Fatal("verification request created an implementation dispatch")
	}
	if source.outboxRecords["L-0100"].Verification != verificationInflight {
		t.Fatalf("state = %q", source.outboxRecords["L-0100"].Verification)
	}
	if _, found, err := newVerificationExpectationStore(root).get("L-0100", generation); err != nil || !found {
		t.Fatalf("successful relay did not retain expectation: found:%v err:%v", found, err)
	}
}

func TestFormatOutboxCardShowsDefaultForEmptyRoute(t *testing.T) {
	content, _ := formatOutboxCard(NewI18n(LangEnglish), outboxRecord{Thread: "alpha", To: "dev-pro", QueryPath: "F:\\archive\\L-0100.query.md", Generation: "g1", Summary: "Ship it"}, "L-0100", "", 0, 0)
	if !strings.Contains(content, "Route: default") {
		t.Fatalf("missing default route in %q", content)
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
	if _, err := os.Stat(filepath.Join(root, "outbox_manual.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy manual ledger was written: %v", err)
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

func TestOutboxStoreWritesUnifiedLedgerNotLegacyFile(t *testing.T) {
	root := t.TempDir()
	store := newOutboxStore(root)
	if err := store.save(outboxLedger{Records: map[string]outboxRecord{"L-0100": {Thread: "alpha", Generation: "g"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox_ledger.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy outbox ledger was written: %v", err)
	}
	delivery, err := newDeliveryStore(root).load()
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Records["L-0100"].OutboxRecord == nil || delivery.Records["L-0100"].OutboxRecord.Generation != "g" {
		t.Fatalf("unified outbox = %#v", delivery)
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

func TestPublishOutboxRefreshesExistingCardForChangedQuery(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.outboxConfig = OutboxConfig{Platform: "telegram", SessionKey: "telegram:123:123"}
	locator := &MessageLocator{Platform: "telegram", ChatID: 1, ThreadID: 2, MessageID: 3}
	e.outboxRecords = map[string]outboxRecord{"L-0100": {
		Thread: "alpha", To: "dev-pro", Route: "heavy", QueryPath: "L-0100.query.md", Summary: "before", Generation: "old", Card: locator,
	}}

	e.publishOutbox(queryFileInfo{Letter: "L-0100", Thread: "alpha", To: "reviewer-seat", Route: "flash", Path: "L-0100.query.md", Summary: "after", Digest: "new"})

	if p.receiptCardsSent != 0 || p.receiptCardsUpdated != 1 {
		t.Fatalf("card lifecycle = sent %d updated %d, want 0/1", p.receiptCardsSent, p.receiptCardsUpdated)
	}
	if !strings.Contains(p.updatedContent, "To: reviewer-seat") || !strings.Contains(p.updatedContent, "Summary: after") {
		t.Fatalf("updated card = %q", p.updatedContent)
	}
	record := e.outboxRecords["L-0100"]
	if record.Card != locator || record.Generation != "new" || record.To != "reviewer-seat" {
		t.Fatalf("refreshed record = %#v", record)
	}
	if got := p.updatedButtons[0][1].Data; got != "cmd:/outbox manual L-0100 new" {
		t.Fatalf("manual button = %q", got)
	}
}

func TestOutboxFailedSendPersistsRetryBackoff(t *testing.T) {
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}, sendErr: errors.New("unavailable")}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.outboxConfig = OutboxConfig{Platform: "telegram", SessionKey: "telegram:123:123"}
	e.outboxRecords = map[string]outboxRecord{}
	e.publishOutbox(queryFileInfo{Letter: "L-0100", Thread: "alpha", To: "dev-pro", Route: "heavy", Path: "L-0100.query.md", Summary: "queued", Digest: "digest"})
	record := e.outboxRecords["L-0100"]
	if record.Attempts != 1 || record.RetryAt.IsZero() {
		t.Fatalf("retry state = %#v", record)
	}
	e.publishOutbox(queryFileInfo{Letter: "L-0100", Thread: "alpha", To: "dev-pro", Route: "heavy", Path: "L-0100.query.md", Summary: "queued", Digest: "digest"})
	if p.receiptCardsSent != 1 {
		t.Fatalf("backoff sends = %d, want 1", p.receiptCardsSent)
	}
}

func TestCheckOutboxPublishesAfterPlanningLockIsReleased(t *testing.T) {
	// publishOutbox owns its own brief state locks. This regression calls the
	// watcher path, which used to hold outboxMu across SendReceiptCard.
	root := t.TempDir()
	threads, index := filepath.Join(root, "threads"), filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: dev-pro\nRoute: heavy\nDate: 2026-07-18\n---\n\n## Query\nqueued\n")
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.dataDir, e.outboxConfig, e.outboxRecords, e.outboxManual, e.outboxSeeded = root, OutboxConfig{Enabled: true, IndexPath: index, Platform: "telegram", SessionKey: "telegram:123:123"}, map[string]outboxRecord{}, map[string]bool{}, true
	e.checkOutbox()
	if p.receiptCardsSent != 1 {
		t.Fatalf("send count = %d, want 1", p.receiptCardsSent)
	}
}

func TestCheckOutboxRetriesPendingCardRefreshWithoutArchiveChange(t *testing.T) {
	root := t.TempDir()
	threads, index := filepath.Join(root, "threads"), filepath.Join(root, "INDEX.md")
	indexBody := []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n")
	if err := os.WriteFile(index, indexBody, 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0100", "---\nID: L-0100\nThread: alpha\nType: QUERY\nTo: reviewer-seat\nRoute: flash\nDate: 2026-07-18\n---\n\n## Query\nupdated\n")
	queries, err := scanOutboxQueries(threads, index, nil)
	if err != nil || len(queries) != 1 {
		t.Fatalf("queries = %#v, %v", queries, err)
	}
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.dataDir = root
	e.deliveryStore = newDeliveryStore(root)
	if err := e.deliveryStore.save(deliveryLedger{LastFullAudit: time.Now().UTC(), Records: map[string]deliveryRecord{"L-0100": {Scanner: deliveryScannerState{QueryFingerprint: queries[0].Digest, IndexFingerprint: contentDigest(indexBody)}}}}); err != nil {
		t.Fatal(err)
	}
	e.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index, Platform: "telegram", SessionKey: "telegram:123:123"}
	e.outboxSeeded = true
	e.outboxRecords = map[string]outboxRecord{"L-0100": {Thread: "alpha", Generation: queries[0].Digest, Card: &MessageLocator{Platform: "telegram", MessageID: 3}, RefreshPending: true, RetryAt: time.Now().Add(-time.Second)}}

	e.checkOutbox()

	if p.receiptCardsSent != 0 || p.receiptCardsUpdated != 1 {
		t.Fatalf("retry lifecycle = sent %d updated %d, want 0/1", p.receiptCardsSent, p.receiptCardsUpdated)
	}
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
	if err := os.WriteFile(index, []byte("| L-0100 | QUERY | alpha | ROOT | queued | 2026-07-18 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanOutboxQueries(threads, index, nil)
	if err != nil || len(got) != 1 || got[0].Digest != contentDigest([]byte(body)) {
		t.Fatalf("query = %#v, %v", got, err)
	}
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
