package transcript

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

// startSession writes a transcript the way a real session does.
func startSession(t *testing.T, fs *FileStore, id string) {
	t.Helper()
	if err := fs.Start(context.Background(), StartCommand{
		SessionID: id,
		ProjectID: "proj",
		Provider:  "anthropic",
		Model:     "model",
		Time:      time.Now(),
	}); err != nil {
		t.Fatalf("Start(%s): %v", id, err)
	}
	if err := fs.AppendMessage(context.Background(), AppendMessageCommand{
		SessionID: id,
		MessageID: id + "-m1",
		Time:      time.Now(),
		Role:      "user",
		Content:   []ContentBlock{{Type: "text", Text: "hello from " + id}},
	}); err != nil {
		t.Fatalf("AppendMessage(%s): %v", id, err)
	}
	if err := fs.FlushIndex(); err != nil {
		t.Fatalf("FlushIndex: %v", err)
	}
}

func listIDs(t *testing.T, fs *FileStore) []string {
	t.Helper()
	items, err := fs.List(context.Background(), "proj", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.SessionID)
	}
	return ids
}

// Losing transcripts-index.json used to make every prior session permanently
// invisible in /resume. Start runs at the first turn of the next session, and
// it replaced the unreadable index with an empty one — so by the time the
// picker looked, the file parsed fine and its own rebuild never fired. The
// .jsonl transcripts sat on disk with no way back to them.
func TestStartRebuildsARemovedIndexInsteadOfDiscardingIt(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "proj")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	startSession(t, fs, "older-1")
	startSession(t, fs, "older-2")

	// Restore from a backup that skipped the index, or a truncating crash.
	if err := os.Remove(fs.indexPath()); err != nil {
		t.Fatalf("remove index: %v", err)
	}

	// A fresh process: new store, no cached index, then a new session starts.
	reopened, err := NewFileStore(dir, "proj")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	startSession(t, reopened, "new-3")

	ids := listIDs(t, reopened)
	for _, want := range []string{"older-1", "older-2", "new-3"} {
		if !contains(ids, want) {
			t.Errorf("session %q is missing from the picker; got %v", want, ids)
		}
	}
}

// An unparseable index is the same situation as a missing one.
func TestStartRebuildsACorruptIndex(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "proj")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	startSession(t, fs, "older-1")

	if err := os.WriteFile(fs.indexPath(), []byte("{ truncated"), 0o644); err != nil {
		t.Fatalf("corrupt index: %v", err)
	}

	reopened, err := NewFileStore(dir, "proj")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	startSession(t, reopened, "new-2")

	if ids := listIDs(t, reopened); !contains(ids, "older-1") {
		t.Errorf("the pre-existing session was dropped; got %v", ids)
	}
}

// A brand new store has no transcripts to rebuild from; the empty index is the
// right answer and must not be treated as a failure.
func TestStartOnAnEmptyStoreStillWorks(t *testing.T) {
	fs, err := NewFileStore(t.TempDir(), "proj")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	startSession(t, fs, "first")

	if ids := listIDs(t, fs); !contains(ids, "first") {
		t.Errorf("the first session is missing; got %v", ids)
	}
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
