package session_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
	session "github.com/genai-io/san/internal/session"
	taskTracker "github.com/genai-io/san/internal/todo"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// newTestStore creates a Store using a temp directory instead of ~/.san/projects/.
func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	return store
}

// makeUserEntry creates a user text message for testing.
func makeUserEntry(uuid, text string) core.ChatMessage {
	return core.ChatMessage{ID: uuid, Role: core.ChatUser, Content: text}
}

// makeAssistantEntry creates an assistant text message for testing.
func makeAssistantEntry(uuid, text string) core.ChatMessage {
	return core.ChatMessage{ID: uuid, Role: core.ChatAssistant, Content: text}
}

// getEntryText returns a message's text.
func getEntryText(m core.ChatMessage) string {
	return m.Content
}

func TestSession_SaveAndLoad(t *testing.T) {
	store := newTestStore(t)

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:       "test-1",
			Title:    "Test Session",
			Provider: "fake",
			Model:    "fake-model",
			Cwd:      "/tmp/project",
		},
		Messages: []core.ChatMessage{
			makeUserEntry("u1", "hello"),
			makeAssistantEntry("a1", "hi there"),
		},
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := store.Load("test-1")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if loaded.Metadata.Title != "Test Session" {
		t.Errorf("expected title 'Test Session', got %q", loaded.Metadata.Title)
	}
	if loaded.Metadata.Provider != "fake" {
		t.Errorf("expected provider 'fake', got %q", loaded.Metadata.Provider)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("expected 2 entries, got %d", len(loaded.Messages))
	}
	if getEntryText(loaded.Messages[0]) != "hello" {
		t.Errorf("expected first entry 'hello', got %q", getEntryText(loaded.Messages[0]))
	}
}

func TestSession_List(t *testing.T) {
	store := newTestStore(t)

	for i, title := range []string{"First", "Second", "Third"} {
		sess := &session.Snapshot{
			Metadata: session.SessionMetadata{
				ID:        title,
				Title:     title,
				UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
			},
		}
		if err := store.Save(sess); err != nil {
			t.Fatalf("Save(%s) error: %v", title, err)
		}
		// Small sleep so timestamps differ
		time.Sleep(10 * time.Millisecond)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	if len(list) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(list))
	}

	// Sorted by update time, newest first
	if list[0].Title != "Third" {
		t.Errorf("expected newest first ('Third'), got %q", list[0].Title)
	}
}

func TestSession_GetLatest(t *testing.T) {
	store := newTestStore(t)

	sess1 := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "old", Title: "Old"},
	}
	if err := store.Save(sess1); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	sess2 := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "new", Title: "New"},
	}
	if err := store.Save(sess2); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	latest, err := store.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest() error: %v", err)
	}

	if latest.Metadata.Title != "New" {
		t.Errorf("expected latest 'New', got %q", latest.Metadata.Title)
	}
}

func TestSession_Delete(t *testing.T) {
	store := newTestStore(t)

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "to-delete", Title: "Delete Me"},
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	_, err := store.Load("to-delete")
	if err == nil {
		t.Error("expected error loading deleted session")
	}
}

func TestSession_AppendBehavior(t *testing.T) {
	store := newTestStore(t)

	// First save with 1 entry
	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:    "append-test",
			Title: "Append Test",
		},
		Messages: []core.ChatMessage{
			makeUserEntry("u1", "hello"),
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("first Save() error: %v", err)
	}

	// Second save with 3 entries (original + 2 new)
	sess.Messages = append(sess.Messages,
		makeAssistantEntry("a1", "hi there"),
		makeUserEntry("u2", "how are you?"),
	)
	if err := store.Save(sess); err != nil {
		t.Fatalf("second Save() error: %v", err)
	}

	// Load and verify all 3 entries are present
	loaded, err := store.Load("append-test")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(loaded.Messages) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(loaded.Messages))
	}
	if getEntryText(loaded.Messages[2]) != "how are you?" {
		t.Errorf("expected third entry 'how are you?', got %q", getEntryText(loaded.Messages[2]))
	}
}

