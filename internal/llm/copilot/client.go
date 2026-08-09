// Package copilot implements the Provider interface for GitHub Copilot.
//
// Copilot's chat endpoint speaks OpenAI Chat Completions, so the openai-go SDK
// drives it with a Copilot base URL plus the editor headers the backend gates
// on. What differs from a normal OpenAI-compatible provider is authentication:
// a GitHub subscription login rather than an API key, with a short-lived bearer
// re-minted from the stored GitHub token as it expires.
package copilot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/llm/copilot/oauth"
	"github.com/genai-io/san/internal/llm/openaicompat"
)

// modelsFetchTimeout bounds the catalog request so a slow or blocked fetch
// doesn't wedge the connect flow.
const modelsFetchTimeout = 8 * time.Second

// Client implements the Provider interface for GitHub Copilot using the OpenAI SDK.
type Client struct {
	client openai.Client
	name   string
}

// NewClient creates a new Copilot client with the given OpenAI SDK client.
func NewClient(client openai.Client, name string) *Client {
	return &Client{client: client, name: name}
}

// Name returns the provider name.
func (c *Client) Name() string { return c.name }

// Stream sends a completion request and returns a channel of streaming chunks.
func (c *Client) Stream(ctx context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	return openaicompat.StreamChatCompletions(ctx, openaicompat.ChatStreamConfig{
		Client:           c.client,
		ProviderName:     c.name,
		Options:          opts,
		ConvertAssistant: openaicompat.DefaultAssistantMessage,
		ConfigureParams:  useLegacyMaxTokens,
		RequestOptions:   perRequestOptions(opts.Messages),
	})
}

// useLegacyMaxTokens moves the output cap onto `max_tokens`, the field the
// editor plugins send. GitHub publishes no schema for this endpoint, and
// whether it also honours OpenAI's newer `max_completion_tokens` is unverified
// — an unread cap means unbounded replies, so send the name known to work. The
// two are mutually exclusive, so the new one has to go.
func useLegacyMaxTokens(params *openai.ChatCompletionNewParams) {
	if !params.MaxCompletionTokens.Valid() {
		return
	}
	params.MaxTokens = openai.Int(params.MaxCompletionTokens.Value)
	params.MaxCompletionTokens = param.Opt[int64]{}
}

// perRequestOptions builds the Copilot headers that depend on what this turn
// actually sends. The rest of the editor identity is fixed on the client.
func perRequestOptions(messages []core.Message) []option.RequestOption {
	// Copilot meters an agent's follow-up turns differently from a turn the
	// user typed, and reads that from X-Initiator. San infers it from the
	// history — anything past the opening user message means the loop is
	// driving — which is an approximation: a resumed session's first user turn
	// still reads as "agent".
	initiator := "user"
	vision := false
	for _, msg := range messages {
		if msg.Role == core.RoleAssistant || msg.ToolResult != nil {
			initiator = "agent"
		}
		if len(msg.Images) > 0 {
			vision = true
		}
	}

	opts := []option.RequestOption{option.WithHeader("X-Initiator", initiator)}
	if vision {
		// Image parts are rejected unless the request opts into vision.
		opts = append(opts, option.WithHeader("Copilot-Vision-Request", "true"))
	}
	return opts
}

// ListModels returns the models this Copilot account is entitled to.
//
// Errors are propagated rather than masked with a fallback list: connect
// verifies the subscription by listing models, and Copilot's lineup changes
// often enough that a hardcoded catalog would mostly be wrong. A signed-out or
// unentitled account must fail here instead of being recorded as connected and
// failing on the first real request.
func (c *Client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	var resp modelsResponse
	err := c.client.Get(ctx, "models", nil, &resp, option.WithRequestTimeout(modelsFetchTimeout))
	if err != nil {
		// Credential-source failures (raised by the auth middleware before the
		// request is sent) and a 401/403 from the endpoint both mean the same
		// thing here: sign in again. Subscription auth has no API key to check,
		// so don't route either through the API-key-oriented normalizer.
		var credErr *oauth.CredentialError
		if errors.As(err, &credErr) || openaicompat.IsAuthError(err) {
			return nil, fmt.Errorf("GitHub Copilot sign-in required: %w", err)
		}
		return nil, openaicompat.NormalizeAPIError(c.name, err)
	}

	models := resp.toModelInfos()
	if len(models) == 0 {
		return nil, errors.New("this GitHub account has no Copilot chat models available")
	}
	return models, nil
}

// Ensure Client implements Provider
var _ llm.Provider = (*Client)(nil)
