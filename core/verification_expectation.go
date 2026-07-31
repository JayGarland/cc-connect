package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type verificationExpectationState string

const verificationExpectationInflight verificationExpectationState = "inflight"

type VerificationExpectation struct {
	LetterID    string                       `json:"letter_id"`
	Generation  string                       `json:"generation"`
	Verifier    string                       `json:"verifier"`
	QueryPath   string                       `json:"query_path"`
	State       verificationExpectationState `json:"state"`
	RequestedAt time.Time                    `json:"requested_at"`
}

type verificationExpectationLedger struct {
	Entries []VerificationExpectation `json:"entries"`
}

type verificationExpectationStore struct {
	mu   sync.Mutex
	path string
}

func newVerificationExpectationStore(dataDir string) *verificationExpectationStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &verificationExpectationStore{path: filepath.Join(dataDir, "verification_expectations.json")}
}

func (s *verificationExpectationStore) loadLocked() (verificationExpectationLedger, error) {
	var ledger verificationExpectationLedger
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if strings.TrimSpace(string(data)) != "" {
		err = json.Unmarshal(data, &ledger)
	}
	return ledger, err
}

func (s *verificationExpectationStore) saveLocked(ledger verificationExpectationLedger) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.path, append(data, '\n'), 0o644)
}

func (s *verificationExpectationStore) get(letter, generation string) (VerificationExpectation, bool, error) {
	if s == nil {
		return VerificationExpectation{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return VerificationExpectation{}, false, err
	}
	for _, entry := range ledger.Entries {
		if entry.LetterID == letter && entry.Generation == generation {
			return entry, true, nil
		}
	}
	return VerificationExpectation{}, false, nil
}

func (s *verificationExpectationStore) request(entry VerificationExpectation) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	for _, existing := range ledger.Entries {
		if existing.LetterID == entry.LetterID && existing.Generation == entry.Generation {
			return false, nil
		}
	}
	ledger.Entries = append(ledger.Entries, entry)
	return true, s.saveLocked(ledger)
}

func (s *verificationExpectationStore) release(letter, generation string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range ledger.Entries {
		if ledger.Entries[i].LetterID == letter && ledger.Entries[i].Generation == generation {
			ledger.Entries = append(ledger.Entries[:i], ledger.Entries[i+1:]...)
			return s.saveLocked(ledger)
		}
	}
	return nil
}

func (e *Engine) ensureVerificationExpectationStore() *verificationExpectationStore {
	if e.verificationExpectationStore == nil {
		e.verificationExpectationStore = newVerificationExpectationStore(e.dataDir)
	}
	return e.verificationExpectationStore
}

func (e *Engine) outboxRecordForVerificationCallbackLocked(callbackToken string) (string, outboxRecord, bool) {
	for letterID, record := range e.outboxRecords {
		if verificationCallbackToken(letterID, record.Generation) == callbackToken {
			return letterID, record, true
		}
	}
	return "", outboxRecord{}, false
}

// RequestVerification binds the durable request to archive letter ID plus its
// observed generation. It never validates or rewrites Verified:; completion is
// exclusively the archive-text classifier on a later Outbox refresh.
func (e *Engine) RequestVerification(_ Platform, callbackToken string) (string, bool, error) {
	e.outboxMu.RLock()
	letterID, record, ok := e.outboxRecordForVerificationCallbackLocked(callbackToken)
	e.outboxMu.RUnlock()
	if !ok || (record.Verification != verificationAwaiting && record.Verification != verificationInflight) {
		return "", false, nil
	}
	generation := record.Generation
	entry := VerificationExpectation{LetterID: letterID, Generation: generation, Verifier: record.Verify, QueryPath: record.QueryPath, State: verificationExpectationInflight, RequestedAt: time.Now().UTC()}
	store := e.ensureVerificationExpectationStore()
	requested, err := store.request(entry)
	if err != nil || !requested {
		return "", requested, err
	}
	// The expectation is persisted before delivery. Any delivery failure releases
	// it again, leaving the archive-text-derived awaiting state actionable.
	release := func(deliveryErr error) (string, bool, error) {
		if releaseErr := store.release(letterID, generation); releaseErr != nil {
			return "", false, fmt.Errorf("%v; release verification expectation: %w", deliveryErr, releaseErr)
		}
		return "", false, deliveryErr
	}
	if e.relayManager == nil {
		return release(fmt.Errorf("verification relay manager is not configured"))
	}
	sourceSessionKey := strings.TrimSpace(e.outboxConfig.SessionKey)
	if sourceSessionKey == "" {
		sourceSessionKey = strings.TrimSpace(e.dispatchConfig.DashboardSessionKey)
	}
	if sourceSessionKey == "" {
		return release(fmt.Errorf("verification dashboard session key is not configured"))
	}
	verificationMessage := "[PRE-DISPATCH VERIFICATION]\nQuery: " + record.QueryPath + "\n\nPerform the protocol pre-dispatch verification. Update the QUERY verification fields only when appropriate. Do not create a RESULT or dispatch the implementation task."
	if _, err := e.relayManager.Send(context.Background(), RelayRequest{From: e.name, To: record.Verify, SessionKey: sourceSessionKey, Message: verificationMessage}); err != nil {
		return release(fmt.Errorf("deliver verification request: %w", err))
	}
	e.outboxMu.Lock()
	if current, exists := e.outboxRecords[letterID]; exists && current.Generation == generation {
		current.Verification = verificationInflight
		e.outboxRecords[letterID] = current
		e.persistOutboxLocked()
	}
	e.outboxMu.Unlock()
	return "Verification requested for " + record.Verify + ".", true, nil
}
