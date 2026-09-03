// Agent session lifecycle: building params, delegating to *agent.Session,
// and wrapping channels in tea.Cmds for the TUI.
package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/mcp"
	"github.com/genai-io/san/internal/persona"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/reviewer"
	"github.com/genai-io/san/internal/session/transcript"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
)

// ============================================================
// Build params from model state
// ============================================================

// activePersona returns the currently-selected persona, or nil for the
// built-in default (no settings.persona, or it doesn't resolve). A configured
// name that doesn't resolve logs a warning and falls back to the default.
func (m *model) activePersona() *persona.Persona {
	snap := m.services.Setting.Snapshot()
	if snap == nil || snap.Persona == "" || snap.Persona == persona.DefaultName {
		return nil
	}
	p, ok := m.services.Persona.Get(snap.Persona)
	if !ok || p.IsBuiltin() {
		log.Logger().Warn("configured persona not found; using built-in default",
			zap.String("persona", snap.Persona))
		return nil
	}
	return p
}

// personaPrompt resolves the system.Persona (prompt-part overrides) to build
// with — the active persona's three parts, or empty (the built-in default)
// when no persona is selected.
func (m *model) personaPrompt() system.Persona {
	if p := m.activePersona(); p != nil {
		return system.Persona{Identity: p.Identity, Behavior: p.Behavior, Rules: p.Rules}
	}
	return system.Persona{}
}

// applyPersonaSkills loads the active persona's bundled skills into the skill
// registry (in-memory, active by default), or clears them when no persona is
// selected. The skills-directory reminder picks the set up on its next emit.
func (m *model) applyPersonaSkills() {
	if p := m.activePersona(); p != nil {
		var states map[string]string
		if p.Settings != nil {
			states = p.Settings.Skills
		}
		m.services.Skill.LoadPersona(p.SkillDirs, states)
		return
	}
	m.services.Skill.ClearPersona()
}

// applyPersonaAgents restricts the visible subagents to the active persona's
// `agents` allow-list (empty/none = all visible), or clears the restriction
// when no persona is selected. In-memory, mirroring applyPersonaSkills.
func (m *model) applyPersonaAgents() {
	if p := m.activePersona(); p != nil && p.Settings != nil && len(p.Settings.Agents) > 0 {
		m.services.Subagent.LoadPersona(p.Settings.Agents)
		return
	}
	m.services.Subagent.ClearPersona()
}

// promptParams fills in the fields that shape the system prompt and the
// toolset, and only those. It is free of side effects, unlike buildAgentParams
// below, which layers the recorder, hook wiring, and permission closures on
// top — so anything that only wants to know what the agent would send (see
// contextUsage) can call this without starting anything.
//
// Both callers going through one constructor is what keeps them in step: a new
// prompt-shaping input added here reaches the inspector for free, where a
// second hand-written literal would silently under-report.
func (m *model) promptParams() agent.BuildParams {
	return agent.BuildParams{
		CWD:            m.env.CWD,
		Persona:        m.personaPrompt(),
		AgentDirectory: func() string { return m.services.Subagent.PromptSection() },
		DisabledTools:  m.services.Setting.DisabledTools(),
		MCPTools:       mcp.AsCoreTools(m.services.MCP.GetToolSchemas(), mcp.NewCaller(m.services.MCP)),

		// Inject the Evolve trigger tool (tailored to the enabled capabilities)
		// so the model can queue its own self-learning reviews.
		ExtraTools: m.selfLearnExtraTools(),
	}
}

