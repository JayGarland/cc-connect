package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bulkCloseListMax bounds how many same-thread unclosed letters L-0694
// Option B ever lists on a card. Telegram's message limit is ~4096 chars
// (maxPlatformMessageLen=4000 is this codebase's working ceiling, see
// core/engine.go); each list line is an ID + thread + one-line summary,
// conservatively ~120 chars once a long RESULT summary is included. 15
// lines is ~1800 chars, leaving headroom under maxPlatformMessageLen for
// the review card's header/buttons/other same-card content — the same
// margin discipline receiptCompactTextLimit (3500, core/notify.go) already
// applies for the single-receipt card. Letters beyond this cap are reported
// as a truncated count and excluded from the batch entirely (never closed
// without being shown).
const bulkCloseListMax = 15

// BulkCloseOutcome buckets the per-letter result of an executed (or
// retried) batch close (L-0694 B-1 result-presentation table). Every letter
// offered on the review card ends up in exactly one bucket — none are
// silently dropped.
type BulkCloseOutcome struct {
	Closed    []string          `json:"closed"`
	LocalOnly map[string]string `json:"local_only,omitempty"` // letter -> push_error
	Skipped   map[string]string `json:"skipped,omitempty"`    // letter -> reason
	Failed    map[string]string `json:"failed,omitempty"`     // letter -> error text
}

func newBulkCloseOutcome() BulkCloseOutcome {
	return BulkCloseOutcome{LocalOnly: map[string]string{}, Skipped: map[string]string{}, Failed: map[string]string{}}
}

// retryable reports the letters eligible for "🔁 重试失败项": the failed and
// local-only-unsynced buckets, per L-0694 B-1's result table ("只带失败桶与
// 未同步桶的 ID"). Order is deterministic (sorted) so the retry button and
// any rendered list agree.
func (o BulkCloseOutcome) retryable() []string {
	var out []string
	for letter := range o.Failed {
		out = append(out, letter)
	}
	for letter := range o.LocalOnly {
		out = append(out, letter)
	}
	sort.Strings(out)
	return out
}

// applyBulkCloseRetry merges a retry's delta outcome (over exactly the
// `retried` letters) into a prior base outcome. Every retried letter is
// first cleared from base's failed/local-only buckets, then re-added
// wherever delta actually placed it — so a letter that succeeds on retry
// moves to Closed and a letter that fails again lands back in Failed with
// its fresh error, never duplicated across buckets.
func applyBulkCloseRetry(base BulkCloseOutcome, retried []string, delta BulkCloseOutcome) BulkCloseOutcome {
	merged := BulkCloseOutcome{
		Closed:    append([]string{}, base.Closed...),
		LocalOnly: map[string]string{},
		Skipped:   map[string]string{},
		Failed:    map[string]string{},
	}
	for k, v := range base.LocalOnly {
		merged.LocalOnly[k] = v
	}
	for k, v := range base.Skipped {
		merged.Skipped[k] = v
	}
	for k, v := range base.Failed {
		merged.Failed[k] = v
	}
	for _, letter := range retried {
		delete(merged.LocalOnly, letter)
		delete(merged.Failed, letter)
	}
	merged.Closed = append(merged.Closed, delta.Closed...)
	for k, v := range delta.LocalOnly {
		merged.LocalOnly[k] = v
	}
	for k, v := range delta.Skipped {
		merged.Skipped[k] = v
	}
	for k, v := range delta.Failed {
		merged.Failed[k] = v
	}
	return merged
}

// PendingBulkClose is an in-review "一并封信" batch offered on a dispatch
// confirmation card (L-0694 Option B): the same-thread letters that have a
// RESULT row but no CLOSED row and a lower letter number than the letter
// being dispatched. Token equals the dispatch letter's own ID — one dispatch
// proposal has at most one bulk-close offer in flight, so no separate token
// minting is needed. Generations/Summaries/Threads are snapshotted at
// listing time so a later change can be detected and fail-closed at confirm
// time (B-1 step 6) without re-reading the archive.
type PendingBulkClose struct {
	Token          string            `json:"token"`
	Letters        []string          `json:"letters"`
	Generations    map[string]string `json:"generations"`
	Summaries      map[string]string `json:"summaries"`
	Threads        map[string]string `json:"threads"`
	TruncatedCount int               `json:"truncated_count"`
	ReviewedAt     time.Time         `json:"reviewed_at"`
	// Outcome is set once the batch has been confirmed and executed. Its
	// presence makes confirmBulkClose idempotent (a second press re-renders
	// the stored outcome instead of re-executing) and lets retryBulkClose
	// operate on the same token without a fresh review.
	Outcome *BulkCloseOutcome `json:"outcome,omitempty"`
}