func TestSession_MetadataUpdatesOnNewMessage(t *testing.T) {
	store := newTestStore(t)

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:    "metadata-update-test",
			Title: "Metadata Update Test",
		},
		Messages: []core.ChatMessage{
			makeUserEntry("u1", "hello"),
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("first Save() error: %v", err)
	}

	first, err := store.Load("metadata-update-test")
	if err != nil {
		t.Fatalf("first Load() error: %v", err)
	}
	if first.Metadata.MessageCount != 1 {
		t.Fatalf("first message count = %d, want 1", first.Metadata.MessageCount)
	}

	time.Sleep(10 * time.Millisecond)

	sess.Messages = append(sess.Messages, makeAssistantEntry("a1", "hi there"))
	if err := store.Save(sess); err != nil {
		t.Fatalf("second Save() error: %v", err)
	}

	second, err := store.Load("metadata-update-test")
	if err != nil {
		t.Fatalf("second Load() error: %v", err)
	}
	if second.Metadata.MessageCount != 2 {
		t.Errorf("second message count = %d, want 2", second.Metadata.MessageCount)
	}
	if !second.Metadata.UpdatedAt.After(first.Metadata.UpdatedAt) {
		t.Errorf("UpdatedAt did not advance: first=%v second=%v", first.Metadata.UpdatedAt, second.Metadata.UpdatedAt)
	}
}

func TestSession_MessageTypes_PersistRoundTrip(t *testing.T) {
	store := newTestStore(t)

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:       "message-types-roundtrip",
			Title:    "Message Types Roundtrip",
			Provider: "fake",
			Model:    "fake-model",
			Cwd:      "/tmp/project",
		},
		Messages: []core.ChatMessage{
			makeUserEntry("u1", "read this file"),
			{
				ID:                "a1",
				Role:              core.ChatAssistant,
				Content:           "I'll inspect it.",
				Thinking:          "need to inspect the file",
				ThinkingSignature: "sig-1",
				ToolCalls:         []core.ToolCall{{ID: "tc-1", Name: "Read", Input: `{"file_path":"/tmp/test.txt"}`}},
			},
			{
				ID:         "u2",
				Role:       core.ChatUser,
				ToolResult: &core.ToolResult{ToolCallID: "tc-1", ToolName: "Read", Content: ai.TextContent("file contents")},
			},
			makeAssistantEntry("a2", "done"),
		},
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := store.Load("message-types-roundtrip")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(loaded.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(loaded.Messages))
	}

	assistant := loaded.Messages[1]
	if assistant.Thinking != "need to inspect the file" || assistant.ThinkingSignature != "sig-1" {
		t.Errorf("thinking did not round-trip: %+v", assistant)
	}
	if assistant.Content != "I'll inspect it." {
		t.Errorf("assistant content did not round-trip: %q", assistant.Content)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != "Read" {
		t.Errorf("tool_use did not round-trip: %+v", assistant.ToolCalls)
	}

	userResult := loaded.Messages[2]
	if userResult.ToolResult == nil || userResult.ToolResult.ToolCallID != "tc-1" {
		t.Fatalf("tool_result did not round-trip: %+v", userResult.ToolResult)
	}
	if userResult.ToolResult.Content.Text() != "file contents" {
		t.Errorf("tool_result content mismatch: %q", userResult.ToolResult.Content.Text())
	}
	// ToolName is backfilled from the assistant tool_use block on load.
	if userResult.ToolResult.ToolName != "Read" {
		t.Errorf("tool_result ToolName = %q, want Read", userResult.ToolResult.ToolName)
	}
}

