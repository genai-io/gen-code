// Package sdk serves San's LLM providers through github.com/genai-io/sdk-go.
//
// It is one adapter rather than fifteen provider packages. Every vendor San
// talks to is a row in the SDK's catalog, and the wire work — four protocols,
// their streaming shapes, their reasoning dialects — belongs to the SDK's
// drivers. What is left here is the seam: San's llm.Provider on one side, the
// SDK's ai.Client on the other, and the translation between them.
//
// # Where things live
//
//	provider.go   the seam: one llm.Provider backed by one configured endpoint
//	convert.go    San's conversation types and the SDK's, in both directions
//	errors.go     the SDK's typed failures, tagged for San's agent loop
//	vendors.go    which San provider is which catalog vendor, and how to reach it
//	auth.go       interactive sign-in, kept in San's own credential store
//	codex.go      the one endpoint that publishes its models its own way
package sdk

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
)

// Provider is one configured vendor endpoint, presented as an llm.Provider.
type Provider struct {
	// name is San's provider identity, "vendor:auth_method" — the form every
	// existing provider reports and the store keys connections by.
	name   string
	vendor catalog.Vendor
	// endpoint carries the credential, the host and the live model listing.
	endpoint *sdkprovider.Provider
	// turnHeaders are the headers whose value depends on what a turn sends,
	// for the one endpoint that has any. Nil everywhere else.
	turnHeaders func([]core.Message) map[string]string

	mu      sync.Mutex
	listed  bool                  // the endpoint's listing has been fetched
	clients map[string]*ai.Client // one client per model and header set
}

// newProvider builds the adapter for one vendor endpoint.
func newProvider(name string, vendor catalog.Vendor, cfg sdkprovider.Config) *Provider {
	return &Provider{
		name:        name,
		vendor:      vendor,
		endpoint:    vendor.Provider(cfg),
		turnHeaders: turnHeadersFor(vendor.ID),
		clients:     make(map[string]*ai.Client),
	}
}

// Name returns San's provider identity, e.g. "anthropic:api_key".
func (p *Provider) Name() string { return p.name }

// ---------------------------------------------------------------------------
// Inference
// ---------------------------------------------------------------------------

