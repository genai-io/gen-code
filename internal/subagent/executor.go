package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/mcp"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	"go.uber.org/zap"
)

// ProviderResolver turns a vendor name into a live provider so a subagent can
// run on a different vendor than its parent. The app wires an *llm.ProviderPool;
// unresolved explicit "vendor/model" overrides fall back to the parent provider.
type ProviderResolver interface {
	Resolve(ctx context.Context, provider llm.ProviderID) (llm.Provider, error)
}

// Executor runs agent LLM loops
type Executor struct {
	provider                   llm.Provider
	registry                   *Registry
	resolver                   ProviderResolver // resolves "vendor/model" overrides; nil = same-provider only
	modelStore                 *llm.Store       // optional cached provider catalog for validating same-provider overrides
	parentProviderName         llm.ProviderID   // canonical provider key for the parent connection
	parentAuthMethod           llm.AuthMethod   // auth-specific catalog key for the parent connection
	cwd                        string
	parentModelID              string // Parent conversation's model ID (used when inheriting)
	parentPermissionModeGetter func() PermissionMode
	hooks                      hook.Handler
	sessionStore               SubagentSessionStore // Optional: when set, subagent sessions are persisted
	parentSessionID            string               // Parent session ID for linking subagent sessions
	projectInstructions        string               // project memory (AGENTS.md) for edit-capable subagents
	skillsPrompt               string               // available skills section for capable subagents
	mcpTools                   mcp.Tools            // tool schemas + execution
	mcpServers                 mcp.Servers          // connect/disconnect for per-subagent server sets
	disabledToolsMu            sync.RWMutex
	disabledTools              map[string]bool // effective global disabled tools, copied on set/read
}

type SubagentSessionStore interface {
	SaveSubagentConversation(parentSessionID, title, modelID, cwd string, messages []core.Message) (string, string, error)
}

type runConfig struct {
	config      *AgentConfig
	provider    llm.Provider // provider this run talks to (parent's, or a routed vendor)
	modelID     string
	maxSteps    int
	displayName string
	brief       system.SubagentBrief // identity/charter for this run; immutable
	permMode    PermissionMode
}

// PermissionModeFromOperationMode preserves the parent session's effective
// policy for mode="default" without exposing privileged spellings in the tool
// schema.
func PermissionModeFromOperationMode(mode setting.OperationMode) PermissionMode {
	switch mode {
	case setting.ModeAutoAccept, setting.ModeAutoPilot:
		return PermissionAcceptEdits
	case setting.ModeBypassPermissions:
		return PermissionBypass
	case setting.ModeDontAsk:
		return PermissionDontAsk
	case setting.ModeReadOnly:
		return PermissionExplore
	default:
		return PermissionDefault
	}
}

// NewExecutor creates a new agent executor. parentModelID is used for model
// inheritance; hookEngine, when non-nil, fires subagent lifecycle hooks.
// Headless callers inherit the safe default permission policy unless they set a
// parent permission mode getter.
func NewExecutor(llmProvider llm.Provider, cwd string, parentModelID string, hookEngine hook.Handler) *Executor {
	return &Executor{
		provider:      llmProvider,
		registry:      Default(),
		cwd:           cwd,
		parentModelID: parentModelID,
		hooks:         hookEngine,
	}
}

// SetParentPermissionMode provides the parent session's live permission mode.
// It is evaluated for every run so mode="default" follows mode changes made
// after the executor was configured, including entering bypass mode.
func (e *Executor) SetParentPermissionMode(getMode func() PermissionMode) {
	e.parentPermissionModeGetter = getMode
}

func (e *Executor) currentParentPermissionMode() PermissionMode {
	if e.parentPermissionModeGetter == nil {
		return PermissionDefault
	}
	return NormalizePermissionMode(string(e.parentPermissionModeGetter()))
}

