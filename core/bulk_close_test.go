package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// letterID renders a zero-padded "L-0042"-shaped ID for test fixtures.
func letterID(n int) string {
	return fmt.Sprintf("L-%04d", n)
}

// TestSameThreadUnclosedLetters is the pure-function half of L-0694 Option
// B's candidate computation: same thread, RESULT without CLOSED, strictly
// below the upper bound, ascending order.
func TestSameThreadUnclosedLetters(t *testing.T) {
	tail := strings.Join([]string{
		"# INDEX",
		"| L-0100 | QUERY | alpha | ROOT | q | 2026-07-01 |",
		"| L-0100 | RESULT | alpha | ROOT | r | 2026-07-02 |",
		"| L-0101 | QUERY | alpha | ROOT | q | 2026-07-03 |",
		"| L-0101 | RESULT | alpha | ROOT | r | 2026-07-04 |",
		"| L-0101 | CLOSED | alpha | ROOT | c | 2026-07-05 |",
		"| L-0102 | QUERY | beta | ROOT | q | 2026-07-06 |",
		"| L-0102 | RESULT | beta | ROOT | r | 2026-07-07 |",
		"| L-0103 | QUERY | alpha | ROOT | q | 2026-07-08 |",
		"| L-0200 | QUERY | alpha | ROOT | q | 2026-07-09 |",
	}, "\n")

	got := sameThreadUnclosedLetters(tail, "alpha", "L-0200")
	want := []string{"L-0100"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("sameThreadUnclosedLetters() = %v, want %v (L-0101 has CLOSED, L-0102 is a different thread, L-0103 has no RESULT, L-0200 is the upper bound itself)", got, want)
	}
}

func TestSameThreadUnclosedLettersOrdersAscendingAndExcludesUpperBoundAndAbove(t *testing.T) {
	tail := strings.Join([]string{
		"| L-0050 | RESULT | alpha | ROOT | r | 2026-07-01 |",
		"| L-0049 | RESULT | alpha | ROOT | r | 2026-07-01 |",
		"| L-0051 | RESULT | alpha | ROOT | r | 2026-07-01 |",
	}, "\n")
	got := sameThreadUnclosedLetters(tail, "alpha", "L-0051")
	want := []string{"L-0049", "L-0050"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sameThreadUnclosedLetters() = %v, want %v (ascending, upper bound and anything >= it excluded)", got, want)
	}
}

func writeIndexFile(t *testing.T, root string, lines []string) string {
	t.Helper()
	path := filepath.Join(root, "INDEX.md")
	body := "# INDEX\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestComputeSameThreadUnclosedLettersTruncatesAndExcludesUnlisted is the
// required truncation failure-edge regression (L-0694 Expected Output #2):
// candidates beyond bulkCloseListMax must not appear in the offered
// Letters, must be reported via truncated, and — critically — a bulk close
// executed against the returned entry must never touch them.
func TestComputeSameThreadUnclosedLettersTruncatesAndExcludesUnlisted(t *testing.T) {
	root := t.TempDir()
	var lines []string
	threadsDir := filepath.Join(root, "threads")
	total := bulkCloseListMax + 3
	for i := 1; i <= total; i++ {
		letter := letterID(i)
		lines = append(lines, "| "+letter+" | RESULT | alpha | ROOT | r | 2026-07-01 |")
	}
	indexPath := writeIndexFile(t, root, lines)

	e := NewEngine("test", &stubAgent{}, []Platform{&receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}}, "", LangEnglish)
	e.notifyConfig.IndexPath = indexPath
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	for i := 1; i <= total; i++ {
		letter := letterID(i)
		resultPath := writeResultFile(t, threadsDir, "alpha", letter, "ID: "+letter+"\nStatus: DONE\n---\n")
		if err := e.notifyStore.recordArrival(indexResultRow{Letter: letter, Thread: "alpha", Path: resultPath, Status: "DONE", Summary: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	upperBound := letterID(total + 1)

	letters, generations, _, _, truncated := e.computeSameThreadUnclosedLetters("alpha", upperBound)
	if len(letters) != bulkCloseListMax {
		t.Fatalf("offered letters = %d, want %d (bulkCloseListMax)", len(letters), bulkCloseListMax)
	}
	if truncated != 3 {
		t.Fatalf("truncated = %d, want 3", truncated)
	}
	offered := map[string]bool{}
	for _, l := range letters {
		offered[l] = true
	}
	if offered[letterID(total)] || offered[letterID(total-1)] || offered[letterID(total-2)] {
		t.Fatalf("the 3 truncated letters must not appear in the offered set, got %v", letters)
	}

	// The critical failure edge: closing against exactly what was offered
	// must never touch the truncated-out letters.
	outcome := e.executeBulkCloseLetters(letters, generations)
	touched := map[string]bool{}
	for _, l := range outcome.Closed {
		touched[l] = true
	}
	for k := range outcome.LocalOnly {
		touched[k] = true
	}
	for k := range outcome.Failed {
		touched[k] = true
	}
	for k := range outcome.Skipped {
		touched[k] = true
	}
	if touched[letterID(total)] {
		t.Fatalf("a letter excluded by truncation must never be closed, but %s appears in the outcome: %+v", letterID(total), outcome)
	}
}

// TestExecuteBulkCloseLettersSkipsOnGenerationDrift is the required
// generation-change failure-edge regression (L-0694 B-1 step 6): if the
// live receipt's Generation no longer matches the review-time snapshot, the
// letter must be skipped (fail-closed), not closed.
func TestExecuteBulkCloseLettersSkipsOnGenerationDrift(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0300", "ID: L-0300\nStatus: DONE\n---\n")
	e := NewEngine("test", &stubAgent{}, []Platform{&receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}}, "", LangEnglish)
	e.notifyConfig.IndexPath = filepath.Join(root, "INDEX.md")
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0300", Thread: "alpha", Path: resultPath, Status: "DONE", Generation: "gen-1"}); err != nil {
		t.Fatal(err)
	}
	// Simulate content changing after the review snapshot was taken: a new
	// arrival bumps the live Generation past what the batch was reviewed at.
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0300", Thread: "alpha", Path: resultPath, Status: "DONE", Generation: "gen-2"}); err != nil {
		t.Fatal(err)
	}

	staleSnapshot := map[string]string{"L-0300": "gen-1"}
	outcome := e.executeBulkCloseLetters([]string{"L-0300"}, staleSnapshot)
	if len(outcome.Closed) != 0 || len(outcome.LocalOnly) != 0 || len(outcome.Failed) != 0 {
		t.Fatalf("stale-generation letter must land only in Skipped, got %+v", outcome)
	}
	if _, skipped := outcome.Skipped["L-0300"]; !skipped {
		t.Fatalf("expected L-0300 in Skipped bucket, got %+v", outcome)
	}
	record, err := e.notifyStore.receipt("L-0300")
	if err != nil || record.ClosedAt != "" {
		t.Fatalf("stale-generation letter must not be closed: %+v, err=%v", record, err)
	}
}

