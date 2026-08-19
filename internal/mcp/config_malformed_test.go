package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SaveServer used to read mcp.json, ignore the parse error, and write back a
// config containing only the server being added — silently dropping every
// other server the user had configured. A file it cannot parse must stop it.
func TestSaveServerRefusesToClobberMalformedConfig(t *testing.T) {
	base := t.TempDir()
	loader := NewConfigLoaderForTest(base)
	path := loader.GetFilePath(ScopeProject)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Two configured servers, one stray trailing comma.
	original := `{
  "mcpServers": {
    "github": {"command": "gh-mcp"},
    "postgres": {"command": "pg-mcp"},
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	err := loader.SaveServer("newone", ServerConfig{Command: "new-mcp"}, ScopeProject)
	if err == nil {
		t.Fatal("SaveServer succeeded on a malformed mcp.json; it must report the parse error")
	}
	if !strings.Contains(err.Error(), "mcp.json") {
		t.Errorf("error should name the offending file, got: %v", err)
	}

	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(after) != original {
		t.Errorf("the user's other servers were dropped:\n%s", after)
	}
}

func TestSaveServerStillCreatesAMissingConfig(t *testing.T) {
	loader := NewConfigLoaderForTest(t.TempDir())
	if err := loader.SaveServer("github", ServerConfig{Command: "gh-mcp"}, ScopeProject); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	servers, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := servers["github"]; !ok {
		t.Errorf("server was not saved: %v", servers)
	}
}