// SetProjectInstructions provides the project's instruction memory
// (AGENTS.md). Edit-capable subagents receive it as a
// <system-reminder> so their changes follow project conventions; read-only
// agents do not carry it.
func (e *Executor) SetProjectInstructions(instructions string) {
	e.projectInstructions = instructions
}

// SetResolver enables cross-provider routing: a subagent whose model is an
// explicit "vendor/model" override resolves through this resolver instead of
// reusing the parent's provider. Unavailable routes fall back to the parent.
func (e *Executor) SetResolver(r ProviderResolver) {
	e.resolver = r
}

// SetModelStore supplies the cached catalog and parent connection identity used
// to reject unsupported same-provider overrides without fetching models on the
// agent startup path.
func (e *Executor) SetModelStore(store *llm.Store, provider llm.ProviderID, authMethod llm.AuthMethod) {
	e.modelStore = store
	e.parentProviderName = provider
	e.parentAuthMethod = authMethod
}

// SetSkillsDirectory provides the skills directory section so subagents
// with the Skill tool can see and invoke available skills.
func (e *Executor) SetSkillsDirectory(skillsPrompt string) {
	e.skillsPrompt = skillsPrompt
}

// SetMCPDependencies wires MCP tool access and the per-Agent connection lease
// registry. The registry is a concrete *mcp.Registry rather than the mcp.Servers
// interface because lease operations (acquireConnectionLease/releaseConnectionLease)
// are unexported lifecycle details not exposed on Servers — adding them would
// leak internal ownership semantics to every Servers consumer.
func (e *Executor) SetMCPDependencies(tools mcp.Tools, connectionRegistry *mcp.Registry) {
	e.mcpTools = tools
	e.mcpServers = connectionRegistry
}

// SetDisabledTools supplies the effective global disabled-tool policy inherited
// by subsequently built subagents. The map is copied so settings reloads and
// concurrent runs cannot mutate a tool set while it is being constructed.
func (e *Executor) SetDisabledTools(disabled map[string]bool) {
	e.disabledToolsMu.Lock()
	e.disabledTools = maps.Clone(disabled)
	e.disabledToolsMu.Unlock()
}

func (e *Executor) disabledToolsSnapshot() map[string]bool {
	e.disabledToolsMu.RLock()
	defer e.disabledToolsMu.RUnlock()
	return maps.Clone(e.disabledTools)
}

// SetSessionStore configures session persistence for subagent conversations.
// When set, completed subagent conversations are saved under the parent session.
func (e *Executor) SetSessionStore(store SubagentSessionStore, parentSessionID string) {
	e.sessionStore = store
	e.parentSessionID = parentSessionID
}

// GetParentModelID returns the parent model ID
func (e *Executor) GetParentModelID() string {
	return e.parentModelID
}

// Run executes an agent request and returns the result.
// For background agents, this should be called in a goroutine.
//
// Every exit path fires the SubagentStop hook with the same AgentID the
// SubagentStart hook carried. Cancelled runs persist their conversation and
// partial output for inspection.
func (e *Executor) Run(ctx context.Context, req tool.AgentExecRequest) (*AgentResult, error) {
	run, err := e.prepareRun(ctx, req)
	if err != nil {
		return nil, err
	}

	ctx = e.attachRunContext(ctx, run.cfg.displayName)
	e.logRunStart(run)
	e.fireSubagentStart(run.req, run.hookID)

	result, err := e.executePreparedRun(ctx, run)
	if err != nil && shouldRetryWithParentModel(err, run.cfg.modelID, e.parentModelID) {
		run.cfg.provider = e.provider
		run.cfg.modelID = e.parentModelID
		result, err = e.executePreparedRun(ctx, run)
	}
	if err != nil {
		if unfinished := e.buildUnfinishedAgentResult(run, result); unfinished != nil {
			return unfinished, err
		}
		e.fireSubagentStop(run.req, run.hookID, "", "")
		return nil, fmt.Errorf("LLM completion failed: %w", err)
	}

	return e.buildAgentResult(run, result), nil
}

