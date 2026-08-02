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

type verificationState string

const (
	verificationReady    verificationState = "ready"
	verificationAwaiting verificationState = "awaiting_verification"
	verificationInflight verificationState = "verification_inflight"
	// verificationPendingQC marks an unregistered letter in the 先审后存 queue
	// (INDEX has no QUERY row) that names a verifier. Its card is read-only —
	// "查看原文" only, no dispatch buttons — until verification passes and the
	// author registers it (L-0766 N1). It never reaches awaiting/inflight because
	// the record-less auto-trigger (L-0762 item 1) drives verification without an
	// outbox record.
	verificationPendingQC verificationState = "pending_qc"
)

// classifyVerification is intentionally archive-text-only. A blank Verify is
// legacy/exempt-ready; a named request awaits a nonempty Verified field. It
// performs no grammar, identity, authorship, or content validation.
func classifyVerification(verify, verified string) verificationState {
	// Protocol (L-0687): Verify is either a named verifier seat, or the
	// exemption literal "none — <basis>". Both the legacy blank form and the
	// "none" exemption literal are exempt-ready; only a named verifier with an
	// empty Verified field awaits verification.
	v := strings.TrimSpace(verify)
	if v == "" || strings.HasPrefix(strings.ToLower(v), "none") || strings.TrimSpace(verified) != "" {
		return verificationReady
	}
	return verificationAwaiting
}

type queryFileInfo struct {
	Letter, Thread, Path, To, Route, Verify, Verified, Summary, Digest string
	// From is the letter's author seat header (e.g. "secretary-L-0042"), used by
	// the pending-QC scan to route BLOCK findings back to the author for
	// correction. Registered letters reuse To for relay; pending-QC letters have
	// no outbox record and no dispatch target, so the author is the only relay
	// recipient.
	From string
	// Unregistered is set by scanPendingReviewQueries: the file exists in
	// threads/ but has no INDEX QUERY row (先审后存). Such files render as
	// read-only ⏳ 待质检 cards with no dispatch buttons (L-0766 N1).
	Unregistered bool
	ModTime      time.Time
}
type outboxRecord struct {
	Thread, To, Route, Verify, QueryPath, Generation, Summary string
	// Verified mirrors the front-matter header so a pending-QC card can show the
	// PASS state (N2) without re-parsing the file on render.
	Verified string `json:"verified,omitempty"`
	// Unregistered marks a 先审后存 letter that exists in threads/ but has no
	// INDEX QUERY row yet; such letters render as read-only ⏳ 待质检 cards.
	Unregistered bool `json:"unregistered,omitempty"`
	Verification verificationState `json:"verification"`
	Card         *MessageLocator
	Dispatched   bool
	RefreshPending bool      `json:"refresh_pending,omitempty"`
	Attempts     int         `json:"attempts,omitempty"`
	RetryAt      time.Time   `json:"retry_at,omitempty"`
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
		if h["ID"] != letter || h["Type"] != "QUERY" || h["Thread"] == "" || h["To"] == "" || h["Date"] == "" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, queryFileInfo{Letter: letter, Thread: h["Thread"], Path: path, To: h["To"], Route: h["Route"], Verify: h["Verify"], Verified: h["Verified"], From: h["From"], Summary: firstNonEmptyAfter(strings.Split(string(body), "\n"), "## Query"), Digest: contentDigest(body), ModTime: info.ModTime()})
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

