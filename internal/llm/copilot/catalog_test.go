package copilot

import (
	"encoding/json"
	"testing"
)

// copilotCatalogJSON is a trimmed sample of the /models reply, covering each
// kind of entry the conversion has to make a decision about.
const copilotCatalogJSON = `{"data":[
  {"id":"gpt-5","name":"GPT-5","vendor":"OpenAI","model_picker_enabled":true,
   "capabilities":{"type":"chat","limits":{"max_context_window_tokens":272000,"max_output_tokens":128000},
   "supports":{"tool_calls":true,"vision":true}}},
  {"id":"gpt-5","name":"GPT-5 (duplicate version)","model_picker_enabled":true,
   "capabilities":{"type":"chat","limits":{"max_context_window_tokens":1},"supports":{"tool_calls":true}}},
  {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","vendor":"Anthropic","model_picker_enabled":true,
   "capabilities":{"type":"chat","limits":{"max_prompt_tokens":144000,"max_output_tokens":16000},
   "supports":{"tool_calls":true,"vision":true}}},
  {"id":"text-embedding-3-small","model_picker_enabled":true,
   "capabilities":{"type":"embeddings","supports":{"tool_calls":false}}},
  {"id":"gpt-4o-internal","model_picker_enabled":false,
   "capabilities":{"type":"chat","supports":{"tool_calls":true}}},
  {"id":"no-tools-model","model_picker_enabled":true,
   "capabilities":{"type":"chat","supports":{"tool_calls":false}}},
  {"id":"unaccepted-terms","model_picker_enabled":true,"policy":{"state":"unconfigured"},
   "capabilities":{"type":"chat","supports":{"tool_calls":true}}}
]}`

func parseCatalog(t *testing.T) modelsResponse {
	t.Helper()
	var resp modelsResponse
	if err := json.Unmarshal([]byte(copilotCatalogJSON), &resp); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}
	return resp
}

func TestCatalogKeepsOnlyRunnableChatModels(t *testing.T) {
	models := parseCatalog(t).toModelInfos()

	got := make([]string, len(models))
	for i, m := range models {
		got[i] = m.ID
	}
	// Sorted by id; embeddings, picker-hidden, tool-less, policy-blocked and
	// duplicate entries are all dropped.
	want := []string{"claude-sonnet-4.5", "gpt-5"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}

func TestCatalogCarriesTokenLimits(t *testing.T) {
	byID := make(map[string]int)
	outputByID := make(map[string]int)
	for _, m := range parseCatalog(t).toModelInfos() {
		byID[m.ID] = m.InputTokenLimit
		outputByID[m.ID] = m.OutputTokenLimit
	}

	if byID["gpt-5"] != 272000 {
		t.Errorf("gpt-5 input limit = %d, want 272000", byID["gpt-5"])
	}
	if outputByID["gpt-5"] != 128000 {
		t.Errorf("gpt-5 output limit = %d, want 128000", outputByID["gpt-5"])
	}
	// Entries that only report a prompt budget still get a usable input limit —
	// a zero one would break the context bar and auto-compact.
	if byID["claude-sonnet-4.5"] != 144000 {
		t.Errorf("claude-sonnet-4.5 input limit = %d, want the reported prompt budget 144000", byID["claude-sonnet-4.5"])
	}
}

func TestCatalogFallsBackToIDWhenUnnamed(t *testing.T) {
	resp := modelsResponse{Data: []catalogModel{{
		ID:                 "bare-model",
		ModelPickerEnabled: true,
		Capabilities:       modelCapabilities{Type: "chat", Supports: modelSupports{ToolCalls: true}},
	}}}

	models := resp.toModelInfos()
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].DisplayName != "bare-model" {
		t.Errorf("DisplayName = %q, want the id as fallback", models[0].DisplayName)
	}
}
