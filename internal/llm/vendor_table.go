package llm

import (
	"context"
	"fmt"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"

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
type vendorEntry struct {
	meta     Meta
	vendorID string

	// configure fills in what the catalog cannot: a credential from San's
	// secret store, an endpoint the user set, a deployment. Nil means the
	// common case — the vendor's first key variable and its default host.
	configure func(catalog.Vendor, *sdkprovider.Config) error
}

// displays are the provider-level UI rows, one per San provider.
var vendorDisplays = map[ProviderID]ProviderDisplay{
	Anthropic:      {Name: "Anthropic", Order: 10},
	OpenAI:         {Name: "OpenAI", Order: 20},
	Copilot:        {Name: "GitHub Copilot", Order: 25},
	Google:         {Name: "Google", Order: 30},
	DeepSeek:       {Name: "DeepSeek", Order: 40},
	SenseNova:      {Name: "SenseNova", Order: 50},
	MinMax:         {Name: "MiniMax", Order: 60},
	Moonshot:       {Name: "Moonshot", Order: 70},
	Alibaba:        {Name: "Alibaba", Order: 80},
	BigModel:       {Name: "Z.ai (GLM series)", Order: 90},
	Ollama:         {Name: "Ollama (Local)", Order: 100},
	Mimo:           {Name: "Xiaomi MiMo", Order: 110},
	Volcengine:     {Name: "Volcengine Ark", Order: 120},
	AgnesAI:        {Name: "Agnes-AI", Order: 130},
	CustomProvider: {Name: "Custom", Order: 140},
}

var vendorEntries = []vendorEntry{
	{
		meta:     Meta{Provider: Anthropic, AuthMethod: AuthAPIKey, EnvVars: []string{"ANTHROPIC_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "anthropic",
	},
	{
		meta:      Meta{Provider: Anthropic, AuthMethod: AuthVertex, EnvVars: []string{"CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID"}, DisplayName: "Vertex AI"},
		vendorID:  "anthropic-vertex",
		configure: configureVertex,
	},
	{
		meta:     Meta{Provider: OpenAI, AuthMethod: AuthAPIKey, EnvVars: []string{"OPENAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "openai",
	},
	{
		meta:      Meta{Provider: OpenAI, AuthMethod: AuthSubscription, DisplayName: "ChatGPT Subscription"},
		vendorID:  "openai-codex",
		configure: configureSignIn,
	},
	{
		meta:      Meta{Provider: Copilot, AuthMethod: AuthSubscription, DisplayName: "Copilot Subscription"},
		vendorID:  "copilot",
		configure: configureSignIn,
	},
	{
		meta:     Meta{Provider: Google, AuthMethod: AuthAPIKey, EnvVars: []string{"GOOGLE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "google",
	},
	{
		meta:     Meta{Provider: DeepSeek, AuthMethod: AuthAPIKey, EnvVars: []string{"DEEPSEEK_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "deepseek",
	},
	{
		meta:     Meta{Provider: SenseNova, AuthMethod: AuthAPIKey, EnvVars: []string{"SENSENOVA_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID: "sensenova",
	},
	{
		meta:     Meta{Provider: MinMax, AuthMethod: AuthAPIKey, EnvVars: []string{"MINIMAX_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "minmax",
	},
	{
		meta:     Meta{Provider: Moonshot, AuthMethod: AuthAPIKey, EnvVars: []string{"MOONSHOT_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "moonshot",
	},
	{
		meta:     Meta{Provider: Alibaba, AuthMethod: AuthAPIKey, EnvVars: []string{"DASHSCOPE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "alibaba",
	},
	{
		meta:     Meta{Provider: BigModel, AuthMethod: AuthAPIKey, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "bigmodel",
	},
	{
		meta:      Meta{Provider: BigModel, AuthMethod: AuthCoding, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Coding Plan"},
		vendorID:  "bigmodel",
		configure: configureBigModelCoding,
	},
	{
		meta:     Meta{Provider: Ollama, AuthMethod: AuthAPIKey, EnvVars: []string{"OLLAMA_BASE_URL"}, DisplayName: "Local (Ollama)"},
		vendorID: "ollama",
	},
	{
		meta:     Meta{Provider: Mimo, AuthMethod: AuthAPIKey, EnvVars: []string{"MIMO_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "mimo",
	},
	{
		meta:      Meta{Provider: Volcengine, AuthMethod: AuthAPIKey, EnvVars: []string{"VOLCENGINE_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID:  "volcengine",
		configure: configureVolcengine,
	},
	{
		meta:     Meta{Provider: AgnesAI, AuthMethod: AuthAPIKey, EnvVars: []string{"AGNESAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "agnesai",
	},
	{
		meta:      Meta{Provider: CustomProvider, AuthMethod: AuthAPIKey, EnvVars: []string{CustomAPIKeyEnvVar}, DisplayName: "Direct API"},
		configure: configureCustom,
	},
}

// init makes every vendor in the table reachable through the registry above.
// Importing this package is enough; there is nothing to switch on.
func init() { registerVendors() }

func registerVendors() {
	for name, display := range vendorDisplays {
		RegisterProviderDisplay(name, display)
	}
	for _, e := range vendorEntries {
		Register(e.meta, e.factory())
		if e.vendorID != "" {
			RegisterCostEstimator(e.meta.Provider, costEstimator(e.vendorID))
		}
	}
	registerAuthenticators()
}

// factory returns the constructor San's registry calls to open this endpoint.
func (e vendorEntry) factory() Factory {
	return func(context.Context) (Provider, error) {
		vendor, err := e.resolveVendor()
		if err != nil {
			return nil, err
		}

		cfg := sdkprovider.Config{APIKey: vendorAPIKey(vendor), BaseURL: vendor.ResolveBaseURL(secret.Resolve(vendor.BaseURLEnv))}
		if e.configure != nil {
			if err := e.configure(vendor, &cfg); err != nil {
				return nil, err
			}
		}
		return newVendorProvider(providerName(e.meta), vendor, cfg), nil
	}
}

// resolveVendor returns the catalog row this entry serves. An entry with no
// vendor ID builds its own, which is how the user-defined endpoint — a host
// that exists in no catalog — reaches the same code path as every other.
func (e vendorEntry) resolveVendor() (catalog.Vendor, error) {
	if e.vendorID == "" {
		return customVendor()
	}
	vendor, ok := catalog.Find(e.vendorID)
	if !ok {
		return catalog.Vendor{}, fmt.Errorf("llm: no catalog vendor %q", e.vendorID)
	}
	return vendor, nil
}

// providerName is San's provider identity for a registration, "vendor:auth".
func providerName(meta Meta) string {
	return string(meta.Provider) + ":" + string(meta.AuthMethod)
}

// vendorAPIKey reads the vendor's credential from San's secret store, which resolves
// the environment first and its own file second.
func vendorAPIKey(vendor catalog.Vendor) string {
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
		return fmt.Errorf("llm: set ANTHROPIC_VERTEX_PROJECT_ID to the Google Cloud project serving the model")
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

// customVendor builds a catalog row for the OpenAI-compatible endpoint the
// user configured in the app. It exists in no catalog, so San supplies what a
// vendor entry would have said: the protocol, the host, and nothing else.
func customVendor() (catalog.Vendor, error) {
	store, err := NewStore()
	if err != nil {
		return catalog.Vendor{}, fmt.Errorf("llm: loading the provider store: %w", err)
	}
	cfg := store.CustomProvider()
	if cfg == nil || cfg.BaseURL == "" {
		return catalog.Vendor{}, fmt.Errorf("custom provider not configured: set a base URL under /models > Providers > Custom")
	}
	return catalog.Vendor{
		ID:          string(CustomProvider),
		DisplayName: "Custom",
		API:         ai.APIOpenAIChat,
		BaseURL:     cfg.BaseURL,
		KeyEnv:      []string{CustomAPIKeyEnvVar},
		Input:       []ai.Modality{ai.ModalityText, ai.ModalityImage},
		Compat:      ai.OpenAIChatCompat{},
	}, nil
}

// configureCustom is a no-op beyond what the vendor row already carries: the
// host came from the store and the key from the secret store.
func configureCustom(catalog.Vendor, *sdkprovider.Config) error { return nil }

// costEstimator prices a turn from the vendor's published rate card. A model
// with no card reports unknown, which San renders as "--" rather than as free.
func costEstimator(vendorID string) CostEstimator {
	return func(modelID string, usage Usage) (Money, bool) {
		vendor, ok := catalog.Find(vendorID)
		if !ok {
			return Money{}, false
		}
		pricing := vendor.Model(modelID).Pricing
		if !pricing.Known() {
			return Money{}, false
		}
		cost := pricing.Cost(usage)
		return Money{Amount: cost.Total, Currency: Currency(cost.Currency)}, true
	}
}