func (m *model) buildAgentParams() agent.BuildParams {
	params := m.promptParams()

	maxTokens := kit.GetMaxTokens(m.services.LLM.Store(), m.env.CurrentModel, setting.DefaultMaxTokens)
	var onEvent func(core.Event)
	rec := m.services.Session.NewRecorder("main", m.env.LLMProvider.Name(), m.env.GetModelID(), maxTokens)
	if rec != nil {
		onEvent = rec.OnAgentEvent
		m.services.Hook.SetAuditCallback(func(a hook.HookFiredAudit) {
			rec.RecordHook(transcript.HookRecord{
				Event:     a.Event,
				Source:    a.Source,
				Matcher:   a.Matcher,
				Outcome:   a.Outcome,
				Reason:    a.Reason,
				LatencyMs: a.Duration.Milliseconds(),
			})
		})
		m.services.Skill.SetStateChangeObserver(func(name, previous, current, caller string) {
			rec.RecordSkillState(transcript.SkillRecord{
				Name:     name,
				Previous: previous,
				Current:  current,
				Caller:   caller,
			})
		})
	}

	// Build the autopilot judge into the atomic slot so /autopilot Save can
	// hot-swap it mid-session; the closures below Load it per call.
	m.rebuildAutopilotReviewer()

	// Stream timeout overrides. Priority: SAN_* env var > settings.json > core
	// default (zero value => core picks FirstChunkTimeout=5m, IdleTimeout=60s).
	streamFirstChunk := firstValidDuration(
		setting.Getenv("STREAM_FIRST_CHUNK_TIMEOUT"),
		m.services.Setting.StreamFirstChunkTimeout(),
	)
	streamIdle := firstValidDuration(
		setting.Getenv("STREAM_IDLE_TIMEOUT"),
		m.services.Setting.StreamIdleTimeout(),
	)

	params.Provider = m.env.LLMProvider
	params.ModelID = m.env.GetModelID()
	params.MaxTokens = maxTokens
	params.ThinkingEffort = m.env.EffectiveThinkingEffort()
	params.OnEvent = onEvent
	params.CWDFunc = func() string { return m.env.CWD }
	params.HookEngine = m.services.Hook

	params.AskUser = func(ctx context.Context, req *tool.QuestionRequest) (*tool.QuestionResponse, error) {
		return m.conv.AgentToUI.Ask(ctx, 0, req)
	}
	params.ToolActivity = func(toolCallID string, msg string) {
		m.conv.AgentToUI.SendForToolCall(toolCallID, msg)
	}
	params.BashPromptResponder = func(ctx context.Context) tool.BashPromptResponder {
		// Interactive answering is opt-in via the Bash steer, read live (via
		// the synchronized snapshot — this runs on the agent goroutine) so a
		// mid-session toggle takes effect: without it, bash stays on the
		// normal non-tty path even in auto-review.
		if !m.autopilotEngaged() || !m.liveAutopilotConfig().Steers.BashPrompt {
			return nil
		}
		return bashPromptResponder{model: m}
	}

	params.PermissionRules = func(name string, args map[string]any) agent.PermDecisionResult {
		decision := m.services.Setting.HasPermissionToUseTool(name, args, m.env.SessionPermissions)
		mode := m.env.SessionMode()
		input := marshalPermInput(args)
		switch decision.Behavior {
		case perm.Permit:
			rec.RecordPermissionDecided(transcript.PermissionRecord{
				Tool: name, Input: input, Decision: permDecisionFor(true), Source: transcript.PermissionSourceConfig,
				Reason: decision.Reason, Mode: mode,
			})
			return agent.PermDecisionResult{Decision: decision.Behavior, Reason: decision.Reason}
		case perm.Reject:
			rec.RecordPermissionDecided(transcript.PermissionRecord{
				Tool: name, Input: input, Decision: permDecisionFor(false), Source: transcript.PermissionSourceConfig,
				Reason: decision.Reason, Mode: mode,
			})
			return agent.PermDecisionResult{Decision: decision.Behavior, Reason: decision.Reason}
		default:
			return agent.PermDecisionResult{
				Decision:    perm.Prompt,
				Reason:      decision.Reason,
				ToolName:    name,
				Description: decision.Reason,
				RequestID:   core.NewMessageID(),
				Reviewable:  decision.Reviewable,
			}
		}
	}

	params.PermissionReview = func(ctx context.Context, name string, args map[string]any, reason string) agent.PermReviewResult {
		// Steers are read live via the synchronized snapshot (agent goroutine)
		// so a mid-session toggle takes effect.
		if !m.autopilotEngaged() {
			return agent.PermReviewResult{}
		}
		cfg := m.liveAutopilotConfig()
		// Defense in depth: no steer may approve an unrecoverable action —
		// circuit-breaker tier included — even if one somehow reaches here.
		// The recoverable git tier is deliberately not checked — weighing it
		// is exactly what the judge is for.
		if r := setting.CircuitBreakerReason(name, args); r != "" {
			m.recordDecision(ctx, false, r)
			return agent.PermReviewResult{}
		}
		if r := setting.UnrecoverableReason(name, args); r != "" {
			m.recordDecision(ctx, false, r)
			return agent.PermReviewResult{}
		}
		// Skill steer: trust skill loads — approve them outright, no judge, so
		// the copilot can pull in skills without a prompt. Deliberately separate
		// from the Permission steer, since the judge tends to escalate a skill
		// load (it can run scripts) and you may want skills without opening the
		// whole gray zone.
		if name == tool.ToolSkill && cfg.Steers.Skill {
			m.env.SessionPermissions.AllowPattern(setting.BuildRule(name, args))
			m.recordDecision(ctx, true, "skill steer: trusted skill load")
			return agent.PermReviewResult{Allow: true, Reason: "autopilot: skill steer"}
		}
		// Permission steer: delegate gray-zone prompts to the judge. Off ⇒ every
		// gray-zone call escalates to the human.
		if !cfg.Steers.PermissionOn() {
			return agent.PermReviewResult{}
		}
		rev := m.autopilot.Load().judge
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		verdict, err := rev.Permission(ctx, reviewer.Request{
			ToolName: name, Args: args, Reason: reason, CWD: m.env.CWD, Mission: cfg.Mission,
			UnderGit: m.env.IsGit,
		})
		log.Logger().Debug("auto-review verdict",
			zap.String("tool", name),
			zap.Bool("allow", err == nil && verdict.Allow),
			zap.String("reason", verdict.Reason),
			zap.Error(err))
		if err != nil || !verdict.Allow {
			m.recordDecision(ctx, false, escalationReason(verdict.Reason, err))
			return agent.PermReviewResult{} // fail closed → escalate to human
		}
		if rec != nil {
			rec.RecordPermissionDecided(transcript.PermissionRecord{
				Tool: name, Input: marshalPermInput(args), Decision: permDecisionFor(true),
				Source: transcript.PermissionSourceReviewer, Reason: verdict.Reason, Mode: m.env.SessionMode(),
			})
		}
		// Cache the approval as a session grant so an identical repeat hits the
		// static fast path instead of the judge, shrinking the gray zone over
		// the session. Safe to write here: the agent goroutine is the only
		// mutator (the human-approval writer runs only while it is parked).
		m.env.SessionPermissions.AllowPattern(setting.BuildRule(name, args))
		// Tally it and stash the decision so the renderer can draw it inline
		// under the tool call the judge just let through (the status-bar count
		// alone doesn't say what was approved or why).
		m.recordDecision(ctx, true, verdict.Reason)
		return agent.PermReviewResult{Allow: true, Reason: "autopilot: " + verdict.Reason}
	}

	params.HookAllowResolver = func(name string, args map[string]any) bool {
		return m.services.Setting.ResolveHookAllow(name, args, m.env.SessionPermissions)
	}

	params.StreamFirstChunkTimeout = streamFirstChunk
	params.StreamIdleTimeout = streamIdle

	return params
}

