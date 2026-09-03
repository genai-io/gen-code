package tool_test

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"

	// Register the built-in tools so schemas resolve from the live registry —
	// the same wiring the app uses. Without this blank import GetToolSchemas
	// would find no registered tools.
	_ "github.com/genai-io/san/internal/tool/registry"
)

func findSchema(schemas []core.ToolSchema, name string) (core.ToolSchema, bool) {
	for _, s := range schemas {
		if s.Name == name {
			return s, true
		}
	}
	return core.ToolSchema{}, false
}

func TestManageableToolsIncludeConditionalEvolve(t *testing.T) {
	if _, ok := findSchema(tool.GetToolSchemas(), tool.ToolEvolve); ok {
		t.Fatal("Evolve must remain conditional in the model-facing default schemas")
	}
	if _, ok := findSchema(tool.GetManageableToolSchemasWith(tool.SchemaOptions{}), tool.ToolEvolve); !ok {
		t.Fatal("Evolve is missing from the /tools-manageable schema set")
	}
}

// TestBuiltinToolsAllRegistered guards the invariant the self-describing
// refactor establishes: every name in the presentation order resolves to a
// registered tool that describes itself, so a tool can't silently vanish from
// the model's view because its schema and implementation drifted apart.
func TestBuiltinToolsAllRegistered(t *testing.T) {
	schemas := tool.GetToolSchemas()
	for _, name := range []string{
		tool.ToolRead, tool.ToolWebFetch, tool.ToolWebSearch,
		tool.ToolEdit, tool.ToolWrite, tool.ToolBash, tool.ToolAskUserQuestion,
		tool.ToolSkill, tool.ToolAgent, tool.ToolAgentStop, tool.ToolSendMessage,
		tool.ToolTaskCreate, tool.ToolTaskGet, tool.ToolTaskUpdate,
		tool.ToolCron,
	} {
		if _, ok := findSchema(schemas, name); !ok {
			t.Errorf("built-in tool %q is missing from GetToolSchemas output", name)
		}
	}
}

// TestBuiltinOrderCoversEveryRegisteredTool guards the reverse of
// TestBuiltinToolsAllRegistered: every registered tool must appear in the
// model-facing schema list. Without it, registering a tool but forgetting to
// add it to builtinToolOrder ships a tool that executes yet stays invisible to
// the model — the exact schema/implementation drift this refactor removes.
func TestBuiltinOrderCoversEveryRegisteredTool(t *testing.T) {
	presented := make(map[string]bool)
	for _, s := range tool.GetToolSchemas() {
		presented[s.Name] = true
	}

	// Tools deliberately kept out of builtinToolOrder. Evolve is injected
	// per-turn with capability-tailored parameters (SchemaOptions.ExtraTools),
	// never through the registry-sourced order. A new entry here must be a
	// conscious decision, not a forgotten wiring step.
	exempt := map[string]bool{
		tool.ToolEvolve: true,
	}

	for _, name := range tool.List() {
		// Resolve each registry entry back to its tool and compare on the
		// canonical Schema().Name, so deprecated aliases (e.g. AgentStop →
		// TaskStop) fold onto the tool the model actually sees.
		canonical := name
		if tl, ok := tool.Get(name); ok {
			canonical = tl.Schema().Name
		}
		if exempt[canonical] {
			continue
		}
		if !presented[canonical] {
			t.Errorf("registered tool %q (canonical %q) is absent from the model-facing schema list; add it to builtinToolOrder (or exempt it deliberately)", name, canonical)
		}
	}
}

func TestGetToolSchemasUsesDirectoryGetter(t *testing.T) {
	schemas := tool.GetToolSchemasWith(tool.SchemaOptions{
		AgentDirectory: func() string {
			return "Available agents for the Agent tool:\n\n- explorer: Read-only research"
		},
	})

	agent, ok := findSchema(schemas, tool.ToolAgent)
	if !ok {
		t.Fatal("Agent schema not found in GetToolSchemasWith output")
	}
	if !strings.Contains(agent.Description, "explorer: Read-only research") {
		t.Errorf("Agent schema description missing directory entry, got: %s", agent.Description)
	}
}

// TestAgentDirectoryReevaluatedPerCall verifies the AgentDirectory getter is
// invoked on each schema build, so toggling /agents produces an updated
// description on the next rebuild.
func TestAgentDirectoryReevaluatedPerCall(t *testing.T) {
	directory := "v1"
	getter := func() string { return directory }

	first, _ := findSchema(tool.GetToolSchemasWith(tool.SchemaOptions{AgentDirectory: getter}), tool.ToolAgent)
	directory = "v2"
	second, _ := findSchema(tool.GetToolSchemasWith(tool.SchemaOptions{AgentDirectory: getter}), tool.ToolAgent)

	if !strings.Contains(first.Description, "v1") {
		t.Error("first build should embed directory v1")
	}
	if !strings.Contains(second.Description, "v2") {
		t.Error("second build should embed directory v2 (getter must run each call)")
	}
	if strings.Contains(second.Description, "v1") {
		t.Error("second build should NOT contain stale v1 directory")
	}
}

func TestBuiltInToolSchemasAreOpenAICompatibleObjects(t *testing.T) {
	for _, schema := range tool.GetToolSchemas() {
		params, ok := schema.Definition.(map[string]any)
		if !ok {
			t.Fatalf("%s parameters must be a JSON schema object map", schema.Name)
		}
		if typ, _ := params["type"].(string); typ != "object" {
			t.Fatalf("%s parameters must declare top-level type object, got %v", schema.Name, params["type"])
		}
		for _, keyword := range []string{"oneOf", "anyOf", "allOf", "enum", "not"} {
			if _, exists := params[keyword]; exists {
				t.Fatalf("%s parameters must not use top-level %q", schema.Name, keyword)
			}
		}
	}
}

func TestAskUserQuestionSchemaRejectsEmptyQuestionsShape(t *testing.T) {
	schema, ok := findSchema(tool.GetToolSchemas(), tool.ToolAskUserQuestion)
	if !ok {
		t.Fatal("AskUserQuestion schema not found")
	}
	params := schema.Definition.(map[string]any)
	if got, ok := params["minProperties"].(int); !ok || got != 1 {
		t.Fatalf("AskUserQuestion must require at least one input property, got %#v", params["minProperties"])
	}

	properties := params["properties"].(map[string]any)
	questions := properties["questions"].(map[string]any)
	if got, ok := questions["minItems"].(int); !ok || got != 1 {
		t.Fatalf("AskUserQuestion questions must require at least one item, got %#v", questions["minItems"])
	}
	if got, ok := questions["maxItems"].(int); !ok || got != 8 {
		t.Fatalf("AskUserQuestion questions must allow at most eight items, got %#v", questions["maxItems"])
	}

	item := questions["items"].(map[string]any)
	itemProps := item["properties"].(map[string]any)
	options := itemProps["options"].(map[string]any)
	if got, ok := options["minItems"].(int); !ok || got != 2 {
		t.Fatalf("AskUserQuestion nested options must require at least two items, got %#v", options["minItems"])
	}
	if got, ok := options["maxItems"].(int); !ok || got != 8 {
		t.Fatalf("AskUserQuestion nested options must allow at most eight items, got %#v", options["maxItems"])
	}
}