// scanPendingReviewQueries finds letters that must be auto-submitted for
// pre-dispatch verification but have no outbox record yet: files whose QUERY row
// is not in the INDEX, whose `Verify:` names a verifier (not a `none` exemption),
// and whose `Verified:` is still empty. Under 先审后存 (review-before-register)
// these are written and left unregistered until verification passes, so they
// never enter the awaiting_verification state the button path serves.
//
// Coverage declaration (L-0697 / L-0757): this scan walks the same threads dir
// as scanOutboxQueries and applies one inverted predicate — `registeredQuery` is
// skipped instead of required. Every `.query.md` file that names a verifier
// (`Verify:` non-empty and not a `none` exemption) and is not yet registered is
// returned, regardless of whether `Verified:` is still empty (pending) or already
// filled (PASS, awaiting author registration — L-0766 N2). No further whitelist
// narrows it. The only exclusions are the ones scanOutboxQueries already applies
// (invalid letter id, terminal result) plus the registered-query skip, each
// justified in code below.
func scanPendingReviewQueries(threadsDir, indexPath string) ([]queryFileInfo, error) {
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
		if _, resultErr := os.Stat(filepath.Join(filepath.Dir(path), letter+".result.md")); resultErr == nil {
			terminal = true
		}
		// Registered letters are served by the outbox card flow; pending-QC is
		// specifically the unregistered population.
		if registeredQuery || !validLetterID(letter) || terminal {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h := parseArchiveFrontMatter(string(body))
		if h["ID"] != letter || h["Type"] != "QUERY" || h["Thread"] == "" || h["To"] == "" || h["Date"] == "" {
			return nil
		}
		// Pending-QC predicate: the letter names a verifier (`Verify:` non-empty
		// and not a `none` exemption). `Verified:` may be empty (still pending,
		// auto-trigger submits it) or non-empty (PASS reached; N2 relays the
		// register request to the author). Exempt and `none` files classify ready
		// and are excluded, so the scan never submits an exempt or registered file.
		v := strings.TrimSpace(h["Verify"])
		if v == "" || strings.HasPrefix(strings.ToLower(v), "none") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, queryFileInfo{Letter: letter, Thread: h["Thread"], Path: path, To: h["To"], Route: h["Route"], Verify: h["Verify"], Verified: h["Verified"], From: h["From"], Summary: firstNonEmptyAfter(strings.Split(string(body), "\n"), "## Query"), Digest: contentDigest(body), Unregistered: true, ModTime: info.ModTime()})
		return nil
	})
	return out, err
}

// checkPendingReview is the 先审后存 auto-trigger. On every outbox poll it scans
// for unregistered pending-QC letters and, for each, advances the review round:
// a letter with no BLOCK yet is relayed to its verifier (record-less entry); a
// letter carrying a 校验 BLOCK comment is relayed back to its author seat for
// correction (L-0762 item 3). Idempotency is the expectation store's
// LetterID+Generation key: repeated scans of the same file generation produce no
// second relay, and any edit (author correction) changes the content digest so
// the next scan fires the next round.
func (e *Engine) checkPendingReview() {
	if !e.outboxConfig.Enabled || e.outboxConfig.IndexPath == "" {
		return
	}
	queries, err := scanPendingReviewQueries(e.outboxConfig.threadsDir(), e.outboxConfig.IndexPath)
	if err != nil {
		slog.Warn("pending review: scan failed", "error", err)
		return
	}
	for _, q := range queries {
		e.advancePendingReview(q)
	}
}

// pendingReviewAction classifies the next step for a pending-QC letter based on
// its current file state (archive text only).
type pendingReviewAction int

const (
	pendingReviewNone pendingReviewAction = iota
	pendingReviewRequestVerifier
	pendingReviewRelayBlockToAuthor
	// pendingReviewRegisterToAuthor fires when the verifier has PASSed the letter
	// (`Verified:` non-empty): the author seat is relayed a request to register
	// it (L-0766 N2), which closes the 先审后存 loop — registration surfaces the
	// INDEX row and the card migrates to dispatchable.
	pendingReviewRegisterToAuthor
)

// classifyPendingReview reads a pending-QC query front matter and body and
// decides which seat must act next:
//
//   - Verified non-empty (PASS) → author registers (N2).
//   - latest 校验 comment is BLOCK with no Correction after it → author corrects.
//   - otherwise (fresh, or author-corrected, or PASS-adjacent) → verifier.
//
// A letter whose latest review comment is a PASS verdict but whose `Verified:`
// header is still empty falls through to the verifier branch — the scan only
// treats a non-empty `Verified:` as PASS (L-0760 boundary: cc-connect never
// writes `Verified:` itself).
func classifyPendingReview(verified, body string) pendingReviewAction {
	if strings.TrimSpace(verified) != "" {
		return pendingReviewRegisterToAuthor
	}
	// The latest review comment governs the next actor. A BLOCK comment written
	// by the verifier is append-only: `<!-- 校验 ... —— BLOCK: ... -->`. The
	// author's Correction comments are `<!-- Correction ... -->`. A correction
	// that follows a BLOCK means the author already acted, so the verifier is
	// next.
	lastVerify := strings.LastIndex(body, "<!-- 校验")
	lastCorrection := strings.LastIndex(body, "<!-- Correction")
	if lastVerify < 0 {
		return pendingReviewRequestVerifier
	}
	if lastCorrection > lastVerify {
		// Author corrected after the last verification comment → verifier re-reviews.
		return pendingReviewRequestVerifier
	}
	// Last review comment is a verification verdict. BLOCK → author corrects.
	if strings.Contains(body[lastVerify:], "BLOCK") {
		return pendingReviewRelayBlockToAuthor
	}
	return pendingReviewRequestVerifier
}