// resolveReviewerModel picks the provider and model for the auto-review judge.
// A "vendor/model" ref (e.g. "anthropic/claude-haiku-4-5") routes to that
// connected provider; a bare id stays on the session provider; empty uses the
// session model. An unresolvable vendor falls back to the session model.
func (m *model) resolveReviewerModel(ref string) (llm.Provider, string) {
	if ref == "" {
		return m.env.LLMProvider, m.env.GetModelID()
	}
	if vendor, id, ok := llm.ParseVendorModel(ref); ok {
		if p, err := llm.NewProviderPool(m.services.LLM.Store()).Resolve(context.Background(), vendor); err == nil {
			return p, id
		}
		log.Logger().Warn("auto-review model vendor unavailable; using session model", zap.String("model", ref))
		return m.env.LLMProvider, m.env.GetModelID()
	}
	return m.env.LLMProvider, ref
}

// recordDecision tallies one auto-review decision for the status-bar count and
// stashes it for the renderer, keyed by the tool call ID carried in ctx and
// consumed (load + delete) in TakeReviewDecision — so the handoff map only ever
// holds calls whose result has not arrived yet. The approved flag
// picks the counter, so the count and the inline annotations can never drift.
// The count is bumped even when ctx carries no tool call ID; only the stash
// needs one.
func (m *model) recordDecision(ctx context.Context, approved bool, reason string) {
	if approved {
		m.reviewerApprovals.Add(1)
	} else {
		m.reviewerEscalations.Add(1)
	}
	if id := core.ToolCallIDFromContext(ctx); id != "" {
		m.pendingDecisions.Store(id, core.ReviewDecision{Approved: approved, Reason: reason})
	}
}

