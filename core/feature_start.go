package core

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

const (
	featureChefSeat    = "chef-seat"
	featureImplSeat    = "dev-deepseek"
	featureCounselSeat = "counsel-seat"
	featureReviewSeat  = "reviewer-seat"
)

type featureStartOptions struct {
	Title  string
	Impl   bool
	Risk   bool
	Review bool
}

func parseFeatureStartArgs(args []string) (featureStartOptions, error) {
	var opts featureStartOptions
	titleParts := make([]string, 0, len(args))
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "":
			continue
		case "--impl":
			opts.Impl = true
		case "--risk":
			opts.Risk = true
		case "--review":
			opts.Review = true
		default:
			if strings.HasPrefix(arg, "--") {
				return opts, fmt.Errorf("unknown flag %s", arg)
			}
			titleParts = append(titleParts, arg)
		}
	}
	opts.Title = strings.TrimSpace(strings.Join(titleParts, " "))
	if opts.Title == "" {
		return opts, fmt.Errorf("feature title is required")
	}
	return opts, nil
}

func (e *Engine) cmdFeatureStart(p Platform, msg *Message, args []string) {
	opts, err := parseFeatureStartArgs(args)
	if err != nil {
		e.reply(p, msg.ReplyCtx, "Usage: `/feature-start <title> [--impl] [--risk] [--review]`")
		return
	}

	chef := e.featureChefEngine()
	if chef == nil {
		e.reply(p, msg.ReplyCtx, "❌ /feature-start requires a running chef-seat engine")
		return
	}
	chefPlatform := chef.platformByName(msg.Platform)
	if chefPlatform == nil {
		if chef == e {
			chefPlatform = p
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ chef-seat has no %s platform available", msg.Platform))
			return
		}
	}
	if strings.TrimSpace(chef.dataDir) == "" {
		e.reply(p, msg.ReplyCtx, "❌ /feature-start requires data_dir so it can write the local board")
		return
	}

	boardStore := NewFeatureBoardStore(chef.dataDir)
	repoWorktree := chef.commandWorkDir(chef.agent, msg)
	task, err := boardStore.Create(
		opts.Title,
		featureChefSeat,
		repoWorktree,
		"Chef scope feature and decide whether counsel/dev/reviewer seats are needed.",
	)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ feature board write failed: %v", err))
		return
	}

	refreshed := []string{}
	warnings := []string{}
	if _, err := chef.refreshFeatureSeatSession(msg, task); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Chef refresh failed: %v", err))
		return
	}
	refreshed = append(refreshed, featureChefSeat)

	for _, target := range opts.refreshTargets() {
		engine := chef.featureEngineByName(target)
		if engine == nil {
			warnings = append(warnings, fmt.Sprintf("%s not running", target))
			continue
		}
		if _, err := engine.refreshFeatureSeatSession(msg, task); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s refresh failed: %v", target, err))
			continue
		}
		refreshed = append(refreshed, target)
	}

	packet := chef.buildFeatureStartPacket(task, boardStore.Path(), opts, refreshed, warnings)
	if err := chef.injectFeatureStartPacket(chefPlatform, msg, packet); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Chef cold-start packet failed: %v", err))
		return
	}

	if opts.Risk {
		chef.launchFeatureCounselAudit(msg, task, boardStore.Path())
	}

	reply := fmt.Sprintf("✅ Feature started: %s\nTask: `%s`\nBoard: `%s`\nRefreshed: %s",
		task.Title, task.TaskID, boardStore.Path(), strings.Join(refreshed, ", "))
	if len(warnings) > 0 {
		reply += "\nWarnings: " + strings.Join(warnings, "; ")
	}
	if opts.Risk {
		reply += "\nRisk flag: counsel-seat audit requested through relay."
	}
	e.reply(p, msg.ReplyCtx, reply)
}

func (opts featureStartOptions) refreshTargets() []string {
	var targets []string
	if opts.Impl {
		targets = append(targets, featureImplSeat)
	}
	if opts.Risk {
		targets = append(targets, featureCounselSeat)
	}
	if opts.Review {
		targets = append(targets, featureReviewSeat)
	}
	return targets
}

func (e *Engine) featureChefEngine() *Engine {
	if e.name == featureChefSeat {
		return e
	}
	return e.featureEngineByName(featureChefSeat)
}

func (e *Engine) featureEngineByName(name string) *Engine {
	if e != nil && e.name == name {
		return e
	}
	if e == nil || e.relayManager == nil {
		return nil
	}
	return e.relayManager.Engine(name)
}

func (e *Engine) platformByName(name string) Platform {
	for _, p := range e.platforms {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

func (e *Engine) refreshFeatureSeatSession(msg *Message, task *FeatureTask) (*Session, error) {
	if e == nil {
		return nil, fmt.Errorf("engine is nil")
	}
	_, sessions := e.sessionContextForKey(msg.SessionKey)
	if sessions == nil {
		return nil, fmt.Errorf("session manager is nil")
	}
	interactiveKey := e.interactiveKeyForSessionKey(msg.SessionKey)
	e.cleanupInteractiveState(interactiveKey)

	old := sessions.GetOrCreateActive(msg.SessionKey)
	old.SetAgentSessionID("", "")
	old.ClearHistory()
	sessions.Save()

	name := fmt.Sprintf("feature-start %s %s", task.TaskID, task.Title)
	return sessions.NewSession(msg.SessionKey, name), nil
}

func (e *Engine) injectFeatureStartPacket(p Platform, msg *Message, packet string) error {
	agent, sessions, interactiveKey, workspaceDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		return err
	}
	session := sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		return fmt.Errorf("chef session is still processing")
	}
	packetMsg := *msg
	packetMsg.Content = packet
	packetMsg.Images = nil
	packetMsg.Files = nil
	go e.processInteractiveMessageWith(p, &packetMsg, session, agent, sessions, interactiveKey, workspaceDir, msg.SessionKey)
	return nil
}