type pendingBulkCloseLedger struct {
	Entries []PendingBulkClose `json:"entries"`
}

// pendingBulkCloseStore is the atomic-write/mutex-guarded JSON store for
// in-review bulk-close batches, mirroring pendingDispatchStore's shape
// (core/dispatch_confirm.go).
type pendingBulkCloseStore struct {
	mu   sync.Mutex
	path string
}

func newPendingBulkCloseStore(dataDir string) *pendingBulkCloseStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &pendingBulkCloseStore{path: filepath.Join(dataDir, "pending_bulk_close.json")}
}

func (s *pendingBulkCloseStore) upsert(entry PendingBulkClose) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range ledger.Entries {
		if ledger.Entries[i].Token == entry.Token {
			ledger.Entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		ledger.Entries = append(ledger.Entries, entry)
	}
	return s.saveLocked(ledger)
}

func (s *pendingBulkCloseStore) peek(token string) (PendingBulkClose, bool, error) {
	var zero PendingBulkClose
	if s == nil {
		return zero, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return zero, false, err
	}
	for i := range ledger.Entries {
		if ledger.Entries[i].Token == token {
			return ledger.Entries[i], true, nil
		}
	}
	return zero, false, nil
}

func (s *pendingBulkCloseStore) loadLocked() (pendingBulkCloseLedger, error) {
	var ledger pendingBulkCloseLedger
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ledger, nil
		}
		return ledger, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return ledger, nil
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, err
	}
	return ledger, nil
}

func (s *pendingBulkCloseStore) saveLocked(ledger pendingBulkCloseLedger) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(s.path, data, 0o644)
}

func (e *Engine) ensurePendingBulkCloseStore() *pendingBulkCloseStore {
	if e.pendingBulkCloseStore != nil {
		return e.pendingBulkCloseStore
	}
	e.pendingBulkCloseStore = newPendingBulkCloseStore(e.dataDir)
	return e.pendingBulkCloseStore
}

// letterOrdinal parses the numeric part of an "L-0042"-shaped letter ID.
// Returns ok=false for anything not matching dispatchLetterRe.
func letterOrdinal(id string) (int, bool) {
	if !dispatchLetterRe.MatchString(id) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "L-"))
	if err != nil {
		return 0, false
	}
	return n, true
}

// sameThreadUnclosedLetters is the pure INDEX-parsing half of L-0694's
// candidate computation: it scans an already-read INDEX.md tail for letters
// in `thread` that have a RESULT row but no CLOSED row and sort strictly
// below upperBoundLetter numerically. It performs no I/O and does not know
// about cc-connect's own receipt ledger — computeSameThreadUnclosedLetters
// narrows this list further against notifyStore and applies bulkCloseListMax.
// Ordering is ascending by letter ordinal (oldest first).
func sameThreadUnclosedLetters(indexTail, thread, upperBoundLetter string) []string {
	upperOrdinal, ok := letterOrdinal(upperBoundLetter)
	if !ok {
		return nil
	}
	hasResult := map[string]bool{}
	hasClosed := map[string]bool{}
	threadOf := map[string]string{}
	seen := map[string]bool{}
	var order []string
	for _, line := range strings.Split(indexTail, "\n") {
		row, ok := parseIndexRow(line)
		if !ok {
			continue
		}
		if !seen[row.id] {
			seen[row.id] = true
			order = append(order, row.id)
		}
		threadOf[row.id] = row.thread
		switch row.typ {
		case "RESULT":
			hasResult[row.id] = true
		case "CLOSED":
			hasClosed[row.id] = true
		}
	}
	var candidates []string
	for _, id := range order {
		if threadOf[id] != thread || !hasResult[id] || hasClosed[id] {
			continue
		}
		ord, ok := letterOrdinal(id)
		if !ok || ord >= upperOrdinal {
			continue
		}
		candidates = append(candidates, id)
	}
	sort.Slice(candidates, func(i, j int) bool {
		oi, _ := letterOrdinal(candidates[i])
		oj, _ := letterOrdinal(candidates[j])
		return oi < oj
	})
	return candidates
}

