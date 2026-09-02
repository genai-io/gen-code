package hook

import (
	"context"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/setting"
)

// LLMCompleter performs a single-turn LLM completion for hook execution.
// The caller supplies the system prompt, user message, and model identifier;
// the implementation owns provider construction and streaming details.
type LLMCompleter func(ctx context.Context, systemPrompt, userMessage, model string) (string, error)

// HookFiredAudit is the payload delivered to AuditCallback after every hook
// invocation. The session recorder uses this to write one hook.fired
// transcript record per invocation.
type HookFiredAudit struct {
	Event    string // hook EventType as a string
	Source   string // script path / function ID identifying which hook ran
	Matcher  string // configured matcher
	Outcome  string // "ran" | "blocked" | "error" | "async"
	Reason   string // hook-supplied block reason or error message
	Duration time.Duration
}

// AuditCallback is invoked once per completed hook execution (sync or async).
// It MUST NOT block — the engine calls it inline and audit failures shouldn't
// stall the hook pipeline.
type AuditCallback func(HookFiredAudit)

// defaultTimeout is the default timeout for hook commands in seconds.
const defaultTimeout = 600

// defaultStdinIdle is how long a hook has to produce its first line before its
// stdin is closed. A hook that reads to EOF — `input=$(cat)`, which most of
// them do — cannot proceed until that happens, so this is a cost every hook
// pays and the reason it is short.
const defaultStdinIdle = 500 * time.Millisecond

// Engine executes hooks from settings, plugins, and runtime/session registration.
type Engine struct {
	settings       *setting.Data
	sessionID      string
	cwd            string
	transcriptPath string
	permissionMode string

	promptCallback PromptCallback
	llmCompleter   LLMCompleter
	hookModel      string
	httpClient     *http.Client
	asyncCallback  AsyncHookCallback
	auditCallback  AuditCallback
	envProvider    func(context.Context) []string
	// stdinIdle is how long an interactive hook has to say something before
	// its stdin is closed. See the timer in executors_command.go for what it
	// is guarding against; a test sets it because it cannot control how loaded
	// the machine is, and racing a wall clock is how a test becomes flaky.
	stdinIdle time.Duration

	mu         sync.RWMutex
	store      *hookStore
	status     *statusTracker
	detachedWg sync.WaitGroup // tracks fire-and-forget goroutines
}

// NewEngine creates a new hook execution engine.
func NewEngine(settings *setting.Data, sessionID, cwd, transcriptPath string) *Engine {
	return &Engine{
		settings:       settings,
		sessionID:      sessionID,
		cwd:            cwd,
		transcriptPath: transcriptPath,
		permissionMode: "default",
		httpClient:     http.DefaultClient,
		store:          newHookStore(),
		status:         newStatusTracker(),
		stdinIdle:      defaultStdinIdle,
	}
}

// SetAuditCallback wires the per-execution audit hook. nil clears it.
func (e *Engine) SetAuditCallback(cb AuditCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auditCallback = cb
}

func (e *Engine) audit(a HookFiredAudit) {
	e.mu.RLock()
	cb := e.auditCallback
	e.mu.RUnlock()
	if cb != nil {
		cb(a)
	}
}

// SetPermissionMode sets the current permission mode (normal, auto, plan).
func (e *Engine) SetPermissionMode(mode string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.permissionMode = mode
}

// SetPromptCallback sets the callback for bidirectional prompt exchanges.
func (e *Engine) SetPromptCallback(cb PromptCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.promptCallback = cb
}

// SetTranscriptPath updates the transcript path after engine creation.
func (e *Engine) SetTranscriptPath(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.transcriptPath = path
}

// SetCwd updates the working directory used for hook input and subprocess execution.
func (e *Engine) SetCwd(cwd string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cwd = cwd
}

// SetLLMCompleter configures the completion function and default model used by
// prompt and agent hooks.
func (e *Engine) SetLLMCompleter(fn LLMCompleter, model string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.llmCompleter = fn
	e.hookModel = model
}

// SetAsyncHookCallback configures delivery of background asyncRewake hook results.
func (e *Engine) SetAsyncHookCallback(cb AsyncHookCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asyncCallback = cb
}

// SetEnvProvider configures a function that supplies additional environment
// variables for hook subprocess execution.
func (e *Engine) SetEnvProvider(fn func(context.Context) []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.envProvider = fn
}

// SetSettings swaps the settings-backed hook source used by the engine.
// Callers should merge plugin hooks into settings before calling this.
func (e *Engine) SetSettings(settings *setting.Data) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settings = settings
}

// ClearSessionHooks removes all session-scoped hooks.
func (e *Engine) ClearSessionHooks() {
	e.store.ClearSessionHooks()
}