// extractBlockFinding returns the finding text of the most recent 校验 BLOCK
// comment, used as the relay body when a BLOCK is handed back to the author.
func extractBlockFinding(body string) string {
	marker := "BLOCK:"
	i := strings.LastIndex(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.Index(rest, "-->"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// advancePendingReview drives one pending-QC letter through its next review
// round. It is idempotent per LetterID+Generation via the expectation store, so
// it is safe to call on every poll.
func (e *Engine) advancePendingReview(q queryFileInfo) {
	// Re-read the body so classification and the generation hash both describe
	// the current file state; a concurrent edit changes the digest and therefore
	// the expectation key, invalidating any stale entry.
	body, err := os.ReadFile(q.Path)
	if err != nil {
		slog.Warn("pending review: cannot read query", "letter", q.Letter, "error", err)
		return
	}
	generation := contentDigest(body)
	action := classifyPendingReview(q.Verified, string(body))
	switch action {
	case pendingReviewRequestVerifier:
		receipt, ok, err := e.requestVerificationRecordless(q.Letter, generation, q.Verify, q.Path)
		if err != nil {
			slog.Warn("pending review: request failed", "letter", q.Letter, "error", err)
			return
		}
		if !ok {
			slog.Debug("pending review: already requested", "letter", q.Letter, "receipt", receipt)
		}
	case pendingReviewRelayBlockToAuthor:
		author := pendingReviewAuthorProject(q.From)
		if author == "" {
			slog.Warn("pending review: cannot resolve author seat", "letter", q.Letter, "from", q.From)
			return
		}
		receipt, ok, err := e.relayVerificationBlock(q.Letter, generation, q.Path, author, q.Verify, extractBlockFinding(string(body)))
		if err != nil {
			slog.Warn("pending review: block relay failed", "letter", q.Letter, "error", err)
			return
		}
		if !ok {
			slog.Debug("pending review: block already relayed", "letter", q.Letter, "receipt", receipt)
		}
	case pendingReviewRegisterToAuthor:
		author := pendingReviewAuthorProject(q.From)
		if author == "" {
			slog.Warn("pending review: cannot resolve author seat for registration", "letter", q.Letter, "from", q.From)
			return
		}
		receipt, ok, err := e.relayVerificationRegister(q.Letter, generation, q.Path, author, q.Verify)
		if err != nil {
			slog.Warn("pending review: register relay failed", "letter", q.Letter, "error", err)
			return
		}
		if !ok {
			slog.Debug("pending review: register already relayed", "letter", q.Letter, "receipt", receipt)
		}
	}
}

// pendingReviewAuthorProject resolves the QUERY's `From:` logical seat (L-0539
// instance form `<seat>-L-XXXX`) to the relay-registered project name that must
// receive BLOCK findings for correction. The author is the seat that wrote the
// letter; corrections belong to the author seat per L-0761. Returns "" when the
// seat cannot be mapped.
func pendingReviewAuthorProject(from string) string {
	seat := from
	if i := strings.Index(from, "-L-"); i > 0 {
		seat = from[:i]
	}
	if seat == "" {
		return ""
	}
	// Role aliases mirror executeDispatch's resolution (dispatch.go:679-689) plus
	// the seats that write QUERYs (secretary) or review them, mapping a logical
	// seat to the configured project name that hosts it. The pending-QC author is
	// typically the secretary (writes QUERYs) or an engineer under Boss's
	// horizontal direction.
	alias := map[string]string{
		"architect":        "architect-claude",
		"secretary":        "secretary-seat",
		"dev":              "dev-pro",
		"reviewer":         "reviewer-seat",
		"counsel":          "counsel-seat",
		"researcher":       "researcher-seat",
		"security-auditor": "security-auditor-seat",
	}
	if mapped, ok := alias[seat]; ok {
		return mapped
	}
	return seat
}

func displayOutboxRoute(route string) string {
	if route == "" {
		return "default"
	}
	return route
}

func verificationCallbackToken(letter, generation string) string {
	// Keep the full letter and generation in the durable ledger. Callback data
	// carries only this fixed-width lookup token, resolved against an existing
	// current record; it never supplies a verifier destination.
	return contentDigest([]byte(letter + "\x00" + generation))
}

func formatOutboxCard(i18n *I18n, record outboxRecord, letter, body string, page, pageCount int) (string, [][]ButtonOption) {
	route := displayOutboxRoute(record.Route)
	content := fmt.Sprintf("📤 %s\nThread: %s\nTo: %s\nRoute: %s\nSummary: %s\nQuery: %s", letter, record.Thread, record.To, route, record.Summary, filepath.Base(record.QueryPath))
	if record.Verification == verificationPendingQC {
		// 先审后存 read-only card (L-0766 N1): an unregistered letter awaiting
		// verification. No dispatch buttons — the only action is viewing the
		// original. Registration (N2) happens after PASS, which migrates the card
		// to the dispatchable branch below.
		content += "\n⏳ 待质检（未登记）"
		if record.Verify != "" {
			content += "\nVerifier: " + record.Verify
		}
		if strings.TrimSpace(record.Verified) != "" {
			content += "\n✅ 已校验 · 待登记"
		}
		return content, [][]ButtonOption{{
			{Text: i18n.T(MsgReceiptViewOriginal), Data: "cmd:/outbox page " + letter + " " + record.Generation + " 0"},
		}}
	}
	if record.Verification == verificationAwaiting || record.Verification == verificationInflight {
		content += "\nAwaiting verification: " + record.Verify
		if record.Verification == verificationInflight {
			return content + "\nVerification requested.", [][]ButtonOption{{
				{Text: i18n.T(MsgReceiptViewOriginal), Data: "cmd:/outbox page " + letter + " " + record.Generation + " 0"},
			}}
		}
		return content, [][]ButtonOption{{
			{Text: i18n.T(MsgReceiptViewOriginal), Data: "cmd:/outbox page " + letter + " " + record.Generation + " 0"},
			{Text: "🔎 Request verification", Data: "verification_request:" + verificationCallbackToken(letter, record.Generation)},
		}}
	}
	if pageCount <= 0 {
		return content, [][]ButtonOption{{
			{Text: i18n.T(MsgReceiptViewOriginal), Data: "cmd:/outbox page " + letter + " " + record.Generation + " 0"},
			{Text: "🙋 我自己发", Data: "cmd:/outbox manual " + letter + " " + record.Generation},
						{Text: "⚡ 直接开始", Data: "cmd:/outbox secretary " + letter + " " + record.Generation},
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
					e.checkPendingReview()
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
	// 先审后存 pending-QC population (L-0766 N1): unregistered letters that name
	// a verifier render as read-only ⏳ 待质检 cards. Registered letters are served
	// by the registered query flow above; exempt (Verify none) letters are absent
	// from this scan entirely.
	pendingQueries, perr := scanPendingReviewQueries(e.outboxConfig.threadsDir(), e.outboxConfig.IndexPath)
	if perr != nil {
		slog.Warn("outbox: pending-QC scan failed", "error", perr)
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
			e.outboxRecords[q.Letter] = outboxRecord{Thread: q.Thread, To: q.To, Route: q.Route, Verify: q.Verify, QueryPath: q.Path, Generation: q.Digest, Summary: q.Summary, Verified: q.Verified, Verification: classifyVerification(q.Verify, q.Verified)}
		}
		for _, q := range pendingQueries {
			e.outboxRecords[q.Letter] = outboxRecord{Thread: q.Thread, To: q.To, Route: q.Route, Verify: q.Verify, QueryPath: q.Path, Generation: q.Digest, Summary: q.Summary, Verified: q.Verified, Unregistered: true, Verification: verificationPendingQC}
		}
		e.outboxSeeded = true
		if err := e.outboxStore.save(outboxLedger{Seeded: true, Records: e.outboxRecords}); err != nil {
			slog.Warn("outbox: failed to persist baseline", "error", err)
		}
		e.outboxMu.Unlock()
		return
	}
	current := map[string]bool{}
	toPublish := make([]queryFileInfo, 0, len(queries)+len(pendingQueries))
	for _, q := range queries {
		current[q.Letter] = true
		record, known := e.outboxRecords[q.Letter]
		retryRefresh := known && record.RefreshPending && (record.RetryAt.IsZero() || !time.Now().Before(record.RetryAt))
		if e.deliveryStore == nil || affected[q.Letter] || retryRefresh {
			toPublish = append(toPublish, q)
		}
	}
	// Pending-QC files join the card surface too. Publish whenever a card is
	// missing or stale — the transition from ⏳ 待质检 to 已校验 · 待登记 (N2) to
	// registered-dispatchable all flow through this same publish path.
	for _, q := range pendingQueries {
		current[q.Letter] = true
		record, known := e.outboxRecords[q.Letter]
		if known && record.Generation == q.Digest && record.Card != nil && !record.RefreshPending {
			continue
		}
		toPublish = append(toPublish, q)
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
		if prior.Dispatched || (!prior.RetryAt.IsZero() && time.Now().Before(prior.RetryAt)) || (prior.Card != nil && !prior.RefreshPending) {
			e.outboxMu.Unlock()
			return
		}
	}
	e.outboxMu.Unlock()
	record := outboxRecord{Thread: q.Thread, To: q.To, Route: q.Route, Verify: q.Verify, QueryPath: q.Path, Generation: generation, Summary: q.Summary, Verified: q.Verified, Unregistered: q.Unregistered, Verification: classifyVerification(q.Verify, q.Verified)}
	if q.Unregistered {
		// 先审后存 pending-QC file: no outbox record exists and the letter is not
		// registered, so the card renders read-only (N1). Verification state is
		// tracked separately via the expectation store and the archive text, not
		// via the awaiting/inflight outbox states the button path uses.
		record.Verification = verificationPendingQC
	}
	if hadPrior {
		record.Card = prior.Card
	}
	if hadPrior && (prior.Generation == generation || prior.RefreshPending) {
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
		// If the stored locator points at a different platform, treat it as missing so we can send a fresh card.
		if record.Card != nil && record.Card.Platform != p.Name() {
			record.Card = nil
		}
		if record.Card != nil {
			cards, ok := p.(ReceiptCardManager)
			if !ok {
				record.RefreshPending = true
				record.Attempts++
				record.RetryAt = time.Now().Add(30 * time.Second)
				// Keep prior generation so callback buttons on the existing card remain valid until refresh succeeds.
				if hadPrior {
					record.Generation = prior.Generation
				}
				slog.Warn("outbox: platform cannot refresh existing card; will retry without creating duplicate", "letter", q.Letter)
			} else if err := cards.UpdateReceiptCard(ctx, *record.Card, content, buttons); err == nil {
				record.RefreshPending = false
				record.Attempts = 0
				record.RetryAt = time.Time{}
			} else {
				record.RefreshPending = true
				record.Attempts++
				record.RetryAt = time.Now().Add(30 * time.Second)
				// Keep prior generation so callback buttons on the existing card remain valid until refresh succeeds.
				if hadPrior {
					record.Generation = prior.Generation
				}
				slog.Warn("outbox: failed to refresh card; will retry without creating duplicate", "letter", q.Letter, "attempts", record.Attempts, "retry_at", record.RetryAt, "error", err)
			}
		} else if cards, ok := p.(ReceiptCardManager); ok {
			card, err := cards.SendReceiptCard(ctx, replyCtx, content, buttons)
			if err == nil {
				record.Card = &card
				record.RefreshPending = false
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
	if latest, ok := e.outboxRecords[q.Letter]; ok && latest.Generation != "" && latest.Generation != generation && (!hadPrior || latest.Generation != prior.Generation) {
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
			lines = append(lines, fmt.Sprintf("%s · %s · %s · %s", letter, record.To, displayOutboxRoute(record.Route), record.Thread))
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
	if (record.Verification == verificationAwaiting || record.Verification == verificationInflight) && (args[0] == "manual" || args[0] == "secretary") {
		e.reply(p, msg.ReplyCtx, "⚠️ Dispatch is awaiting verification.")
		return true
	}
	if record.Verification == verificationPendingQC && (args[0] == "manual" || args[0] == "secretary") {
		// 先审后存 read-only card: the letter is not registered and cannot be
		// dispatched. Only registration after PASS (N2) makes it dispatchable.
		e.reply(p, msg.ReplyCtx, "⏳ 待质检（未登记）——校验通过并登记后才能派发。")
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