// TakeReviewDecision consumes the decision stashed for a tool call (load + delete),
// so it stamps exactly one rendered result and the handoff map holds only
// in-flight calls. Returns nil when the call was not auto-reviewed. Mirrors the
// tool side-effect handoff (Tool.PopSideEffect), consumed at the same point in
// OnToolResult.
func (m *model) TakeReviewDecision(callID string) *core.ReviewDecision {
	// No empty-callID guard: recordDecision never stores under an empty key, so
	// LoadAndDelete("") already misses and returns nil.
	v, ok := m.pendingDecisions.LoadAndDelete(callID)
	if !ok {
		return nil
	}
	decision := v.(core.ReviewDecision)
	return &decision
}

// escalationReason is the one-line explanation shown when the judge sends a
// gray-zone call back to the user: the judge's own reason, or a generic note
// when it could not reach a decision (error / timeout / unparseable reply).
func escalationReason(judgeReason string, err error) string {
	if err != nil || judgeReason == "" {
		return "autopilot unavailable — asking you"
	}
	return judgeReason
}

// marshalPermInput serializes the tool args for a permission audit record.
// Errors are logged but not propagated — audit must never block the decider.
func marshalPermInput(args map[string]any) json.RawMessage {
	if len(args) == 0 {
		return nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		log.Logger().Warn("perm input marshal failed", zap.Error(err))
		return nil
	}
	return data
}