func TestSession_PersistToolResult(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}

	sessionID := "tool-overflow-test"
	toolCallID := "tc-abc123"
	content := strings.Repeat("x", 200_000) // 200KB

	if err := store.PersistToolResult(sessionID, toolCallID, content); err != nil {
		t.Fatalf("PersistToolResult() error: %v", err)
	}

	// Verify the file was created with correct content
	resultPath := filepath.Join(dir, "blobs", "tool-result", sessionID, toolCallID)
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("failed to read persisted tool result: %v", err)
	}
	if len(data) != 200_000 {
		t.Errorf("persisted content size = %d, want 200000", len(data))
	}

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:    sessionID,
			Title: "Overflow",
		},
		Messages: []core.ChatMessage{
			{
				ID:   "u1",
				Role: core.ChatUser,
				ToolResult: &core.ToolResult{
					ToolCallID: toolCallID,
					Content:    ai.TextContent("preview\n\n[Full output persisted to blobs/tool-result/" + sessionID + "/" + toolCallID + "]"),
				},
			},
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got := loaded.Messages[0].ToolResult.Content.Text()
	if got != content {
		t.Fatalf("hydrated tool result len = %d, want %d", len(got), len(content))
	}
}

// TestSession_JSONL_Integrity verifies that every line written to a JSONL
// session file is valid JSON. This guards against serialisation regressions
// where a malformed entry silently breaks session loading.
func TestSession_JSONL_Integrity(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:       "jsonl-integrity-test",
			Title:    "JSONL Integrity Test",
			Provider: "fake",
			Model:    "fake-model",
			Cwd:      "/tmp/project",
		},
		Messages: []core.ChatMessage{
			makeUserEntry("u1", "first message"),
			makeAssistantEntry("a1", "first response"),
			makeUserEntry("u2", "second message"),
			makeAssistantEntry("a2", "second response with special chars: <>&\"'"),
		},
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Read the raw JSONL file and verify every non-empty line is valid JSON.
	filePath := store.SessionPath(sess.Metadata.ID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	lines := strings.Split(string(data), "\n")
	validLines := 0
	for i, line := range lines {
		if line == "" {
			continue // trailing newline is expected
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d is not valid JSON: %v\ncontent: %s", i+1, err, line)
		} else {
			validLines++
		}
	}

	// Expect at least entries + 1 metadata line
	if validLines < len(sess.Messages)+1 {
		t.Errorf("expected at least %d valid JSON lines, got %d", len(sess.Messages)+1, validLines)
	}
}

// TestSession_ContinueRestoresMessages verifies that loading a session after
// multiple Save calls returns all messages in the original order. This
// simulates the "-c" (--continue) flag behaviour where the previous
// conversation must be fully replayed.
func TestSession_ContinueRestoresMessages(t *testing.T) {
	store := newTestStore(t)

	// Build a multi-turn conversation.
	turns := []struct{ role, text string }{
		{"user", "hello"},
		{"assistant", "hi there"},
		{"user", "what is 2+2?"},
		{"assistant", "4"},
		{"user", "thanks"},
	}

	var entries []core.ChatMessage
	for i, turn := range turns {
		uuid := fmt.Sprintf("id-%d", i)
		switch turn.role {
		case "user":
			entries = append(entries, makeUserEntry(uuid, turn.text))
		case "assistant":
			entries = append(entries, makeAssistantEntry(uuid, turn.text))
		}
	}

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:       "continue-test",
			Title:    "Continue Test",
			Provider: "fake",
			Model:    "fake-model",
			Cwd:      "/tmp/project",
		},
		Messages: entries,
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Simulate "-c": load the session and verify messages are in order.
	loaded, err := store.Load("continue-test")
	if err != nil {
		t.Fatalf("Load() (continue) error: %v", err)
	}

	if len(loaded.Messages) != len(turns) {
		t.Fatalf("expected %d entries after continue, got %d", len(turns), len(loaded.Messages))
	}

	for i, want := range turns {
		got := getEntryText(loaded.Messages[i])
		if got != want.text {
			t.Errorf("entry[%d]: want %q, got %q", i, want.text, got)
		}

		wantRole := core.ChatUser
		if want.role == "assistant" {
			wantRole = core.ChatAssistant
		}
		if loaded.Messages[i].Role != wantRole {
			t.Errorf("entry[%d]: want role %q, got %q", i, wantRole, loaded.Messages[i].Role)
		}
	}
}

