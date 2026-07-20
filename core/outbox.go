package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OutboxConfig is deliberately separate from NotifyConfig: QUERY discovery is
// a queue view and never has receipt/handoff semantics.
type OutboxConfig struct {
	Enabled         bool
	IndexPath       string
	PollInterval    time.Duration
	Platform        string
	SessionKey      string
	TelegramEnabled bool
}

func (c OutboxConfig) threadsDir() string { return filepath.Join(filepath.Dir(c.IndexPath), "threads") }

type queryFileInfo struct {
	Letter, Thread, Path, To, Route, Summary, Digest string
	ModTime                                          time.Time
}
type outboxRecord struct {
	Thread, To, Route, QueryPath, Generation, Summary string
	Card                                              *MessageLocator
	Dispatched                                        bool
	Attempts                                          int       `json:"attempts,omitempty"`
	RetryAt                                           time.Time `json:"retry_at,omitempty"`
}

// outboxLedger is the daemon-owned delivery projection. Archive files and
// dispatch_ledger.json remain the sources of protocol and dispatch truth.
type outboxLedger struct {
	Seeded  bool                    `json:"seeded"`
	Records map[string]outboxRecord `json:"records"`
}

type outboxStore struct {
	mu       sync.Mutex
	path     string // legacy read-only fallback
	delivery *deliveryStore
}

func newOutboxStore(dataDir string) *outboxStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &outboxStore{path: filepath.Join(dataDir, "outbox_ledger.json"), delivery: newDeliveryStore(dataDir)}
}

