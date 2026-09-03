package session

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"testing"

	"github.com/genai-io/san/internal/core"
)

func TestStoreSaveAndLoadPreservesCustomTitle(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}

	msgs := []core.Message{
		{Role: ai.RoleUser, Content: "hello"},
		{Role: ai.RoleAssistant, Content: "world"},
	}

	// Save with a custom title
	sess := &Snapshot{
		Metadata: SessionMetadata{
			Title: "My Custom Session",
		},
		Messages: msgs,
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	id := sess.Metadata.ID
	if id == "" {
		t.Fatal("Save did not assign session ID")
	}

	// Load and verify title
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Metadata.Title != "My Custom Session" {
		t.Fatalf("Title = %q, want %q", loaded.Metadata.Title, "My Custom Session")
	}
}

func TestStoreSaveTitleChangePersists(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}

	msgs := []core.Message{
		{Role: ai.RoleUser, Content: "hello"},
	}

	// Save with initial title
	sess := &Snapshot{
		Metadata: SessionMetadata{
			Title: "Initial Title",
		},
		Messages: msgs,
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() initial: %v", err)
	}

	id := sess.Metadata.ID

	// Rename: change the title and save again
	sess.Metadata.Title = "Renamed Title"
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save() rename: %v", err)
	}

	// Load and verify new title
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Metadata.Title != "Renamed Title" {
		t.Fatalf("Title = %q, want %q after rename", loaded.Metadata.Title, "Renamed Title")
	}
}

func TestStoreListPreservesCustomTitle(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}

	msgs := []core.Message{
		{Role: ai.RoleUser, Content: "first message"},
	}

	sess := &Snapshot{
		Metadata: SessionMetadata{
			Title: "Session From Listing",
		},
		Messages: msgs,
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	// List and verify title appears in listing
	list, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected at least 1 session in listing")
	}
	found := false
	for _, meta := range list {
		if meta.Title == "Session From Listing" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected title %q in listing, got: %+v", "Session From Listing", list)
	}
}
