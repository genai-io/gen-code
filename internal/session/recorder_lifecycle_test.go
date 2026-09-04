package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session/transcript"
)

// Regression: NewRecorder must write session.started before returning, so
// that subsequent telemetry writes from SetObserver replay don't beat it to
// the disk. If session.started were written lazily (or only by Store.Save),
// the transcript file would already exist when Save calls Start — Start
// would short-circuit and provider/model metadata would be lost.
func TestRecorder_WritesSessionStartedBeforeTelemetry(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	// Construct the recorder — should write session.started synchronously.
	rec := NewRecorder(RecorderOptions{
		FileStore: fs,
		SessionID: "sess",
		AgentID:   "main",
		Provider:  "anthropic",
		Model:     "claude-x",
		MaxTokens: 4096,
		Cwd:       "/tmp/project",
		ProjectID: "proj-1",
	})

	// Fire a SystemChange right after — mirroring what SetObserver replay
	// does inside core.NewAgent.
	rec.OnAgentEvent(core.SystemChange{
		Name: "identity", Slot: 0, Content: "You are X", Caller: "system:init",
	})

	data, err := os.ReadFile(filepath.Join(dir, "transcripts", "sess.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var firstType, foundProvider, foundModel string
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
		if i == 0 {
			firstType, _ = raw["type"].(string)
		}
		if t, _ := raw["type"].(string); t == "session.started" {
			if sess, ok := raw["session"].(map[string]any); ok {
				foundProvider, _ = sess["provider"].(string)
				foundModel, _ = sess["model"].(string)
			}
		}
	}
	if firstType != "session.started" {
		t.Fatalf("first record type = %q, want session.started", firstType)
	}
	if foundProvider != "anthropic" || foundModel != "claude-x" {
		t.Fatalf("session.started provider=%q model=%q, want anthropic/claude-x", foundProvider, foundModel)
	}
}
