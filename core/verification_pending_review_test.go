package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── L-0762 item 1: auto-trigger + record-less entry ────────────────────────

// TestCheckPendingReviewAutoTriggersRecordLess tests the 先审后存 auto-trigger:
// a pending-QC file (unregistered + Verify named + Verified empty) is relayed to
// its verifier with NO button click — the record-less entry builds the
// expectation straight from the scan result. This is the red/green proof that
// "改前必须点击才 relay / 改后无点击即 relay": the button path still exists
// (TestRequestVerification*), while this scan fires without any callback.
func TestCheckPendingReviewAutoTriggersRecordLess(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeQueryFile(t, threads, "alpha", "L-0900", "---\nID: L-0900\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nFrom: secretary-L-0900\nDate: 2026-08-02\n---\n\n## Query\nVerify me\n")

	verifierSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	verifier := NewEngine("architect-codex", &msgRecordingAgent{nextSession: verifierSession}, nil, "", LangEnglish)
	source := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	source.dataDir = root
	source.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index, SessionKey: "telegram:1:0"}
	source.relayManager = NewRelayManager(root)
	source.relayManager.RegisterEngine("secretary-seat", source)
	source.relayManager.RegisterEngine("architect-codex", verifier)
	source.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "architect-codex": ""})

	source.checkPendingReview()

	prompts := verifierSession.prompts()
	if len(prompts) != 1 {
		t.Fatalf("auto-trigger delivered %d relay(s), want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "Query: "+path) {
		t.Fatalf("verifier prompt = %q, want Query path", prompts[0])
	}
	if _, found, err := newVerificationExpectationStore(root).get("L-0900", contentDigest(mustReadFile(t, path))); err != nil || !found {
		t.Fatalf("record-less request must persist expectation: found=%v err=%v", found, err)
	}
}

// TestCheckPendingReviewIdempotentAcrossRefresh proves the same LetterID +
// Generation re-scanned relays exactly once — the expectation store's key is the
// idempotency mechanism, so a second scan is a no-op.
func TestCheckPendingReviewIdempotentAcrossRefresh(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0901", "---\nID: L-0901\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nFrom: secretary-L-0901\nDate: 2026-08-02\n---\n\n## Query\nVerify me\n")

	verifierSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	verifier := NewEngine("architect-codex", &msgRecordingAgent{nextSession: verifierSession}, nil, "", LangEnglish)
	source := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	source.dataDir = root
	source.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index, SessionKey: "telegram:1:0"}
	source.relayManager = NewRelayManager(root)
	source.relayManager.RegisterEngine("secretary-seat", source)
	source.relayManager.RegisterEngine("architect-codex", verifier)
	source.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "architect-codex": ""})

	source.checkPendingReview()
	source.checkPendingReview()

	if got := len(verifierSession.prompts()); got != 1 {
		t.Fatalf("idempotency: second scan delivered %d relay(s), want still 1", got)
	}
}

// TestCheckPendingReviewIdempotentAcrossRestart proves the persisted expectation
// survives process restart: a fresh engine sharing the same dataDir must not
// re-relay a generation that already has an expectation on disk.
func TestCheckPendingReviewIdempotentAcrossRestart(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0902", "---\nID: L-0902\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nFrom: secretary-L-0902\nDate: 2026-08-02\n---\n\n## Query\nVerify me\n")

	mkSource := func() *Engine {
		verifierSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
		verifier := NewEngine("architect-codex", &msgRecordingAgent{nextSession: verifierSession}, nil, "", LangEnglish)
		s := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
		s.dataDir = root
		s.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index, SessionKey: "telegram:1:0"}
		s.relayManager = NewRelayManager(root)
		s.relayManager.RegisterEngine("secretary-seat", s)
		s.relayManager.RegisterEngine("architect-codex", verifier)
		s.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "architect-codex": ""})
		return s
	}

	first := mkSource()
	first.checkPendingReview()

	second := mkSource()
	second.checkPendingReview()

	if got := len(second.relayManager.Engine("architect-codex").agent.(*msgRecordingAgent).nextSession.prompts()); got != 0 {
		t.Fatalf("restart re-delivered %d relay(s), want 0 (expectation persisted)", got)
	}
}