// Regression: the append-only persistence path must not duplicate prior
// messages when Save is called repeatedly with the same UUIDs. The dedup
// cache hinges on stable IDs from upstream; this test pins that contract
// at the Store.Save boundary.
func TestSession_SaveTwice_NoDuplication(t *testing.T) {
	store := newTestStore(t)

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "save-twice"},
		Messages: []core.ChatMessage{
			makeUserEntry("m1", "hello"),
			makeAssistantEntry("m2", "hi"),
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save #1: %v", err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save #2: %v", err)
	}

	loaded, err := store.Load("save-twice")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 entries after two saves, got %d (dedup failed)", len(loaded.Messages))
	}

	// Count raw message records on disk too — projection only shows the
	// active chain, so a duplicated history could pass the entry count check.
	raw, err := os.ReadFile(store.SessionPath("save-twice"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	count := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"type":"message.appended"`) ||
			strings.Contains(line, `"type":"transcript.message.appended"`) {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("on-disk message records = %d, want 2 (dedup leaked duplicates)", count)
	}
}

// Regression: after a process restart the lastEmittedState cache is empty.
// If a user clears a previously-set field (Tag "urgent" → "") on the first
// save after restart, StateOpsDiff(zero, {Tag:""}) would see "" == "" and
// emit no op — leaving the stale value on disk forever. The store must
// rehydrate prev from disk on cold cache so the clear actually lands.
func TestSession_FirstSaveAfterRestart_PicksUpDiskState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	store1, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	first := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "restart-tag", Tag: "urgent"},
		Messages: []core.ChatMessage{makeUserEntry("m1", "hi")},
	}
	if err := store1.Save(first); err != nil {
		t.Fatalf("Save first: %v", err)
	}

	// Fresh store simulates a process restart — lastEmittedState is empty.
	store2, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir #2: %v", err)
	}
	cleared := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "restart-tag", Tag: ""},
		Messages: []core.ChatMessage{makeUserEntry("m1", "hi")},
	}
	if err := store2.Save(cleared); err != nil {
		t.Fatalf("Save cleared: %v", err)
	}

	loaded, err := store2.Load("restart-tag")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Metadata.Tag != "" {
		t.Fatalf("Tag = %q, want empty (clear did not survive cold cache)", loaded.Metadata.Tag)
	}
}

// Regression: clearing tasks (or worktree) on a subsequent Save must clear
// them on reload. Under append-only last-wins projection, StateOpsDiff
// must emit an empty-array tasks op when the previous snapshot had tasks
// and the new one doesn't, so absence-of-op doesn't resurrect stale state.
func TestSession_SaveClearedTasks_ClearsOnReload(t *testing.T) {
	store := newTestStore(t)

	withTasks := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "clear-tasks"},
		Messages: []core.ChatMessage{makeUserEntry("m1", "hi")},
		Tasks: []taskTracker.Item{{
			ID: "t1", Subject: "do thing", Status: "in_progress",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}},
	}
	if err := store.Save(withTasks); err != nil {
		t.Fatalf("Save with tasks: %v", err)
	}

	cleared := &session.Snapshot{
		Metadata: session.SessionMetadata{ID: "clear-tasks"},
		Messages: []core.ChatMessage{makeUserEntry("m1", "hi")},
		Tasks:    nil,
	}
	if err := store.Save(cleared); err != nil {
		t.Fatalf("Save cleared: %v", err)
	}

	loaded, err := store.Load("clear-tasks")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Tasks) != 0 {
		t.Fatalf("expected tasks to be cleared, got %d (stale tasks resurrected)", len(loaded.Tasks))
	}
}