// RunBackground executes an agent in the background and returns the task.
func (e *Executor) RunBackground(req tool.AgentExecRequest) (*task.AgentTask, error) {
	if err := e.validateRequest(req); err != nil {
		return nil, err
	}
	config, ok := e.resolveRequestAgentConfig(req)
	if !ok {
		return nil, fmt.Errorf("unknown or disabled agent: %s", req.Agent)
	}

	identity := config.Name
	ctx, cancel := context.WithCancel(context.Background())

	agentTask := task.NewAgentTask(
		generateShortID(),
		identity,
		req.Description,
		ctx,
		cancel,
	)
	agentTask.SetIdentity(identity, "")

	task.Default().RegisterTask(agentTask)

	req.TaskID = agentTask.GetID()
	req.OnActivity = func(msg string) {
		agentTask.AppendProgress(msg)
	}
	// Background subagents run unattended: no interactive question channel.
	req.OnQuestion = nil

	go func() {
		defer cancel()

		result, err := e.Run(ctx, req)
		if err != nil {
			// A cancelled run still returns its partial work — keep it in the
			// task output and record its persisted session for inspection.
			if result != nil {
				if result.Content != "" {
					agentTask.AppendOutput([]byte(result.Content + "\n"))
				}
				agentTask.SetIdentity(identity, result.AgentID)
				agentTask.UpdateProgress(result.StepCount, result.TokenUsage.InputTokens+result.TokenUsage.OutputTokens)
			}
			agentTask.AppendOutput([]byte(fmt.Sprintf("Error: %v\n", err)))
			agentTask.Complete(err)
			return
		}

		if result.Content != "" {
			agentTask.AppendOutput([]byte(result.Content))
		}

		agentTask.SetIdentity(identity, result.AgentID)
		agentTask.SetOutputFile(result.TranscriptPath)
		agentTask.UpdateProgress(result.StepCount, result.TokenUsage.InputTokens+result.TokenUsage.OutputTokens)

		if result.Success {
			agentTask.Complete(nil)
		} else {
			agentTask.Complete(fmt.Errorf("%s", result.Error))
		}
	}()

	return agentTask, nil
}

func (e *Executor) validateRequest(req tool.AgentExecRequest) error {
	if strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("agent prompt cannot be empty")
	}
	switch strings.TrimSpace(req.Mode) {
	case "", "default", "explore", "edit":
		return nil
	default:
		return fmt.Errorf("invalid agent mode %q: must be explore, edit, or default", req.Mode)
	}
}

func baseAgentConfig() *AgentConfig {
	return &AgentConfig{
		Description:    baseAgentDescription,
		Model:          "inherit",
		PermissionMode: PermissionDefault,
		MaxSteps:       defaultMaxSteps,
	}
}

// resolveAgentConfig returns the base configuration for an omitted or unknown
// name. A matching enabled definition overrides the base configuration; a
// matching disabled definition is rejected instead of silently bypassed.
func resolveAgentConfig(name string) (*AgentConfig, bool) {
	return resolveAgentConfigFrom(Default(), name)
}

func resolveAgentConfigFrom(registry *Registry, name string) (*AgentConfig, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return baseAgentConfig(), true
	}
	if registry == nil {
		return displayOnlyAgentConfig(name), true
	}
	if config, exists, enabled := registry.LookupAgent(name); exists {
		return config, enabled
	}
	return displayOnlyAgentConfig(name), true
}

// displayOnlyAgentConfig preserves an unknown requested name for identity and
// UI display while otherwise using the base agent template. In particular it
// has no custom prompt, skills, tool rules, model override, or mode override.
func displayOnlyAgentConfig(name string) *AgentConfig {
	config := baseAgentConfig()
	config.Name = strings.TrimSpace(name)
	config.displayOnly = true
	return config
}

func (e *Executor) resolveAgentConfig(name string) (*AgentConfig, bool) {
	return resolveAgentConfigFrom(e.registry, name)
}