// computeSameThreadUnclosedLetters resolves the offer for L-0694 Option B's
// "🔒 一并封信" button: it reads INDEX.md (bounded to the same
// mailDefaultTailLines tail window reconcileClosedReceipts and /mail already
// use — an existing, not a new, coverage limit), narrows sameThreadUnclosedLetters'
// candidates to those cc-connect actually holds a receipt for (a letter
// without a receipt has no Generation to snapshot and nothing this UI can
// safely offer to close), snapshots each survivor's current Generation,
// Summary, and Thread, and caps the result at bulkCloseListMax. Letters
// dropped purely by the cap are reported via truncated; letters dropped for
// lacking a receipt are simply not offered (see RESULT Open Points).
func (e *Engine) computeSameThreadUnclosedLetters(thread, upperBoundLetter string) (letters []string, generations, summaries, threads map[string]string, truncated int) {
	indexPath := e.mailIndexPath()
	if indexPath == "" || e.notifyStore == nil {
		return nil, nil, nil, nil, 0
	}
	tail := readTail(indexPath, mailDefaultTailLines)
	if tail == "" {
		return nil, nil, nil, nil, 0
	}
	candidates := sameThreadUnclosedLetters(tail, thread, upperBoundLetter)
	generations = map[string]string{}
	summaries = map[string]string{}
	threads = map[string]string{}
	var usable []string
	for _, id := range candidates {
		record, err := e.notifyStore.receipt(id)
		if err != nil || record.ClosedAt != "" {
			continue
		}
		usable = append(usable, id)
		generations[id] = record.Generation
		summaries[id] = record.Summary
		threads[id] = record.Thread
	}
	if len(usable) > bulkCloseListMax {
		truncated = len(usable) - bulkCloseListMax
		usable = usable[:bulkCloseListMax]
	}
	trimmedGen := map[string]string{}
	trimmedSum := map[string]string{}
	trimmedThread := map[string]string{}
	for _, id := range usable {
		trimmedGen[id] = generations[id]
		trimmedSum[id] = summaries[id]
		trimmedThread[id] = threads[id]
	}
	return usable, trimmedGen, trimmedSum, trimmedThread, truncated
}

// executeBulkCloseLetters is the sole execution loop for L-0694 Option B: it
// reloads each letter's live receipt, fail-closes it into Skipped if the
// Generation drifted from the review-time snapshot (B-1 step 6 — content
// changed since Boss looked at the review card means Boss did not confirm
// what's live now), and otherwise calls executeArchiveClose — the exact same
// runArchiveClose->markClosed effects closeReceiptFromInbox uses for a
// single letter — bucketing the result. This is called by both
// confirmBulkClose (full batch) and retryBulkClose (failed/local-only
// subset); neither re-implements the close logic itself.
func (e *Engine) executeBulkCloseLetters(letters []string, generations map[string]string) BulkCloseOutcome {
	outcome := newBulkCloseOutcome()
	for _, letter := range letters {
		record, err := e.notifyStore.receipt(letter)
		if err != nil {
			outcome.Failed[letter] = err.Error()
			continue
		}
		if record.ClosedAt != "" {
			outcome.Closed = append(outcome.Closed, letter)
			continue
		}
		if record.Generation != generations[letter] {
			outcome.Skipped[letter] = "content updated since review; not closed"
			continue
		}
		result := e.executeArchiveClose(letter, record)
		switch {
		case result.CloseErr != nil:
			outcome.Failed[letter] = result.CloseErr.Error()
		case !result.Pushed:
			outcome.LocalOnly[letter] = result.PushErr
		case result.MarkErr != nil || !result.Changed:
			outcome.Failed[letter] = "closed in archive but local receipt bookkeeping failed"
		default:
			outcome.Closed = append(outcome.Closed, letter)
			e.applyClosedCardState(letter, result.Record)
		}
	}
	return outcome
}

// formatBulkCloseListSection renders the read-only list appended to the
// dispatch confirmation card (L-0694 B-1 step 1) — no buttons here, just
// the same-thread unclosed letters the "🔒 一并封信" button below will offer
// to close.
func formatBulkCloseListSection(i18n *I18n, entry PendingBulkClose) string {
	if len(entry.Letters) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(i18n.Tf(MsgBulkCloseListHeader, len(entry.Letters)))
	for _, id := range entry.Letters {
		b.WriteString("\n")
		b.WriteString(i18n.Tf(MsgBulkCloseListItem, id, entry.Threads[id]))
	}
	if entry.TruncatedCount > 0 {
		b.WriteString("\n")
		b.WriteString(i18n.Tf(MsgBulkCloseTruncatedNote, entry.TruncatedCount))
	}
	return b.String()
}

// formatBulkCloseButton is the single row appended below the dispatch
// card's own "✅ Confirm Dispatch" button when there is at least one
// candidate to offer.
func formatBulkCloseButton(i18n *I18n, token string, n int) []ButtonOption {
	return []ButtonOption{{Text: i18n.Tf(MsgBulkCloseButton, n), Data: "cmd:/receipt bulkclose " + token}}
}

