package subagent

import (
	"iter"

	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/skill"
	"github.com/genai-io/san/internal/tool"
	_ "github.com/genai-io/san/internal/tool/registry" // registers built-in schemas used by tool.Set.Tools
)

// llm.ParseVendorModel gates "vendor/model" routing on registered providers, so
// the tests that exercise routing register the vendors they reference. (The app
// wires these via blank imports in cmd/san/main.go.)
func init() {
	llm.RegisterProviderDisplay(llm.DeepSeek, llm.ProviderDisplay{Name: "DeepSeek"})
	llm.RegisterProviderDisplay(llm.Anthropic, llm.ProviderDisplay{Name: "Anthropic"})
}

type stubSubagentSessionStore struct {
	saveParentID string
	saveTitle    string
	saveModelID  string
	saveCwd      string
	saveMessages []core.Message
}

func (s *stubSubagentSessionStore) SaveSubagentConversation(parentSessionID, title, modelID, cwd string, messages []core.Message) (string, string, error) {
	s.saveParentID = parentSessionID
	s.saveTitle = title
	s.saveModelID = modelID
	s.saveCwd = cwd
	s.saveMessages = append([]core.Message(nil), messages...)
	return "agent-1", "/tmp/transcripts/agent-1.jsonl", nil
}

func TestPrepareRunConfigRespectsOverrides(t *testing.T) {
	executor := &Executor{parentModelID: "parent-model"}

	rc, err := executor.prepareRunConfig(context.Background(), tool.AgentExecRequest{
		Model:    "override-model",
		MaxSteps: 700,
		Mode:     "edit",
	})
	if err != nil {
		t.Fatalf("prepareRunConfig() error: %v", err)
	}

	if rc.displayName != "Editor" {
		t.Fatalf("expected mode-based display name, got %q", rc.displayName)
	}
	if rc.modelID != "override-model" {
		t.Fatalf("expected model override, got %q", rc.modelID)
	}
	if rc.maxSteps != 700 {
		t.Fatalf("expected max steps override, got %d", rc.maxSteps)
	}
	if rc.permMode != PermissionAcceptEdits {
		t.Fatalf("expected permission mode override, got %q", rc.permMode)
	}
	if rc.brief.Mode != string(PermissionAcceptEdits) {
		t.Fatalf("expected accept-edits mode in brief, got %q", rc.brief.Mode)
	}
}

func TestPrepareRunConfigDoesNotLowerMinimumMaxSteps(t *testing.T) {
	executor := &Executor{parentModelID: "parent-model"}

	rc, err := executor.prepareRunConfig(context.Background(), tool.AgentExecRequest{
		MaxSteps: 20,
	})
	if err != nil {
		t.Fatalf("prepareRunConfig() error: %v", err)
	}

	if rc.maxSteps != defaultMaxSteps {
		t.Fatalf("expected low max steps override to be raised to %d, got %d", defaultMaxSteps, rc.maxSteps)
	}
}

func TestResolveModelUsesConfigBeforeParent(t *testing.T) {
	executor := &Executor{parentModelID: "parent-model"}
	ctx := context.Background()

	if _, got, _ := executor.resolveModel(ctx, "", ""); got != "parent-model" {
		t.Fatalf("empty config model = %q, want parent", got)
	}
	if _, got, _ := executor.resolveModel(ctx, "", "sonnet"); got != "claude-sonnet-4-6" {
		t.Fatalf("config model = %q, want sonnet alias", got)
	}
	if _, got, _ := executor.resolveModel(ctx, "", "inherit"); got != "parent-model" {
		t.Fatalf("inherit model = %q, want parent", got)
	}
	if _, got, _ := executor.resolveModel(ctx, "override-model", "sonnet"); got != "override-model" {
		t.Fatalf("request override = %q, want override", got)
	}
}

type stubProvider struct{}

