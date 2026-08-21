// Package cli_test covers CLI startup mode integration tests.
// These tests exercise the flags and session handling logic without requiring
// a real LLM provider. They work by exercising store / session logic directly
// or by inspecting the binary output via os/exec.
package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
	session "github.com/genai-io/san/internal/session"
)

// buildBinary compiles the san binary into a temp file and returns its path.
// The binary is removed when the test completes.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "san-test")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/san")
	cmd.Dir = projectRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return bin
}

// projectRoot returns the repository root by walking up from this file's
// directory. It panics if the root cannot be found.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up until we find go.mod
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod — are we inside the repo?")
		}
		dir = parent
	}
}

// TestVersionCommand verifies that "san version" prints version and build
// information and exits cleanly without requiring any provider configuration.
func TestVersionCommand(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "version")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("san version exited with error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines of output, got %d:\n%s", len(lines), out)
	}

	// First line: san version <ver>
	first := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(first, "san version ") {
		t.Errorf("expected first line to start with 'san version ', got: %q", first)
	}
	ver := strings.TrimPrefix(first, "san version ")
	if ver == "" {
		t.Error("version string is empty")
	}

	// There should be a "go:" line
	foundGo := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "go:") {
			foundGo = true
			break
		}
	}
	if !foundGo {
		t.Errorf("expected output to contain a 'go:' line:\n%s", out)
	}
}

// TestVersionCommandJSON verifies that "san version --json" outputs valid JSON
// with the expected fields.
func TestVersionCommandJSON(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "version", "--json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("san version --json exited with error: %v", err)
	}

	var info map[string]string
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	for _, key := range []string{"version", "build_time", "go_version", "commit"} {
		if _, ok := info[key]; !ok {
			t.Errorf("JSON output missing key %q", key)
		}
	}
	// The hard-coded version (no ldflags) must be present
	if info["version"] == "" {
		t.Error("version field is empty")
	}
	if info["go_version"] == "" {
		t.Error("go_version field is empty")
	}
}

// TestHelpCommand verifies that "san help" exits cleanly and emits usage text
// without requiring any provider configuration.
func TestHelpCommand(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "help")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("san help exited with error: %v", err)
	}

	output := string(out)
	for _, expected := range []string{"-p", "--continue", "--resume", "san -r <session-id>", "--plugin-dir", "--persona", "version"} {
		if !strings.Contains(output, expected) {
			t.Errorf("help output missing %q\nfull output:\n%s", expected, output)
		}
	}
}

// TestNonInteractivePrintMode verifies that when -p is used and no provider is
// configured, the binary exits with a non-zero code and a descriptive error
// message (rather than launching a TUI or panicking). This test does not
// require a live provider.
func TestNonInteractivePrintMode(t *testing.T) {
	bin := buildBinary(t)

	// Use an isolated HOME directory so that no providers.json from
	// ~/.san/providers.json is picked up, and unset all API key env vars so
	// no provider can be initialised via environment.
	isolatedHome := t.TempDir()
	env := filteredEnv(
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GOOGLE_API_KEY",
		"MOONSHOT_API_KEY",
		"ALIBABA_API_KEY",
		"BIGMODEL_API_KEY",
		"ANTHROPIC_VERTEX_PROJECT_ID",
		"HOME",
	)
	env = append(env, "HOME="+isolatedHome)

	cmd := exec.Command(bin, "-p", "hello")
	cmd.Env = env
	out, err := cmd.CombinedOutput()

	// With no provider, the binary should exit non-zero.
	if err == nil {
		t.Fatalf("-p with no provider should exit non-zero, got output: %s", out)
	}

	output := string(out)
	// The error message must mention "provider" so the user understands the failure.
	if !strings.Contains(strings.ToLower(output), "provider") {
		t.Errorf("-p error output should mention 'provider', got: %q", output)
	}
}

// TestSessionFork_IsIndependent verifies that forking a session creates a new,
// independent session: the fork contains the same conversation history as the
// source, but saving to the fork does not modify the original.
func TestSessionFork_IsIndependent(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}

	// 1. Create and save the original session.
	original := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:       "source-session",
			Title:    "Source",
			Provider: "fake",
			Model:    "fake-model",
			Cwd:      dir,
		},
		Messages: []core.Message{
			{ID: "u1", Role: core.RoleUser, Content: "original message"},
			{ID: "a1", Role: core.RoleAssistant, Content: "original reply"},
		},
	}
	if err := store.Save(original); err != nil {
		t.Fatalf("Save(original): %v", err)
	}

	// 2. Fork the session.
	forked, err := store.Fork("source-session")
	if err != nil {
		t.Fatalf("Fork(): %v", err)
	}

	// The fork must have a different ID.
	if forked.Metadata.ID == "source-session" {
		t.Fatal("forked session must have a different ID than the source")
	}

	// The fork must have the same conversation history as the source.
	if len(forked.Messages) != len(original.Messages) {
		t.Fatalf("fork should have %d entries, got %d", len(original.Messages), len(forked.Messages))
	}
	if forked.Messages[0].Content != "original message" {
		t.Errorf("fork message[0] text mismatch")
	}

	// The fork must reference the source via ParentSessionID.
	if forked.Metadata.ParentSessionID != "source-session" {
		t.Errorf("fork ParentSessionID = %q, want %q", forked.Metadata.ParentSessionID, "source-session")
	}

	// 3. Append a new entry to the fork and save it.
	forked.Messages = append(forked.Messages, core.Message{
		ID:      "u2",
		Role:    core.RoleUser,
		Content: "fork-only message",
	})
	if err := store.Save(forked); err != nil {
		t.Fatalf("Save(forked): %v", err)
	}

	// 4. Reload the original and confirm it is unchanged.
	time.Sleep(10 * time.Millisecond) // ensure timestamps differ
	reloaded, err := store.Load("source-session")
	if err != nil {
		t.Fatalf("Load(source-session): %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Errorf("original should still have 2 entries, got %d", len(reloaded.Messages))
	}

	// 5. Reload the fork and confirm it has the extra entry.
	reloadedFork, err := store.Load(forked.Metadata.ID)
	if err != nil {
		t.Fatalf("Load(forked): %v", err)
	}
	if len(reloadedFork.Messages) != 3 {
		t.Errorf("fork should have 3 entries, got %d", len(reloadedFork.Messages))
	}

	// 6. Both must appear in the session list.
	list, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	ids := make(map[string]bool)
	for _, s := range list {
		ids[s.ID] = true
	}
	if !ids["source-session"] {
		t.Error("List() should include the original session")
	}
	if !ids[forked.Metadata.ID] {
		t.Error("List() should include the forked session")
	}
}

// filteredEnv returns os.Environ() with the specified keys removed.
func filteredEnv(removeKeys ...string) []string {
	remove := make(map[string]bool, len(removeKeys))
	for _, k := range removeKeys {
		remove[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		key := kv
		if before, _, ok := strings.Cut(kv, "="); ok {
			key = before
		}
		if !remove[key] {
			env = append(env, kv)
		}
	}
	return env
}
