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

const (
	verificationExpectationInflight verificationExpectationState = "inflight"
	// verificationExpectationBlocked records a BLOCK finding relayed back to the
	// letter's author seat for correction (L-0762 item 3). It is keyed by the
	// same LetterID + Generation as the request leg: an author edit changes the
	// content digest, so the blocked entry becomes stale exactly as an inflight
	// entry does.
	verificationExpectationBlocked verificationExpectationState = "blocked"
)

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

// verificationVenueSessionKey resolves the session key a verification relay runs
// on. Verification traffic must never land on the outbox topic (L-0762 item 2),
// so instead of the outbox session key the relay uses the same per-letter topic
// machinery dispatch uses (virtualTopicSessionKey, L-0429/L-0674): the verifier
// seat's conversation is keyed by the letter's thread id, off the outbox topic.
// Falls back to the dispatch dashboard key when a per-letter topic cannot be
// derived (non-telegram dashboard).
func (e *Engine) verificationVenueSessionKey(letter string) (string, error) {
	dashboard := strings.TrimSpace(e.dispatchConfig.DashboardSessionKey)
	if dashboard == "" {
		dashboard = strings.TrimSpace(e.outboxConfig.SessionKey)
	}
	if dashboard == "" {
		return "", fmt.Errorf("verification venue: no dashboard session key configured")
	}
	if sessionKey, _, _, err := virtualTopicSessionKey(dashboard, letter); err == nil && strings.TrimSpace(sessionKey) != "" {
		return sessionKey, nil
	}
	return dashboard, nil
}

// deliverVerificationRequest is the shared delivery core behind both the manual
// button path (RequestVerification) and the record-less pending-QC auto-trigger
// (requestVerificationRecordless). It persists the durable LetterID+Generation
// expectation, relays the request to the verifier on the per-letter venue, and
// releases the expectation on delivery failure so the archive-text-derived
// awaiting state stays actionable. Idempotency is the expectation store's
// LetterID+Generation key: a repeated call for the same generation returns
// requested=false without a second relay.
func (e *Engine) deliverVerificationRequest(letterID, generation, verifier, queryPath string) (string, bool, error) {
	store := e.ensureVerificationExpectationStore()
	entry := VerificationExpectation{LetterID: letterID, Generation: generation, Verifier: verifier, QueryPath: queryPath, State: verificationExpectationInflight, RequestedAt: time.Now().UTC()}
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
	sourceSessionKey, err := e.verificationVenueSessionKey(letterID)
	if err != nil {
		return release(err)
	}
	verificationMessage := "[PRE-DISPATCH VERIFICATION]\nQuery: " + queryPath + "\n\nPerform the protocol pre-dispatch verification. Update the QUERY verification fields only when appropriate. Do not create a RESULT or dispatch the implementation task."
	// Verification is machine-to-machine (the verifier seat reads the file and
	// writes Verified:/校验 comments); the group echo would only repeat a
	// prompt Boss explicitly keeps out of view. Failures stay observable through
	// the persisted expectation, cc-connect logs, and the card's awaiting state.
	if _, err := e.relayManager.Send(context.Background(), RelayRequest{From: e.name, To: verifier, SessionKey: sourceSessionKey, Message: verificationMessage, Visibility: RelayVisibilityNone}); err != nil {
		return release(fmt.Errorf("deliver verification request: %w", err))
	}
	return "Verification requested for " + verifier + ".", true, nil
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
	receipt, ok, err := e.deliverVerificationRequest(letterID, record.Generation, record.Verify, record.QueryPath)
	if err != nil || !ok {
		return receipt, ok, err
	}
	e.outboxMu.Lock()
	if current, exists := e.outboxRecords[letterID]; exists && current.Generation == record.Generation {
		current.Verification = verificationInflight
		e.outboxRecords[letterID] = current
		e.persistOutboxLocked()
	}
	e.outboxMu.Unlock()
	return receipt, true, nil
}

// requestVerificationRecordless is the pending-QC auto-trigger entry (先审后存):
// a letter written but not yet registered has no outbox record, so this builds
// the durable expectation straight from the scan result {LetterID, Generation,
// Verifier, QueryPath} and relays. Idempotency and generation invalidation are
// identical to the button path — the store keys on LetterID+Generation, so any
// edit to the file (a BLOCK comment, an author correction) changes the content
// digest and makes the old expectation automatically stale.
func (e *Engine) requestVerificationRecordless(letterID, generation, verifier, queryPath string) (string, bool, error) {
	return e.deliverVerificationRequest(letterID, generation, verifier, queryPath)
}

// relayVerificationBlock relays a verifier's BLOCK finding back to the letter's
// author seat for correction (L-0762 item 3). The verifier stays append-only and
// never edits the body; the author owns every correction. The relay is keyed by
// the same LetterID+Generation so a given file generation is relayed at most
// once; when the author edits the file the content digest changes and the next
// pending-QC scan re-requests the verifier under the new generation.
func (e *Engine) relayVerificationBlock(letterID, generation, queryPath, author, verifier, finding string) (string, bool, error) {
	store := e.ensureVerificationExpectationStore()
	entry := VerificationExpectation{LetterID: letterID, Generation: generation, Verifier: verifier, QueryPath: queryPath, State: verificationExpectationBlocked, RequestedAt: time.Now().UTC()}
	requested, err := store.request(entry)
	if err != nil || !requested {
		return "", requested, err
	}
	release := func(deliveryErr error) (string, bool, error) {
		if releaseErr := store.release(letterID, generation); releaseErr != nil {
			return "", false, fmt.Errorf("%v; release verification expectation: %w", deliveryErr, releaseErr)
		}
		return "", false, deliveryErr
	}
	if e.relayManager == nil {
		return release(fmt.Errorf("verification relay manager is not configured"))
	}
	sourceSessionKey, err := e.verificationVenueSessionKey(letterID)
	if err != nil {
		return release(err)
	}
	blockMessage := "[PRE-DISPATCH VERIFICATION BLOCK]\nQuery: " + queryPath + "\nVerifier: " + verifier + "\n\n" + finding + "\n\nCorrect the QUERY per the BLOCK finding (factual and design alike — the correction belongs to the author seat). Append a Correction comment for the record, then the verification request re-submits automatically."
	if _, err := e.relayManager.Send(context.Background(), RelayRequest{From: e.name, To: author, SessionKey: sourceSessionKey, Message: blockMessage, Visibility: RelayVisibilityNone}); err != nil {
		return release(fmt.Errorf("deliver verification block: %w", err))
	}
	return "Verification BLOCK relayed to " + author + ".", true, nil
}