func (e *Executor) resolveRequestAgentConfig(req tool.AgentExecRequest) (*AgentConfig, bool) {
	if req.ResolvedAgentConfig != nil {
		config, ok := req.ResolvedAgentConfig.(*AgentConfig)
		return config, ok && config != nil
	}
	return e.resolveAgentConfig(req.Agent)
}

func (e *Executor) prepareRunConfig(ctx context.Context, req tool.AgentExecRequest) (*runConfig, error) {
	config, ok := e.resolveRequestAgentConfig(req)
	if !ok {
		return nil, fmt.Errorf("unknown or disabled agent: %s", req.Agent)
	}

	displayName := e.displayNameFor(config, req)

	permMode := e.requestPermissionMode(config, req)

	maxSteps := max(config.MaxSteps, defaultMaxSteps)
	if req.MaxSteps > maxSteps {
		maxSteps = req.MaxSteps
	}

	provider, modelID, err := e.resolveModel(ctx, req.Model, config.Model)
	if err != nil {
		return nil, err
	}

	return &runConfig{
		config:      config,
		provider:    provider,
		modelID:     modelID,
		maxSteps:    maxSteps,
		displayName: displayName,
		brief:       e.buildBrief(config, permMode),
		permMode:    permMode,
	}, nil
}

func (e *Executor) fireSubagentStart(req tool.AgentExecRequest, agentHookID string) {
	if e.hooks == nil {
		return
	}
	e.hooks.ExecuteAsync(hook.SubagentStart, hook.HookInput{
		AgentName:   strings.TrimSpace(req.Agent),
		AgentID:     agentHookID,
		Description: req.Description,
	})
}

func (e *Executor) buildAgent(ctx context.Context, run *preparedRun, onToolExec func(string, map[string]any), onEvent func(core.Event)) (core.Agent, func(), error) {
	rc := run.cfg
	agentCwd := run.cwd
	cleanup := func() {}

	if len(rc.config.McpServers) > 0 && e.mcpServers != nil {
		mcpCleanup, errs := mcp.ConnectServers(ctx, e.mcpServers, rc.config.McpServers)
		if mcpCleanup != nil {
			cleanup = mcpCleanup
		}
		for _, err := range errs {
			log.Logger().Warn("Agent MCP server connection failed", zap.Error(err))
		}
	}

	// Subagent system prompt deliberately omits skills and memory — those
	// ride on the first user message as <system-reminder> blocks built by
	// loadConversation, keeping subagents on the same harness channel
	// pattern as the main agent.
	sys := system.Build(core.ScopeSubagent,
		system.WithSubagentIdentity(rc.brief),
		system.WithEnvironment(system.Environment{Cwd: agentCwd}),
	)

	// Tools — adapt legacy tool registry + MCP tools
	var mcpGetter func() []core.ToolSchema
	if e.mcpTools != nil {
		mcpGetter = e.mcpTools.GetToolSchemas
	}
	toolSet := newAgentToolSet(rc.config.AllowTools.Names(), rc.config.DenyTools.BareNames(), e.disabledToolsSnapshot(), mcpGetter)
	schemas := filterSchemasForPermission(toolSet.Tools(), rc.permMode, rc.config.AllowTools)
	var ag core.Agent
	adaptOpts := []tool.AdaptOption{tool.WithMessagesGetterProvider(func() []core.Message {
		if ag == nil {
			return nil
		}
		return ag.Messages()
	})}
	// Foreground runs route AskUserQuestion through the spawning turn's UI.
	// Background runs have no question channel (OnQuestion is stripped).
	if run.req.OnQuestion != nil {
		adaptOpts = append(adaptOpts, tool.WithAskUser(tool.AskUserFunc(run.req.OnQuestion)))
	}
	tools := tool.AdaptToolRegistry(schemas, func() string { return agentCwd }, adaptOpts...)

	// Add MCP tool executors
	if e.mcpTools != nil {
		mcpCaller := mcp.NewCaller(e.mcpTools)
		for _, t := range mcp.AsCoreTools(schemas, mcpCaller) {
			tools.Add(t, "mcp:"+t.Schema().Name)
		}
	}

	var coreTools core.Tools = tools
	if onToolExec != nil {
		coreTools = &activityTools{inner: tools, onExec: onToolExec}
	}

	// Wrap tools with permission decorator
	permFn := subagentPermissionFunc(rc.permMode, rc.config.AllowTools, rc.config.DenyTools)
	coreTools = tool.WithPermission(coreTools, permFn)

	llmClient := llm.NewClient(rc.provider, rc.modelID, 0)
	ag = core.NewAgent(core.Config{
		Client:      llmClient.TurnClient,
		CallOptions: llmClient.CallOptions,
		InputLimit:  llmClient.InputLimit,
		System:      sys,
		Tools:       coreTools,
		CompactFunc: subagentCompactFunc(llmClient),
		CWD:         agentCwd,
		MaxSteps:    rc.maxSteps,
		OutboxBuf:   -1,
		OnEvent:     onEvent,
	})

	return ag, cleanup, nil
}