func TestExecuteBulkCloseLettersClosesSuccessfully(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0301", "ID: L-0301\nStatus: DONE\n---\n")
	writeFakeArchiveDailyScript(t, root, `Write-Output '{"status":"ready","ids":["L-0301"],"pushed":true,"push_error":""}'`)
	e := NewEngine("test", &stubAgent{}, []Platform{&receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}}, "", LangEnglish)
	e.notifyConfig.IndexPath = filepath.Join(root, "INDEX.md")
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0301", Thread: "alpha", Path: resultPath, Status: "DONE", Generation: "gen-1"}); err != nil {
		t.Fatal(err)
	}
	outcome := e.executeBulkCloseLetters([]string{"L-0301"}, map[string]string{"L-0301": "gen-1"})
	if len(outcome.Closed) != 1 || outcome.Closed[0] != "L-0301" {
		t.Fatalf("expected L-0301 in Closed bucket, got %+v", outcome)
	}
	record, err := e.notifyStore.receipt("L-0301")
	if err != nil || record.ClosedAt == "" {
		t.Fatalf("successful bulk close must mark the receipt closed: %+v, err=%v", record, err)
	}
}

func TestExecuteBulkCloseLettersLocalOnlyWhenPushFails(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0302", "ID: L-0302\nStatus: DONE\n---\n")
	writeFakeArchiveDailyScript(t, root, `Write-Output '{"status":"ready","ids":["L-0302"],"pushed":false,"push_error":"non-fast-forward"}'`)
	e := NewEngine("test", &stubAgent{}, []Platform{&receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}}, "", LangEnglish)
	e.notifyConfig.IndexPath = filepath.Join(root, "INDEX.md")
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0302", Thread: "alpha", Path: resultPath, Status: "DONE", Generation: "gen-1"}); err != nil {
		t.Fatal(err)
	}
	outcome := e.executeBulkCloseLetters([]string{"L-0302"}, map[string]string{"L-0302": "gen-1"})
	if _, ok := outcome.LocalOnly["L-0302"]; !ok {
		t.Fatalf("expected L-0302 in LocalOnly bucket, got %+v", outcome)
	}
	record, err := e.notifyStore.receipt("L-0302")
	if err != nil || record.ClosedAt != "" {
		t.Fatalf("a local-only (unpushed) close must not mark the ledger closed: %+v, err=%v", record, err)
	}
}

