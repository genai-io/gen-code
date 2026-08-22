// Reaching a vendor, through github.com/genai-io/sdk-go.
//
// It is one adapter rather than one package per vendor. Every vendor San talks
// to is a row in the SDK's catalog, and the wire work — four protocols, their
// streaming shapes, their reasoning dialects — belongs to the SDK's drivers.
// What is here is the seam: San's Provider on one side, the SDK's ai.Client on
// the other, and the translation between them.
//
// The vendor_ files divide as follows, one subject each:
//
//	vendor.go              the seam: one Provider backed by one configured endpoint
//	vendor_table.go        which San provider is which catalog vendor, and how to reach it
//	vendor_convert.go      San's conversation types and the SDK's, in both directions
//	vendor_errors.go       the SDK's typed failures, tagged for the agent loop
//	vendor_signin.go       the vendors that authenticate a person, not a service
//	vendor_credentials.go  those credentials, kept in San's own secret store
//	vendor_models.go       the two endpoints that answer about their models their own way
package llm

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
)

// vendorProvider is one configured vendor endpoint, presented as a Provider.
type vendorProvider struct {
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

// newVendorProvider builds the adapter for one vendor endpoint.
func newVendorProvider(name string, vendor catalog.Vendor, cfg sdkprovider.Config) *vendorProvider {
	return &vendorProvider{
		name:        name,
		vendor:      vendor,
		endpoint:    vendor.Provider(cfg),
		turnHeaders: turnHeadersFor(vendor.ID),
		clients:     make(map[string]*ai.Client),
	}
}

// Name returns San's provider identity, e.g. "anthropic:api_key".
func (p *vendorProvider) Name() string { return p.name }

// ---------------------------------------------------------------------------
// Inference
// ---------------------------------------------------------------------------

// Stream runs one turn and forwards it as San's chunks. Text and thinking
// arrive as they are generated; tool calls ride complete in the final
// response, which is the shape San's agent loop consumes.
func (p *vendorProvider) Stream(ctx context.Context, opts CompletionOptions) <-chan StreamChunk {
	ch := make(chan StreamChunk)

	go func() {
		defer close(ch)

		send := func(chunk StreamChunk) bool {
			select {
			case ch <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		client, err := p.client(opts.Model, p.headersFor(opts.Messages))
		if err != nil {
			send(StreamChunk{Type: ChunkTypeError, Error: classifyVendorError(err)})
			return
		}

		messages := toMessages(opts.Messages, client.Model())
		for event, err := range client.Stream(ctx, messages, requestOptions(opts)...) {
			if err != nil {
				send(StreamChunk{Type: ChunkTypeError, Error: classifyVendorError(err)})
				return
			}
			switch event.Type {
			case ai.EventBlockDelta:
				switch event.Block.Type {
				case ai.BlockText:
					if event.Block.Text != "" && !send(StreamChunk{Type: ChunkTypeText, Text: event.Block.Text}) {
						return
					}
				case ai.BlockThinking:
					if event.Block.Text != "" && !send(StreamChunk{Type: ChunkTypeThinking, Text: event.Block.Text}) {
						return
					}
				}
			case ai.EventDone:
				send(StreamChunk{Type: ChunkTypeDone, Response: toResponse(event.Response)})
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
func requestOptions(opts CompletionOptions) []ai.Option {
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
func (p *vendorProvider) headersFor(msgs []core.Message) map[string]string {
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
func (p *vendorProvider) client(modelID string, headers map[string]string) (*ai.Client, error) {
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
func (p *vendorProvider) model(modelID string) ai.Model {
	m := p.vendor.Model(modelID)
	for _, live := range p.endpoint.Models() {
		if strings.EqualFold(live.ID, modelID) {
			return sdkprovider.MergeListing(m, live)
		}
	}
	return m
}

// ListModels resolves each entry the same way, so a model's window in the
// picker is the one inference will use.

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// ListModels returns the models this endpoint serves.
//
// The endpoint's own listing is fetched once and kept; a vendor with a catalog
// answers from it when the fetch fails, and one without — an aggregator, a
// local Ollama, a user's own endpoint — reports the failure, because there is
// nothing else to show and a silent empty list reads as a working connection.
//
// Every entry is resolved through the vendor rather than taken from the
// listing as it arrived. Most endpoints publish an ID and nothing else, and a
// vendor knows how to size a model its own listing says nothing about — from
// its table, or from the ID itself. Reading the listing raw reports no window
// for those, which switches off the context percentage and auto-compaction
// without saying why.
func (p *vendorProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if err := p.refresh(ctx); err != nil && len(p.vendor.Models) == 0 {
		return nil, err
	}

	listed := ai.Available(p.endpoint.Models())
	out := make([]ModelInfo, 0, len(listed))
	for _, m := range listed {
		out = append(out, toModelInfo(sdkprovider.MergeListing(p.vendor.Model(m.ID), m)))
	}
	return out, nil
}

// refresh fetches the endpoint's live listing once. A failure is reported but
// not remembered, so the next call tries again rather than sticking with a
// catalog that a passing outage froze.
func (p *vendorProvider) refresh(ctx context.Context) error {
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
func (p *vendorProvider) ThinkingEfforts(model string) []string {
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
func (p *vendorProvider) DefaultThinkingEffort(model string) string {
	if level, ok := p.model(model).DefaultLevel(); ok {
		return string(level.Effort)
	}
	return ""
}

// SupportsImages reports whether the model accepts image input.
func (p *vendorProvider) SupportsImages(model string) bool {
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
func (p *vendorProvider) CachesToolsAndSystemPrompt() bool {
	switch p.vendor.API {
	case ai.APIAnthropicMessages, ai.APIAnthropicVertex:
		return true
	default:
		return false
	}
}

var (
	_ Provider                  = (*vendorProvider)(nil)
	_ ThinkingEffortProvider    = (*vendorProvider)(nil)
	_ ImageSupportProvider      = (*vendorProvider)(nil)
	_ PromptPrefixCacheProvider = (*vendorProvider)(nil)
	_ ModelLimitsFetcher        = (*vendorProvider)(nil)
)