// subagentCompactFunc summarizes the conversation on the run's own model so
// long subagent runs survive context-window pressure instead of dying on
// prompt-too-long. Mirrors the main agent's compaction.
func subagentCompactFunc(client *llm.Client) func(context.Context, []core.Message) (string, error) {
	return func(ctx context.Context, msgs []core.Message) (string, error) {
		text := core.BuildCompactionText(msgs)
		resp, err := client.Complete(ctx, system.CompactPrompt(), []core.Message{core.UserMessage(text, nil)}, core.CompactMaxTokens)
		if err != nil {
			return "", err
		}
		summary := strings.TrimSpace(resp.Content)
		if summary == "" {
			return "", fmt.Errorf("compaction produced empty summary")
		}
		return summary, nil
	}
}

func (e *Executor) loadConversation(ag core.Agent, ctx context.Context, rc *runConfig, req tool.AgentExecRequest) error {
	// Harness-managed reminders ride on the first user message as
	// <system-reminder> blocks, matching the main agent's pattern.
	reminders := e.collectSubagentReminders(e.skillsDirectoryFor(rc.config), rc.permMode, rc.config.AllowTools)
	prompt := reminder.AttachToContent(req.Prompt, reminders)
	ag.Append(ctx, core.UserMessage(prompt, nil))
	return nil
}

// collectSubagentReminders returns the <system-reminder> blocks for the
// subagent's first user message. Subagents get the skills directory (so they
// can invoke capabilities) and — when they can modify the workspace — the
// project's instruction memory, so their edits follow project conventions.
// User memory stays with the main loop: a subagent is a one-shot worker
// bounded by its own charter.
func (e *Executor) collectSubagentReminders(skills string, mode PermissionMode, allow ToolList) []string {
	var reminders []string
	reminders = append(reminders, wrapNonEmpty(skills)...)
	if canEditWorkspace(mode, allow) {
		reminders = append(reminders, wrapNonEmpty(reminder.WrapMemory("project", e.projectInstructions))...)
	}
	return reminders
}

func wrapNonEmpty(body string) []string {
	if w := reminder.Wrap(body); w != "" {
		return []string{w}
	}
	return nil
}

// canEditWorkspace reports whether the subagent can change files in the
// workspace — the signal for whether project conventions are worth handing it.
// True when the permission mode auto-accepts mutations (edit/bypass), or when
// an explicit allow_tools rule grants an edit-class tool (Edit/Write/…) even
// under a mode, such as default, that would otherwise deny them.
func canEditWorkspace(mode PermissionMode, allow ToolList) bool {
	switch operationMode(mode) {
	case setting.ModeAutoAccept, setting.ModeBypassPermissions:
		return true
	}
	return slices.ContainsFunc(allow.Names(), perm.IsEditTool)
}

