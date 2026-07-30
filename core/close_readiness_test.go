package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCloseReadiness is the required instance-gate truth table for L-0694
// Option A (A-1 supplement): each row is evaluated in isolation. Rows are
// listed in the same order closeReadiness itself evaluates them so a
// mis-ordered priority (e.g. STUCK checked after DONE) shows up as a
// specific failing row, not just a generic mismatch.
func TestCloseReadiness(t *testing.T) {
	cases := []struct {
		name    string
		record  receiptRecord
		verdict closeReadinessVerdict
	}{
		{
			name:    "row 1: ClosedAt set wins over everything else, even a DONE status",
			record:  receiptRecord{ClosedAt: "2026-07-30T00:00:00Z", Status: "DONE"},
			verdict: closeReadinessClosed,
		},
		{
			name:    "row 2: STUCK needs direction",
			record:  receiptRecord{Status: "STUCK"},
			verdict: closeReadinessNeedsDirection,
		},
		{
			name:    "row 2: BLOCKED needs direction",
			record:  receiptRecord{Status: "BLOCKED"},
			verdict: closeReadinessNeedsDirection,
		},
		{
			name:    "row 3: DONE with a post-arrival update is ready_with_changes",
			record:  receiptRecord{Status: "DONE", Update: receiptUpdate{Sections: []receiptSection{{Heading: "h", Body: "b"}}}},
			verdict: closeReadinessReadyWithChanges,
		},
		{
			name:    "row 4: DONE with no update is ready",
			record:  receiptRecord{Status: "DONE"},
			verdict: closeReadinessReady,
		},
		{
			name:    "row 5: empty Status is unknown",
			record:  receiptRecord{Status: ""},
			verdict: closeReadinessUnknown,
		},
		{
			name:    "row 5: unrecognized Status is unknown",
			record:  receiptRecord{Status: "WEIRD"},
			verdict: closeReadinessUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := closeReadiness(tc.record); got != tc.verdict {
				t.Fatalf("closeReadiness(%+v) = %v, want %v", tc.record, got, tc.verdict)
			}
		})
	}
}

// TestCloseReadinessNeverGatesCloseButton is A-1 hard constraint #2: the
// verdict text is advisory only and must never affect whether the 🔒封信
// button renders. Every non-closed verdict must still produce the close
// button on the compact inbox card.
func TestCloseReadinessNeverGatesCloseButton(t *testing.T) {
	i18n := NewI18n(LangEnglish)
	cases := []receiptRecord{
		{Status: "STUCK", Thread: "alpha", ResultPath: "/tmp/x"},
		{Status: "BLOCKED", Thread: "alpha", ResultPath: "/tmp/x"},
		{Status: "DONE", Thread: "alpha", ResultPath: "/tmp/x"},
		{Status: "DONE", Thread: "alpha", ResultPath: "/tmp/x", Update: receiptUpdate{Sections: []receiptSection{{Heading: "h", Body: "b"}}}},
		{Status: "", Thread: "alpha", ResultPath: "/tmp/x"},
	}
	for _, record := range cases {
		_, buttons := formatReceiptInboxCard(i18n, "L-0001", record, "", 0, 0)
		found := false
		for _, row := range buttons {
			for _, btn := range row {
				if strings.Contains(btn.Data, "receipt close L-0001") {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("close button missing for record with Status=%q — verdict must never gate the button, got buttons=%#v", record.Status, buttons)
		}
	}
}

// TestCloseReadinessAdvisoryPhrasingNeverAsserts guards the A-1 hard
// constraint #1 (advisory, not assertive language): the ready-state card
// lines must contain the "建议"/"Suggest" hedge, not a bare claim.
func TestCloseReadinessAdvisoryPhrasingNeverAsserts(t *testing.T) {
	i18n := NewI18n(LangEnglish)
	for _, record := range []receiptRecord{
		{Status: "DONE"},
		{Status: "DONE", Update: receiptUpdate{Sections: []receiptSection{{Heading: "h", Body: "b"}}}},
	} {
		line := closeReadinessLine(i18n, record)
		if !strings.Contains(strings.ToLower(line), "suggest") {
			t.Fatalf("close-readiness line %q must use advisory phrasing (\"Suggest\"), not an assertion", line)
		}
	}
}

// TestCloseReadinessSingleDefinitionSite is the L-0694 class gate (Expected
// Output #2). It is deliberately narrow: it forbids any file other than
// close_readiness.go from directly comparing a Status-shaped field against
// the DONE/STUCK/BLOCKED literals that closeReadiness's decision ladder
// owns — the exact signature a second, ad hoc implementation of the same
// verdict would reproduce (the L-0690 defect shape this gate exists to
// prevent: one predicate, two independently-reasoned call sites). It does
// NOT forbid ClosedAt comparisons generally: those already appear ~24 times
// across engine.go/notify.go for unrelated idempotency/dedup guards having
// nothing to do with the close-readiness verdict, so gating on ClosedAt
// broadly would be noisy from day one rather than a clean, forward-looking
// constraint — see L-0694 RESULT's Gate Deposited section for the measured
// baseline (zero hits for this narrower pattern) this is frozen against.
func TestCloseReadinessSingleDefinitionSite(t *testing.T) {
	pattern := regexp.MustCompile(`\.Status\s*(==|!=)\s*"(DONE|STUCK|BLOCKED)"`)
	err := filepath.Walk(".", func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(path) == "close_readiness.go" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if loc := pattern.FindIndex(data); loc != nil {
			line := 1 + strings.Count(string(data[:loc[0]]), "\n")
			t.Errorf("%s:%d reproduces closeReadiness's Status-literal comparison outside its single definition site (close_readiness.go) — call closeReadiness(record) instead of re-deriving the verdict", path, line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