// formatBulkCloseReviewCard renders the two-stage confirm step (B-1 step
// 3): nothing is closed yet, every offered letter is listed with its
// summary, and the only actions are confirm-the-whole-batch or cancel.
func formatBulkCloseReviewCard(i18n *I18n, entry PendingBulkClose) (string, [][]ButtonOption) {
	var b strings.Builder
	b.WriteString(i18n.Tf(MsgBulkCloseReviewHeader, len(entry.Letters)))
	for _, id := range entry.Letters {
		b.WriteString("\n")
		b.WriteString(i18n.Tf(MsgBulkCloseReviewItem, id, entry.Threads[id], entry.Summaries[id]))
	}
	buttons := [][]ButtonOption{{
		{Text: i18n.Tf(MsgBulkCloseConfirmBtn, len(entry.Letters)), Data: "cmd:/receipt bulkcloseconfirm " + entry.Token},
		{Text: i18n.T(MsgBulkCloseCancelBtn), Data: "cmd:/receipt bulkclosecancel " + entry.Token},
	}}
	return b.String(), buttons
}

// formatBulkCloseOutcomeCard renders the four-bucket result (B-1 result
// table) after a confirm or retry executes. Every letter offered on the
// review card appears in exactly one bucket line; a retry button is
// attached only when the failed/local-only buckets are non-empty.
func formatBulkCloseOutcomeCard(i18n *I18n, entry PendingBulkClose) (string, [][]ButtonOption) {
	outcome := entry.Outcome
	if outcome == nil {
		return i18n.T(MsgBulkCloseUnavailable), nil
	}
	total := len(outcome.Closed) + len(outcome.LocalOnly) + len(outcome.Skipped) + len(outcome.Failed)
	var b strings.Builder
	b.WriteString(i18n.Tf(MsgBulkCloseResultHeader, total))
	if len(outcome.Closed) > 0 {
		b.WriteString("\n")
		b.WriteString(i18n.Tf(MsgBulkCloseResultClosed, strings.Join(sortedKeys(outcome.Closed), ", ")))
	}
	if len(outcome.LocalOnly) > 0 {
		b.WriteString("\n")
		b.WriteString(i18n.Tf(MsgBulkCloseResultLocal, strings.Join(sortedMapKeys(outcome.LocalOnly), ", ")))
	}
	if len(outcome.Skipped) > 0 {
		b.WriteString("\n")
		b.WriteString(i18n.Tf(MsgBulkCloseResultSkipped, strings.Join(sortedMapKeys(outcome.Skipped), ", ")))
	}
	if len(outcome.Failed) > 0 {
		b.WriteString("\n")
		b.WriteString(i18n.Tf(MsgBulkCloseResultFailed, strings.Join(sortedMapKeys(outcome.Failed), ", ")))
	}
	retry := outcome.retryable()
	if len(retry) == 0 {
		return b.String(), nil
	}
	buttons := [][]ButtonOption{{{Text: i18n.T(MsgBulkCloseRetryBtn), Data: "cmd:/receipt bulkcloseretry " + entry.Token}}}
	return b.String(), buttons
}

