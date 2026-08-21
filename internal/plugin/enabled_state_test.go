package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/confdir"
)

// saveEnabledState rewrites the whole settings.json from an untyped map. It
// used to ignore the parse error on the way in, so enabling one plugin against
// a settings.json it could not read replaced the file with nothing but an
// "enabledPlugins" block — model, permissions, hooks and all.
func TestEnableRefusesToClobberMalformedSettings(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(confdir.Dir(cwd), "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "model": "claude-opus-5",
  "hooks": {"PreToolUse": []},
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	r.cwd = cwd
	r.plugins["demo"] = &Plugin{Manifest: Manifest{Name: "demo"}}

	err := r.Enable("demo", ScopeProject)
	if err == nil {
		t.Fatal("Enable succeeded on a malformed settings.json; it must report the parse error")
	}
	if !strings.Contains(err.Error(), "settings.json") {
		t.Errorf("error should name the offending file, got: %v", err)
	}

	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(after) != original {
		t.Errorf("the user's settings were replaced:\n%s", after)
	}
}

func TestEnableStillWritesToAFreshSettingsFile(t *testing.T) {
	cwd := t.TempDir()
	r := NewRegistry()
	r.cwd = cwd
	r.plugins["demo"] = &Plugin{Manifest: Manifest{Name: "demo"}}

	if err := r.Enable("demo", ScopeProject); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(confdir.Dir(cwd), "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), `"demo": true`) {
		t.Errorf("plugin state was not written:\n%s", data)
	}
}