// firstValidDuration returns the first value that parses as a time.Duration
// (e.g. "30s", "3m"), in priority order. Empty/invalid values are skipped;
// zero means "no override" and lets the core default apply.
func firstValidDuration(vals ...string) time.Duration {
	for _, v := range vals {
		if v == "" {
			continue
		}
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 0
}

// ============================================================
// Agent lifecycle (delegates to services.Agent)
// ============================================================

// disabledToolsSignature is a stable fingerprint of the effective disabled-tool
// set. ensureAgentSession compares it against the live agent's build-time value
// so a /tool enable/disable (which the built-in and MCP tool filters both read)
// rebuilds the toolset on the next turn.
func (m *model) disabledToolsSignature() string {
	disabled := m.services.Setting.DisabledTools()
	names := make([]string, 0, len(disabled))
	for name, off := range disabled {
		if off {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return strings.Join(names, "\x00")
}

// ensureAgentSession lazily starts the agent goroutine, preloading the
// existing conversation. If pendingSend is non-empty and matches the
// trailing user message in m.conv, it's dropped from the preload — the
// caller is about to re-deliver it via sendToAgent and we'd otherwise see
// the input twice. Pass "" when the caller hasn't yet appended the message.
func (m *model) ensureAgentSession(pendingSend string) (tea.Cmd, error) {
	if m.services.Agent.Active() {
		// The toolset is fixed at agent-build time. If the enabled Evolve
		// capabilities or the disabled-tool set drifted since the build (a
		// /evolve save, a /tool toggle, an external settings edit), stop the
		// stale session and fall through to rebuild with the current toolset —
		// the one choke point every turn passes through, so all drift sources
		// are covered uniformly.
		if m.selfLearnCapabilities() == m.agentEvolveCaps &&
			m.disabledToolsSignature() == m.agentDisabledToolsSignature {
			return nil, nil
		}
		m.StopAgentSession()
	}

	// Rebuild the subagent executor alongside the main agent so it inherits the
	// same effective disabled-tool snapshot after settings/persona drift.
	m.ReconfigureAgentTool()
	params := m.buildAgentParams()
	coreMessages := m.seedAgentMessages(pendingSend)

	if err := m.services.Agent.Start(params, coreMessages); err != nil {
		return nil, err
	}
	// Keep the snapshot across a failed Start so the user can retry without
	// losing history; a successful replacement owns its own copy now.
	m.agentRestartMessages = nil

	// Record what the toolset was built with, for the drift check above.
	m.agentEvolveCaps = m.selfLearnCapabilities()
	m.agentDisabledToolsSignature = m.disabledToolsSignature()

	// Wire L1 self-learning *after* Agent.Start so the ReviewFunc can capture
	// the live Agent + System for its fork. Builds nothing if both arms are
	// off (§3.1 zero-overhead guarantee).
	m.wireSelfLearn(params)

	cmds := []tea.Cmd{
		conv.DrainAgentOutbox(m.services.Agent.Outbox()),
		conv.PollPermGate(m.services.Agent.PermissionGate()),
	}
	if m.conv.AgentToUI != nil {
		cmds = append(cmds, m.conv.AgentToUI.Check())
	}
	return tea.Batch(cmds...), nil
}

// seedAgentMessages returns the authoritative chain for a newly-built main
// agent. A deliberate stop snapshots the live core.Agent before discarding it;
// that snapshot wins over the UI rendering model, whose rows may have been
// committed, cleared, or assigned different IDs. Cold starts and restored
// sessions have no snapshot and therefore seed from the UI conversation.
func (m *model) seedAgentMessages(pendingSend string) []core.Message {
	coreMessages := m.agentRestartMessages
	if len(coreMessages) == 0 {
		coreMessages = m.conv.ConvertToProvider()
	}
	if pendingSend != "" && len(coreMessages) > 0 {
		last := coreMessages[len(coreMessages)-1]
		if last.Role == core.RoleUser && last.Content == pendingSend {
			coreMessages = coreMessages[:len(coreMessages)-1]
		}
	}
	return m.dropImagesTextOnlyModelRejects(coreMessages)
}

// dropImagesTextOnlyModelRejects removes image attachments from a seeded chain
// the active model can't accept, keeping the surrounding text. The adaptTurnForProvider
// call in the submit path handles new turns, but history predating the current model
// still holds them — switch a conversation from a vision-capable model to a
// text-only one and every later turn replays those images, which the provider
// rejects outright. Every rebuild passes through here, so the switch itself
// (which stops the session) is enough to clear them.
func (m *model) dropImagesTextOnlyModelRejects(msgs []core.Message) []core.Message {
	if llm.SupportsImages(m.env.LLMProvider, m.env.GetModelID()) {
		return msgs
	}
	stripped := make([]core.Message, len(msgs))
	for i, msg := range msgs {
		msg.Images = nil
		stripped[i] = msg
	}
	return stripped
}

func (m *model) sendToAgent(content string, images []core.Attachment) tea.Cmd {
	if !m.services.Agent.Active() {
		return nil
	}
	svc := m.services.Agent
	content = m.attachPendingReminders(content)
	return func() tea.Msg {
		svc.Send(content, images)
		return nil
	}
}

// attachPendingReminders drains the reminder queue and appends any pending
// <system-reminder> blocks to the user message content. The harness uses this
// channel to deliver session/project context (skills, memory, one-time notices)
// without invalidating the system-prompt cache prefix.
func (m *model) attachPendingReminders(content string) string {
	pending := m.services.Reminder.Drain()
	if len(pending) == 0 {
		return content
	}
	return reminder.AttachToContent(content, pending)
}

// wireReminderProviders registers the harness providers that emit on
// SessionStart and PostCompact. Each provider's render closure captures the
// services struct pointer so it always reads the live registry/cache state
// — that way settings reload and skill toggles surface in the next emission
// without ever mutating the cached system prompt.
func (m *model) wireReminderProviders() {
	// Skill.PromptSection already produces a self-introduced body
	// ("Use the Skill tool to invoke these capabilities: ...") so it goes
	// inside <system-reminder> verbatim, matching Claude Code's shape.
	m.services.Reminder.Register(reminder.NewProvider(reminder.ProviderSkillsDirectory, func() string {
		return m.services.Skill.PromptSection()
	}))
	m.services.Reminder.Register(reminder.NewProvider(reminder.ProviderMemoryUser, func() string {
		return reminder.WrapMemory("user", m.env.CachedUserInstructions)
	}))
	m.services.Reminder.Register(reminder.NewProvider(reminder.ProviderMemoryProject, func() string {
		return reminder.WrapMemory("project", m.env.CachedProjectInstructions)
	}))
	// Agent-written auto-memory (L1 reviewer's store). Read at Render() time so
	// PostCompact / cwd change picks up the latest written entries without a
	// separate refresh hook (see notes/active/l1-background-review.md §4.5).
	// Kept as its own scope so agent-written entries never mix with the
	// user-authored memory above.
	m.services.Reminder.Register(reminder.NewProvider(reminder.ProviderMemoryAuto, func() string {
		// Honor a configured memory storage path so the injected memory matches
		// where the reviewer writes.
		override := ""
		if snap := m.services.Setting.Snapshot(); snap != nil {
			override = snap.SelfLearn.Memory.Path
		}
		body, ok := system.LoadAutoMemoryAt(system.ResolveAutoMemoryDir(m.env.CWD, override))
		if !ok {
			return ""
		}
		return reminder.WrapMemory("auto", body)
	}))
}

func (m *model) StopAgentSession() {
	// Capture before Stop clears Session.agent. This also covers stops that
	// happen before ensureAgentSession runs (agent toggles, provider changes,
	// terminal outbox events), which the old rebuild-local carry-over missed.
	if messages := m.services.Agent.Messages(); len(messages) > 0 {
		m.agentRestartMessages = messages
	}
	m.services.Agent.Stop()
	m.teardownSelfLearn()
}

// ResetAgentSession stops the agent without making its live chain eligible for
// the next rebuild. Use it when the conversation itself is being cleared or
// replaced (for example /clear and /resume).
func (m *model) ResetAgentSession() {
	m.agentRestartMessages = nil
	m.services.Agent.Stop()
	// Stop feeding the L1 reviewer AND cancel the session-scoped context
	// so an in-flight fork unblocks immediately instead of holding tokens /
	// HTTP for up to forkDeadline.
	m.teardownSelfLearn()
}

// ============================================================
// Agent outbox and permission gate
// ============================================================

func (m *model) ContinueOutbox() tea.Cmd {
	if !m.services.Agent.Active() {
		return nil
	}
	return conv.DrainAgentOutbox(m.services.Agent.Outbox())
}

func (m *model) HandlePermGate(req *conv.PermGateRequest) tea.Cmd {
	m.services.Agent.SetPendingPermission(req)
	if req == nil {
		m.conv.Tool.ClearAwaitingApproval()
		return nil
	}
	// The call was stamped as started by its PreToolEvent, which fired before
	// this request: name it so its row reports waiting on the user while its
	// batch siblings keep reporting the work they really are doing. A request
	// with no ID names nothing, which leaves the rows on the blunt freeze the
	// modal already forces (see conv.modalNamesNoCall) — correct, but only
	// because it degrades, so say so rather than let a future caller lose the
	// per-call state without a trace.
	if req.ToolCallID == "" {
		log.Logger().Warn("permission request carries no tool call ID; every in-flight row falls back to the docked-modal freeze",
			zap.String("tool", req.ToolName))
	}
	m.conv.Tool.MarkAwaitingApproval(req.ToolCallID)

	permReq := m.preparePermissionRequest(req)
	// Emit permission.required with the metadata about to be rendered to the
	// user — that way the audit captures the same context the user saw.
	if rec := m.services.Session.Recorder(); rec != nil {
		rec.RecordPermissionRequired(transcript.PermissionRecord{
			RequestID:      req.RequestID,
			Tool:           req.ToolName,
			Input:          marshalPermInput(req.Input),
			Detail:         permDetail(permReq),
			OptionsOffered: input.BuildApprovalOptions(permReq),
			Source:         transcript.PermissionSourceAsk,
			Mode:           m.env.SessionMode(),
		})
	}
	if len(m.flush.pendingPrints) != 0 {
		m.deferredApproval = permReq
		return nil
	}
	m.userInput.Approval.Show(permReq, m.env.Width, m.env.Height)
	return nil
}

// showDeferredApproval opens a permission panel only after every queued
// insertAbove has finished. Its contents are ephemeral UI, never transcript
// output, so it must not be present in the managed frame Bubble Tea preserves
// around a scrollback print.
func (m *model) showDeferredApproval() {
	if m.deferredApproval == nil || len(m.flush.pendingPrints) != 0 {
		return
	}
	request := m.deferredApproval
	m.deferredApproval = nil
	m.userInput.Approval.Show(request, m.env.Width, m.env.Height)
}

// permDetail serializes the *derived* permission context — fields the
// resolver computed or looked up beyond the raw tool args. Anything that is
// already a verbatim echo of req.Input (Bash command/description, Skill
// args/name, the file_path that auditors can read straight from input) is
// stripped so the audit record doesn't double-store the same values.
func permDetail(req *perm.PermissionRequest) json.RawMessage {
	if req == nil {
		return nil
	}
	var payload any
	switch {
	case req.SkillMeta != nil:
		m := req.SkillMeta
		payload = struct {
			Description string   `json:"description,omitempty"`
			ScriptCount int      `json:"scriptCount,omitempty"`
			RefCount    int      `json:"refCount,omitempty"`
			Scripts     []string `json:"scripts,omitempty"`
			References  []string `json:"references,omitempty"`
		}{m.Description, m.ScriptCount, m.RefCount, m.Scripts, m.References}
	case req.BashMeta != nil:
		payload = struct {
			LineCount int `json:"lineCount,omitempty"`
		}{req.BashMeta.LineCount}
	case req.AgentMeta != nil:
		payload = req.AgentMeta
	case req.DiffMeta != nil:
		payload = req.DiffMeta
	default:
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		log.Logger().Warn("perm detail marshal failed", zap.Error(err))
		return nil
	}
	return data
}

// ============================================================
// Agent tool configuration
// ============================================================

func (m *model) preparePermissionRequest(req *conv.PermGateRequest) *perm.PermissionRequest {
	if resolved, ok := tool.Get(req.ToolName); ok {
		if pat, ok := resolved.(tool.PermissionAwareTool); ok {
			if rich, err := pat.PreparePermission(context.Background(), req.Input, m.env.CWD); err == nil && rich != nil {
				return rich
			}
		}
	}
	return &perm.PermissionRequest{
		ToolName:    req.ToolName,
		Description: req.Description,
	}
}

func (m *model) ReconfigureAgentTool() {
	if m.env.LLMProvider == nil {
		return
	}
	m.ensureMemoryContextLoaded()

	executor := subagent.NewExecutor(m.env.LLMProvider, m.env.CWD, m.env.GetModelID(), m.services.Hook)
	executor.SetResolver(llm.NewProviderPool(m.services.LLM.Store()))
	if m.services.Session.GetStore() != nil && m.services.Session.ID() != "" {
		executor.SetSessionStore(m.services.Session.GetStore(), m.services.Session.ID())
	}
	executor.SetProjectInstructions(m.env.CachedProjectInstructions)
	executor.SetSkillsDirectory(m.services.Skill.PromptSection())
	executor.SetMCPDependencies(m.services.MCP, m.services.MCP)
	executor.SetDisabledTools(m.services.Setting.DisabledTools())

	adapter := subagent.NewExecutorAdapter(executor)
	type executorSetter interface{ SetExecutor(tool.AgentExecutor) }
	for _, name := range []string{tool.ToolAgent, tool.ToolSendMessage} {
		if t, ok := m.services.Tool.Get(name); ok {
			if setter, ok := t.(executorSetter); ok {
				setter.SetExecutor(adapter)
			}
		}
	}
}

// ============================================================
// LLM client
// ============================================================

func (m *model) buildLLMClient() *llm.Client {
	c := llm.NewClient(m.env.LLMProvider, m.env.GetModelID(), kit.GetMaxTokens(m.services.LLM.Store(), m.env.CurrentModel, setting.DefaultMaxTokens))
	c.SetThinkingEffort(m.env.EffectiveThinkingEffort())
	return c
}
