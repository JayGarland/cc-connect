package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ControlPlaneAuditEntry is one record of a decision the ControlPlane
// itself made — currently only "dispatch" (L-0662, the narrow Phase-2
// slice of the L-0658 RFC).
//
// This is an append-only historical log of an action the control plane
// took, not a mirror of state owned elsewhere. Contrast
// core/sessions_snapshot.go (Phase 1, L-0660), which deliberately reads
// SessionManager's live state on demand instead of maintaining a separate
// persisted copy, to avoid a second, driftable source of truth. A
// component logging its own decision does not have that problem: the log
// entry IS the primary record of that decision, not a copy of one owned
// by something else.
type ControlPlaneAuditEntry struct {
	Action           string    `json:"action"`
	Letter           string    `json:"letter,omitempty"`
	Thread           string    `json:"thread,omitempty"`
	To               string    `json:"to,omitempty"`
	SourceProject    string    `json:"source_project,omitempty"`
	SourceSessionKey string    `json:"source_session_key,omitempty"`
	Outcome          string    `json:"outcome"`
	At               time.Time `json:"at"`
}

type controlPlaneAuditLog struct {
	Entries []ControlPlaneAuditEntry `json:"entries"`
}

// controlPlaneAuditStore is the atomic-write/mutex-guarded JSON store for
// the control-plane audit log, mirroring dispatchStore's shape
// (core/dispatch.go:119-236).
type controlPlaneAuditStore struct {
	mu   sync.Mutex
	path string
}

func newControlPlaneAuditStore(dataDir string) *controlPlaneAuditStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &controlPlaneAuditStore{path: filepath.Join(dataDir, "control_plane_audit.json")}
}

func (s *controlPlaneAuditStore) append(entry ControlPlaneAuditEntry) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log, err := s.loadLocked()
	if err != nil {
		return err
	}
	log.Entries = append(log.Entries, entry)
	return s.saveLocked(log)
}

// list returns every audit entry recorded so far, oldest first.
func (s *controlPlaneAuditStore) list() ([]ControlPlaneAuditEntry, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	return log.Entries, nil
}

func (s *controlPlaneAuditStore) loadLocked() (controlPlaneAuditLog, error) {
	var log controlPlaneAuditLog
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return log, nil
		}
		return log, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return log, nil
	}
	if err := json.Unmarshal(data, &log); err != nil {
		return log, err
	}
	return log, nil
}

func (s *controlPlaneAuditStore) saveLocked(log controlPlaneAuditLog) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(s.path, data, 0o644)
}