// AddSessionFunctionHook registers an in-memory function hook scoped to the
// current engine/session instance.
func (e *Engine) AddSessionFunctionHook(event EventType, matcher string, hook FunctionHook) string {
	return e.store.AddSessionFunctionHook(event, matcher, hook)
}

// AddRuntimeFunctionHook registers an in-memory function hook for the current
// process.
func (e *Engine) AddRuntimeFunctionHook(event EventType, matcher string, hook FunctionHook) string {
	return e.store.AddRuntimeFunctionHook(event, matcher, hook)
}

// Execute runs all matching hooks for an event synchronously.
func (e *Engine) Execute(ctx context.Context, event EventType, input HookInput) HookOutcome {
	outcome := HookOutcome{ShouldContinue: true}
	hooks := e.getMatchingHooks(event, &input)
	if len(hooks) == 0 {
		return outcome
	}

	for _, hook := range hooks {
		if hook.Command != nil && (hook.Command.Async || hook.Command.AsyncRewake) {
			hookCopy, inputCopy := hook, input
			e.detachedWg.Go(func() {
				e.executeDetachedHook(context.Background(), hookCopy, inputCopy)
			})
			// Emit at launch; the detached goroutine re-emits with the real
			// outcome when it finishes. Two records make the lifecycle visible.
			e.audit(HookFiredAudit{
				Event:   string(event),
				Source:  matchedHookIdentity(hook),
				Matcher: hook.Matcher,
				Outcome: outcomeAsync,
			})
			continue
		}

		hookStart := time.Now()
		result := e.executeMatchedHook(ctx, hook, input)
		duration := time.Since(hookStart)
		log.Logger().Debug("hook executed",
			zap.String("event", string(event)),
			zap.String("source", matchedHookIdentity(hook)),
			zap.Duration("duration", duration),
			zap.Bool("error", result.Error != nil),
		)
		e.audit(HookFiredAudit{
			Event:    string(event),
			Source:   matchedHookIdentity(hook),
			Matcher:  hook.Matcher,
			Outcome:  outcomeOf(result),
			Reason:   reasonOf(result),
			Duration: duration,
		})
		if result.Error != nil {
			log.Logger().Warn("hook execution failed",
				zap.String("event", string(event)),
				zap.String("source", result.HookSource),
				zap.Error(result.Error),
			)
			continue
		}
		if !result.ShouldContinue {
			return result
		}
		outcome = e.mergeOutcome(outcome, result)
	}

	return outcome
}

func outcomeOf(r HookOutcome) string {
	switch {
	case r.Error != nil:
		return outcomeError
	case !r.ShouldContinue:
		return outcomeBlocked
	default:
		return outcomeRan
	}
}

// Package-local mirrors of transcript.HookOutcome* so the hook package
// doesn't import the transcript package. The two sides are joined by an
// audit consumer (session.Recorder) that does the final mapping.
const (
	outcomeRan     = "ran"
	outcomeBlocked = "blocked"
	outcomeError   = "error"
	outcomeAsync   = "async"
)

// reasonOf extracts the human-readable reason for blocked/error outcomes.
func reasonOf(r HookOutcome) string {
	if r.Error != nil {
		return r.Error.Error()
	}
	if !r.ShouldContinue {
		return r.BlockReason
	}
	return ""
}

// ExecuteAsync runs all matching hooks asynchronously (fire-and-forget).
func (e *Engine) ExecuteAsync(event EventType, input HookInput) {
	hooks := e.getMatchingHooks(event, &input)
	for _, hook := range hooks {
		hookCopy, inputCopy := hook, input
		e.detachedWg.Go(func() {
			e.executeDetachedHook(context.Background(), hookCopy, inputCopy)
		})
	}
}

// HasHooks returns true if there are any hooks configured for the given event.
func (e *Engine) HasHooks(event EventType) bool {
	e.mu.RLock()
	settings := e.settings
	e.mu.RUnlock()
	return e.store.HasHooks(event, settings)
}

// StopHookActive returns a *bool indicating whether Stop hooks are configured.
func (e *Engine) StopHookActive() *bool {
	active := e.HasHooks(Stop)
	return &active
}

func (e *Engine) getAsyncHookCallback() AsyncHookCallback {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.asyncCallback
}

// CurrentStatusMessage returns the most recently-started active hook status.
func (e *Engine) CurrentStatusMessage() string {
	return e.status.CurrentMessage()
}

// stdinIdleOrDefault is the grace period, tolerating a zero-value Engine.
func (e *Engine) stdinIdleOrDefault() time.Duration {
	if e.stdinIdle <= 0 {
		return defaultStdinIdle
	}
	return e.stdinIdle
}
