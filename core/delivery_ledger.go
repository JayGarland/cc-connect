package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const deliveryLedgerVersion = 1

type deliveryLedger struct {
	Version        int                       `json:"version"`
	Records        map[string]deliveryRecord `json:"records"`
	LastFullAudit  time.Time                 `json:"last_full_audit,omitempty"`
	InboxSeeded    bool                      `json:"inbox_seeded,omitempty"`
	OutboxSeeded   bool                      `json:"outbox_seeded,omitempty"`
	LegacyImported bool                      `json:"legacy_imported,omitempty"`
}

const deliveryFullAuditInterval = 15 * time.Minute

func (l deliveryLedger) fullAuditDue(now time.Time) bool {
	return l.LastFullAudit.IsZero() || !now.Before(l.LastFullAudit.Add(deliveryFullAuditInterval))
}

// deliveryRecord is the per-letter durable delivery projection. Its fields
// deliberately model archive facts separately from Telegram effects.
type deliveryRecord struct {
	Thread, QueryPath, ResultPath string               `json:",omitempty"`
	QueryDigest, ResultDigest     string               `json:",omitempty"`
	Inbox                         deliveryInboxState   `json:"inbox"`
	Outbox                        deliveryOutboxState  `json:"outbox"`
	Scanner                       deliveryScannerState `json:"scanner"`
	Receipt                       *receiptRecord       `json:"receipt,omitempty"`
	OutboxRecord                  *outboxRecord        `json:"outbox_record,omitempty"`
	InboxNotified                 string               `json:"inbox_notified,omitempty"`
	OutboxManual                  bool                 `json:"outbox_manual,omitempty"`
}

type deliveryInboxState struct {
	Status string          `json:"status,omitempty"`
	Card   *MessageLocator `json:"card,omitempty"`
}
type deliveryOutboxState struct {
	Status   string          `json:"status,omitempty"`
	Card     *MessageLocator `json:"card,omitempty"`
	Attempts int             `json:"attempts,omitempty"`
	RetryAt  time.Time       `json:"retry_at,omitempty"`
}
type deliveryScannerState struct {
	QueryFingerprint  string `json:"query_fingerprint,omitempty"`
	ResultFingerprint string `json:"result_fingerprint,omitempty"`
	IndexFingerprint  string `json:"index_fingerprint,omitempty"`
}

// desiredDeliveryState is the single protocol projection rule used by the
// later Inbox/Outbox effect planners. Archive and dispatch facts decide state;
// Telegram locators never decide whether a letter is terminal.
func desiredDeliveryState(query queryFileInfo, result resultFileInfo, dispatched bool) deliveryRecord {
	r := deliveryRecord{Thread: query.Thread, QueryPath: query.Path, QueryDigest: query.Digest}
	if result.Path != "" {
		r.ResultPath = result.Path
		r.Inbox.Status = "pending"
	}
	if result.Path != "" || dispatched {
		r.Outbox.Status = "terminal"
	} else if query.Path != "" {
		r.Outbox.Status = "pending"
	}
	return r
}

// changedDeliveryInputs compares persisted archive fingerprints with the
// current discovery pass. Callers reconcile only the returned L-IDs; a
// periodic full audit can supply every current input as the safety fallback.
func changedDeliveryInputs(prior deliveryLedger, current map[string]deliveryScannerState) map[string]bool {
	changed := map[string]bool{}
	for id, state := range current {
		old, exists := prior.Records[id]
		if !exists || old.Scanner != state {
			changed[id] = true
		}
	}
	for id := range prior.Records {
		if _, exists := current[id]; !exists {
			changed[id] = true
		}
	}
	return changed
}

func (s *deliveryStore) recordResultFingerprints(files []resultFileInfo) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return nil, err
	}
	current := map[string]deliveryScannerState{}
	for _, f := range files {
		r := ledger.Records[f.Letter]
		r.Scanner.ResultFingerprint = f.ModTime.UTC().Format(time.RFC3339Nano)
		current[f.Letter] = r.Scanner
	}
	changed := changedDeliveryInputs(ledger, current)
	if ledger.fullAuditDue(time.Now().UTC()) {
		for id := range current {
			changed[id] = true
		}
		ledger.LastFullAudit = time.Now().UTC()
	}
	for _, f := range files {
		r := ledger.Records[f.Letter]
		r.Thread, r.ResultPath, r.Scanner = f.Thread, f.Path, current[f.Letter]
		ledger.Records[f.Letter] = r
	}
	return changed, s.save(ledger)
}

func (s *deliveryStore) recordQueryAndIndexFingerprints(queries []queryFileInfo, indexFingerprint string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return nil, err
	}
	current := map[string]deliveryScannerState{}
	for _, q := range queries {
		r := ledger.Records[q.Letter]
		r.Scanner.QueryFingerprint, r.Scanner.IndexFingerprint = q.Digest, indexFingerprint
		current[q.Letter] = r.Scanner
	}
	changed := changedDeliveryInputs(ledger, current)
	if ledger.fullAuditDue(time.Now().UTC()) {
		for id := range current {
			changed[id] = true
		}
		ledger.LastFullAudit = time.Now().UTC()
	}
	for _, q := range queries {
		r := ledger.Records[q.Letter]
		r.Thread, r.QueryPath, r.QueryDigest = q.Thread, q.Path, q.Digest
		r.Scanner = current[q.Letter]
		ledger.Records[q.Letter] = r
	}
	return changed, s.save(ledger)
}

