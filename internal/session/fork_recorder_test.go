package session

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"os"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// A Recorder's session is fixed at construction, so forking has to get a new
// one. The live agent holds an onEvent closure over whichever Recorder it was
// built with, which is why app.forkSession stops the agent: the rebuild is what
// picks the new Recorder up. These pin the two halves of that contract.

func TestNewRecorderRebindsAfterTheSessionIDChanges(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	setup := &Setup{Store: store, SessionID: "parent"}

	parentRec := setup.NewRecorder("main", "anthropic", "model", 1000)
	if parentRec == nil {
		t.Fatal("NewRecorder returned nil for the parent session")
	}
	if got := parentRec.SessionID(); got != "parent" {
		t.Fatalf("parent recorder session = %q, want %q", got, "parent")
	}

	// Forking re-points the Setup at the new session.
	setup.SetID("fork")

	forkRec := setup.NewRecorder("main", "anthropic", "model", 1000)
	if forkRec == nil {
		t.Fatal("NewRecorder returned nil after the session changed")
	}
	if got := forkRec.SessionID(); got != "fork" {
		t.Errorf("recorder session = %q, want %q — the cache did not invalidate", got, "fork")
	}
	if forkRec == parentRec {
		t.Error("the parent's Recorder was reused; records would land in the parent transcript")
	}
}

// Setup.Recorder deliberately refuses a stale recorder rather than letting a
// caller write into the wrong transcript. That guard is what the agent's
// captured closure bypasses, so it is worth pinning.
func TestSetupRecorderRefusesAStaleRecorder(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	setup := &Setup{Store: store, SessionID: "parent"}

	if setup.NewRecorder("main", "anthropic", "model", 1000) == nil {
		t.Fatal("NewRecorder returned nil for the parent session")
	}
	if setup.Recorder() == nil {
		t.Fatal("Recorder() should return the recorder bound to the current session")
	}

	setup.SetID("fork")

	if got := setup.Recorder(); got != nil {
		t.Errorf("Recorder() handed back a recorder bound to %q while the session is %q",
			got.SessionID(), "fork")
	}
}

// Post-fork messages must land in the fork's transcript, not the parent's.
// This drives the recorder through OnAgentEvent — the exact closure the live
// agent captures — and reads both transcripts back.
func TestForkRecorderWritesToTheForkNotTheParent(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	setup := &Setup{Store: store, SessionID: "parent"}
	parentRec := setup.NewRecorder("main", "anthropic", "model", 1000)
	// The live agent stamps an id in append(); OnAppend drops messages without
	// one, so the harness has to do the same.
	parentRec.OnAgentEvent(core.AppendEvent("main", core.Message{
		ID: "m1", Role: ai.RoleUser, Content: "before the fork",
	}))

	// Fork: the session id moves, and the rebuilt agent picks up a recorder
	// bound to it. Using parentRec here instead is the bug.
	setup.SetID("fork")
	forkRec := setup.NewRecorder("main", "anthropic", "model", 1000)
	forkRec.OnAgentEvent(core.AppendEvent("main", core.Message{
		ID: "m2", Role: ai.RoleUser, Content: "after the fork",
	}))

	parentBody := readTranscript(t, store, "parent")
	forkBody := readTranscript(t, store, "fork")

	if !strings.Contains(forkBody, "after the fork") {
		t.Error("the post-fork message is missing from the fork transcript")
	}
	if strings.Contains(parentBody, "after the fork") {
		t.Error("the post-fork message landed in the parent transcript; " +
			"resuming the original would replay history from after the fork")
	}
	if !strings.Contains(parentBody, "before the fork") {
		t.Error("the parent lost its own pre-fork history")
	}
}

func readTranscript(t *testing.T, store *Store, sessionID string) string {
	t.Helper()
	data, err := os.ReadFile(store.transcriptStore.TranscriptPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read transcript %s: %v", sessionID, err)
	}
	return string(data)
}
