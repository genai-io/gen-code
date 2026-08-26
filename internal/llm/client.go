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

// modelLimits memoizes what the provider says a model takes and produces, so
// the lookup runs once per client instead of once per inference step.
//
// The distinction that matters is between a provider that could not answer and
// one that answered "I don't know". The first is transient and must be asked
// again. The second is settled: the listing is what it is, and re-asking on
// every step re-fetches an entire endpoint catalog — hundreds of models, for
// Model Studio — to be told the same thing. InputLimit sits inside the agent's
// step loop, so that was a network round-trip per step of every turn for any
// model whose window nobody publishes.
//
// One resolution answers both figures, because one listing states both.
type modelLimits struct {
	mu sync.Mutex
	// answered records that the provider replied. in and out are then final
	// for this client's lifetime, zero included.
	answered bool
	in, out  int
}

func (c *modelLimits) input(p Provider, model string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolve(p, model)
	return c.in
}

func (c *modelLimits) output(p Provider, model string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolve(p, model)
	return c.out
}

// resolve asks the provider, once it has answered. Callers hold c.mu.
func (c *modelLimits) resolve(p Provider, model string) {
	if c.answered {
		return
	}
	c.in, c.out, c.answered = resolveModelLimits(p, model)
}

// resolveModelLimits asks the provider what the model takes and produces,
// falling back to its per-model endpoint for the vendors whose listing says
// nothing (see ModelLimitsFetcher).
//
// answered reports whether the provider replied at all — not whether it knew.
// Either figure may come back 0 alongside answered=true, which is the provider
// saying it publishes none; callers treat 0 as "unknown" and skip whatever
// they would have done with it rather than acting on a guess.
func resolveModelLimits(p Provider, model string) (in, out int, answered bool) {
	if p == nil {
		return 0, 0, false
	}
	models, err := p.ListModels(context.TODO())
	if err != nil {
		return 0, 0, false
	}
	for _, m := range models {
		if m.ID == model {
			in, out = m.InputTokenLimit, m.OutputTokenLimit
			break
		}
	}

	fetcher, ok := p.(ModelLimitsFetcher)
	if (in > 0 && out > 0) || !ok {
		// Either the listing stated everything, or it is all there is to ask.
		return in, out, true
	}
	fetchedIn, fetchedOut, err := fetcher.FetchModelLimits(context.TODO(), model)
	if err != nil {
		// The listing answered, but the endpoint that fills its gaps did not —
		// so the pair is incomplete for a reason that may pass.
		return in, out, false
	}
	return max(in, fetchedIn), max(out, fetchedOut), true
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