func sortedKeys(letters []string) []string {
	out := append([]string{}, letters...)
	sort.Strings(out)
	return out
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// showBulkCloseReview handles "/receipt bulkclose <token>" (the "🔒 一并封信
// (N)" button on a dispatch confirmation card). It never closes anything —
// it edits the same card in place into the batch review dialog (B-1 step
// 3), exactly mirroring showReceiptCloseConfirm's single-letter shape lifted
// to a batch. If the batch was already confirmed (Outcome set), it re-shows
// the stored outcome instead of re-opening review.
func (e *Engine) showBulkCloseReview(p Platform, msg *Message, token string) bool {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/receipt bulkclose"))
		return true
	}
	entry, found, err := e.ensurePendingBulkCloseStore().peek(token)
	if err != nil || !found {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	updater, ok := p.(InlineMessageUpdater)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	var content string
	var buttons [][]ButtonOption
	if entry.Outcome != nil {
		content, buttons = formatBulkCloseOutcomeCard(e.i18n, entry)
	} else {
		content, buttons = formatBulkCloseReviewCard(e.i18n, entry)
	}
	if err := updater.UpdateMessageWithButtons(e.ctx, msg.ReplyCtx, content, buttons); err != nil {
		slog.Warn("bulk close: review card edit failed", "token", token, "error", err)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
	}
	return true
}

// cancelBulkClose handles "/receipt bulkclosecancel <token>": it restores
// the original dispatch proposal card (list + both buttons) without closing
// or forgetting anything — a second "🔒 一并封信" press can reopen review
// against the same snapshot (mirrors cancelReceiptClose's non-destructive
// cancel).
func (e *Engine) cancelBulkClose(p Platform, msg *Message, token string) bool {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/receipt bulkclosecancel"))
		return true
	}
	entry, found, err := e.ensurePendingBulkCloseStore().peek(token)
	if err != nil || !found {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	updater, ok := p.(InlineMessageUpdater)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	if entry.Outcome != nil {
		content, buttons := formatBulkCloseOutcomeCard(e.i18n, entry)
		_ = updater.UpdateMessageWithButtons(e.ctx, msg.ReplyCtx, content, buttons)
		return true
	}
	pending, found, err := e.ensurePendingDispatchStore().peekByLetter(token)
	content := e.i18n.T(MsgBulkCloseCancelled)
	var buttons [][]ButtonOption
	if err == nil && found {
		content = formatDispatchProposalCard(pending.Request) + formatBulkCloseListSection(e.i18n, entry)
		buttons = [][]ButtonOption{
			{{Text: "✅ Confirm Dispatch", Data: "dispatch_confirm:" + token}},
			formatBulkCloseButton(e.i18n, token, len(entry.Letters)),
		}
	}
	if err := updater.UpdateMessageWithButtons(e.ctx, msg.ReplyCtx, content, buttons); err != nil {
		slog.Warn("bulk close: cancel card restore failed", "token", token, "error", err)
	}
	return true
}

// confirmBulkClose handles "/receipt bulkcloseconfirm <token>": the one
// irreversible step. isAdmin is checked here — the same gate
// closeReceiptFromInbox applies (core/engine.go) — and execution is
// delegated entirely to executeBulkCloseLetters (single implementation,
// shared with retryBulkClose). A second press after Outcome is already set
// re-renders the stored result rather than re-executing.
func (e *Engine) confirmBulkClose(p Platform, msg *Message, token string) bool {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/receipt bulkcloseconfirm"))
		return true
	}
	store := e.ensurePendingBulkCloseStore()
	entry, found, err := store.peek(token)
	if err != nil || !found {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	updater, ok := p.(InlineMessageUpdater)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	if entry.Outcome == nil {
		outcome := e.executeBulkCloseLetters(entry.Letters, entry.Generations)
		entry.Outcome = &outcome
		if err := store.upsert(entry); err != nil {
			slog.Warn("bulk close: failed to persist outcome", "token", token, "error", err)
		}
	}
	content, buttons := formatBulkCloseOutcomeCard(e.i18n, entry)
	if err := updater.UpdateMessageWithButtons(e.ctx, msg.ReplyCtx, content, buttons); err != nil {
		slog.Warn("bulk close: outcome card edit failed", "token", token, "error", err)
		e.reply(p, msg.ReplyCtx, content)
	}
	return true
}

// retryBulkClose handles "/receipt bulkcloseretry <token>": re-executes
// exactly the failed and local-only-unsynced letters from the stored
// outcome (never the already-closed or already-skipped ones) through the
// same executeBulkCloseLetters loop, using the original review-time
// Generation snapshot — a letter whose content changed since the original
// review is still fail-closed into Skipped on retry, not silently closed.
func (e *Engine) retryBulkClose(p Platform, msg *Message, token string) bool {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/receipt bulkcloseretry"))
		return true
	}
	store := e.ensurePendingBulkCloseStore()
	entry, found, err := store.peek(token)
	if err != nil || !found || entry.Outcome == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	updater, ok := p.(InlineMessageUpdater)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBulkCloseUnavailable))
		return true
	}
	retry := entry.Outcome.retryable()
	if len(retry) > 0 {
		delta := e.executeBulkCloseLetters(retry, entry.Generations)
		merged := applyBulkCloseRetry(*entry.Outcome, retry, delta)
		entry.Outcome = &merged
		if err := store.upsert(entry); err != nil {
			slog.Warn("bulk close: failed to persist retry outcome", "token", token, "error", err)
		}
	}
	content, buttons := formatBulkCloseOutcomeCard(e.i18n, entry)
	if err := updater.UpdateMessageWithButtons(e.ctx, msg.ReplyCtx, content, buttons); err != nil {
		slog.Warn("bulk close: retry outcome card edit failed", "token", token, "error", err)
		e.reply(p, msg.ReplyCtx, content)
	}
	return true
}