func (s *outboxStore) load() (outboxLedger, error) {
	ledger := outboxLedger{Records: map[string]outboxRecord{}}
	if s == nil {
		return ledger, nil
	}
	if s.delivery != nil {
		if _, err := os.Stat(s.delivery.path); err == nil {
			delivery, err := s.delivery.load()
			if err != nil {
				return ledger, err
			}
			for id, record := range delivery.Records {
				if record.OutboxRecord != nil {
					ledger.Records[id] = *record.OutboxRecord
				}
			}
			ledger.Seeded = delivery.OutboxSeeded
			return ledger, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if len(strings.TrimSpace(string(data))) != 0 {
		if err := json.Unmarshal(data, &ledger); err != nil {
			return ledger, err
		}
	}
	if ledger.Records == nil {
		ledger.Records = map[string]outboxRecord{}
	}
	return ledger, nil
}

func (s *outboxStore) save(ledger outboxLedger) error {
	if s == nil {
		return nil
	}
	if s.delivery != nil {
		return s.delivery.update(func(delivery *deliveryLedger) {
			delivery.OutboxSeeded = ledger.Seeded
			for id, record := range ledger.Records {
				entry := delivery.Records[id]
				copied := record
				entry.OutboxRecord = &copied
				delivery.Records[id] = entry
			}
			for id, entry := range delivery.Records {
				if entry.OutboxRecord != nil {
					if _, exists := ledger.Records[id]; !exists {
						entry.OutboxRecord = nil
						delivery.Records[id] = entry
					}
				}
			}
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ledger.Records == nil {
		ledger.Records = map[string]outboxRecord{}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.path, append(data, '\n'), 0o644)
}

func loadLegacyOutboxLedger(path string) (outboxLedger, error) {
	ledger := outboxLedger{Records: map[string]outboxRecord{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if len(strings.TrimSpace(string(data))) != 0 {
		if err := json.Unmarshal(data, &ledger); err != nil {
			return ledger, err
		}
	}
	if ledger.Records == nil {
		ledger.Records = map[string]outboxRecord{}
	}
	return ledger, nil
}

func scanOutboxQueries(threadsDir, indexPath string, dispatched map[string]bool) ([]queryFileInfo, error) {
	index, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	registered := string(index)
	var out []queryFileInfo
	err = filepath.WalkDir(threadsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".query.md") {
			return nil
		}
		letter := strings.TrimSuffix(d.Name(), ".query.md")
		registeredQuery := strings.Contains(registered, "| "+letter+" | QUERY |")
		terminal := strings.Contains(registered, "| "+letter+" | RESULT |") || strings.Contains(registered, "| "+letter+" | CLOSED |")
		// RESULT delivery is file-driven by protocol: an INDEX RESULT row is an
		// optional compatibility radar, so its absence must not leave a finished
		// QUERY dispatchable in Outbox.
		if _, resultErr := os.Stat(filepath.Join(filepath.Dir(path), letter+".result.md")); resultErr == nil {
			terminal = true
		}
		if !validLetterID(letter) || dispatched[letter] || !registeredQuery || terminal {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h := parseArchiveFrontMatter(string(body))
		if h["ID"] != letter || h["Type"] != "QUERY" || h["Thread"] == "" || h["To"] == "" || h["Route"] == "" || h["Date"] == "" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, queryFileInfo{Letter: letter, Thread: h["Thread"], Path: path, To: h["To"], Route: h["Route"], Summary: firstNonEmptyAfter(strings.Split(string(body), "\n"), "## Query"), Digest: contentDigest(body), ModTime: info.ModTime()})
		return nil
	})
	// Historical archives may contain duplicate L-IDs. The Outbox lifecycle is
	// keyed by L-ID, so ambiguity must be rejected rather than oscillating cards.
	counts := map[string]int{}
	for _, q := range out {
		counts[q.Letter]++
	}
	unique := out[:0]
	for _, q := range out {
		if counts[q.Letter] == 1 {
			unique = append(unique, q)
		} else {
			slog.Warn("outbox: skipping ambiguous duplicate letter", "letter", q.Letter)
		}
	}
	return unique, err
}

func formatOutboxCard(i18n *I18n, record outboxRecord, letter, body string, page, pageCount int) (string, [][]ButtonOption) {
	content := fmt.Sprintf("📤 %s\nThread: %s\nTo: %s\nRoute: %s\nSummary: %s\nQuery: %s", letter, record.Thread, record.To, record.Route, record.Summary, filepath.Base(record.QueryPath))
	if pageCount <= 0 {
		return content, [][]ButtonOption{{
			{Text: i18n.T(MsgReceiptViewOriginal), Data: "cmd:/outbox page " + letter + " " + record.Generation + " 0"},
			{Text: "🙋 我自己发", Data: "cmd:/outbox manual " + letter + " " + record.Generation},
			{Text: "🧑‍💼 交秘书发", Data: "cmd:/outbox secretary " + letter + " " + record.Generation},
		}}
	}
	content += "\n\n" + i18n.Tf(MsgReceiptCardPage, page+1, pageCount, body)
	buttons := [][]ButtonOption{}
	if page > 0 {
		buttons = append(buttons, []ButtonOption{{Text: i18n.T(MsgCardPrev), Data: fmt.Sprintf("cmd:/outbox page %s %s %d", letter, record.Generation, page-1)}})
	}
	if page+1 < pageCount {
		buttons = append(buttons, []ButtonOption{{Text: i18n.T(MsgCardNext), Data: fmt.Sprintf("cmd:/outbox page %s %s %d", letter, record.Generation, page+1)}})
	}
	buttons = append(buttons, []ButtonOption{{Text: i18n.T(MsgReceiptCollapse), Data: "cmd:/outbox collapse " + letter + " " + record.Generation}})
	return content, buttons
}

func (e *Engine) SetOutboxConfig(cfg OutboxConfig) { e.configureOutbox(cfg) }

func (e *Engine) configureOutbox(cfg OutboxConfig) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 10 * time.Second
	}
	if cfg.Platform == "" {
		cfg.Platform = "telegram"
	}
	e.outboxConfig = cfg
	if cfg.Enabled && cfg.IndexPath != "" && !e.outboxWatcherStarted {
		if e.deliveryStore == nil {
			e.deliveryStore = newDeliveryStore(e.dataDir)
		}
		if err := e.deliveryStore.migrateLegacyOnce(e.dataDir); err != nil {
			slog.Warn("delivery: legacy migration failed", "error", err)
		}
		e.outboxStore = newOutboxStore(e.dataDir)
		e.bindDeliveryStores()
		ledger, err := e.outboxStore.load()
		if err != nil {
			slog.Warn("outbox: failed to load ledger", "error", err)
			ledger = outboxLedger{Records: map[string]outboxRecord{}}
		}
		e.outboxRecords = ledger.Records
		e.outboxSeeded = ledger.Seeded
		e.outboxManual = e.loadOutboxManual()
		// Preserve legacy manual decisions in the durable projection before
		// future scans start relying solely on it. The old file remains a
		// read-only fallback for compatibility until Phase 3/4 removes it.
		for letter := range e.outboxManual {
			if _, exists := e.outboxRecords[letter]; !exists {
				e.outboxRecords[letter] = outboxRecord{Dispatched: true}
			}
		}
		if err := e.outboxStore.save(outboxLedger{Seeded: e.outboxSeeded, Records: e.outboxRecords}); err != nil {
			slog.Warn("outbox: failed to persist legacy manual migration", "error", err)
		}
		e.outboxWatcherStarted = true
		go func() {
			ticker := time.NewTicker(cfg.PollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-e.ctx.Done():
					return
				case <-ticker.C:
					e.checkOutbox()
				}
			}
		}()
	}
}

func (e *Engine) checkOutbox() {
	e.outboxMu.Lock()
	if !e.outboxConfig.Enabled {
		e.outboxMu.Unlock()
		return
	}
	if e.outboxStore == nil {
		e.outboxStore = newOutboxStore(e.dataDir)
	}
	dispatched := e.ensureDispatchStore().letters()
	for letter := range e.outboxManual {
		dispatched[letter] = true
	}
	queries, err := scanOutboxQueries(e.outboxConfig.threadsDir(), e.outboxConfig.IndexPath, dispatched)
	if err != nil {
		slog.Warn("outbox: scan failed", "error", err)
		e.outboxMu.Unlock()
		return
	}
	affected := map[string]bool{}
	if e.deliveryStore != nil {
		indexBytes, readErr := os.ReadFile(e.outboxConfig.IndexPath)
		if readErr == nil {
			if changed, err := e.deliveryStore.recordQueryAndIndexFingerprints(queries, contentDigest(indexBytes)); err != nil {
				slog.Warn("delivery: failed to persist query/index fingerprints", "error", err)
			} else {
				affected = changed
				slog.Debug("delivery: affected query records", "count", len(changed))
			}
		}
	}
	// First scan establishes a quiet baseline. Existing archive history remains
	// available through /outbox, but must never be emitted as a burst of cards.
	if !e.outboxSeeded {
		for _, q := range queries {
			e.outboxRecords[q.Letter] = outboxRecord{Thread: q.Thread, To: q.To, Route: q.Route, QueryPath: q.Path, Generation: q.Digest, Summary: q.Summary}
		}
		e.outboxSeeded = true
		if err := e.outboxStore.save(outboxLedger{Seeded: true, Records: e.outboxRecords}); err != nil {
			slog.Warn("outbox: failed to persist baseline", "error", err)
		}
		e.outboxMu.Unlock()
		return
	}
	current := map[string]bool{}
	toPublish := make([]queryFileInfo, 0, len(queries))
	for _, q := range queries {
		current[q.Letter] = true
		if e.deliveryStore == nil || affected[q.Letter] {
			toPublish = append(toPublish, q)
		}
	}
	for letter, record := range e.outboxRecords {
		if record.Dispatched {
			continue
		}
		if current[letter] {
			continue
		}
		if record.Card != nil {
			// Keep a durable cleanup record; retryOutboxCleanup performs the
			// Telegram deletion after this lock is released.
			record.Dispatched = true
			e.outboxRecords[letter] = record
			continue
		}
		delete(e.outboxRecords, letter)
	}
	e.persistOutboxLocked()
	e.outboxMu.Unlock()
	e.retryOutboxCleanup()
	// Network I/O must not extend the critical section above. publishOutbox
	// re-checks and commits its result under a short lock after sending.
	for _, q := range toPublish {
		e.publishOutbox(q)
	}
}

// retryOutboxCleanup removes cards only after a confirmed successful delete.
// Failed Telegram deletes retain their dispatched record for the next poll.
func (e *Engine) retryOutboxCleanup() {
	e.outboxMu.Lock()
	type cleanup struct {
		letter string
		card   MessageLocator
	}
	var pending []cleanup
	for letter, record := range e.outboxRecords {
		if !record.Dispatched || record.Card == nil {
			continue
		}
		pending = append(pending, cleanup{letter, *record.Card})
	}
	e.outboxMu.Unlock()
	for _, item := range pending {
		deleted := false
		for _, p := range e.platforms {
			if p.Name() == e.outboxConfig.Platform {
				if d, ok := p.(ReceiptCardDeleter); ok {
					ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
					deleted = d.DeleteReceiptCard(ctx, item.card) == nil
					cancel()
				}
				break
			}
		}
		if deleted {
			e.outboxMu.Lock()
			if r, ok := e.outboxRecords[item.letter]; ok && r.Dispatched && r.Card != nil && *r.Card == item.card {
				delete(e.outboxRecords, item.letter)
				e.persistOutboxLocked()
			}
			e.outboxMu.Unlock()
		}
	}
}

// persistOutboxLocked snapshots the in-memory projection after every lifecycle
// transition. Callers hold outboxMu when they are part of watcher/command flow.
func (e *Engine) persistOutboxLocked() {
	if e.outboxStore == nil {
		return
	}
	if err := e.outboxStore.save(outboxLedger{Seeded: e.outboxSeeded, Records: e.outboxRecords}); err != nil {
		slog.Warn("outbox: failed to persist ledger", "error", err)
	}
}

// markOutboxDispatched removes the interactive card when possible. If the
// platform refuses deletion, it leaves an inert status card and keeps a record
// so the watcher can retry without re-dispatching the letter.
func (e *Engine) markOutboxDispatched(p Platform, letter string, replyCtx any) {
	record, ok := e.outboxRecords[letter]
	if !ok {
		return
	}
	record.Dispatched = true
	e.outboxRecords[letter] = record
	e.persistOutboxLocked()
	// This function is called while handleOutboxCommand owns outboxMu. Queue
	// cleanup so DeleteMessage runs only after that callback releases the lock.
	go e.retryOutboxCleanup()
	if updater, ok := p.(InlineMessageUpdater); ok {
		_ = updater.UpdateMessageWithButtons(e.ctx, replyCtx, "✅ 已分发，正在清理…", nil)
	}
}

func (e *Engine) publishOutbox(q queryFileInfo) {
	e.outboxMu.Lock()
	generation := q.Digest
	prior, hadPrior := e.outboxRecords[q.Letter]
	if hadPrior && prior.Generation == generation {
		if prior.Card != nil || prior.Dispatched || (!prior.RetryAt.IsZero() && time.Now().Before(prior.RetryAt)) {
			e.outboxMu.Unlock()
			return
		}
	}
	e.outboxMu.Unlock()
	record := outboxRecord{Thread: q.Thread, To: q.To, Route: q.Route, QueryPath: q.Path, Generation: generation, Summary: q.Summary}
	if hadPrior && prior.Generation == generation {
		record.Attempts = prior.Attempts
	}
	for _, p := range e.platforms {
		if p.Name() != e.outboxConfig.Platform {
			continue
		}
		replyCtx := any(e.outboxConfig.SessionKey)
		if rc, ok := p.(ReplyContextReconstructor); ok {
			if got, err := rc.ReconstructReplyCtx(e.outboxConfig.SessionKey); err == nil {
				replyCtx = got
			}
		}
		content, buttons := formatOutboxCard(e.i18n, record, q.Letter, "", 0, 0)
		ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
		if cards, ok := p.(ReceiptCardManager); ok {
			card, err := cards.SendReceiptCard(ctx, replyCtx, content, buttons)
			if err == nil {
				record.Card = &card
				record.Attempts = 0
				record.RetryAt = time.Time{}
			} else {
				record.Attempts++
				record.RetryAt = time.Now().Add(30 * time.Second)
				slog.Warn("outbox: failed to send card; will retry", "letter", q.Letter, "attempts", record.Attempts, "retry_at", record.RetryAt, "error", err)
			}
		} else if buttonsPlatform, ok := p.(InlineButtonSender); ok {
			_ = buttonsPlatform.SendWithButtons(ctx, replyCtx, content, buttons)
		} else {
			_ = p.Send(ctx, replyCtx, content)
		}
		cancel()
		break
	}
	e.outboxMu.Lock()
	// A simultaneous dispatch or a newer QUERY generation wins over this
	// completed network effect; do not resurrect a stale card.
	if latest, ok := e.outboxRecords[q.Letter]; ok && latest.Generation != "" && latest.Generation != generation {
		e.outboxMu.Unlock()
		return
	}
	e.outboxRecords[q.Letter] = record
	e.persistOutboxLocked()
	e.outboxMu.Unlock()
}

func (e *Engine) handleOutboxCommand(p Platform, msg *Message, args []string) bool {
	e.outboxMu.Lock()
	defer e.outboxMu.Unlock()
	if len(args) == 0 {
		var lines []string
		for letter, record := range e.outboxRecords {
			if record.Dispatched {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s · %s · %s · %s", letter, record.To, record.Route, record.Thread))
		}
		if len(lines) == 0 {
			e.reply(p, msg.ReplyCtx, "Outbox is empty.")
		} else {
			e.reply(p, msg.ReplyCtx, "Pending outbox:\n"+strings.Join(lines, "\n"))
		}
		return true
	}
	if (args[0] != "page" && args[0] != "collapse" && args[0] != "manual" && args[0] != "secretary") || len(args) < 3 {
		e.reply(p, msg.ReplyCtx, "❌ Outbox item is unavailable.")
		return true
	}
	record, ok := e.outboxRecords[args[1]]
	if !ok || record.Generation != args[2] {
		if (args[0] == "manual" || args[0] == "secretary") && e.outboxResultExists(args[1]) {
			e.reply(p, msg.ReplyCtx, "✅ This letter is already completed; its RESULT has arrived in Inbox.")
			return true
		}
		e.reply(p, msg.ReplyCtx, "❌ Outbox item is unavailable.")
		return true
	}
	if args[0] == "manual" {
		e.outboxManual[args[1]] = true
		_ = e.saveOutboxManual()
		e.markOutboxDispatched(p, args[1], msg.ReplyCtx)
		return true
	}
	if args[0] == "secretary" {
		receipt, err := e.executeDispatch(p, msg.SessionKey, dispatchRequest{Letter: args[1], Thread: record.Thread, To: record.To, Path: record.QueryPath})
		if err != nil {
			e.reply(p, msg.ReplyCtx, "⚠️ Dispatch rejected: "+err.Error())
		} else {
			e.markOutboxDispatched(p, args[1], msg.ReplyCtx)
			e.reply(p, msg.ReplyCtx, receipt)
		}
		return true
	}
	updater, ok := p.(InlineMessageUpdater)
	if !ok {
		e.reply(p, msg.ReplyCtx, "❌ Outbox item is unavailable.")
		return true
	}
	if args[0] == "collapse" {
		content, buttons := formatOutboxCard(e.i18n, record, args[1], "", 0, 0)
		_ = updater.UpdateMessageWithButtons(e.ctx, msg.ReplyCtx, content, buttons)
		return true
	}
	page := 0
	if len(args) == 4 {
		if parsed, err := strconv.Atoi(args[3]); err != nil || parsed < 0 {
			e.reply(p, msg.ReplyCtx, "❌ Outbox item is unavailable.")
			return true
		} else {
			page = parsed
		}
	}
	pages, err := receiptOriginalPages(receiptRecord{ResultPath: record.QueryPath}, "(Query is empty)")
	if err != nil || len(pages) == 0 {
		e.reply(p, msg.ReplyCtx, "❌ Outbox item is unavailable.")
		return true
	}
	if page >= len(pages) {
		e.reply(p, msg.ReplyCtx, "❌ Outbox item is unavailable.")
		return true
	}
	content, buttons := formatOutboxCard(e.i18n, record, args[1], pages[page], page, len(pages))
	_ = updater.UpdateMessageWithButtons(e.ctx, msg.ReplyCtx, content, buttons)
	return true
}

func (e *Engine) outboxResultExists(letter string) bool {
	for _, f := range mustScanResultFiles(e.outboxConfig.threadsDir()) {
		if f.Letter == letter {
			return true
		}
	}
	return false
}

func mustScanResultFiles(threadsDir string) []resultFileInfo {
	files, err := scanResultFiles(threadsDir)
	if err != nil {
		return nil
	}
	return files
}

func (e *Engine) outboxManualPath() string { return filepath.Join(e.dataDir, "outbox_manual.json") }
func (e *Engine) loadOutboxManual() map[string]bool {
	out := map[string]bool{}
	if e.deliveryStore == nil && strings.TrimSpace(e.dataDir) != "" {
		e.deliveryStore = newDeliveryStore(e.dataDir)
		e.bindDeliveryStores()
	}
	if e.deliveryStore != nil {
		if _, err := os.Stat(e.deliveryStore.path); err == nil {
			if delivery, err := e.deliveryStore.load(); err == nil {
				for letter, record := range delivery.Records {
					if record.OutboxManual {
						out[letter] = true
					}
				}
				return out
			}
		}
	}
	data, err := os.ReadFile(e.outboxManualPath())
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}
func (e *Engine) saveOutboxManual() error {
	if e.deliveryStore == nil && strings.TrimSpace(e.dataDir) != "" {
		e.deliveryStore = newDeliveryStore(e.dataDir)
		e.bindDeliveryStores()
	}
	if e.deliveryStore != nil {
		return e.deliveryStore.update(func(delivery *deliveryLedger) {
			for letter, manual := range e.outboxManual {
				record := delivery.Records[letter]
				record.OutboxManual = manual
				delivery.Records[letter] = record
			}
			for letter, record := range delivery.Records {
				if record.OutboxManual && !e.outboxManual[letter] {
					record.OutboxManual = false
					delivery.Records[letter] = record
				}
			}
		})
	}
	return nil
}
