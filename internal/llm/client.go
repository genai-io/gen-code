package llm

import (
	"context"
	"errors"
	"sync"

	"github.com/genai-io/san/internal/core"
)

// A Provider plus a model, as the agent loop sees it.
//
// core.LLM is what the loop actually holds, and Client is the only thing that
// implements it: it fixes a model on a provider, resolves the two token limits
// that model implies, tags a failure with what the loop needs to know about
// it, and keeps a running token count for the session. Everything above it
// talks about "the LLM"; everything below it talks about a vendor.

// defaultMaxTokens is the fallback max output tokens when neither the caller
// nor the provider specifies a limit.
const defaultMaxTokens = 8192

// completeMaxAttempts bounds in-place retries for one-shot utility completions.
const completeMaxAttempts = 3

// Client adapts one Provider and one model to core.LLM, and is also what the
// loop and app layers stream and complete through. SetThinkingEffort may be
// called while the agent is running; it takes effect on the next call.
type Client struct {
	// provider, model and maxTokens are fixed at construction and read without
	// the lock. Only thinkingEffort changes over a client's life, which is what
	// mu guards.
	provider  Provider
	model     string
	maxTokens int

	mu             sync.RWMutex
	thinkingEffort string

	// Token limits resolve from the provider's ListModels, which is a live
	// network round-trip for OpenAI-compatible providers (Anthropic/Google
	// cache it internally). A client's model is fixed for its lifetime, so the
	// resolved limits are memoized here to keep ListModels off the
	// per-inference-step hot path (InputLimit for compaction, output cap for
	// every Infer/Stream).
	limits modelLimits
}

// modelLimits memoizes the two token limits for the client's fixed model, so a
// provider ListModels lookup runs once instead of on every inference step.
//
// One resolution answers both questions, because one listing states both —
// asking separately paid for the same network round-trip twice. But each
// figure is kept on its own: a listing routinely states a window and no output
// cap, and holding the pair hostage to the missing half would re-list on every
// call forever. A zero means "unknown, retry later" and is never stored, so a
// transient provider failure retries rather than sticking at 0.
type modelLimits struct {
	mu  sync.Mutex
	in  int
	out int
}

func (c *modelLimits) input(p Provider, model string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.in == 0 {
		c.resolve(p, model)
	}
	return c.in
}

func (c *modelLimits) output(p Provider, model string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.out == 0 {
		c.resolve(p, model)
	}
	return c.out
}

// resolve fills in whatever the provider can state. Callers hold c.mu.
func (c *modelLimits) resolve(p Provider, model string) {
	in, out := resolveModelLimits(p, model)
	if in > 0 {
		c.in = in
	}
	if out > 0 {
		c.out = out
	}
}

// resolveModelLimits asks the provider what the model takes and produces,
// falling back to its per-model endpoint for the vendors whose listing says
// nothing (see ModelLimitsFetcher). Either figure may come back 0, which means
// unknown.
func resolveModelLimits(p Provider, model string) (in, out int) {
	if p == nil {
		return 0, 0
	}
	if models, err := p.ListModels(context.TODO()); err == nil {
		for _, m := range models {
			if m.ID == model {
				in, out = m.InputTokenLimit, m.OutputTokenLimit
				break
			}
		}
	}
	if in > 0 && out > 0 {
		return in, out
	}
	fetcher, ok := p.(ModelLimitsFetcher)
	if !ok {
		return in, out
	}
	fetchedIn, fetchedOut, err := fetcher.FetchModelLimits(context.TODO(), model)
	if err != nil {
		return in, out
	}
	return max(in, fetchedIn), max(out, fetchedOut)
}

// NewClient fixes a model on a provider. maxTokens=0 means resolve the cap
// from the model's own metadata, falling back to defaultMaxTokens.
func NewClient(p Provider, model string, maxTokens int) *Client {
	return &Client{provider: p, model: model, maxTokens: maxTokens}
}

// ---------------------------------------------------------------------------
// core.LLM interface
// ---------------------------------------------------------------------------

