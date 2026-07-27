package core

import "testing"

// TestIsTopicBoundToSeat_MatchesLedgerToField verifies the L-0669 core
// primitive: a Topic is "bound" to a seat only when the dispatch ledger's
// exp.To for that TopicID equals this Engine's own name (e.name) — the same
// stable-key match findLetterIDByTopic already uses for workspace pattern
// resolution, reused here so Topic-ownership routing and letter-ID
// resolution can never disagree about who owns a Topic.
func TestIsTopicBoundToSeat_MatchesLedgerToField(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, "", LangEnglish)
	e.dataDir = root
	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:  "L-0669",
		Thread:  "cc-connect-dispatch-architecture-redesign",
		To:      "dev-pro",
		TopicID: "200",
		State:   dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if !e.isTopicBoundToSeat("200") {
		t.Fatal("expected topic 200 to be bound to dev-pro")
	}
	if e.isTopicBoundToSeat("999") {
		t.Fatal("expected an unrelated topic ID to be unbound")
	}

	otherSeat := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	otherSeat.dataDir = root
	if otherSeat.isTopicBoundToSeat("200") {
		t.Fatal("expected topic 200 to NOT be bound to a different seat sharing the same ledger file")
	}
}

// topicOwnershipReceiverStub records the checker Engine.Start() injects, so
// the wiring itself (not just isTopicBoundToSeat's matching logic) is under
// test — mirrors how AsyncRecoverablePlatform.SetLifecycleHandler and
// DispatchConfirmReceiver.SetDispatchConfirmHandler are already verified.
type topicOwnershipReceiverStub struct {
	stubPlatformEngine
	checker TopicOwnershipChecker
}

func (p *topicOwnershipReceiverStub) SetTopicOwnershipChecker(checker TopicOwnershipChecker) {
	p.checker = checker
}

func TestEngineStart_WiresTopicOwnershipChecker(t *testing.T) {
	root := t.TempDir()
	stub := &topicOwnershipReceiverStub{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("dev-pro", &stubAgent{}, []Platform{stub}, "", LangEnglish)
	e.dataDir = root

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if stub.checker == nil {
		t.Fatal("expected Engine.Start to inject a TopicOwnershipChecker")
	}

	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:  "L-0669",
		Thread:  "cc-connect-dispatch-architecture-redesign",
		To:      "dev-pro",
		TopicID: "300",
		State:   dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !stub.checker("300") {
		t.Fatal("expected injected checker to report topic 300 as bound")
	}
	if stub.checker("301") {
		t.Fatal("expected injected checker to report an unrelated topic as unbound")
	}
}
