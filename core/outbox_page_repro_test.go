package core

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
)

// outboxUpdaterPlatform wraps stubPlatformEngine with an InlineMessageUpdater
// so the "view original" page path in handleOutboxCommand is exercised end to
// end (stubPlatformEngine alone would short-circuit with "unavailable").
type outboxUpdaterPlatform struct {
	*stubPlatformEngine
	mu      sync.Mutex
	updates []string
}

func (p *outboxUpdaterPlatform) UpdateMessageWithButtons(_ context.Context, _ any, content string, _ [][]ButtonOption) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates = append(p.updates, content)
	return nil
}

func (p *outboxUpdaterPlatform) lastUpdate() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.updates) == 0 {
		return ""
	}
	return p.updates[len(p.updates)-1]
}

// TestOutboxPageViewOriginalAwaiting repros the live L-0718 outbox record
// shape (delivery_ledger outbox_record: Verification awaiting, real QueryPath,
// matching Generation) and drives the "view original" callback
// (cmd:/outbox page <letter> <generation> 0). It pins that view-original is
// still reachable on an awaiting-verification card — the card's only useful
// action — and that pagination renders the query header.
func TestOutboxPageViewOriginalAwaiting(t *testing.T) {
	const queryPath = `F:\nexus-archive\threads\cc-connect-relay-topic-scope\L-0718.query.md`
	data, err := os.ReadFile(queryPath)
	if err != nil {
		t.Skipf("real L-0718 file not readable: %v", err)
	}
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)
	e.outboxRecords = map[string]outboxRecord{
		"L-0718": {
			Thread:       "cc-connect-relay-topic-scope",
			To:           "architect",
			Route:        "heavy",
			QueryPath:    queryPath,
			Generation:   contentDigest(data),
			Summary:      "实现 L-0714 组件 1",
			Verification: verificationAwaiting,
			Verify:       "none — Boss 当场豁免预派发校验（2026-07-31 pursuit 直发）",
		},
	}
	p := &outboxUpdaterPlatform{stubPlatformEngine: &stubPlatformEngine{n: "telegram"}}
	gen := e.outboxRecords["L-0718"].Generation
	if !e.handleOutboxCommand(p, &Message{ReplyCtx: "chat"}, []string{"page", "L-0718", gen, "0"}) {
		t.Fatal("page command not handled")
	}
	if got := p.lastUpdate(); !strings.Contains(got, "Query: L-0718.query.md") {
		t.Fatalf("card header not rendered in update: %q", got)
	}
}