func (l *Client) Infer(ctx context.Context, req core.InferRequest) (<-chan core.Chunk, error) {
	srcCh := l.provider.Stream(ctx, l.completionOpts(toProviderMessages(req.Messages), req.Tools, req.System))

	ch := make(chan core.Chunk, 8)
	go func() {
		defer close(ch)
		// send forwards a chunk, aborting on ctx cancellation so this bridge
		// goroutine doesn't wedge when streamInfer exits via its ctx.Done.
		send := func(chunk core.Chunk) bool {
			select {
			case ch <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for sc := range srcCh {
			switch sc.Type {
			case ChunkTypeText:
				if !send(core.Chunk{Text: sc.Text}) {
					return
				}
			case ChunkTypeThinking:
				if !send(core.Chunk{Thinking: sc.Text}) {
					return
				}
			case ChunkTypeDone:
				if !send(core.Chunk{Done: true, Response: sc.Response}) {
					return
				}
			case ChunkTypeError:
				// The vendor seam already tagged what it recognised; this is
				// the last chance to tag a terminal error that arrived with
				// its type lost, which is routine on a broken stream.
				send(core.Chunk{Err: classifyStream(sc.Error)})
				return
			}
		}
	}()

	return ch, nil
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// SetThinkingEffort changes the native thinking/reasoning effort value.
func (l *Client) SetThinkingEffort(effort string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.thinkingEffort = effort
}

// ThinkingEffort returns the current native thinking/reasoning effort value.
func (l *Client) ThinkingEffort() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.thinkingEffort
}

// ---------------------------------------------------------------------------
// Streaming & Completion (used by loop/app layer)
// ---------------------------------------------------------------------------

// Stream starts a streaming completion request and returns a chunk channel.
func (l *Client) Stream(ctx context.Context, msgs []core.Message,
	tools []ToolSchema, sysPrompt string,
) <-chan StreamChunk {
	return l.provider.Stream(ctx, l.completionOpts(msgs, tools, sysPrompt))
}

// Complete sends a one-shot completion (custom max tokens, no tools).
// Used for utility calls like conversation compaction.
func (l *Client) Complete(ctx context.Context,
	sysPrompt string, msgs []core.Message, maxTokens int,
) (CompletionResponse, error) {
	opts := l.completionOpts(msgs, nil, sysPrompt)
	opts.MaxTokens = maxTokens

	// Utility calls (e.g. compaction) are not streamed to the UI, so retry
	// them in place on transient failures, sharing the agent loop's backoff.
	var resp CompletionResponse
	var err error
	for attempt := 1; attempt <= completeMaxAttempts; attempt++ {
		if resp, err = Complete(ctx, l.provider, opts); err == nil {
			return resp, nil
		}
		// Tagged on the way out, not just for the retry decision: an error
		// leaving this package is always classified, so a caller cannot get a
		// different answer about the same failure by reaching for a different
		// helper.
		err = classifyStream(err)
		var re core.RetryableError
		if !errors.As(err, &re) || attempt == completeMaxAttempts {
			return resp, err
		}
		if werr := core.BackoffSleep(ctx, attempt, re.RetryAfter()); werr != nil {
			return resp, werr
		}
	}
	return resp, err
}

// ---------------------------------------------------------------------------
// Identity & Limits
// ---------------------------------------------------------------------------

// Name returns the provider name (e.g., "anthropic").
func (l *Client) Name() string {
	if l.provider == nil {
		return ""
	}
	return l.provider.Name()
}

// ModelID returns the model identifier.
func (l *Client) ModelID() string { return l.model }

// InputLimit returns the model's context window, or 0 when it cannot be
// determined — callers treat 0 as "unknown" and skip any size check rather
// than acting on a guess (see InputLimitEnvVar).
//
// It reads the shared resolver (EffectiveInputLimit) rather than taking an
// injected value, so every client resolves the window the same way the status
// bar does without any construction site having to remember to pass it. The
// store answers from memory; the live provider lookup behind it is memoized
// per model, so this stays cheap enough for the per-inference-step compaction
// check to call it.
func (l *Client) InputLimit() int {
	p, model := l.provider, l.model

	// A provider names itself "vendor:auth_method", which is exactly the two
	// things the provider-scoped cache is keyed by — and the reason to split
	// it rather than cast it whole: the store keys connections by the bare
	// vendor, so the composite string misses every lookup and silently falls
	// through to the cross-provider scan this call exists to avoid.
	//
	// Both store methods are nil-receiver safe, and EffectiveInputLimit is
	// called unconditionally so the env override it checks first is honored
	// even before a store exists.
	var provider ProviderID
	var auth AuthMethod
	if p != nil {
		provider, auth = parseProviderKey(p.Name())
	}
	store := Default().Store()
	if auth == "" {
		auth = store.ConnectionAuthMethod(provider)
	}
	if n := store.EffectiveInputLimit(provider, auth, model); n > 0 {
		return n
	}
	return l.limits.input(p, model)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// effectiveMaxTokens resolves the output-token cap: an explicit maxTokens
// override wins, otherwise the memoized provider limit, otherwise the default.
func (l *Client) effectiveMaxTokens() int {
	if l.maxTokens > 0 {
		return l.maxTokens
	}
	p, model := l.provider, l.model
	if out := l.limits.output(p, model); out > 0 {
		return out
	}
	return defaultMaxTokens
}

// completionOpts builds CompletionOptions from the Client's current configuration.
func (l *Client) completionOpts(msgs []core.Message, tools []ToolSchema, sysPrompt string) CompletionOptions {
	return CompletionOptions{
		Model:          l.model,
		Messages:       msgs,
		MaxTokens:      l.effectiveMaxTokens(),
		Tools:          tools,
		SystemPrompt:   sysPrompt,
		ThinkingEffort: l.ThinkingEffort(),
	}
}

// toProviderMessages converts core messages for provider consumption, keeping
// only the fields a provider needs. A tool result is a RoleUser message with a
// non-nil ToolResult; user-typed text is a RoleUser message without one.
func toProviderMessages(msgs []core.Message) []core.Message {
	out := make([]core.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case core.RoleUser:
			if m.ToolResult != nil {
				out = append(out, core.Message{
					Role:       core.RoleUser,
					ToolResult: m.ToolResult,
				})
			} else {
				out = append(out, core.Message{
					Role:    core.RoleUser,
					Content: m.Content,
					Images:  m.Images,
				})
			}
		case core.RoleAssistant:
			out = append(out, core.Message{
				Role:              core.RoleAssistant,
				Content:           m.Content,
				Thinking:          m.Thinking,
				ThinkingSignature: m.ThinkingSignature,
				Reasoning:         m.Reasoning,
				ToolCalls:         m.ToolCalls,
			})
		}
	}
	return out
}