// TestScanPendingReviewExcludesExemptAndRegistered proves the trigger source
// change does not misfire: `Verify: none` exemption files and already-registered
// files are excluded, so the auto-trigger serves only pending-QC files.
func TestScanPendingReviewExcludesExemptAndRegistered(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	indexBody := "| L-0904 | QUERY | alpha | ROOT | queued | 2026-08-02 |\n"
	if err := os.WriteFile(index, []byte(indexBody), 0o644); err != nil {
		t.Fatal(err)
	}
	// pending-QC: unregistered + named verifier + empty Verified → should appear.
	writeQueryFile(t, threads, "alpha", "L-0903", "---\nID: L-0903\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nDate: 2026-08-02\n---\n\n## Query\npending\n")
	// registered → must be excluded.
	writeQueryFile(t, threads, "alpha", "L-0904", "---\nID: L-0904\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nDate: 2026-08-02\n---\n\n## Query\nregistered\n")
	// exempt flow (Verify none) → must be excluded.
	writeQueryFile(t, threads, "alpha", "L-0905", "---\nID: L-0905\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: none — 豁免\nVerified:\nDate: 2026-08-02\n---\n\n## Query\nexempt\n")

	got, err := scanPendingReviewQueries(threads, index)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Letter != "L-0903" {
		t.Fatalf("pending-QC scan = %#v, want exactly L-0903 (exempt + registered excluded)", got)
	}
}

// ─── L-0762 item 2: verification venue off the outbox topic ────────────────

// TestVerificationVenuePerLetterTopicNotOutbox proves the venue decision: the
// verification relay's source session is the per-letter topic, never the outbox
// topic. Without the venue change the relay would run on outboxConfig.SessionKey
// and the verifier's session would hang on the outbox topic.
func TestVerificationVenuePerLetterTopicNotOutbox(t *testing.T) {
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.outboxConfig = OutboxConfig{SessionKey: "telegram:1:0"}   // the "outbox topic" in this test
	e.dispatchConfig = DispatchConfig{DashboardSessionKey: "telegram:1:0"}

	got, err := e.verificationVenueSessionKey("L-0900")
	if err != nil {
		t.Fatal(err)
	}
	if got == "telegram:1:0" {
		t.Fatalf("verification venue = %q, must NOT be the outbox session", got)
	}
	// virtualTopicSessionKey builds telegram:<rawChatID>:<letterNum>:<userID>.
	if !strings.Contains(got, ":0900:") {
		t.Fatalf("verification venue %q does not encode the per-letter topic (letterNum 0900)", got)
	}
}

// TestVerificationVenueChannelKeyDistinctFromDispatchTopic proves sub-question
// (a): the verification-period virtual topic channelKey (rawChatID:letterNum)
// does not collide with the dispatch-period CreateTaskTopic channelKey
// (chatID:realTopicID). virtualTopicSessionKey is synthetic — it creates no real
// Telegram topic — so dispatch's later CreateTaskTopic cannot collide with it;
// reuse of the per-letter session-key machinery is safe.
func TestVerificationVenueChannelKeyDistinctFromDispatchTopic(t *testing.T) {
	dashboard := "telegram:-100:7664413698"
	_, verifyChannel, _, err := virtualTopicSessionKey(dashboard, "L-0762")
	if err != nil {
		t.Fatal(err)
	}
	// Dispatch's CreateTaskTopic assigns a real Telegram thread id (here a mock
	// 855). The dispatch channelKey is chatID:realTopicID (dispatch.go:759/769).
	dispatchChannel := "-100:855"
	if verifyChannel == dispatchChannel {
		t.Fatalf("verification virtual channelKey %q collides with dispatch topic %q", verifyChannel, dispatchChannel)
	}
	if verifyChannel != "-100:0762" {
		t.Fatalf("verification virtual channelKey = %q, want deterministic rawChatID:letterNum", verifyChannel)
	}
}

