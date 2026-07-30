package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PendingDispatch is a [DISPATCH] proposal awaiting Boss's explicit
// confirmation via the Outbox-style confirm card (L-0667, replacing the
// former auto-execute-on-parse behavior). Nothing in this file actuates a
// dispatch — parseDispatchBlock/validateDispatchArchive remain the sensors,
// and ControlPlaneDispatch (L-0662) remains the sole actuator, called only
// when the confirm button is pressed.
type PendingDispatch struct {
	Request          dispatchRequest `json:"request"`
	SourceSessionKey string          `json:"source_session_key"`
	SourcePlatform   string          `json:"source_platform"`
	Card             MessageLocator  `json:"card"`
	ProposedAt       time.Time       `json:"proposed_at"`
}

type pendingDispatchLedger struct {
	Entries []PendingDispatch `json:"entries"`
}

// pendingDispatchStore is the atomic-write/mutex-guarded JSON store for
// pending dispatch proposals, mirroring dispatchStore's shape
// (core/dispatch.go:119-236).
type pendingDispatchStore struct {
	mu   sync.Mutex
	path string
}

func newPendingDispatchStore(dataDir string) *pendingDispatchStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &pendingDispatchStore{path: filepath.Join(dataDir, "pending_dispatch.json")}
}

// upsert records or replaces the pending proposal for this Letter ID. A
// re-proposal of the same letter (e.g. Secretary retries after an earlier
// validation error) supersedes any earlier pending card for it.
func (s *pendingDispatchStore) upsert(entry PendingDispatch) error {
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
		if ledger.Entries[i].Request.Letter == entry.Request.Letter {
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

// takeByLetter removes and returns the pending proposal for a Letter ID, if
// any. Removing on read makes confirmation idempotent: a second callback
// press (double-click, or a press after the proposal already resolved)
// finds nothing pending rather than dispatching twice.
func (s *pendingDispatchStore) takeByLetter(letter string) (PendingDispatch, bool, error) {
	var zero PendingDispatch
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
		if ledger.Entries[i].Request.Letter == letter {
			found := ledger.Entries[i]
			ledger.Entries = append(ledger.Entries[:i], ledger.Entries[i+1:]...)
			if err := s.saveLocked(ledger); err != nil {
				return zero, false, err
			}
			return found, true, nil
		}
	}
	return zero, false, nil
}

// peekByLetter returns the pending proposal for a Letter ID without
// removing it — unlike takeByLetter, used by L-0694's bulk-close review
// flow (showBulkCloseReview/cancelBulkClose), which must be able to look at
// (and, on cancel, redraw) the dispatch card's own content without
// consuming the confirm-dispatch action it is independent of.
func (s *pendingDispatchStore) peekByLetter(letter string) (PendingDispatch, bool, error) {
	var zero PendingDispatch
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
		if ledger.Entries[i].Request.Letter == letter {
			return ledger.Entries[i], true, nil
		}
	}
	return zero, false, nil
}

func (s *pendingDispatchStore) loadLocked() (pendingDispatchLedger, error) {
	var ledger pendingDispatchLedger
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

func (s *pendingDispatchStore) saveLocked(ledger pendingDispatchLedger) error {
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

func (e *Engine) ensurePendingDispatchStore() *pendingDispatchStore {
	if e.pendingDispatchStore != nil {
		return e.pendingDispatchStore
	}
	e.pendingDispatchStore = newPendingDispatchStore(e.dataDir)
	return e.pendingDispatchStore
}

// wireDispatchConfirmHandlers injects ConfirmDispatch into every configured
// Platform that can receive it directly (DispatchConfirmReceiver) — the
// same injection shape Start(handler MessageHandler) already uses
// (core/engine.go's p.Start(e.handleMessage)), so a confirm button press
// calls this Engine's method directly, never via message resynthesis.
func (e *Engine) wireDispatchConfirmHandlers() {
	for _, p := range e.platforms {
		if r, ok := p.(DispatchConfirmReceiver); ok {
			r.SetDispatchConfirmHandler(e.ConfirmDispatch)
		}
	}
}

func formatDispatchProposalCard(req dispatchRequest) string {
	return "📋 Dispatch Proposal\n\n" +
		"Letter: " + req.Letter + "\n" +
		"Thread: " + req.Thread + "\n" +
		"To: " + req.To + "\n" +
		"Path: " + req.Path + "\n\n" +
		"Awaiting confirmation."
}

// ConfirmDispatch is called directly by a Platform's callback-query handler
// when Boss presses the confirm button on a dispatch-proposal card — the
// only actuator for a proposal recorded by maybeHandleDispatchBlock. It is
// idempotent: a missing pending entry (already confirmed, or never
// existed) is reported back as ok=false rather than erroring or dispatching
// twice.
func (e *Engine) ConfirmDispatch(p Platform, letterID string) (receipt string, ok bool, err error) {
	store := e.ensurePendingDispatchStore()
	pending, found, err := store.takeByLetter(letterID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}

	receipt, dispatchErr := e.ControlPlaneDispatch(p, pending.SourceSessionKey, pending.Request)

	cardText := receipt
	if dispatchErr != nil {
		cardText = "⚠️ Dispatch failed: " + dispatchErr.Error()
	}
	if cm, ok := p.(ReceiptCardManager); ok && pending.Card.MessageID != 0 {
		if updateErr := cm.UpdateReceiptCard(context.Background(), pending.Card, cardText, nil); updateErr != nil {
			slog.Warn("dispatch confirm: failed to update card", "letter", letterID, "error", updateErr)
		}
	}
	return cardText, true, dispatchErr
}
