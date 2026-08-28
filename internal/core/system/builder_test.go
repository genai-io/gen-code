package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

func TestBuildEnvironmentRendersFacts(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := renderEnvironment(Environment{Cwd: repo})
	if !strings.Contains(body, "cwd: "+repo) {
		t.Fatalf("renderEnvironment missing cwd: %q", body)
	}
	if !strings.Contains(body, "branch: main") {
		t.Fatalf("renderEnvironment missing branch: %q", body)
	}
}

func TestBuildEnvironmentOmitsBranchOutsideRepo(t *testing.T) {
	body := renderEnvironment(Environment{Cwd: t.TempDir()})
	if strings.Contains(body, "branch:") {
		t.Fatalf("non-repo environment should omit the branch line: %q", body)
	}
}

func TestGitBranch(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feature/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Branch resolves from a subdirectory by walking up to the repo root.
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(sub); got != "feature/x" {
		t.Errorf("gitBranch = %q, want %q", got, "feature/x")
	}

	// Linked worktree: .git is a file pointing at the real git dir.
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(wt); got != "feature/x" {
		t.Errorf("worktree gitBranch = %q, want %q", got, "feature/x")
	}

	// Detached HEAD yields the short hash.
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(repo); got != "0123456 (detached)" {
		t.Errorf("detached gitBranch = %q, want %q", got, "0123456 (detached)")
	}
}

func TestBuildPromptCaching(t *testing.T) {
	sys := Build(core.ScopeMain,
		WithEnvironment(Environment{Cwd: "/tmp/test"}),
	)

	first := sys.Prompt()
	if first == "" {
		t.Error("First Prompt() call should return non-empty string")
	}

	second := sys.Prompt()
	if first != second {
		t.Error("Second Prompt() call should return cached result identical to the first")
	}
}

func TestBuildPromptOmitsMemory(t *testing.T) {
	// Memory (AGENTS.md) no longer lives in the system prompt — it
	// rides on user messages as a <system-reminder> block via the harness's
	// reminder service so memory edits don't invalidate the cache prefix.
	sys := Build(core.ScopeMain,
		WithEnvironment(Environment{Cwd: "/tmp/test"}),
	)

	prompt := sys.Prompt()
	if strings.Contains(prompt, "<memory") {
		t.Error("prompt should NOT contain memory section (now a system-reminder)")
	}
}

func TestBuildPromptOmitsCapabilities(t *testing.T) {
	// Skills and agents directories no longer live in the system prompt.
	// Skills ride on user messages as <system-reminder> blocks; agents are
	// embedded in the Agent tool's description.
	sys := Build(core.ScopeMain,
		WithEnvironment(Environment{Cwd: "/tmp/test"}),
	)

	prompt := sys.Prompt()
	if strings.Contains(prompt, "<skills>") {
		t.Error("prompt should NOT contain <skills> tag (now lives in <system-reminder>)")
	}
	if strings.Contains(prompt, "<agents>") {
		t.Error("prompt should NOT contain <agents> tag (now lives in Agent tool description)")
	}
}

func TestBuildPromptOrder_StableBeforeVolatile(t *testing.T) {
	// Volatile sections (environment) must sit AFTER stable ones so the
	// prompt-cache prefix survives daily date rollovers.
	sys := Build(core.ScopeMain,
		WithEnvironment(Environment{Cwd: "/tmp/test"}),
	)
	prompt := sys.Prompt()

	indices := map[string]int{
		"identity": strings.Index(prompt, "You are a coding agent"),
		"behavior": strings.Index(prompt, "<behavior>"),
		"rules":    strings.Index(prompt, "<rules>"),
		"env":      strings.Index(prompt, "<environment>"),
	}
	for name, idx := range indices {
		if idx < 0 {
			t.Fatalf("section %q not found", name)
		}
	}

	order := []string{"identity", "behavior", "rules", "env"}
	for i := 1; i < len(order); i++ {
		if indices[order[i-1]] >= indices[order[i]] {
			t.Errorf("expected %s before %s; got idx %d vs %d",
				order[i-1], order[i], indices[order[i-1]], indices[order[i]])
		}
	}
}