// Stream runs one turn and forwards it as San's chunks. Text and thinking
// arrive as they are generated; tool calls ride complete in the final
// response, which is the shape San's agent loop consumes.
func (p *Provider) Stream(ctx context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)

	go func() {
		defer close(ch)

		send := func(chunk llm.StreamChunk) bool {
			select {
			case ch <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		client, err := p.client(opts.Model, p.headersFor(opts.Messages))
		if err != nil {
			send(llm.StreamChunk{Type: llm.ChunkTypeError, Error: classify(err)})
			return
		}

		messages := toMessages(opts.Messages, client.Model())
		for event, err := range client.Stream(ctx, messages, requestOptions(opts)...) {
			if err != nil {
				send(llm.StreamChunk{Type: llm.ChunkTypeError, Error: classify(err)})
				return
			}
			switch event.Type {
			case ai.EventBlockDelta:
				switch event.Block.Type {
				case ai.BlockText:
					if event.Block.Text != "" && !send(llm.StreamChunk{Type: llm.ChunkTypeText, Text: event.Block.Text}) {
						return
					}
				case ai.BlockThinking:
					if event.Block.Text != "" && !send(llm.StreamChunk{Type: llm.ChunkTypeThinking, Text: event.Block.Text}) {
						return
					}
				}
			case ai.EventDone:
				send(llm.StreamChunk{Type: llm.ChunkTypeDone, Response: toResponse(event.Response)})
				return
			}
		}
	}()

	return ch
}

// requestOptions turns San's completion options into the SDK's.
//
// An option is only passed when San actually set it: passing one is what marks
// a setting explicit, so sending a zero would override the model's own default
// rather than inherit it.
func requestOptions(opts llm.CompletionOptions) []ai.Option {
	out := []ai.Option{ai.WithSystem(opts.SystemPrompt)}
	if tools := toTools(opts.Tools); len(tools) > 0 {
		out = append(out, ai.WithTools(tools...))
	}
	if opts.MaxTokens > 0 {
		out = append(out, ai.WithMaxTokens(opts.MaxTokens))
	}
	if opts.Temperature > 0 {
		out = append(out, ai.WithTemperature(opts.Temperature))
	}
	// An empty effort is ai.EffortDefault, which is the model's own default
	// rung — the same thing San means by leaving it unset.
	out = append(out, ai.WithEffort(ai.Effort(opts.ThinkingEffort)))
	return out
}

// headersFor returns the headers this turn needs beyond the fixed ones.
func (p *Provider) headersFor(msgs []core.Message) map[string]string {
	if p.turnHeaders == nil {
		return nil
	}
	return p.turnHeaders(msgs)
}

// client returns the client for a model and header set, building it once.
//
// A client is a request template, not a connection — the transport underneath
// is shared — so keying the cache by the headers too costs an entry per
// distinct header set rather than a new connection pool.
func (p *Provider) client(modelID string, headers map[string]string) (*ai.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := clientKey(modelID, headers)
	if client, ok := p.clients[key]; ok {
		return client, nil
	}

	cfg := p.endpoint.ConfigFor(p.model(modelID))
	if len(headers) > 0 {
		if cfg.Headers == nil {
			cfg.Headers = make(map[string]string, len(headers))
		}
		maps.Copy(cfg.Headers, headers)
	}
	client, err := ai.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	p.clients[key] = client
	return client, nil
}

// clientKey identifies one cached client: the model, plus the turn-dependent
// headers it was built with.
func clientKey(modelID string, headers map[string]string) string {
	if len(headers) == 0 {
		return modelID
	}
	names := slices.Sorted(maps.Keys(headers))
	var sb strings.Builder
	sb.WriteString(modelID)
	for _, name := range names {
		sb.WriteString("\x00")
		sb.WriteString(name)
		sb.WriteString("=")
		sb.WriteString(headers[name])
	}
	return sb.String()
}

// model resolves a model ID the way both inference and the picker must see it:
// the vendor's own entry first — which is what carries the protocol dialect, the
// reasoning ladder and the rate card — with anything the endpoint published
// layered over it.
//
// Resolving through the vendor rather than through the endpoint's list matters
// for an ID that is not in either: a model newer than the catalog still
// inherits its vendor's dialect instead of reaching the endpoint stripped of it.
func (p *Provider) model(modelID string) ai.Model {
	m := p.vendor.Model(modelID)
	for _, live := range p.endpoint.Models() {
		if strings.EqualFold(live.ID, modelID) {
			return sdkprovider.MergeListing(m, live)
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// ListModels returns the models this endpoint serves.
//
// The endpoint's own listing is fetched once and kept; a vendor with a catalog
// answers from it when the fetch fails, and one without — an aggregator, a
// local Ollama, a user's own endpoint — reports the failure, because there is
// nothing else to show and a silent empty list reads as a working connection.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if err := p.refresh(ctx); err != nil && len(p.vendor.Models) == 0 {
		return nil, err
	}

	models := ai.Available(p.endpoint.Models())
	out := make([]llm.ModelInfo, len(models))
	for i, m := range models {
		out[i] = toModelInfo(m)
	}
	return out, nil
}

// refresh fetches the endpoint's live listing once. A failure is reported but
// not remembered, so the next call tries again rather than sticking with a
// catalog that a passing outage froze.
func (p *Provider) refresh(ctx context.Context) error {
	p.mu.Lock()
	done := p.listed
	p.mu.Unlock()
	if done {
		return nil
	}

	if err := p.endpoint.Refresh(ctx); err != nil {
		return err
	}

	p.mu.Lock()
	p.listed = true
	p.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// Capabilities San asks a provider about
// ---------------------------------------------------------------------------

// ThinkingEfforts returns the model's reasoning rungs, least to most.
func (p *Provider) ThinkingEfforts(model string) []string {
	efforts := p.model(model).Efforts()
	if len(efforts) == 0 {
		return nil
	}
	out := make([]string, len(efforts))
	for i, effort := range efforts {
		out[i] = string(effort)
	}
	return out
}

// DefaultThinkingEffort returns the rung used when none is chosen.
func (p *Provider) DefaultThinkingEffort(model string) string {
	if level, ok := p.model(model).DefaultLevel(); ok {
		return string(level.Effort)
	}
	return ""
}

// SupportsImages reports whether the model accepts image input.
func (p *Provider) SupportsImages(model string) bool {
	return p.model(model).Accepts(ai.ModalityImage)
}

// CachesToolsAndSystemPrompt reports whether this endpoint's cache tokens
// count exactly the tool definitions plus the system prompt.
//
// True on the Anthropic Messages protocol alone: its driver sets one cache
// breakpoint at the end of the system block, and Anthropic renders a request
// as tools → system → messages, so the cached prefix is those two and nothing
// else. Every other endpoint here caches automatically over a prefix it picks
// itself, which is an unknown span.
func (p *Provider) CachesToolsAndSystemPrompt() bool {
	switch p.vendor.API {
	case ai.APIAnthropicMessages, ai.APIAnthropicVertex:
		return true
	default:
		return false
	}
}

var (
	_ llm.Provider                  = (*Provider)(nil)
	_ llm.ThinkingEffortProvider    = (*Provider)(nil)
	_ llm.ImageSupportProvider      = (*Provider)(nil)
	_ llm.PromptPrefixCacheProvider = (*Provider)(nil)
)