// TestVerificationRelayUsesNoVisibility proves the relay-visibility decision:
// verification relays suppress the group echo (RelayVisibilityNone) so the outbox
// topic receives zero new messages during the whole verification cycle — the
// verifier seat reads the QUERY file and writes Verified:/校验 comments, so no
// human-visible echo is needed, and failures stay observable via the persisted
// expectation plus cc-connect logs.
//
// The source engine's platform records every reconstructed reply context a
// visibility echo would have targeted. Under RelayVisibilityNone the group echo
// is skipped entirely, so nothing is reconstructed — in particular no message is
// ever sent to the outbox topic's group session (platform:chatID:relay, built
// from the relay SessionKey at core/relay.go:361-364). Delivery still happens:
// the verifier receives the prompt through HandleRelay regardless of visibility.
func TestVerificationRelayUsesNoVisibility(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0910", "---\nID: L-0910\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nFrom: secretary-L-0910\nDate: 2026-08-02\n---\n\n## Query\nVerify me\n")

	verifierSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	verifier := NewEngine("architect-codex", &msgRecordingAgent{nextSession: verifierSession}, nil, "", LangEnglish)
	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	source := NewEngine("secretary-seat", &stubAgent{}, []Platform{sourcePlatform}, "", LangEnglish)
	source.dataDir = root
	source.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index, SessionKey: "telegram:1:0"}
	source.relayManager = NewRelayManager(root)
	source.relayManager.RegisterEngine("secretary-seat", source)
	source.relayManager.RegisterEngine("architect-codex", verifier)
	source.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "architect-codex": ""})

	source.checkPendingReview()

	if got := len(verifierSession.prompts()); got != 1 {
		t.Fatalf("verifier received %d relay(s), want 1 (delivery must still work)", got)
	}
	// The outbox topic's group echo target is platform:chatID:relay. With the
	// pre-venue behavior the relay source was outboxConfig.SessionKey
	// ("telegram:1:0"), whose echo would reconstruct "telegram:1:relay". Under
	// RelayVisibilityNone no visibility echo is posted at all, and the only
	// reconstructed context is the handback into the per-letter source session
	// ("telegram:1:0910:0") — never the outbox topic's relay group session.
	sourcePlatform.mu.Lock()
	defer sourcePlatform.mu.Unlock()
	for _, target := range sourcePlatform.reconstructed {
		if target == "telegram:1:relay" {
			t.Fatalf("verification relay posted a visibility echo to the outbox topic group session %q; outbox topic must stay silent", target)
		}
	}
}

// ─── L-0762 item 3: BLOCK auto-relay to the author seat ─────────────────────

