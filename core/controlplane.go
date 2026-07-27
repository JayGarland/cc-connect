package core

import (
	"log/slog"
	"time"
)

// ControlPlaneDispatch is the sole entry point for triggering a [DISPATCH]
// action (L-0658 RFC Phase 2, L-0662's narrow first slice). Prior to this
// change, maybeHandleDispatchBlock called executeDispatch directly — the
// text-block parser was itself the decision-maker. Now it calls this
// method instead, so parsing a [DISPATCH] block only produces a request the
// control plane is asked to act on, and every dispatch decision is recorded
// regardless of which caller triggered it.
//
// This method's own behavior is byte-for-byte executeDispatch's existing
// logic — it is a wrapper, not a rewrite. Session-lifecycle actions
// (/new, /switch, idle-triggered reset) are deliberately NOT wrapped here;
// they remain direct engine.go call sites pending a follow-up "Phase 2b"
// letter, since they sit on the per-message hot path and carry materially
// higher blast-radius risk than the [DISPATCH] path (only reached when a
// [DISPATCH] block is parsed, not on every message).
func (e *Engine) ControlPlaneDispatch(p Platform, sourceSessionKey string, req dispatchRequest) (string, error) {
	receipt, err := e.executeDispatch(p, sourceSessionKey, req)
	e.recordControlPlaneDispatch(req, sourceSessionKey, err)
	return receipt, err
}

func (e *Engine) ensureControlPlaneAudit() *controlPlaneAuditStore {
	if e.controlPlaneAudit != nil {
		return e.controlPlaneAudit
	}
	e.controlPlaneAudit = newControlPlaneAuditStore(e.dataDir)
	return e.controlPlaneAudit
}

func (e *Engine) recordControlPlaneDispatch(req dispatchRequest, sourceSessionKey string, dispatchErr error) {
	store := e.ensureControlPlaneAudit()
	if store == nil {
		return
	}
	outcome := "ok"
	if dispatchErr != nil {
		outcome = "error: " + dispatchErr.Error()
	}
	entry := ControlPlaneAuditEntry{
		Action:           "dispatch",
		Letter:           req.Letter,
		Thread:           req.Thread,
		To:               req.To,
		SourceProject:    e.name,
		SourceSessionKey: sourceSessionKey,
		Outcome:          outcome,
		At:               time.Now(),
	}
	if err := store.append(entry); err != nil {
		slog.Warn("control plane audit: failed to record dispatch",
			"letter", req.Letter, "to", req.To, "error", err)
	}
}
