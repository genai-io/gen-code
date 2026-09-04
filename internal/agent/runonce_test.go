package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// A headless run reads the project's instructions, because a repo that answers
// one way interactively and another way through -p is a repo with two
// behaviours. They ride on the opening message as a memory reminder, the same
// shape the interactive path uses.
func TestAHeadlessRunCarriesTheProjectsInstructions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("always answer PINEAPPLE"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := withInstructions(dir, core.UserMessage("what is the word?", nil)).Text()
	if !strings.Contains(out, "always answer PINEAPPLE") {
		t.Errorf("the instructions did not reach the message: %q", out)
	}
	if !strings.Contains(out, `<memory scope="project">`) {
		t.Errorf("not wrapped as project memory: %q", out)
	}
	if !strings.HasPrefix(out, "what is the word?") {
		t.Errorf("the person's message stopped coming first: %q", out)
	}
}

// A repo with nothing to say adds nothing — no empty scope, no bare wrapper.
func TestNothingToSayAddsNothing(t *testing.T) {
	msg := core.UserMessage("hello", nil)
	out := withInstructions(t.TempDir(), msg)
	if out.Text() != "hello" {
		t.Errorf("an empty project injected %q", out.Text())
	}
}
