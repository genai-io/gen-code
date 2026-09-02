package llm

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"errors"
	"sync"

	"github.com/genai-io/san/internal/core"
)

// A Provider plus a model, as the agent loop sees it. Everything above talks
// about "the LLM"; everything below talks about a vendor.

const (
	// defaultMaxTokens applies when neither the caller nor the provider caps
	// the output.
	defaultMaxTokens = 8192
	// completeMaxAttempts bounds in-place retries for utility completions.
	completeMaxAttempts = 3
)

// Client is the only implementation of core.LLM, and what the loop and app
// layers stream and complete through. SetThinkingEffort may be called while the
// agent is running; it takes effect on the next call.
type Client struct {
	// provider, model and maxTokens are fixed at construction and read without
	// the lock. Only thinkingEffort changes over a client's life, which is what
	// mu guards.
	provider  Provider
	model     string
	maxTokens int

	mu             sync.RWMutex
	thinkingEffort string

	limits modelLimits
}

// modelLimits memoizes what the provider says a model takes and produces. The
// lookup is a live listing, and InputLimit sits inside the agent's step loop,
// so what it memoizes matters: a provider that could not answer is transient
// and asked again, but one that answered "I don't know" is settled — re-asking
// re-fetches an entire catalog to be told the same thing.
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

// resolveModelLimits asks the provider, falling back to its per-model endpoint
// for the vendors whose listing says nothing (see ModelLimitsFetcher).
//
// answered reports whether the provider replied, not whether it knew: a 0
// alongside answered=true is the provider saying it publishes no figure.
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

	// Either the listing stated everything, or it is all there is to ask.
	fetcher, ok := p.(ModelLimitsFetcher)
	if (in > 0 && out > 0) || !ok {
		return in, out, true
	}
	fetchedIn, fetchedOut, err := fetcher.FetchModelLimits(context.TODO(), model)
	if err != nil {
		return in, out, false // incomplete for a reason that may pass
	}
	return max(in, fetchedIn), max(out, fetchedOut), true
}

// NewClient fixes a model on a provider. maxTokens=0 means resolve the cap
// from the model's own metadata, falling back to defaultMaxTokens.
func NewClient(p Provider, model string, maxTokens int) *Client {
	return &Client{provider: p, model: model, maxTokens: maxTokens}
}

// TurnClient hands over the SDK client that answers one turn. Streaming is the
// SDK's job; what this package owns is reaching the endpoint — credentials,
// headers, which model — and choosing one. The messages are a parameter
// because the headers can depend on them: see TurnHeaders.
func (l *Client) TurnClient(msgs []core.Message) (*ai.Client, error) {
	l.mu.RLock()
	p, model := l.provider, l.model
	l.mu.RUnlock()
	if p == nil {
		return nil, errors.New("llm: no provider")
	}
	return p.Client(model, TurnHeaders(p, msgs))
}

// CallOptions are the settings this client carries into every inference: the
// output cap, and the reasoning rung a person may change mid-session.
func (l *Client) CallOptions() []ai.Option {
	return callOptions(l.effectiveMaxTokens(), l.ThinkingEffort(), 0)
}

func (l *Client) SetThinkingEffort(effort string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.thinkingEffort = effort
}

func (l *Client) ThinkingEffort() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.thinkingEffort
}

// Complete sends a one-shot completion, for utility calls like compaction.
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
		err = core.ClassifyStream(err)
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

func (l *Client) Name() string {
	if l.provider == nil {
		return ""
	}
	return l.provider.Name()
}

func (l *Client) ModelID() string { return l.model }

// InputLimit returns the model's context window, or 0 when it cannot be
// determined — callers treat 0 as "unknown" and skip the size check rather
// than acting on a guess (see InputLimitEnvVar).
//
// It goes through EffectiveInputLimit rather than an injected value, so every
// client resolves the window the same way the status bar does.
func (l *Client) InputLimit() int {
	p, model := l.provider, l.model

	// Split rather than cast whole: a provider names itself
	// "vendor:auth_method" while the store keys connections by the bare vendor,
	// so the composite string misses every lookup and falls through to the
	// cross-provider scan this call exists to avoid. Both store methods are
	// nil-receiver safe, and EffectiveInputLimit runs unconditionally so its
	// env override is honored even before a store exists.
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