// TestClassifyPendingReviewStateMachine pins the BLOCK→PASS expectation state
// machine on the file state. A fresh file asks the verifier; a file carrying a
// 校验 BLOCK comment asks the author; a Correction after the BLOCK hands it back
// to the verifier (the Generation change auto-invalidates the old expectation).
func TestClassifyPendingReviewStateMachine(t *testing.T) {
	cases := []struct {
		name string
		body string
		want pendingReviewAction
	}{
		{"fresh no comments → verifier", "---\nVerify: architect-codex\n---\n## Query\nx\n", pendingReviewRequestVerifier},
		{"verifier BLOCK → author", "---\nVerify: architect-codex\n---\n## Query\nx\n\n<!-- 校验 (2026-08-02): architect-codex-L-0911 —— BLOCK: cite the line number -->\n", pendingReviewRelayBlockToAuthor},
		{"author Correction after BLOCK → verifier again", "---\nVerify: architect-codex\n---\n## Query\nx\n\n<!-- 校验 (2026-08-02): architect-codex-L-0911 —— BLOCK: stale baseline -->\n\n<!-- Correction (2026-08-02): secretary-L-0911 —— fixed baseline -->\n", pendingReviewRequestVerifier},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPendingReview(tc.body); got != tc.want {
				t.Fatalf("classifyPendingReview = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckPendingReviewRelaysBlockToAuthor proves the BLOCK auto-relay: a
// pending-QC file whose latest review comment is a 校验 BLOCK is relayed to the
// author seat (resolved from From:), not re-sent to the verifier.
func TestCheckPendingReviewRelaysBlockToAuthor(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0911", "---\nID: L-0911\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nFrom: secretary-L-0911\nDate: 2026-08-02\n---\n\n## Query\nVerify me\n\n<!-- 校验 (2026-08-02): architect-codex-L-0911 —— BLOCK: cite the line number -->\n")

	verifierSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	verifier := NewEngine("architect-codex", &msgRecordingAgent{nextSession: verifierSession}, nil, "", LangEnglish)
	authorSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	author := NewEngine("secretary-seat", &msgRecordingAgent{nextSession: authorSession}, nil, "", LangEnglish)
	source := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	source.dataDir = root
	source.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index, SessionKey: "telegram:1:0"}
	source.relayManager = NewRelayManager(root)
	source.relayManager.RegisterEngine("secretary-seat", author)
	source.relayManager.RegisterEngine("architect-codex", verifier)
	source.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "architect-codex": ""})

	source.checkPendingReview()

	if got := len(verifierSession.prompts()); got != 0 {
		t.Fatalf("verifier received %d relay(s) on a BLOCKed file, want 0", got)
	}
	if got := len(authorSession.prompts()); got != 1 {
		t.Fatalf("author received %d BLOCK relay(s), want 1", got)
	}
	if finding := authorSession.prompts()[0]; !strings.Contains(finding, "cite the line number") {
		t.Fatalf("BLOCK relay finding = %q, want the BLOCK text", finding)
	}
}

// TestCheckPendingReviewBlockRelayIdempotent proves the BLOCK relay shares the
// same LetterID+Generation idempotency as the request leg: re-scanning the same
// generation does not re-relay the BLOCK to the author.
func TestCheckPendingReviewBlockRelayIdempotent(t *testing.T) {
	root := t.TempDir()
	threads := filepath.Join(root, "threads")
	index := filepath.Join(root, "INDEX.md")
	if err := os.WriteFile(index, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueryFile(t, threads, "alpha", "L-0912", "---\nID: L-0912\nThread: alpha\nType: QUERY\nTo: dev-pro\nVerify: architect-codex\nVerified:\nFrom: secretary-L-0912\nDate: 2026-08-02\n---\n\n## Query\nVerify me\n\n<!-- 校验 (2026-08-02): architect-codex-L-0912 —— BLOCK: finding -->\n")

	verifierSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	verifier := NewEngine("architect-codex", &msgRecordingAgent{nextSession: verifierSession}, nil, "", LangEnglish)
	authorSession := &msgRecordingAgentSession{events: make(chan Event, 1)}
	author := NewEngine("secretary-seat", &msgRecordingAgent{nextSession: authorSession}, nil, "", LangEnglish)
	source := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	source.dataDir = root
	source.outboxConfig = OutboxConfig{Enabled: true, IndexPath: index, SessionKey: "telegram:1:0"}
	source.relayManager = NewRelayManager(root)
	source.relayManager.RegisterEngine("secretary-seat", author)
	source.relayManager.RegisterEngine("architect-codex", verifier)
	source.relayManager.Bind("telegram", "1", map[string]string{"secretary-seat": "", "architect-codex": ""})

	source.checkPendingReview()
	source.checkPendingReview()

	if got := len(authorSession.prompts()); got != 1 {
		t.Fatalf("idempotency: BLOCK relayed %d time(s), want 1", got)
	}
}
