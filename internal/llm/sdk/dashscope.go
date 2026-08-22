package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Model Studio serves hundreds of models and publishes no window for any of
// them in its listing — asking for all of them up front would be hundreds of
// round trips before the picker could draw. It does answer per model, so the
// window is fetched when one is actually chosen, which is what San's
// llm.ModelLimitsFetcher exists for.

// modelDetailTimeout bounds one model-detail lookup. It sits on the path that
// resolves a context window, so a slow answer must not stall a turn.
const modelDetailTimeout = 8 * time.Second

// FetchModelLimits reports one model's token limits, for the endpoints that
// answer per model rather than in their listing.
//
// It reports an error for every other vendor, which is the honest answer:
// San's resolver only reaches for it when the listing already came back
// without a window, and a vendor with nothing further to ask has nothing
// further to say.
func (p *Provider) FetchModelLimits(ctx context.Context, modelID string) (inputLimit, outputLimit int, err error) {
	if p.vendor.ID != alibabaVendor {
		return 0, 0, fmt.Errorf("sdk: %s publishes no per-model limits", p.vendor.ID)
	}
	return dashscopeModelLimits(ctx, p, modelID)
}

// alibabaVendor is the one endpoint here with a per-model detail lookup.
const alibabaVendor = "alibaba"

// dashscopeModelLimits reads extra_info.default_envs off a Model Studio model.
func dashscopeModelLimits(ctx context.Context, p *Provider, modelID string) (int, int, error) {
	cfg := p.endpoint.ConfigFor(p.model(modelID))

	ctx, cancel := context.WithTimeout(ctx, modelDetailTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL()+"/models/"+modelID, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	for name, value := range cfg.MergedHeaders() {
		req.Header.Set(name, value)
	}

	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, 0, err
	}
	if res.StatusCode >= 400 {
		return 0, 0, fmt.Errorf("sdk: model detail for %s: http %d", modelID, res.StatusCode)
	}

	var detail struct {
		ExtraInfo struct {
			DefaultEnvs struct {
				MaxInputTokens  int `json:"max_input_tokens"`
				MaxOutputTokens int `json:"max_output_tokens"`
			} `json:"default_envs"`
		} `json:"extra_info"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return 0, 0, err
	}
	return detail.ExtraInfo.DefaultEnvs.MaxInputTokens, detail.ExtraInfo.DefaultEnvs.MaxOutputTokens, nil
}