func (e *Engine) buildFeatureStartPacket(task *FeatureTask, boardPath string, opts featureStartOptions, refreshed, warnings []string) string {
	nexusRoot := e.featureNexusRoot()
	wakePath := filepath.Join(nexusRoot, "WAKE.md")
	handoffPath := filepath.Join(nexusRoot, "HANDOFF.md")
	var b strings.Builder
	b.WriteString("[FEATURE-START]\n")
	b.WriteString("This is a clean cold-start packet created by cc-connect /feature-start.\n\n")
	b.WriteString("User request/title:\n")
	b.WriteString(task.Title)
	b.WriteString("\n\nBoard item:\n")
	b.WriteString(fmt.Sprintf("- task_id: %s\n", task.TaskID))
	b.WriteString(fmt.Sprintf("- title: %s\n", task.Title))
	b.WriteString(fmt.Sprintf("- owner: %s\n", task.Owner))
	b.WriteString(fmt.Sprintf("- status: %s\n", task.Status))
	b.WriteString(fmt.Sprintf("- repo/worktree: %s\n", task.RepoWorktree))
	b.WriteString(fmt.Sprintf("- blocker: %s\n", task.Blocker))
	b.WriteString(fmt.Sprintf("- last_heartbeat: %s\n", task.LastHeartbeat.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- evidence: %v\n", task.Evidence))
	b.WriteString(fmt.Sprintf("- handback_state: %s\n", task.HandbackState))
	b.WriteString(fmt.Sprintf("- next_action: %s\n", task.NextAction))
	b.WriteString(fmt.Sprintf("- board_path: %s\n\n", boardPath))
	b.WriteString("Context files:\n")
	b.WriteString(fmt.Sprintf("- WAKE: %s\n", wakePath))
	b.WriteString(fmt.Sprintf("- HANDOFF: %s\n\n", handoffPath))
	b.WriteString("Refresh flags:\n")
	b.WriteString(fmt.Sprintf("- --impl: %t\n", opts.Impl))
	b.WriteString(fmt.Sprintf("- --risk: %t\n", opts.Risk))
	b.WriteString(fmt.Sprintf("- --review: %t\n", opts.Review))
	b.WriteString(fmt.Sprintf("- refreshed seats: %s\n", strings.Join(refreshed, ", ")))
	if len(warnings) > 0 {
		b.WriteString(fmt.Sprintf("- warnings: %s\n", strings.Join(warnings, "; ")))
	}
	b.WriteString("\nInstructions:\n")
	b.WriteString("- Scope the feature before implementation.\n")
	b.WriteString("- Decide whether counsel-seat, dev-deepseek, reviewer-seat, or another seat is needed.\n")
	b.WriteString("- Mark pricing, API capability, product, architecture, security, and other uncertain assumptions as verified or speculative.\n")
	b.WriteString("- Speculative price/API capability claims require spike evidence or counsel/reviewer audit before implementation.\n")
	b.WriteString("- Do not say \"I'll monitor\" unless there is a real board item, watcher, heartbeat check, scheduled follow-up, or other durable follow-through mechanism.\n")
	if opts.Risk {
		b.WriteString("- Risk flag is set: wait for or request counsel-seat adversarial audit before pushing implementation forward.\n")
	}
	if opts.Impl {
		b.WriteString("- Impl flag is set: dev-deepseek has been refreshed and may be used when implementation starts.\n")
	}
	if opts.Review {
		b.WriteString("- Review flag is set: reviewer-seat has been refreshed and may be used when review starts.\n")
	}
	return b.String()
}

func (e *Engine) featureNexusRoot() string {
	dataDir := strings.TrimSpace(e.dataDir)
	if dataDir != "" {
		return filepath.Dir(dataDir)
	}
	configPath := strings.TrimSpace(e.configPath)
	if configPath != "" {
		return filepath.Dir(configPath)
	}
	return ""
}

func (e *Engine) launchFeatureCounselAudit(msg *Message, task *FeatureTask, boardPath string) {
	if e == nil || e.relayManager == nil {
		return
	}
	prompt := fmt.Sprintf(`[FEATURE-START COUNSEL AUDIT]
Task ID: %s
Title: %s
Board: %s

Chef requested adversarial audit because /feature-start used --risk.
Check pricing, API capability, product, architecture, security, and implementation-assumption risk.
Return concise findings with verified/speculative labels and any blocker that should stop implementation.`,
		task.TaskID, task.Title, boardPath)
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Minute)
		defer cancel()
		if _, err := e.relayManager.Send(ctx, RelayRequest{
			From:       featureChefSeat,
			To:         featureCounselSeat,
			SessionKey: msg.SessionKey,
			Message:    prompt,
		}); err != nil {
			slog.Warn("feature-start: counsel audit relay failed", "task_id", task.TaskID, "error", err)
		}
	}()
}
