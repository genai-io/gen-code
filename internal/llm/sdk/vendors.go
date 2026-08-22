package sdk

import (
	"context"
	"fmt"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/secret"

	// The four wire protocols San's vendors speak.
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/anthropic"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/anthropic/vertex"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/google"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/openai/chat"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/openai/responses"
)

// Which San provider is which catalog vendor.
//
// San names a connection by provider *and* auth method — "anthropic:vertex" is
// the same models reached a different way — while the SDK's catalog gives each
// way of reaching an endpoint its own vendor row. The table below is that
// mapping, and it is all there is to adding a vendor: no package, no client, no
// conversion code.

// entry is one San provider/auth-method pair served by one catalog vendor.
type entry struct {
	meta     llm.Meta
	vendorID string

	// configure fills in what the catalog cannot: a credential from San's
	// secret store, an endpoint the user set, a deployment. Nil means the
	// common case — the vendor's first key variable and its default host.
	configure func(catalog.Vendor, *sdkprovider.Config) error
}

// displays are the provider-level UI rows, one per San provider.
var displays = map[llm.Name]llm.ProviderDisplay{
	llm.Anthropic:      {Name: "Anthropic", Order: 10},
	llm.OpenAI:         {Name: "OpenAI", Order: 20},
	llm.Copilot:        {Name: "GitHub Copilot", Order: 25},
	llm.Google:         {Name: "Google", Order: 30},
	llm.DeepSeek:       {Name: "DeepSeek", Order: 40},
	llm.SenseNova:      {Name: "SenseNova", Order: 50},
	llm.MinMax:         {Name: "MiniMax", Order: 60},
	llm.Moonshot:       {Name: "Moonshot", Order: 70},
	llm.Alibaba:        {Name: "Alibaba", Order: 80},
	llm.BigModel:       {Name: "Z.ai (GLM series)", Order: 90},
	llm.Ollama:         {Name: "Ollama (Local)", Order: 100},
	llm.Mimo:           {Name: "Xiaomi MiMo", Order: 110},
	llm.Volcengine:     {Name: "Volcengine Ark", Order: 120},
	llm.AgnesAI:        {Name: "Agnes-AI", Order: 130},
	llm.CustomProvider: {Name: "Custom", Order: 140},
}

