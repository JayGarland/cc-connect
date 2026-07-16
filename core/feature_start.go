package core

import (
	"fmt"
	"log/slog"
	"strings"
)

const (
	featureChefSeat    = "chef-seat"
	featureImplSeat    = "dev-deepseek"
	featureCounselSeat = "counsel-seat"
)

// refreshFeatureSeatSessionForKey starts a fresh session for a seat picking up
// an active feature, clearing any prior agent session/history so the seat
// gets a clean context to receive the feature packet into.
func (e *Engine) refreshFeatureSeatSessionForKey(sessions *SessionManager, sessionKey, interactiveKey string, task *FeatureTask) (*Session, error) {
	if e == nil {
		return nil, fmt.Errorf("engine is nil")
	}
	if sessions == nil {
		return nil, fmt.Errorf("session manager is nil")
	}
	e.cleanupInteractiveState(interactiveKey)

	old := sessions.GetOrCreateActive(sessionKey)
	old.SetAgentSessionID("", "")
	old.ClearHistory()
	sessions.Save()

	name := fmt.Sprintf("feature-start %s %s", task.TaskID, task.Title)
	return sessions.NewSession(sessionKey, name), nil
}

func (e *Engine) applyLazyFeatureContextToMessage(msg *Message, sessions *SessionManager, interactiveKey string) (*Session, bool, error) {
	if e == nil || msg == nil || sessions == nil || strings.TrimSpace(e.dataDir) == "" {
		return nil, false, nil
	}
	store := NewFeatureBoardStore(e.dataDir)
	task, shouldRefresh, err := store.ActiveTaskForSeat(e.name)
	if err != nil || !shouldRefresh {
		return nil, shouldRefresh, err
	}
	session, err := e.refreshFeatureSeatSessionForKey(sessions, msg.SessionKey, interactiveKey, task)
	if err != nil {
		return nil, true, err
	}
	if err := store.MarkSeatRefreshed(task.TaskID, e.name, msg.SessionKey); err != nil {
		return nil, true, err
	}
	msg.Content = e.prependFeatureContext(task, store.Path(), msg.Content)
	slog.Info("feature-start: lazy refreshed seat", "project", e.name, "task_id", task.TaskID, "session", msg.SessionKey)
	return session, true, nil
}

func (e *Engine) applyLazyFeatureContextToRelayMessage(sessions *SessionManager, relaySessionKey, sourceSessionKey string, message *string) error {
	if e == nil || sessions == nil || message == nil || strings.TrimSpace(e.dataDir) == "" {
		return nil
	}
	store := NewFeatureBoardStore(e.dataDir)
	task, shouldRefresh, err := store.ActiveTaskForSeat(e.name)
	if err != nil || !shouldRefresh {
		return err
	}
	if _, err := e.refreshFeatureSeatSessionForKey(sessions, relaySessionKey, relaySessionKey, task); err != nil {
		return err
	}
	if err := store.MarkSeatRefreshed(task.TaskID, e.name, relaySessionKey); err != nil {
		return err
	}
	*message = e.prependFeatureContext(task, store.Path(), *message)
	slog.Info("feature-start: lazy refreshed relay seat",
		"project", e.name,
		"task_id", task.TaskID,
		"relay_session", relaySessionKey,
		"source_session", sourceSessionKey,
	)
	return nil
}

func (e *Engine) prependFeatureContext(task *FeatureTask, boardPath, content string) string {
	if task == nil {
		return content
	}
	var b strings.Builder
	b.WriteString("[FEATURE-CONTEXT]\n")
	b.WriteString("This seat has just been lazily refreshed for the active Nexus feature.\n")
	b.WriteString("Do not treat this context block alone as a task. Process the actual message after the separator.\n")
	b.WriteString(fmt.Sprintf("Task ID: %s\n", task.TaskID))
	b.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	b.WriteString(fmt.Sprintf("Board: %s\n", boardPath))
	b.WriteString(fmt.Sprintf("Seat: %s\n", e.name))
	if task.NextAction != "" {
		b.WriteString(fmt.Sprintf("Board next_action: %s\n", task.NextAction))
	}
	b.WriteString("[/FEATURE-CONTEXT]\n---\n")
	b.WriteString(content)
	return b.String()
}