func (stubProvider) Client(string, map[string]string) (*ai.Client, error) {
	return nil, errors.New("no client: this double never streams")
}
func (stubProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (stubProvider) Name() string                                        { return "stub" }

// stubResolver records the vendor it was asked to resolve.
type stubResolver struct {
	provider llm.Provider
	vendor   llm.ProviderID
	err      error
}

func (s *stubResolver) Resolve(_ context.Context, p llm.ProviderID) (llm.Provider, error) {
	s.vendor = p
	return s.provider, s.err
}

func TestResolveModelRoutesQualifiedRefToResolver(t *testing.T) {
	stub := &stubResolver{provider: stubProvider{}}
	executor := &Executor{parentModelID: "parent-model", resolver: stub}

	_, modelID, err := executor.resolveModel(context.Background(), "deepseek/deepseek-v4", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if stub.vendor != llm.DeepSeek {
		t.Fatalf("resolver vendor = %q, want %q", stub.vendor, llm.DeepSeek)
	}
	if modelID != "deepseek-v4" {
		t.Fatalf("modelID = %q, want deepseek-v4", modelID)
	}
}

func TestResolveModelQualifiedRefWithoutResolverInheritsParent(t *testing.T) {
	executor := &Executor{parentModelID: "parent-model"} // no resolver wired

	provider, modelID, err := executor.resolveModel(context.Background(), "deepseek/deepseek-v4", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if provider != executor.provider || modelID != executor.parentModelID {
		t.Fatalf("resolveModel() = (%v, %q), want parent (%v, %q)", provider, modelID, executor.provider, executor.parentModelID)
	}
}

func TestResolveModelResolverErrorInheritsParent(t *testing.T) {
	stub := &stubResolver{err: errors.New("provider \"deepseek\" is not connected")}
	executor := &Executor{parentModelID: "parent-model", resolver: stub}

	provider, modelID, err := executor.resolveModel(context.Background(), "deepseek/deepseek-v4", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if provider != executor.provider || modelID != executor.parentModelID {
		t.Fatalf("resolveModel() = (%v, %q), want parent (%v, %q)", provider, modelID, executor.provider, executor.parentModelID)
	}
}

func newModelStore(t *testing.T, models []llm.ModelInfo) *llm.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("llm.NewStore() error: %v", err)
	}
	if err := store.CacheModels(llm.OpenAI, llm.AuthSubscription, models); err != nil {
		t.Fatalf("CacheModels() error: %v", err)
	}
	return store
}

func TestResolveModelUnavailableOverrideInheritsParent(t *testing.T) {
	executor := &Executor{
		provider:           stubProvider{},
		modelStore:         newModelStore(t, []llm.ModelInfo{{ID: "gpt-5.6-sol"}}),
		parentProviderName: llm.OpenAI,
		parentAuthMethod:   llm.AuthSubscription,
		parentModelID:      "gpt-5.6-sol",
	}

	_, modelID, err := executor.resolveModel(context.Background(), "haiku", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if modelID != executor.parentModelID {
		t.Fatalf("resolveModel(haiku) model = %q, want parent %q", modelID, executor.parentModelID)
	}
}

func TestResolveModelUnavailableParentProviderQualifiedOverrideInheritsParent(t *testing.T) {
	executor := &Executor{
		provider:           stubProvider{},
		resolver:           &stubResolver{provider: stubProvider{}},
		modelStore:         newModelStore(t, []llm.ModelInfo{{ID: "gpt-5.6-sol"}}),
		parentProviderName: llm.OpenAI,
		parentAuthMethod:   llm.AuthSubscription,
		parentModelID:      "gpt-5.6-sol",
	}

	_, modelID, err := executor.resolveModel(context.Background(), "openai/nonexistent-model", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if modelID != executor.parentModelID {
		t.Fatalf("resolveModel(openai/nonexistent-model) model = %q, want parent %q", modelID, executor.parentModelID)
	}
}

func TestResolveModelEmptyCachedCatalogInheritsParent(t *testing.T) {
	executor := &Executor{
		provider:           stubProvider{},
		modelStore:         newModelStore(t, nil),
		parentProviderName: llm.OpenAI,
		parentAuthMethod:   llm.AuthSubscription,
		parentModelID:      "gpt-5.6-sol",
	}

	_, modelID, err := executor.resolveModel(context.Background(), "haiku", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if modelID != executor.parentModelID {
		t.Fatalf("resolveModel(haiku) model = %q, want parent %q", modelID, executor.parentModelID)
	}
}

func TestResolveModelAvailableOverrideIsPreserved(t *testing.T) {
	executor := &Executor{
		provider: stubProvider{},
		modelStore: newModelStore(t, []llm.ModelInfo{
			{ID: "gpt-5.6-sol"},
			{ID: "gpt-5.6-terra"},
		}),
		parentProviderName: llm.OpenAI,
		parentAuthMethod:   llm.AuthSubscription,
		parentModelID:      "gpt-5.6-sol",
	}

	_, modelID, err := executor.resolveModel(context.Background(), "gpt-5.6-terra", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if modelID != "gpt-5.6-terra" {
		t.Fatalf("resolveModel() model = %q, want gpt-5.6-terra", modelID)
	}
}

func TestResolveModelMissingCatalogLeavesOverrideUnverified(t *testing.T) {
	executor := &Executor{provider: stubProvider{}, parentModelID: "gpt-5.6-sol"}

	_, modelID, err := executor.resolveModel(context.Background(), "haiku", "")
	if err != nil {
		t.Fatalf("resolveModel() error: %v", err)
	}
	if modelID != "claude-haiku-4-5" {
		t.Fatalf("resolveModel() model = %q, want unresolved override to pass through", modelID)
	}
}

func TestParseVendorModel(t *testing.T) {
	tests := []struct {
		ref    string
		vendor llm.ProviderID
		model  string
		ok     bool
	}{
		{"deepseek/deepseek-v4", llm.DeepSeek, "deepseek-v4", true},
		{"anthropic/claude-opus-4-20250514", llm.Anthropic, "claude-opus-4-20250514", true},
		{"acme/some-model", "", "", false},        // unknown vendor -> treated as a bare model id
		{"xiaomi/mimo-v2-flash", "", "", false},   // mimo ships slash ids; "xiaomi" is not a vendor name
		{"opus", "", "", false},                   // alias, not a qualified ref
		{"claude-opus-4-20250514", "", "", false}, // bare model id, no slash
		{"deepseek/", "", "", false},              // empty model
		{"/deepseek-v4", "", "", false},           // empty vendor
	}
	for _, tt := range tests {
		vendor, model, ok := llm.ParseVendorModel(tt.ref)
		if ok != tt.ok || vendor != tt.vendor || model != tt.model {
			t.Fatalf("ParseVendorModel(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.ref, vendor, model, ok, tt.vendor, tt.model, tt.ok)
		}
	}
}

func TestShouldRetryWithParentModelOnlyForMissingDifferentModel(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		modelID     string
		parentModel string
		want        bool
	}{
		{name: "openai model not found", err: errors.New(`infer: POST "https://api.openai.com/v1/responses": 400 Bad Request {"code":"model_not_found"}`), modelID: "claude-sonnet-4-20250514", parentModel: "gpt-5.5", want: true},
		{name: "same model", err: errors.New("model_not_found"), modelID: "gpt-5.5", parentModel: "gpt-5.5", want: false},
		{name: "no parent", err: errors.New("model_not_found"), modelID: "missing-model", parentModel: "", want: false},
		{name: "other error", err: errors.New("authentication failed"), modelID: "missing-model", parentModel: "gpt-5.5", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryWithParentModel(tt.err, tt.modelID, tt.parentModel); got != tt.want {
				t.Fatalf("shouldRetryWithParentModel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildUnfinishedAgentResultUsesPreparedRunMetadata(t *testing.T) {
	executor := &Executor{}
	run := &preparedRun{
		req: tool.AgentExecRequest{},
		cfg: &runConfig{
			config:      &AgentConfig{Name: "Scout"},
			displayName: "Scout",
			modelID:     "test-model",
		},
		startedAt: time.Now().Add(-time.Second),
		activity:  []string{"Read(main.go)"},
	}

	result := executor.buildUnfinishedAgentResult(run, &core.Result{
		Content:    "partial",
		Messages:   []core.Message{{Role: core.RoleAssistant, Content: "partial"}},
		Steps:      2,
		ToolUses:   1,
		StopReason: core.StopCancelled,
	})
	if result == nil {
		t.Fatal("expected cancelled result")
	}
	if result.AgentName != "Scout" {
		t.Fatalf("expected prepared display name, got %q", result.AgentName)
	}
	if result.Model != "test-model" {
		t.Fatalf("expected prepared model, got %q", result.Model)
	}
	if len(result.Activity) != 1 || result.Activity[0] != "Read(main.go)" {
		t.Fatalf("unexpected activity: %#v", result.Activity)
	}
	if result.Error != "agent cancelled" {
		t.Fatalf("unexpected error: %q", result.Error)
	}
}

func TestFormatToolActivityUsesReadableAgentLabel(t *testing.T) {
	got := formatToolActivity("Agent", map[string]any{
		"name":        "custom-reviewer",
		"description": "HA code structure",
		"prompt":      "Inspect the codebase",
	})

	if got != "Agent - Custom Reviewer: HA code structure" {
		t.Fatalf("formatToolActivity() = %q, want %q", got, "Agent - Custom Reviewer: HA code structure")
	}
}

func TestFormatToolActivityPreservesLiteralSubagentName(t *testing.T) {
	got := formatToolActivity("Agent", map[string]any{
		"name":        "subagent",
		"description": "update repo references",
	})

	if got != "Agent - Subagent: update repo references" {
		t.Fatalf("formatToolActivity() = %q, want %q", got, "Agent - Subagent: update repo references")
	}
}

func TestFormatToolActivityNamesGeneralAgentByMode(t *testing.T) {
	for _, tc := range []struct {
		agent string
		mode  string
		desc  string
		want  string
	}{
		{agent: "", mode: "explore", desc: "inspect repo", want: "Agent - Explorer: inspect repo"},
		{agent: "", mode: "acceptEdits", desc: "update files", want: "Agent - Editor: update files"},
		{agent: "subagent", mode: "explore", desc: "inspect repo", want: "Agent - Subagent: inspect repo"},
		{agent: "explorer", mode: "acceptEdits", desc: "update files", want: "Agent - Explorer: update files"},
	} {
		got := formatToolActivity("Agent", map[string]any{
			"name":        tc.agent,
			"description": tc.desc,
			"mode":        tc.mode,
		})
		if got != tc.want {
			t.Fatalf("formatToolActivity(mode=%s) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestFormatToolActivityFallsBackToEmptyParensForUnmappedTool(t *testing.T) {
	got := formatToolActivity("CustomTool", map[string]any{
		"task_id": "task-123",
	})

	if got != "CustomTool()" {
		t.Fatalf("formatToolActivity() = %q, want %q", got, "CustomTool()")
	}
}

func TestBuildSystemPrompt_IncludesAdditionalInstructionsAndPreloadedSkills(t *testing.T) {
	prev := skill.DefaultIfInit()
	t.Cleanup(func() { skill.SetDefaultRegistry(prev) })

	tmpDir := t.TempDir()
	skillFile := filepath.Join(tmpDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(`---
name: commit
description: Write commit messages
---
Use conventional commits.
`), 0o644); err != nil {
		t.Fatalf("WriteFile(skill): %v", err)
	}

	userStore, err := skill.NewStore(filepath.Join(tmpDir, "user-skills.json"))
	if err != nil {
		t.Fatalf("NewStore(user): %v", err)
	}
	projectStore, err := skill.NewStore(filepath.Join(tmpDir, "project-skills.json"))
	if err != nil {
		t.Fatalf("NewStore(project): %v", err)
	}

	skill.SetDefaultRegistry(skill.NewRegistryForTest(map[string]*skill.Skill{
		"git:commit": {
			Name:      "commit",
			Namespace: "git",
			FilePath:  skillFile,
			SkillDir:  tmpDir,
			State:     skill.StateActive,
		},
	}, userStore, projectStore))

	executor := &Executor{}
	brief := executor.buildBrief(&AgentConfig{
		Name:         "Reviewer",
		Description:  "Reviews code changes.",
		SystemPrompt: "Prefer minimal, surgical fixes.",
		Skills:       []string{"git:commit"},
	}, PermissionDefault)

	if !strings.Contains(brief.CustomPrompt, "Prefer minimal, surgical fixes.") {
		t.Fatal("expected custom system prompt content in brief")
	}
	if !strings.Contains(brief.CustomPrompt, `<skill-invocation name="git:commit">`) {
		t.Fatal("expected preloaded skill invocation block in brief")
	}
	if !strings.Contains(brief.CustomPrompt, "Use conventional commits.") {
		t.Fatal("expected skill instructions in brief")
	}
}

func TestExploreBriefDescribesEffectiveReadOnlyBashConstraint(t *testing.T) {
	executor := &Executor{}
	brief := executor.buildBrief(&AgentConfig{
		Name: "subagent",
		AllowTools: ToolList{
			{Name: "Read"},
			{Name: "Bash", Pattern: "git diff*"},
		},
	}, PermissionExplore)

	if !slices.Contains(brief.ToolConstraints, "Bash limited to commands classified as read-only") {
		t.Fatalf("tool constraints = %#v, want read-only Bash policy", brief.ToolConstraints)
	}
	if slices.Contains(brief.ToolConstraints, "Bash(git diff*)") {
		t.Fatalf("tool constraints should describe effective policy, got %#v", brief.ToolConstraints)
	}
}

func TestSkillsDirectoryFollowsReachableTools(t *testing.T) {
	executor := &Executor{skillsPrompt: "- review: Review changes"}

	if got := executor.skillsDirectoryFor(&AgentConfig{AllowTools: nil}); got == "" {
		t.Fatal("nil AllowTools should expose the skills directory")
	}
	if got := executor.skillsDirectoryFor(&AgentConfig{AllowTools: ToolNames("Read", "Skill")}); got == "" {
		t.Fatal("Skill-capable agent should expose the skills directory")
	}
	if got := executor.skillsDirectoryFor(&AgentConfig{AllowTools: ToolNames("Read")}); got != "" {
		t.Fatalf("agent without the Skill tool should not expose skills, got %q", got)
	}
}

func TestCanEditWorkspace(t *testing.T) {
	cases := []struct {
		name  string
		mode  PermissionMode
		allow ToolList
		want  bool
	}{
		{"edit mode can edit", PermissionAcceptEdits, nil, true},
		{"bypass mode can edit", PermissionBypass, nil, true},
		{"explore mode is read-only", PermissionExplore, nil, false},
		{"default mode alone cannot edit", PermissionDefault, nil, false},
		{"default with read-only allow list cannot edit", PermissionDefault, ToolNames("Read", "Grep"), false},
		{"default with an edit tool in the allow list can edit", PermissionDefault, ToolNames("Read", "Edit"), true},
	}
	for _, tc := range cases {
		if got := canEditWorkspace(tc.mode, tc.allow); got != tc.want {
			t.Errorf("%s: canEditWorkspace(%q, %v) = %v, want %v", tc.name, tc.mode, tc.allow.Names(), got, tc.want)
		}
	}
}

func TestExploreModeFiltersMutatingToolSchemas(t *testing.T) {
	schemas := []core.ToolSchema{
		{Name: "Read"},
		{Name: "Write"},
		{Name: "Bash"},
		{Name: "WebSearch"},
	}

	allowedBash := ToolList{{Name: "Bash", Pattern: "git diff*"}}

	got := filterSchemasForPermission(schemas, PermissionExplore, allowedBash)
	want := []core.ToolSchema{{Name: "Bash"}}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered schemas = %+v, want %+v", got, want)
	}

	// Bash stays visible without an allow list: read-only invocations are
	// explore mode's search tool. Write is still filtered out.
	got = filterSchemasForPermission(schemas, PermissionExplore, nil)
	want = []core.ToolSchema{{Name: "Read"}, {Name: "Bash"}, {Name: "WebSearch"}}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered schemas without git diff = %+v, want %+v", got, want)
	}

	// Explore remains read-only even when a custom definition over-requests
	// mutating schemas. Bash stays visible because each call is classified.
	overbroad := ToolNames("Read", "Write", "Edit", "Bash")
	got = filterSchemasForPermission(schemas, PermissionExplore, overbroad)
	want = []core.ToolSchema{{Name: "Read"}, {Name: "Bash"}}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered overbroad explore schemas = %+v, want %+v", got, want)
	}

	got = filterSchemasForPermission(schemas, PermissionExplore, ToolNames("Write"))
	if len(got) != 0 {
		t.Fatalf("write-only explore schema = %+v, want none", got)
	}
}

func TestExploreModeAllowsOnlyGitDiffBash(t *testing.T) {
	check := subagentPermissionFunc(PermissionExplore, ToolList{{Name: "Bash", Pattern: "git diff*"}}, nil)
	for _, command := range []string{
		"git diff",
		"git diff --stat",
		"git diff --cached -- internal/subagent/executor.go",
	} {
		allow, reason := check(context.Background(), "Bash", map[string]any{"command": command})
		if !allow {
			t.Fatalf("Bash(%q) blocked: %s", command, reason)
		}
	}

	// Read-only bash bypasses the whitelist constraint like the dedicated
	// read-only tools do.
	allow, reason := check(context.Background(), "Bash", map[string]any{"command": "git status"})
	if !allow {
		t.Fatalf("read-only Bash(git status) blocked: %s", reason)
	}

	// Cases that should be blocked: mutating pattern mismatch, or a
	// confirmation-floor destructive suffix hidden in a compound command that
	// the greedy Bash(git diff*) pattern would otherwise match.
	blocked := []string{
		"git commit -m msg",                   // mutating, pattern mismatch
		"git diff && rm -rf /tmp/example",     // confirmation floor: destructive
		"git diff && git push --force origin", // confirmation floor: discards work
	}
	for _, command := range blocked {
		allow, _ := check(context.Background(), "Bash", map[string]any{"command": command})
		if allow {
			t.Fatalf("Bash(%q) allowed, want blocked", command)
		}
	}

	// Without any agent permission, read-only bash still passes; mutating
	// bash is denied by explore mode.
	unlisted := subagentPermissionFunc(PermissionExplore, nil, nil)
	allow, reason = unlisted(context.Background(), "Bash", map[string]any{"command": "git diff"})
	if !allow {
		t.Fatalf("read-only git diff blocked without agent permission: %s", reason)
	}
	allow, _ = unlisted(context.Background(), "Bash", map[string]any{"command": "go build ./..."})
	if allow {
		t.Fatal("mutating bash allowed in explore mode without agent permission")
	}
}

func TestExploreModeCannotElevateThroughAllowTools(t *testing.T) {
	cases := []struct {
		name    string
		allow   ToolList
		tool    string
		input   map[string]any
		allowed bool
	}{
		{name: "write", allow: ToolNames("Write"), tool: "Write", input: map[string]any{"file_path": "x", "content": "x"}},
		{name: "edit", allow: ToolNames("Edit"), tool: "Edit", input: map[string]any{"file_path": "x", "old_string": "a", "new_string": "b"}},
		{name: "bare bash touch", allow: ToolNames("Bash"), tool: "Bash", input: map[string]any{"command": "touch x"}},
		{name: "configured npm install", allow: ToolList{{Name: "Bash", Pattern: "npm install*"}}, tool: "Bash", input: map[string]any{"command": "npm install"}},
		{name: "git commit", allow: ToolNames("Bash"), tool: "Bash", input: map[string]any{"command": "git commit -m x"}},
		{name: "git diff output", allow: ToolNames("Bash"), tool: "Bash", input: map[string]any{"command": "git diff --output=/tmp/x"}},
		{name: "git diff", allow: ToolNames("Bash"), tool: "Bash", input: map[string]any{"command": "git diff"}, allowed: true},
		{name: "git status", allow: ToolNames("Bash"), tool: "Bash", input: map[string]any{"command": "git status"}, allowed: true},
		{name: "git show", allow: ToolNames("Bash"), tool: "Bash", input: map[string]any{"command": "git show HEAD"}, allowed: true},
		{name: "git log", allow: ToolNames("Bash"), tool: "Bash", input: map[string]any{"command": "git log --oneline"}, allowed: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := subagentPermissionFunc(PermissionExplore, tc.allow, nil)
			got, reason := check(context.Background(), tc.tool, tc.input)
			if got != tc.allowed {
				t.Fatalf("allow = %v, want %v (reason=%q)", got, tc.allowed, reason)
			}
		})
	}
}

func TestDefaultModeRestrictsConfiguredBash(t *testing.T) {
	check := subagentPermissionFunc(PermissionDefault, ToolList{{Name: "Bash", Pattern: "git diff*"}}, nil)
	allow, reason := check(context.Background(), "Bash", map[string]any{"command": "git diff --stat"})
	if !allow {
		t.Fatalf("configured Bash command blocked: %s", reason)
	}

	allow, _ = check(context.Background(), "Bash", map[string]any{"command": "npm install"})
	if allow {
		t.Fatal("unconfigured mutating Bash command allowed (allow_tools whitelist constraint)")
	}

	allow, reason = check(context.Background(), "Read", map[string]any{"file_path": "README.md"})
	if !allow {
		t.Fatalf("non-Bash default mode tool blocked: %s", reason)
	}
}

func TestDenyToolRulesMatchPatterns(t *testing.T) {
	check := subagentPermissionFunc(PermissionDefault, nil, ToolList{{Name: "Bash", Pattern: "git status"}})
	allow, _ := check(context.Background(), "Bash", map[string]any{"command": "git status"})
	if allow {
		t.Fatal("denied Bash command allowed")
	}

	allow, reason := check(context.Background(), "Bash", map[string]any{"command": "npm install"})
	// Default mode + no allow_tools -> mutating Bash would Prompt -> Deny in subagent.
	if allow {
		t.Fatalf("default-mode mutating Bash unexpectedly allowed without allow_tools: %s", reason)
	}
}

func TestExploreModeAllowsConfiguredBashPattern(t *testing.T) {
	check := subagentPermissionFunc(PermissionExplore, ToolList{{Name: "Bash", Pattern: "git show*"}}, nil)
	allow, reason := check(context.Background(), "Bash", map[string]any{"command": "git show --stat HEAD"})
	if !allow {
		t.Fatalf("configured bash command blocked: %s", reason)
	}

	allow, _ = check(context.Background(), "Bash", map[string]any{"command": "sed -i s/a/b/ config.yaml"})
	if allow {
		t.Fatal("unconfigured mutating bash command allowed")
	}
}

func TestAcceptEditsModeFiltersApprovalOnlyToolSchemas(t *testing.T) {
	schemas := []core.ToolSchema{
		{Name: "Read"},
		{Name: "Edit"},
		{Name: "Write"},
		{Name: "Bash"},
		{Name: "Agent"},
	}

	// Bash stays visible (read-only invocations auto-permit). Agent never
	// reaches this filter for workers — it is parent-only at the tool.Set
	// level.
	got := filterSchemasForPermission(schemas, PermissionAcceptEdits, nil)
	want := []core.ToolSchema{{Name: "Read"}, {Name: "Edit"}, {Name: "Write"}, {Name: "Bash"}}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered schemas = %+v, want %+v", got, want)
	}
}

func TestBypassModeAllowsEverything(t *testing.T) {
	check := subagentPermissionFunc(PermissionBypass, nil, nil)
	allow, _ := check(context.Background(), "Bash", map[string]any{"command": "git status"})
	if !allow {
		t.Fatal("bypass mode should allow Bash")
	}
	// Bypass skips both confirmation tiers. The circuit-breaker counterpart
	// (rm -rf ~ still denied) is pinned in TestPermissionScenarios.
	allow, reason := check(context.Background(), "Bash", map[string]any{"command": "git push --force origin main"})
	if !allow {
		t.Fatalf("bypass mode should allow work-discarding git: %s", reason)
	}
	allow, reason = check(context.Background(), "Bash", map[string]any{"command": "rm -rf /tmp/example"})
	if !allow {
		t.Fatalf("bypass mode should allow destructive bash on a subpath: %s", reason)
	}
	// Parent-only tools stay denied even in bypass mode — the agent model
	// is flat, and the gate backs up the schema-level exclusion.
	allow, _ = check(context.Background(), "Agent", map[string]any{})
	if allow {
		t.Fatal("parent-only Agent should be denied for workers even in bypass mode")
	}
}

func TestNormalizePermissionModeDefaultsEmpty(t *testing.T) {
	if got := NormalizePermissionMode(""); got != PermissionDefault {
		t.Fatalf("normalize(empty) = %q, want %q", got, PermissionDefault)
	}
	if got := NormalizePermissionMode("  explore  "); got != PermissionExplore {
		t.Fatalf("normalize(\"  explore  \") = %q, want %q", got, PermissionExplore)
	}
}

func TestResolveAgentConfigUsesUnnamedAndNamedDefinitions(t *testing.T) {
	old := Default()
	SetDefaultRegistry(NewRegistry())
	t.Cleanup(func() { SetDefaultRegistry(old) })

	named := &AgentConfig{Name: "reviewer", Description: "Reviews changes", MaxSteps: 700}
	literalSubagent := &AgentConfig{Name: "subagent", Description: "User-defined subagent"}
	Default().Register(named)
	Default().Register(literalSubagent)

	unnamed, ok := resolveAgentConfig("")
	if !ok || unnamed.Name != "" {
		t.Fatalf("unnamed config = %#v, %v; want empty name", unnamed, ok)
	}
	resolved, ok := resolveAgentConfig("reviewer")
	if !ok || resolved != named {
		t.Fatalf("named config = %#v, %v; want registered config", resolved, ok)
	}
	resolved, ok = resolveAgentConfig("subagent")
	if !ok || resolved != literalSubagent {
		t.Fatalf("explicit subagent config = %#v, %v; want registered config", resolved, ok)
	}
	resolved, ok = resolveAgentConfig("missing")
	if !ok {
		t.Fatal("unknown agent name should resolve through the base template")
	}
	if resolved.Name != "missing" || !resolved.displayOnly {
		t.Fatalf("unknown agent config = %#v; want display-only name", resolved)
	}
	if resolved.SystemPrompt != "" || len(resolved.Skills) != 0 || resolved.AllowTools != nil || resolved.DenyTools != nil {
		t.Fatalf("display-only config inherited custom behavior: %#v", resolved)
	}
}

func TestResolveAgentConfigRejectsDisabledDefinition(t *testing.T) {
	old := Default()
	SetDefaultRegistry(NewRegistry())
	t.Cleanup(func() { SetDefaultRegistry(old) })

	registry := Default()
	registry.Register(&AgentConfig{Name: "reviewer"})
	if err := registry.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetEnabled("reviewer", false, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveAgentConfig("reviewer"); ok {
		t.Fatal("disabled agent should not resolve")
	}
}

func TestSubagentToolSetOwnsFilterInputs(t *testing.T) {
	allow := []string{tool.ToolRead}
	disallow := []string{tool.ToolSendMessage}
	disabled := map[string]bool{tool.ToolWrite: true}
	set := newAgentToolSet(allow, disallow, disabled, nil)

	allow[0] = tool.ToolWrite
	disallow[0] = tool.ToolRead
	disabled[tool.ToolWrite] = false

	if len(set.Allow) != 1 || set.Allow[0] != tool.ToolRead {
		t.Fatalf("tool set retained caller-owned allow slice: %v", set.Allow)
	}
	if len(set.Disallow) != 1 || set.Disallow[0] != tool.ToolSendMessage {
		t.Fatalf("tool set retained caller-owned disallow slice: %v", set.Disallow)
	}
	if !set.Disabled[tool.ToolWrite] {
		t.Fatalf("tool set retained caller-owned disabled map: %v", set.Disabled)
	}
}

func TestSubagentToolSetInheritsDisabledTools(t *testing.T) {
	const disabledName = tool.ToolSendMessage

	disabled := map[string]bool{disabledName: true}
	set := newAgentToolSet(nil, nil, disabled, nil)
	// Mutating the caller's map after configuration must not alter the built set.
	disabled[disabledName] = false

	for _, schema := range set.Tools() {
		if schema.Name == disabledName {
			t.Fatalf("disabled %s remained in subagent tool schema", disabledName)
		}
	}
	if executor := tool.AdaptToolRegistry(set.Tools(), func() string { return "" }).Get(disabledName); executor != nil {
		t.Fatalf("disabled %s retained a subagent executor", disabledName)
	}
}

func TestSubagentToolSetKeepsExplicitlyEnabledSendMessage(t *testing.T) {
	set := newAgentToolSet(nil, nil, map[string]bool{tool.ToolSendMessage: false}, nil)
	var found bool
	for _, schema := range set.Tools() {
		if schema.Name == tool.ToolSendMessage {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("explicitly enabled %s missing from subagent tool schema", tool.ToolSendMessage)
	}
	if executor := tool.AdaptToolRegistry(set.Tools(), func() string { return "" }).Get(tool.ToolSendMessage); executor == nil {
		t.Fatalf("explicitly enabled %s missing subagent executor", tool.ToolSendMessage)
	}
}

func TestExecutorDisabledToolsCopiesInputAndSnapshot(t *testing.T) {
	executor := &Executor{}
	disabled := map[string]bool{"Write": true}
	executor.SetDisabledTools(disabled)
	disabled["Write"] = false

	snapshot := executor.disabledToolsSnapshot()
	if !snapshot["Write"] {
		t.Fatal("SetDisabledTools retained caller-owned map")
	}
	snapshot["Write"] = false
	if !executor.disabledToolsSnapshot()["Write"] {
		t.Fatal("disabledToolsSnapshot exposed executor-owned map")
	}
}

func TestExecutorUsesRegistryCapturedAtConstruction(t *testing.T) {
	old := Default()
	projectA := NewRegistry()
	projectA.Register(&AgentConfig{Name: "reviewer", Description: "Project A"})
	SetDefaultRegistry(projectA)
	t.Cleanup(func() { SetDefaultRegistry(old) })

	executor := NewExecutor(nil, "/project-a", "model", nil)
	projectB := NewRegistry()
	projectB.Register(&AgentConfig{Name: "reviewer", Description: "Project B"})
	SetDefaultRegistry(projectB)

	config, ok := executor.resolveAgentConfig("reviewer")
	if !ok || config.Description != "Project A" {
		t.Fatalf("executor resolved %#v, %v; want its captured Project A registry", config, ok)
	}
}

func TestRequestPermissionModeInheritanceAndNamedConfig(t *testing.T) {
	executor := &Executor{}
	executor.SetParentPermissionMode(func() PermissionMode { return PermissionBypass })

	if got := executor.requestPermissionMode(baseAgentConfig(), tool.AgentExecRequest{}); got != PermissionBypass {
		t.Fatalf("unnamed agent mode = %q, want inherited bypass", got)
	}
	displayOnly := displayOnlyAgentConfig("autopilot-suggestion")
	if got := executor.requestPermissionMode(displayOnly, tool.AgentExecRequest{Agent: "autopilot-suggestion"}); got != PermissionBypass {
		t.Fatalf("display-only agent mode = %q, want inherited bypass", got)
	}
	named := &AgentConfig{Name: "reviewer", PermissionMode: PermissionExplore}
	if got := executor.requestPermissionMode(named, tool.AgentExecRequest{Agent: "reviewer"}); got != PermissionExplore {
		t.Fatalf("named agent mode = %q, want configured explore", got)
	}
	if got := executor.requestPermissionMode(named, tool.AgentExecRequest{Agent: "reviewer", Mode: "edit"}); got != PermissionAcceptEdits {
		t.Fatalf("named edit override = %q, want acceptEdits", got)
	}
}

func TestValidateRequestRejectsInjectedBypassMode(t *testing.T) {
	executor := &Executor{}
	for _, mode := range []string{"bypass", "bypassPermissions", "acceptEdits", "readonly"} {
		if err := executor.validateRequest(tool.AgentExecRequest{Prompt: "task", Mode: mode}); err == nil {
			t.Fatalf("validateRequest(mode=%q) unexpectedly allowed non-schema mode", mode)
		}
	}
}

func TestParentPermissionModeGetterUsesLiveSessionSnapshot(t *testing.T) {
	permissions := setting.NewSessionPermissions()
	executor := &Executor{}
	executor.SetParentPermissionMode(func() PermissionMode {
		return PermissionModeFromOperationMode(permissions.Snapshot().Mode)
	})
	config := baseAgentConfig()

	if got := executor.requestPermissionMode(config, tool.AgentExecRequest{Mode: "default"}); got != PermissionDefault {
		t.Fatalf("initial inherited mode = %q, want default", got)
	}
	permissions.SetMode(setting.ModeBypassPermissions)
	if got := executor.requestPermissionMode(config, tool.AgentExecRequest{Mode: "default"}); got != PermissionBypass {
		t.Fatalf("updated inherited mode = %q, want bypass", got)
	}
	if got := executor.requestPermissionMode(config, tool.AgentExecRequest{Mode: "explore"}); got != PermissionExplore {
		t.Fatalf("explicit explore mode = %q, want read-only ceiling", got)
	}
}

func TestPermissionModeFromOperationMode(t *testing.T) {
	if got := PermissionModeFromOperationMode(setting.ModeBypassPermissions); got != PermissionBypass {
		t.Fatalf("bypass parent maps to %q", got)
	}
	if got := PermissionModeFromOperationMode(setting.ModeAutoPilot); got != PermissionAcceptEdits {
		t.Fatalf("autopilot parent maps to %q", got)
	}
}

func TestUnnamedAgentUses500Steps(t *testing.T) {
	config, ok := resolveAgentConfig("")
	if !ok {
		t.Fatal("unnamed agent config not found")
	}
	if config.Name != "" {
		t.Fatalf("unnamed agent name = %q, want empty", config.Name)
	}
	if config.MaxSteps != defaultMaxSteps {
		t.Fatalf("unnamed agent max steps = %d, want %d", config.MaxSteps, defaultMaxSteps)
	}
	if configs := NewRegistry().ListConfigs(); len(configs) != 0 {
		t.Fatalf("registry should have no built-in agent definitions, got %+v", configs)
	}
}

func TestPersistSubagentSessionUsesSessionStore(t *testing.T) {
	store := &stubSubagentSessionStore{}
	executor := &Executor{
		cwd:             "/tmp/project",
		sessionStore:    store,
		parentSessionID: "parent-1",
	}

	sessionID, transcriptPath := executor.persistSubagentSession("General", "test-model", "Inspect code", []core.Message{
		{Role: core.RoleUser, Content: "hello"},
	})

	if sessionID != "agent-1" {
		t.Fatalf("sessionID = %q, want %q", sessionID, "agent-1")
	}
	if transcriptPath != "/tmp/transcripts/agent-1.jsonl" {
		t.Fatalf("transcriptPath = %q", transcriptPath)
	}
	if store.saveParentID != "parent-1" || store.saveTitle != "Inspect code" || store.saveModelID != "test-model" || store.saveCwd != "/tmp/project" {
		t.Fatalf("unexpected save args: %+v", store)
	}
	if len(store.saveMessages) != 1 || store.saveMessages[0].Content != "hello" {
		t.Fatalf("unexpected saved messages: %+v", store.saveMessages)
	}
}

// stubLLM is a minimal core.LLM for tests that don't call inference.
type stubLLM struct{}

func (s *stubLLM) Name() string { return "stub" }

func (s *stubLLM) Stream(context.Context, *ai.Request) iter.Seq2[ai.Delta, error] {
	return func(func(ai.Delta, error) bool) {}
}

// stubSystem is a minimal core.System for tests.
type stubSystem struct{}

func (s *stubSystem) Prompt() string                        { return "" }
func (s *stubSystem) Use(_ core.Section, _ string)          {}
func (s *stubSystem) Drop(_, _ string)                      {}
func (s *stubSystem) Refresh(_, _ string)                   {}
func (s *stubSystem) Sections() []core.Section              { return nil }
func (s *stubSystem) SetObserver(_ func(core.SystemChange)) {}

// TestBuildUnfinishedAgentResultPreservesFailedRun covers the other way a run
// ends early. A cancelled run was already preserved; a run that died on an
// inference failure was not, because ThinkAct returned no Result at all — so
// Run fell through to the bare-error path and persistSubagentSession never
// ran, losing the transcript of everything the agent had done.
func TestBuildUnfinishedAgentResultPreservesFailedRun(t *testing.T) {
	executor := &Executor{}
	run := &preparedRun{
		req:       tool.AgentExecRequest{},
		cfg:       &runConfig{config: &AgentConfig{Name: "Scout"}, displayName: "Scout", modelID: "test-model"},
		startedAt: time.Now().Add(-time.Second),
	}

	result := executor.buildUnfinishedAgentResult(run, &core.Result{
		Content:    "partial",
		Messages:   []core.Message{{Role: core.RoleAssistant, Content: "partial"}},
		Steps:      8,
		StopReason: core.StopError,
		StopDetail: "provider unavailable",
	})
	if result == nil {
		t.Fatal("a failed run must still be preserved, or its transcript is never persisted")
	}
	if result.Success {
		t.Error("Success = true, want false")
	}
	if result.Error != "provider unavailable" {
		t.Errorf("Error = %q, want the underlying failure", result.Error)
	}
	if result.Content != "partial" {
		t.Errorf("Content = %q, want the partial output", result.Content)
	}
	if result.StepCount != 8 {
		t.Errorf("StepCount = %d, want 8", result.StepCount)
	}
}

// A normally-completed run is not "unfinished" — it goes through
// buildAgentResult instead, so this guard must keep rejecting it.
func TestBuildUnfinishedAgentResultRejectsCompletedRun(t *testing.T) {
	executor := &Executor{}
	run := &preparedRun{
		req:       tool.AgentExecRequest{},
		cfg:       &runConfig{config: &AgentConfig{Name: "Scout"}, displayName: "Scout", modelID: "test-model"},
		startedAt: time.Now(),
	}

	if got := executor.buildUnfinishedAgentResult(run, &core.Result{StopReason: core.StopEndTurn}); got != nil {
		t.Fatalf("completed run treated as unfinished: %#v", got)
	}
	if got := executor.buildUnfinishedAgentResult(run, nil); got != nil {
		t.Fatalf("nil Result treated as unfinished: %#v", got)
	}
}
