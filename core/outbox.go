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
	ModTime                                  time.Time
}
type outboxRecord struct {
	Thread, To, Route, QueryPath, Generation, Summary string
	Card                                              *MessageLocator
	Dispatched                                        bool
}

// outboxLedger is the daemon-owned delivery projection. Archive files and
// dispatch_ledger.json remain the sources of protocol and dispatch truth.
type outboxLedger struct {
	Seeded  bool                    `json:"seeded"`
	Records map[string]outboxRecord `json:"records"`
}

type outboxStore struct {
	mu   sync.Mutex
	path string
}

func newOutboxStore(dataDir string) *outboxStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &outboxStore{path: filepath.Join(dataDir, "outbox_ledger.json")}
}

func (s *outboxStore) load() (outboxLedger, error) {
	ledger := outboxLedger{Records: map[string]outboxRecord{}}
	if s == nil {
		return ledger, nil
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
		e.outboxStore = newOutboxStore(e.dataDir)
		ledger, err := e.outboxStore.load()
		if err != nil {
			slog.Warn("outbox: failed to load ledger", "error", err)
			ledger = outboxLedger{Records: map[string]outboxRecord{}}
		}
		e.outboxRecords = ledger.Records
		e.outboxSeeded = ledger.Seeded
		e.outboxManual = e.loadOutboxManual()
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
	defer e.outboxMu.Unlock()
	if !e.outboxConfig.Enabled {
		return
	}
	if e.outboxStore == nil {
		e.outboxStore = newOutboxStore(e.dataDir)
	}
	e.retryOutboxCleanup()
	dispatched := e.ensureDispatchStore().letters()
	for letter := range e.outboxManual {
		dispatched[letter] = true
	}
	queries, err := scanOutboxQueries(e.outboxConfig.threadsDir(), e.outboxConfig.IndexPath, dispatched)
	if err != nil {
		slog.Warn("outbox: scan failed", "error", err)
		return
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
		return
	}
	current := map[string]bool{}
	for _, q := range queries {
		current[q.Letter] = true
		e.publishOutbox(q)
	}
	for letter, record := range e.outboxRecords {
		if record.Dispatched {
			continue
		}
		if current[letter] {
			continue
		}
		if record.Card != nil {
			for _, p := range e.platforms {
				if p.Name() == e.outboxConfig.Platform {
					if deleter, ok := p.(MessageDeleter); ok {
						_ = deleter.DeleteMessage(e.ctx, *record.Card)
					}
					break
				}
			}
		}
		delete(e.outboxRecords, letter)
	}
	e.persistOutboxLocked()
}

// retryOutboxCleanup removes cards only after a confirmed successful delete.
// Failed Telegram deletes retain their dispatched record for the next poll.
func (e *Engine) retryOutboxCleanup() {
	for letter, record := range e.outboxRecords {
		if !record.Dispatched || record.Card == nil {
			continue
		}
		for _, p := range e.platforms {
			if p.Name() != e.outboxConfig.Platform {
				continue
			}
			deleter, ok := p.(MessageDeleter)
			if ok && deleter.DeleteMessage(e.ctx, *record.Card) == nil {
				delete(e.outboxRecords, letter)
			}
			break
		}
	}
	e.persistOutboxLocked()
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
	if deleter, ok := p.(MessageDeleter); ok && deleter.DeleteMessage(e.ctx, replyCtx) == nil {
		delete(e.outboxRecords, letter)
		e.persistOutboxLocked()
		return
	}
	record.Dispatched = true
	e.outboxRecords[letter] = record
	e.persistOutboxLocked()
	if updater, ok := p.(InlineMessageUpdater); ok {
		_ = updater.UpdateMessageWithButtons(e.ctx, replyCtx, "✅ 已分发，正在清理…", nil)
	}
}

func (e *Engine) publishOutbox(q queryFileInfo) {
	generation := q.Digest
	if prior, ok := e.outboxRecords[q.Letter]; ok && prior.Generation == generation {
		return
	}
	record := outboxRecord{Thread: q.Thread, To: q.To, Route: q.Route, QueryPath: q.Path, Generation: generation, Summary: q.Summary}
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
		if cards, ok := p.(ReceiptCardManager); ok {
			card, err := cards.SendReceiptCard(context.Background(), replyCtx, content, buttons)
			if err == nil {
				record.Card = &card
			}
		} else if buttonsPlatform, ok := p.(InlineButtonSender); ok {
			_ = buttonsPlatform.SendWithButtons(context.Background(), replyCtx, content, buttons)
		} else {
			_ = p.Send(context.Background(), replyCtx, content)
		}
		break
	}
	e.outboxRecords[q.Letter] = record
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

func (e *Engine) outboxManualPath() string { return filepath.Join(e.dataDir, "outbox_manual.json") }
func (e *Engine) loadOutboxManual() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(e.outboxManualPath())
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}
func (e *Engine) saveOutboxManual() error {
	data, err := json.Marshal(e.outboxManual)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.outboxManualPath()), 0o755); err != nil {
		return err
	}
	return AtomicWriteFile(e.outboxManualPath(), data, 0o644)
}
