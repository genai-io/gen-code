package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"
)

// The ChatGPT subscription backend publishes its lineup at its own catalog
// endpoint rather than through the Responses protocol's model listing: the
// request carries the Codex client version, and the answer is keyed under
// "models" with a slug where the protocol would put an id. Asking it the
// protocol's own question gets a 401.
//
// It is a listing, not a protocol, which is why it lives here rather than in
// the SDK's driver — provider.Config.Fetch is the seam for exactly this.

const (
	// codexClientVersion is the Codex CLI release this client presents itself
	// as. The backend returns the lineup for the version it is told; bump it
	// if the list ever goes stale.
	codexClientVersion = "0.144.0"

	// codexListTimeout bounds the catalog request, so a slow endpoint delays
	// the model picker rather than wedging it.
	codexListTimeout = 8 * time.Second
)

// codexModels reads the model lineup this subscription is entitled to.
func codexModels(ctx context.Context, p *sdkprovider.Provider) ([]ai.Model, error) {
	cfg := p.ConfigFor(ai.Model{ID: "-", API: ai.APIOpenAIResponses})
	client := cfg.HTTPClient
	if client == nil {
		return nil, fmt.Errorf("sdk: the ChatGPT subscription endpoint needs a signed-in client")
	}

	ctx, cancel := context.WithTimeout(ctx, codexListTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL()+"/models?client_version="+codexClientVersion, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for name, value := range cfg.MergedHeaders() {
		req.Header.Set(name, value)
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, &ai.Error{
			Driver: "openai-responses", Kind: codexErrorKind(res.StatusCode), Status: res.StatusCode,
			Message: "the ChatGPT subscription catalog declined: " + string(body),
		}
	}

	var listing struct {
		Models []struct {
			Slug                     string `json:"slug"`
			DisplayName              string `json:"display_name"`
			ContextWindow            int    `json:"context_window"`
			ShowInPicker             *bool  `json:"show_in_picker"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			SupportedReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil, err
	}

	models := make([]ai.Model, 0, len(listing.Models))
	for _, m := range listing.Models {
		// A null show_in_picker means shown; only an explicit false hides one.
		if m.Slug == "" || (m.ShowInPicker != nil && !*m.ShowInPicker) {
			continue
		}
		model := ai.Model{
			ID:            m.Slug,
			Name:          m.DisplayName,
			API:           ai.APIOpenAIResponses,
			ContextWindow: m.ContextWindow,
		}
		for _, level := range m.SupportedReasoningLevels {
			if level.Effort == "" {
				continue
			}
			model.Reasoning = append(model.Reasoning, ai.ReasoningLevel{
				Effort:  ai.Effort(level.Effort),
				Value:   level.Effort,
				Default: level.Effort == m.DefaultReasoningLevel,
			})
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("sdk: the ChatGPT subscription catalog listed no models")
	}
	return models, nil
}

// codexErrorKind classifies the catalog endpoint's refusal. A rejected
// credential must surface: connecting verifies the account by listing models,
// so a signed-out one recorded as connected would fail on the first real turn.
func codexErrorKind(status int) ai.ErrorKind {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ai.KindAuth
	case status == http.StatusTooManyRequests:
		return ai.KindRateLimit
	case status >= 500:
		return ai.KindOverloaded
	default:
		return ai.KindInvalidRequest
	}
}