func TestBuildPromptEmptyOptionsExcluded(t *testing.T) {
	sys := Build(core.ScopeMain, WithEnvironment(Environment{Cwd: "/tmp/test"}))
	prompt := sys.Prompt()

	if strings.Contains(prompt, "<memory") {
		t.Error("empty memory should not produce <memory> tag")
	}
	if strings.Contains(prompt, "<skills>") {
		t.Error("empty skills should not produce <skills> tag")
	}
	if strings.Contains(prompt, "<agents>") {
		t.Error("empty agents should not produce <agents> tag")
	}
}

func TestBuildScopeSubagent_OmitsMainOnlyGuidelines(t *testing.T) {
	sys := Build(core.ScopeSubagent, WithEnvironment(Environment{Cwd: "/tmp/test"}))
	prompt := sys.Prompt()

	if strings.Contains(prompt, "<behavior>") {
		t.Error("subagent scope should not include the behavior part")
	}
}

func TestBuildSubagentIdentity_ReplacesDefault(t *testing.T) {
	sys := Build(core.ScopeSubagent,
		WithSubagentIdentity(SubagentBrief{
			AgentName:    "custom-reviewer",
			Description:  "Reviews code changes for bugs.",
			Mode:         "explore",
			CustomPrompt: "Use git diff to inspect changes.",
		}),
		WithEnvironment(Environment{Cwd: "/tmp/test"}),
	)
	prompt := sys.Prompt()

	if !strings.Contains(prompt, "You are a custom-reviewer subagent") {
		t.Error("subagent identity should announce agent name")
	}
	if !strings.Contains(prompt, `<identity mode="explore">`) {
		t.Error("identity tag should carry mode attribute")
	}
	if !strings.Contains(prompt, "Use git diff to inspect changes.") {
		t.Error("custom prompt body should appear inside identity")
	}
	// Default identity should be replaced, not duplicated.
	if strings.Contains(prompt, "You are a coding agent") {
		t.Error("default identity should be replaced by subagent identity")
	}
}

func TestBuildSubagentIdentityPreservesExplicitSubagentName(t *testing.T) {
	sys := Build(core.ScopeSubagent,
		WithSubagentIdentity(SubagentBrief{
			AgentName:   "subagent",
			Description: "An agent with a literal name.",
		}),
	)

	if prompt := sys.Prompt(); !strings.Contains(prompt, "You are a subagent subagent") {
		t.Fatalf("explicit name was not preserved:\n%s", prompt)
	}
}

func TestSystemUseDropRefresh(t *testing.T) {
	sys := Build(core.ScopeMain, WithEnvironment(Environment{Cwd: "/tmp/test"}))
	first := sys.Prompt()

	// Use: register a new section.
	sys.Use(core.Section{
		Slot: core.SlotEnvironment, Name: "test-section", Source: core.Dynamic,
		Render: func() string { return "TEST_SECTION_BODY" },
	}, "test")
	if !strings.Contains(sys.Prompt(), "TEST_SECTION_BODY") {
		t.Error("Use should add a new section's content to Prompt()")
	}

	// Drop: remove it.
	sys.Drop("test-section", "test")
	if strings.Contains(sys.Prompt(), "TEST_SECTION_BODY") {
		t.Error("Drop should remove the section from Prompt()")
	}

	// After Drop the prompt should match the original.
	if sys.Prompt() != first {
		t.Error("Prompt should return to original state after Drop")
	}
}

func TestCachedTemplatesNonEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"cachedIdentity": cachedIdentity,
		"cachedBehavior": cachedBehavior,
		"cachedRules":    cachedRules,
		"cachedCompact":  cachedCompact,
	} {
		if body == "" {
			t.Errorf("%s should be non-empty after init()", name)
		}
	}
}

func TestCompactPrompt(t *testing.T) {
	if CompactPrompt() == "" {
		t.Error("CompactPrompt() should return non-empty string")
	}
}
