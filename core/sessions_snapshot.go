package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SessionSnapshotEntry is one logical session cc-connect currently holds in
// memory for this engine.
//
// This is deliberately computed on demand from SessionManager's existing
// read-only accessors (AllSessions/SessionKeyMap) and the workspace pool,
// never written to a separate persisted ledger. A stored mirror of state
// that is already directly observable would itself become a second,
// driftable source of truth for session identity — the exact defect class
// this fleet already paid for twice (PR #53/#54,
// docs/retros/2026-07-23-session-drop-two-causes.md: reuse gated on a
// forking id instead of the stable Session.ID; workspace shard picked from
// volatile message content instead of a stable key). Phase 1 of the L-0658
// control-plane RFC (this file, L-0660) answers "what sessions exist right
// now" by reading the live state directly, so the answer can never be
// stale or disagree with reality. Nothing in this file writes state or
// changes any existing session transition's behavior.
type SessionSnapshotEntry struct {
	Workspace        string
	ShardFile        string
	UserKey          string
	SessionID        string
	AgentSessionID   string
	Active           bool
	UpdatedAt        time.Time
	LastUserActivity time.Time
}

// defaultSessionSnapshotWorkspace labels entries from the engine's single
// default SessionManager (e.sessions) — the one used when multi-workspace
// routing is off, or as the fallback shard.
const defaultSessionSnapshotWorkspace = "(default)"

// sessionSnapshot enumerates every logical session currently held in memory
// for this engine: the default SessionManager plus every per-workspace
// SessionManager the workspace pool has created so far (getOrCreateWorkspaceAgent,
// engine.go). Read-only — does not create, touch, or persist anything.
func (e *Engine) sessionSnapshot() []SessionSnapshotEntry {
	var out []SessionSnapshotEntry

	collect := func(workspace string, sm *SessionManager) {
		if sm == nil {
			return
		}
		idToKey, activeIDs := sm.SessionKeyMap()
		for _, s := range sm.AllSessions() {
			out = append(out, SessionSnapshotEntry{
				Workspace:        workspace,
				ShardFile:        sm.StorePath(),
				UserKey:          idToKey[s.ID],
				SessionID:        s.ID,
				AgentSessionID:   s.GetAgentSessionID(),
				Active:           activeIDs[s.ID],
				UpdatedAt:        s.GetUpdatedAt(),
				LastUserActivity: s.GetLastUserActivity(),
			})
		}
	}

	collect(defaultSessionSnapshotWorkspace, e.sessions)

	// Mirrors the locking pattern getOrCreateWorkspaceAgent uses around
	// e.workspacePool (engine.go): the pool is lazily created under
	// e.interactiveMu, so reading the field must go through the same lock.
	// The pool's own internal mutex (workspace_state.go) protects the map
	// returned by All().
	e.interactiveMu.Lock()
	pool := e.workspacePool
	e.interactiveMu.Unlock()
	if pool != nil {
		for workspace, ws := range pool.All() {
			ws.mu.Lock()
			sm := ws.sessions
			ws.mu.Unlock()
			collect(workspace, sm)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Workspace != out[j].Workspace {
			return out[i].Workspace < out[j].Workspace
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// renderSessionBoardText renders the live session snapshot as plain text
// for the /sessionboard command (L-0660, Phase 1 of the L-0658 RFC).
func (e *Engine) renderSessionBoardText() string {
	entries := e.sessionSnapshot()
	if len(entries) == 0 {
		return fmt.Sprintf("%s: no live sessions.", e.name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d live session(s)\n", e.name, len(entries))
	for _, entry := range entries {
		marker := " "
		if entry.Active {
			marker = "*"
		}
		agentID := entry.AgentSessionID
		if agentID == "" {
			agentID = "-"
		}
		userKey := entry.UserKey
		if userKey == "" {
			userKey = "-"
		}
		idle := "-"
		if !entry.LastUserActivity.IsZero() {
			idle = formatDurationI18n(time.Since(entry.LastUserActivity), e.i18n.CurrentLang()) + " ago"
		}
		fmt.Fprintf(&b, "%s %s | session=%s agent=%s | user=%s | idle=%s\n",
			marker, entry.Workspace, entry.SessionID, agentID, userKey, idle)
	}
	return b.String()
}

func (e *Engine) cmdSessionBoard(p Platform, msg *Message) {
	e.reply(p, msg.ReplyCtx, e.renderSessionBoardText())
}