func interpretStopReason(result *core.Result, maxSteps int) (success bool, errMsg string) {
	success = result.StopReason == core.StopEndTurn
	switch result.StopReason {
	case core.StopMaxSteps:
		errMsg = fmt.Sprintf("reached maximum steps (%d)", maxSteps)
	case core.StopMaxOutputRecoveryExhausted:
		errMsg = "output was repeatedly truncated and recovery was exhausted"
	case core.StopCancelled:
		errMsg = "agent cancelled"
	case core.StopHook, core.StopError:
		errMsg = result.StopDetail
	}
	return success, errMsg
}

// fireSubagentStop fires on every run exit — success, cancellation, or error
// — with the same AgentID that SubagentStart carried, so hook consumers can
// pair the two events. The session id travels via the transcript path.
func (e *Executor) fireSubagentStop(req tool.AgentExecRequest, agentHookID, agentTranscriptPath, resultContent string) {
	if e.hooks == nil {
		return
	}

	e.hooks.ExecuteAsync(hook.SubagentStop, hook.HookInput{
		AgentName:            strings.TrimSpace(req.Agent),
		AgentID:              agentHookID,
		AgentTranscriptPath:  agentTranscriptPath,
		LastAssistantMessage: resultContent,
		StopHookActive:       e.hooks.StopHookActive(),
	})
}

// resolveModel picks the provider and model id for a run, by priority:
// 1. Explicit request override (req.Model)
// 2. Agent configuration (config.Model)
// 3. Parent conversation model ("inherit" or empty)
//
// An explicit "vendor/model" override routes to that vendor through the
// resolver only when a linked provider can be resolved. Otherwise it falls
// back to the parent conversation. Every other form — an alias, a bare model
// id, or "inherit" — stays on the parent's provider, preserving prior behavior.
func (e *Executor) resolveModel(ctx context.Context, requestModel, configModel string) (llm.Provider, string, error) {
	ref := strings.TrimSpace(requestModel)
	if ref == "" {
		ref = strings.TrimSpace(configModel)
	}
	if ref == "" || ref == "inherit" {
		return e.provider, e.parentModelID, nil
	}
	if vendor, modelID, ok := llm.ParseVendorModel(ref); ok {
		if e.resolver == nil {
			return e.provider, e.parentModelID, nil
		}
		if vendor == e.parentProviderName && modelID != e.parentModelID && !e.cachedCatalogAllowsModel(modelID) {
			return e.provider, e.parentModelID, nil
		}
		p, err := e.resolver.Resolve(ctx, vendor)
		if err != nil || p == nil {
			return e.provider, e.parentModelID, nil
		}
		return p, modelID, nil
	}
	// A bare id or alias stays on the parent provider. If the cached catalog
	// positively reports that provider does not offer the model, inherit instead
	// of sending a request that may fail with an opaque 400 response.
	modelID := resolveModelAlias(ref)
	if modelID != e.parentModelID && !e.cachedCatalogAllowsModel(modelID) {
		return e.provider, e.parentModelID, nil
	}
	return e.provider, modelID, nil
}