func TestExecuteBulkCloseLettersFailedWhenScriptErrors(t *testing.T) {
	root := t.TempDir()
	resultPath := writeResultFile(t, root, "alpha", "L-0303", "ID: L-0303\nStatus: DONE\n---\n")
	writeFakeArchiveDailyScript(t, root, `Write-Error 'boom'; exit 1`)
	e := NewEngine("test", &stubAgent{}, []Platform{&receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}}, "", LangEnglish)
	e.notifyConfig.IndexPath = filepath.Join(root, "INDEX.md")
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0303", Thread: "alpha", Path: resultPath, Status: "DONE", Generation: "gen-1"}); err != nil {
		t.Fatal(err)
	}
	outcome := e.executeBulkCloseLetters([]string{"L-0303"}, map[string]string{"L-0303": "gen-1"})
	if _, ok := outcome.Failed["L-0303"]; !ok {
		t.Fatalf("expected L-0303 in Failed bucket, got %+v", outcome)
	}
}

func TestApplyBulkCloseRetryMovesRetriedLettersOutOfFailedBucket(t *testing.T) {
	base := BulkCloseOutcome{
		Closed:    []string{"L-0400"},
		LocalOnly: map[string]string{"L-0401": "push error"},
		Skipped:   map[string]string{"L-0402": "stale"},
		Failed:    map[string]string{"L-0403": "boom"},
	}
	delta := BulkCloseOutcome{
		Closed:    []string{"L-0401", "L-0403"},
		LocalOnly: map[string]string{},
		Skipped:   map[string]string{},
		Failed:    map[string]string{},
	}
	merged := applyBulkCloseRetry(base, []string{"L-0401", "L-0403"}, delta)
	if _, still := merged.LocalOnly["L-0401"]; still {
		t.Fatalf("retried letter must leave LocalOnly once it succeeds, got %+v", merged)
	}
	if _, still := merged.Failed["L-0403"]; still {
		t.Fatalf("retried letter must leave Failed once it succeeds, got %+v", merged)
	}
	closedSet := map[string]bool{}
	for _, l := range merged.Closed {
		closedSet[l] = true
	}
	if !closedSet["L-0400"] || !closedSet["L-0401"] || !closedSet["L-0403"] {
		t.Fatalf("merged Closed must contain the original plus both retried successes, got %v", merged.Closed)
	}
	if _, ok := merged.Skipped["L-0402"]; !ok {
		t.Fatalf("untouched Skipped entries must survive the merge, got %+v", merged)
	}
}

// TestEngineBulkCloseFullFlowReviewConfirmClosesLetters exercises the full
// wiring — /receipt bulkclose review card, then /receipt bulkcloseconfirm —
// through handleCommand, proving the command routing and card edits
// actually connect end to end, not just the underlying helpers.
func TestEngineBulkCloseFullFlowReviewConfirmClosesLetters(t *testing.T) {
	root := t.TempDir()
	writeFakeArchiveDailyScript(t, root, `Write-Output '{"status":"ready","ids":["L-0500"],"pushed":true,"push_error":""}'`)
	p := &receiptActionPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.dataDir = root
	e.notifyConfig.IndexPath = filepath.Join(root, "INDEX.md")
	e.notifyStore = newNotifyStore(filepath.Join(root, "data"))
	e.SetAdminFrom("boss-id")

	resultPath := writeResultFile(t, root, "alpha", "L-0500", "ID: L-0500\nStatus: DONE\n---\n")
	if err := e.notifyStore.recordArrival(indexResultRow{Letter: "L-0500", Thread: "alpha", Path: resultPath, Status: "DONE", Generation: "gen-1"}); err != nil {
		t.Fatal(err)
	}

	entry := PendingBulkClose{
		Token:       "L-0501",
		Letters:     []string{"L-0500"},
		Generations: map[string]string{"L-0500": "gen-1"},
		Summaries:   map[string]string{"L-0500": "ready"},
		Threads:     map[string]string{"L-0500": "alpha"},
	}
	if err := e.ensurePendingBulkCloseStore().upsert(entry); err != nil {
		t.Fatal(err)
	}

	msg := &Message{UserID: "boss-id", UserName: "boss", ReplyCtx: "inbox"}
	if handled := e.handleCommand(p, msg, "/receipt bulkclose L-0501"); !handled {
		t.Fatal("bulkclose review must not start an agent turn")
	}
	if len(p.updatedButtons) != 1 || len(p.updatedButtons[0]) != 2 {
		t.Fatalf("review card must carry confirm+cancel buttons, got %#v", p.updatedButtons)
	}
	if !strings.Contains(p.updatedButtons[0][0].Data, "bulkcloseconfirm L-0501") {
		t.Fatalf("review card confirm button wired wrong: %#v", p.updatedButtons)
	}

	if handled := e.handleCommand(p, msg, "/receipt bulkcloseconfirm L-0501"); !handled {
		t.Fatal("bulkcloseconfirm must not start an agent turn")
	}
	if !strings.Contains(p.updatedContent, "L-0500") {
		t.Fatalf("outcome card must reference L-0500, got %q", p.updatedContent)
	}
	record, err := e.notifyStore.receipt("L-0500")
	if err != nil || record.ClosedAt == "" {
		t.Fatalf("confirmed bulk close must mark the receipt closed: %+v, err=%v", record, err)
	}
}