// deliveryStore is the Phase 3 daemon-local projection. Archive material is
// deliberately excluded: this file only belongs under Engine.dataDir.
type deliveryStore struct {
	path string
	mu   sync.Mutex
}

func newDeliveryStore(dataDir string) *deliveryStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &deliveryStore{path: filepath.Join(dataDir, "delivery_ledger.json")}
}

// bindDeliveryStores gives both watcher facades one mutex and one atomic
// read-modify-write path. They expose legacy-shaped ledgers to their callers,
// but delivery_ledger.json is their sole runtime writer.
func (e *Engine) bindDeliveryStores() {
	if e.deliveryStore == nil {
		e.deliveryStore = newDeliveryStore(e.dataDir)
	}
	if e.notifyStore != nil {
		e.notifyStore.delivery = e.deliveryStore
	}
	if e.outboxStore != nil {
		e.outboxStore.delivery = e.deliveryStore
	}
}

func (s *deliveryStore) load() (deliveryLedger, error) {
	ledger := deliveryLedger{Version: deliveryLedgerVersion, Records: map[string]deliveryRecord{}}
	if s == nil {
		return ledger, nil
	}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &ledger); err != nil {
			return ledger, err
		}
	}
	if ledger.Version == 0 {
		ledger.Version = deliveryLedgerVersion
	}
	if ledger.Records == nil {
		ledger.Records = map[string]deliveryRecord{}
	}
	return ledger, nil
}

func (s *deliveryStore) save(ledger deliveryLedger) error {
	if s == nil {
		return nil
	}
	ledger.Version = deliveryLedgerVersion
	if ledger.Records == nil {
		ledger.Records = map[string]deliveryRecord{}
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return AtomicWriteFile(s.path, append(data, '\n'), 0o644)
}

func (s *deliveryStore) update(fn func(*deliveryLedger)) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return err
	}
	fn(&ledger)
	return s.save(ledger)
}

// migrateLegacyOnce imports daemon-local projections only when the unified
// file is absent. Existing unified state always wins on later restarts.
func (s *deliveryStore) migrateLegacyOnce(dataDir string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger := deliveryLedger{Version: deliveryLedgerVersion, Records: map[string]deliveryRecord{}}
	if _, err := os.Stat(s.path); err == nil {
		var loadErr error
		ledger, loadErr = s.load()
		if loadErr != nil {
			return loadErr
		}
		if ledger.LegacyImported {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if n, err := loadLegacyNotifyLedger(filepath.Join(dataDir, "notify_ledger.json")); err == nil {
		ledger.InboxSeeded = n.Seeded
		for id, r := range n.Receipts {
			d := ledger.Records[id]
			if d.Thread == "" {
				d.Thread = r.Thread
			}
			if d.ResultPath == "" {
				d.ResultPath = r.ResultPath
			}
			if d.ResultDigest == "" {
				d.ResultDigest = r.Generation
			}
			d.Inbox = deliveryInboxState{Status: r.Status, Card: r.Card}
			d.Receipt = ptrReceipt(r)
			d.InboxNotified = n.Notified[id]
			ledger.Records[id] = d
		}
		for id, notified := range n.Notified {
			d := ledger.Records[id]
			d.InboxNotified = notified
			ledger.Records[id] = d
		}
	} else {
		return err
	}
	if o, err := loadLegacyOutboxLedger(filepath.Join(dataDir, "outbox_ledger.json")); err == nil {
		ledger.OutboxSeeded = o.Seeded
		for id, r := range o.Records {
			d := ledger.Records[id]
			if d.Thread == "" {
				d.Thread = r.Thread
			}
			if d.QueryPath == "" {
				d.QueryPath = r.QueryPath
			}
			if d.QueryDigest == "" {
				d.QueryDigest = r.Generation
			}
			d.Outbox = deliveryOutboxState{Card: r.Card, Attempts: r.Attempts, RetryAt: r.RetryAt}
			if r.Dispatched {
				d.Outbox.Status = "dispatched"
			} else {
				d.Outbox.Status = "pending"
			}
			copied := r
			d.OutboxRecord = &copied
			ledger.Records[id] = d
		}
	}
	manual := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(dataDir, "outbox_manual.json")); err == nil {
		_ = json.Unmarshal(data, &manual)
	}
	for id := range manual {
		d := ledger.Records[id]
		d.Outbox.Status = "manual"
		d.OutboxManual = true
		ledger.Records[id] = d
	}
	ledger.LegacyImported = true
	return s.save(ledger)
}

func ptrReceipt(r receiptRecord) *receiptRecord { return &r }