// cachedCatalogAllowsModel rejects only a definitive cached miss. A missing
// store, parent connection identity, or catalog leaves the override unverified
// and therefore allowed.
func (e *Executor) cachedCatalogAllowsModel(modelID string) bool {
	if e.modelStore == nil || e.parentProviderName == "" || modelID == "" {
		return true
	}
	models, ok := e.modelStore.GetCachedModels(e.parentProviderName, e.parentAuthMethod)
	if !ok {
		return true
	}
	for _, model := range models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func shouldRetryWithParentModel(err error, modelID, parentModelID string) bool {
	if err == nil || parentModelID == "" || modelID == "" || modelID == parentModelID {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "model_not_found") || strings.Contains(msg, "model not found") || strings.Contains(msg, "model_not_exist")
}

// operationMode maps a subagent PermissionMode to the setting.OperationMode that
// drives the shared mode-default table (setting.ModeDefault). dontAsk folds to a
// read-only-style denial since subagents never prompt; auto aliases to
// acceptEdits until the safety classifier ships.
func operationMode(mode PermissionMode) setting.OperationMode {
	switch NormalizePermissionMode(string(mode)) {
	case PermissionExplore:
		return setting.ModeReadOnly
	case PermissionAcceptEdits, PermissionAuto:
		return setting.ModeAutoAccept
	case PermissionBypass:
		return setting.ModeBypassPermissions
	case PermissionDontAsk:
		return setting.ModeDontAsk
	default:
		return setting.ModeNormal
	}
}

// skillsDirectoryFor returns the skills section body for the agent, gated on
// its tool allow list — a subagent without the Skill tool gets no skills
// section.
func (e *Executor) skillsDirectoryFor(config *AgentConfig) string {
	if config == nil {
		return ""
	}
	if config.AllowTools == nil || config.AllowTools.HasName("Skill") {
		return e.skillsPrompt
	}
	return ""
}

// subagentPermissionFunc returns the subagent permission gate. The pipeline
// matches docs/concepts/permission-model.md: deny_tools, circuit breaker,
// confirmation tiers, allow_tools, mode default, with Prompt collapsing to
// Deny because subagents cannot ask. bypassPermissions mode skips the
// confirmation tiers — only the root/home-removal circuit breaker holds in
// every mode.
//
// One communication carve-out sits between deny and allow_tools: for a
// mode-gated worker (no explicit allow_tools list) SendMessage is permitted in
// every mode, so even a read-only or default worker can report to main or
// answer a peer — it only moves text between agents and never touches the
// workspace. deny_tools still blocks it, and an explicit allow_tools list is
// honored as written (leave SendMessage out to silence a worker). Spawning is
// not gated here: the Agent tool is parent-only, so workers never see it — the
// agent model is flat.
func subagentPermissionFunc(mode PermissionMode, allowRules, denyRules ToolList) perm.PermissionFunc {
	mode = NormalizePermissionMode(string(mode))
	opMode := operationMode(mode)
	display := displayPermissionMode(mode)

	return func(_ context.Context, name string, input map[string]any) (bool, string) {
		if denyRules.Matches(name, input) {
			return false, fmt.Sprintf("tool %s is blocked by deny_tools", name)
		}
		// Circuit breaker: a recursive removal of the filesystem root or the
		// home directory is denied in every mode, bypass included — subagents
		// cannot ask, so the breaker is a hard deny.
		if reason := setting.CircuitBreakerReason(name, input); reason != "" {
			return false, fmt.Sprintf("tool %s blocked: %s", name, reason)
		}
		// Bypass mode skips both confirmation tiers: after deny_tools and the
		// breaker above, permit every executable worker tool. Keep the
		// parent-only restriction below so a worker still cannot spawn agents
		// or manipulate main-only state.
		if opMode == setting.ModeBypassPermissions && !tool.IsParentOnlyTool(name) {
			return true, ""
		}
		// Outside bypass, both confirmation tiers are hard denies, so a greedy
		// allow_tools pattern cannot smuggle destruction through.
		if reason, _ := setting.ConfirmationTier(name, input); reason != "" {
			return false, fmt.Sprintf("tool %s blocked: %s", name, reason)
		}
		// Parent-only tools never reach a worker, even via allow_tools —
		// checked here so the safe-tool auto-permit below cannot resurrect a
		// hallucinated tracker or cron call.
		if tool.IsParentOnlyTool(name) {
			return false, fmt.Sprintf("tool %s is reserved for the main conversation", name)
		}
		// Explore is an authoritative read-only boundary. Explicit allow rules
		// may narrow its tools but cannot elevate it to workspace mutation.
		if mode == PermissionExplore {
			switch {
			case perm.IsSafeTool(name), name == tool.ToolSendMessage, name == tool.ToolSkill:
				return true, ""
			case name == tool.ToolBash:
				command, _ := input["command"].(string)
				if setting.IsReadOnlyBashCommand(command) {
					return true, ""
				}
			}
			return false, fmt.Sprintf("tool %s is denied in %s mode", name, display)
		}
		// Communication carve-out (see doc comment): a mode-gated worker may
		// always reach main or a peer via SendMessage. A worker with an explicit
		// allow_tools list is governed by that list instead.
		if name == tool.ToolSendMessage && allowRules == nil {
			return true, ""
		}
		if allowRules.Allows(name, input) {
			return true, ""
		}
		// A provably read-only Bash command (search, listing, git inspection)
		// is as safe as the dedicated read-only tools, which also run without
		// being listed in allow_tools. Checked after deny_tools so agents can
		// still block Bash outright.
		if name == "Bash" {
			if cmd, ok := input["command"].(string); ok && setting.IsReadOnlyBashCommand(cmd) {
				return true, ""
			}
		}
		// allow_tools mentions this tool but no pattern matched — the agent
		// declared a constrained whitelist, so deny rather than fall through.
		if allowRules.HasName(name) {
			return false, fmt.Sprintf("tool %s call is outside the allow_tools constraint", name)
		}
		// Loading a skill's instructions has no side effects of its own — the
		// actions those instructions trigger are still gated per tool call —
		// so Skill works in every mode, matching the skills listing already
		// embedded in the worker's prompt.
		if name == tool.ToolSkill {
			return true, ""
		}
		switch setting.ModeDefault(name, opMode).Behavior {
		case perm.Permit:
			return true, ""
		case perm.Reject:
			return false, fmt.Sprintf("tool %s is denied in %s mode", name, display)
		default: // perm.Prompt — subagents cannot ask
			return false, fmt.Sprintf("tool %s would require approval; subagent in %s mode denies it", name, display)
		}
	}
}

// filterSchemasForPermission narrows the LLM-visible tool set to what the
// agent can actually use under its mode + allow_tools. UX hint only — the
// permission gate is still authoritative. A non-nil allowTools acts as a
// whitelist regardless of mode.
func filterSchemasForPermission(schemas []core.ToolSchema, mode PermissionMode, allowTools ToolList) []core.ToolSchema {
	mode = NormalizePermissionMode(string(mode))
	whitelist := allowTools != nil

	filtered := make([]core.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		if whitelist {
			if allowTools.HasName(schema.Name) && (mode != PermissionExplore || modeAllowsSchema(mode, schema.Name)) {
				filtered = append(filtered, schema)
			}
			continue
		}
		if modeAllowsSchema(mode, schema.Name) {
			filtered = append(filtered, schema)
		}
	}
	return filtered
}

func modeAllowsSchema(mode PermissionMode, name string) bool {
	// SendMessage is a communication tool, kept visible in every mode so a
	// mode-gated worker can report to main (the gate permits it — see
	// subagentPermissionFunc). A worker with an explicit allow_tools list takes
	// the whitelist branch in filterSchemasForPermission instead of this one.
	if perm.IsSafeTool(name) || name == tool.ToolSendMessage {
		return true
	}
	switch mode {
	case PermissionBypass, PermissionAuto:
		return true
	case PermissionAcceptEdits:
		if perm.IsEditTool(name) {
			return true
		}
	}
	// Read-only Bash invocations are permitted in every mode, so the Bash
	// schema stays visible even to explore/read-only agents — it is their
	// search tool. Skill stays visible for the same reason the gate permits
	// it: the worker's prompt already lists available skills.
	return name == "Bash" || name == tool.ToolSkill
}

// newAgentToolSet creates a tool.Set for subagents with global and per-agent
// exclusions eagerly initialized.
func newAgentToolSet(allow, disallow []string, disabled map[string]bool, mcpGetter func() []core.ToolSchema) *tool.Set {
	s := &tool.Set{
		Allow: slices.Clone(allow), Disallow: slices.Clone(disallow), Disabled: maps.Clone(disabled),
		MCP: mcpGetter, IsAgent: true,
	}
	s.InitDisallowSet()
	return s
}

// generateShortID creates a short random hex ID for background tasks.
func generateShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