var entries = []entry{
	{
		meta:     llm.Meta{Provider: llm.Anthropic, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"ANTHROPIC_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "anthropic",
	},
	{
		meta:      llm.Meta{Provider: llm.Anthropic, AuthMethod: llm.AuthVertex, EnvVars: []string{"CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID"}, DisplayName: "Vertex AI"},
		vendorID:  "anthropic-vertex",
		configure: configureVertex,
	},
	{
		meta:     llm.Meta{Provider: llm.OpenAI, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"OPENAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "openai",
	},
	{
		meta:      llm.Meta{Provider: llm.OpenAI, AuthMethod: llm.AuthSubscription, DisplayName: "ChatGPT Subscription"},
		vendorID:  "openai-codex",
		configure: configureSignIn,
	},
	{
		meta:      llm.Meta{Provider: llm.Copilot, AuthMethod: llm.AuthSubscription, DisplayName: "Copilot Subscription"},
		vendorID:  "copilot",
		configure: configureSignIn,
	},
	{
		meta:     llm.Meta{Provider: llm.Google, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"GOOGLE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "google",
	},
	{
		meta:     llm.Meta{Provider: llm.DeepSeek, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"DEEPSEEK_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "deepseek",
	},
	{
		meta:     llm.Meta{Provider: llm.SenseNova, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"SENSENOVA_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID: "sensenova",
	},
	{
		meta:     llm.Meta{Provider: llm.MinMax, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"MINIMAX_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "minmax",
	},
	{
		meta:     llm.Meta{Provider: llm.Moonshot, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"MOONSHOT_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "moonshot",
	},
	{
		meta:     llm.Meta{Provider: llm.Alibaba, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"DASHSCOPE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "alibaba",
	},
	{
		meta:     llm.Meta{Provider: llm.BigModel, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "bigmodel",
	},
	{
		meta:      llm.Meta{Provider: llm.BigModel, AuthMethod: llm.AuthCoding, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Coding Plan"},
		vendorID:  "bigmodel",
		configure: configureBigModelCoding,
	},
	{
		meta:     llm.Meta{Provider: llm.Ollama, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"OLLAMA_BASE_URL"}, DisplayName: "Local (Ollama)"},
		vendorID: "ollama",
	},
	{
		meta:     llm.Meta{Provider: llm.Mimo, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"MIMO_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "mimo",
	},
	{
		meta:      llm.Meta{Provider: llm.Volcengine, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"VOLCENGINE_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID:  "volcengine",
		configure: configureVolcengine,
	},
	{
		meta:     llm.Meta{Provider: llm.AgnesAI, AuthMethod: llm.AuthAPIKey, EnvVars: []string{"AGNESAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "agnesai",
	},
	{
		meta:      llm.Meta{Provider: llm.CustomProvider, AuthMethod: llm.AuthAPIKey, EnvVars: []string{llm.CustomAPIKeyEnvVar}, DisplayName: "Direct API"},
		configure: configureCustom,
	},
}

// init registers every vendor in the table, so a blank import is what makes
// San's providers reachable:
//
//	_ "github.com/genai-io/san/internal/llm/sdk"
func init() { Register() }

// Register makes every vendor in the table reachable through San's provider
// registry. Exported for a test that needs the registry populated and would
// rather say so than blank-import a package for its side effect alone.
func Register() {
	for name, display := range displays {
		llm.RegisterProviderDisplay(name, display)
	}
	for _, e := range entries {
		llm.Register(e.meta, e.factory())
		if e.vendorID != "" {
			llm.RegisterCostEstimator(e.meta.Provider, costEstimator(e.vendorID))
		}
	}
	registerAuthenticators()
}

// factory returns the constructor San's registry calls to open this endpoint.
func (e entry) factory() llm.Factory {
	return func(context.Context) (llm.Provider, error) {
		vendor, err := e.resolveVendor()
		if err != nil {
			return nil, err
		}

		cfg := sdkprovider.Config{APIKey: key(vendor), BaseURL: vendor.ResolveBaseURL(secret.Resolve(vendor.BaseURLEnv))}
		if e.configure != nil {
			if err := e.configure(vendor, &cfg); err != nil {
				return nil, err
			}
		}
		return newProvider(providerName(e.meta), vendor, cfg), nil
	}
}

// resolveVendor returns the catalog row this entry serves. An entry with no
// vendor ID builds its own, which is how the user-defined endpoint — a host
// that exists in no catalog — reaches the same code path as every other.
func (e entry) resolveVendor() (catalog.Vendor, error) {
	if e.vendorID == "" {
		return customVendor()
	}
	vendor, ok := catalog.Find(e.vendorID)
	if !ok {
		return catalog.Vendor{}, fmt.Errorf("sdk: no catalog vendor %q", e.vendorID)
	}
	return vendor, nil
}

// providerName is San's provider identity for a registration, "vendor:auth".
func providerName(meta llm.Meta) string {
	return string(meta.Provider) + ":" + string(meta.AuthMethod)
}

// key reads the vendor's credential from San's secret store, which resolves
// the environment first and its own file second.
func key(vendor catalog.Vendor) string {
	for _, name := range vendor.KeyEnv {
		if value := secret.Resolve(name); value != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The entries that need more than a key and a host
// ---------------------------------------------------------------------------

// configureVertex points the Anthropic Messages protocol at a Vertex AI
// deployment. There is no key: the driver authenticates with Google
// Application Default Credentials.
func configureVertex(vendor catalog.Vendor, cfg *sdkprovider.Config) error {
	project := secret.Resolve(vendor.DeploymentEnv["project"])
	if project == "" {
		return fmt.Errorf("sdk: set ANTHROPIC_VERTEX_PROJECT_ID to the Google Cloud project serving the model")
	}
	cfg.APIKey = ""
	cfg.ProtocolConfig = ai.VertexConfig{
		Project: project,
		Region:  secret.Resolve(vendor.DeploymentEnv["region"]),
	}
	return nil
}

// configureBigModelCoding points Z.ai's GLM models at the Coding Plan path,
// which is the same endpoint under a different prefix.
func configureBigModelCoding(_ catalog.Vendor, cfg *sdkprovider.Config) error {
	cfg.BaseURL = secret.Resolve("BIGMODEL_CODING_BASE_URL")
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.bigmodel.cn/api/coding/paas/v4"
	}
	return nil
}

// VolcengineModelEnvVar names the Ark model this account is provisioned for.
//
// Ark serves models through per-account endpoints, so there is no catalog to
// list and its own listing answers for the account rather than for the
// product. Naming the one model is how an Ark user says what they have.
const VolcengineModelEnvVar = "VOLCENGINE_MODEL"

// configureVolcengine seeds the endpoint with the model this account names, so
// the picker has something to show even when Ark's listing says nothing.
func configureVolcengine(vendor catalog.Vendor, cfg *sdkprovider.Config) error {
	modelID := secret.Resolve(VolcengineModelEnvVar)
	if modelID == "" {
		return nil
	}
	// Through the vendor, so the window is read out of the model ID the way it
	// is for every other Ark model.
	cfg.Models = []ai.Model{vendor.Model(modelID)}
	return nil
}

// costEstimator prices a turn from the vendor's published rate card. A model
// with no card reports unknown, which San renders as "--" rather than as free.
func costEstimator(vendorID string) llm.CostEstimator {
	return func(modelID string, usage llm.Usage) (llm.Money, bool) {
		vendor, ok := catalog.Find(vendorID)
		if !ok {
			return llm.Money{}, false
		}
		pricing := vendor.Model(modelID).Pricing
		if !pricing.Known() {
			return llm.Money{}, false
		}
		cost := pricing.Cost(ai.Usage{
			Input:      usage.InputTokens,
			Output:     usage.OutputTokens,
			CacheWrite: usage.CacheCreationInputTokens,
			CacheRead:  usage.CacheReadInputTokens,
		})
		return llm.Money{Amount: cost.Total, Currency: llm.Currency(cost.Currency)}, true
	}
}
