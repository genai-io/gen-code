package session

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session/transcript"
)

// TestRecorderWritesMessageBeforeInference confirms the "causes before
// consumers" invariant: when the agent appends a user message and immediately
// fires PreInfer, the recorder must land message.appended on disk first.
//
// Before the OnAppend wiring, message.appended was written by Store.Save at
// turn end, so the on-disk order was inference.requested → message.appended,
// which made the viewer's "context at this inference" view incorrect (the
// active chain looked empty).
func TestRecorderWritesMessageBeforeInference(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-order", Cwd: "/tmp", Provider: "anthropic", Model: "claude-x", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-order", AgentID: "main",
		Provider: "anthropic", Model: "claude-x", MaxTokens: 4096, Cwd: "/tmp",
	})

	// Simulate one turn: user message appends, then PreInfer fires referencing it.
	userMsg := core.Message{ID: "m1", Role: ai.RoleUser, Content: "hello"}
	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: userMsg})

	rec.OnAgentEvent(core.Event{Type: core.PreInfer, Source: "main", Data: core.InferenceContext{
		SystemDigest: "sha256:sys", ToolsDigest: "sha256:tools", MessageIDs: []string{"m1"},
	}})

	// Assistant reply appends after PostInfer.
	rec.OnAgentEvent(core.Event{Type: core.PostInfer, Source: "main", Data: &core.InferResponse{
		StopReason: core.StopEndTurn, Usage: core.Usage{InputTokens: 10, OutputTokens: 5},
	}})
	assistantMsg := core.Message{ID: "m2", Role: ai.RoleAssistant, Content: "hi"}
	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: assistantMsg})

	records := readAllRecords(t, dir, "sess-order")

	// Find indices of key records.
	idxOf := func(predicate func(transcript.Record) bool) int {
		for i, r := range records {
			if predicate(r) {
				return i
			}
		}
		return -1
	}
	iUserMsg := idxOf(func(r transcript.Record) bool {
		return r.Type == transcript.MessageAppended && r.Message != nil && r.Message.MessageID == "m1"
	})
	iReq := idxOf(func(r transcript.Record) bool { return r.Type == transcript.InferenceRequested })
	iResp := idxOf(func(r transcript.Record) bool { return r.Type == transcript.InferenceResponded })
	iAssistantMsg := idxOf(func(r transcript.Record) bool {
		return r.Type == transcript.MessageAppended && r.Message != nil && r.Message.MessageID == "m2"
	})

	if iUserMsg < 0 || iReq < 0 || iResp < 0 || iAssistantMsg < 0 {
		t.Fatalf("missing records: userMsg=%d req=%d resp=%d assistantMsg=%d (have %d)",
			iUserMsg, iReq, iResp, iAssistantMsg, len(records))
	}

	// "Causes before consumers": user message before its inference.requested.
	if iUserMsg >= iReq {
		t.Errorf("user message at idx %d should precede inference.requested at idx %d", iUserMsg, iReq)
	}
	// Assistant message lands after the responded boundary.
	if iAssistantMsg <= iResp {
		t.Errorf("assistant message at idx %d should follow inference.responded at idx %d", iAssistantMsg, iResp)
	}
	// And of course request precedes response.
	if iReq >= iResp {
		t.Errorf("inference.requested at idx %d must precede inference.responded at idx %d", iReq, iResp)
	}

	// Parent linkage: m2 should chain off m1.
	for _, r := range records {
		if r.Type == transcript.MessageAppended && r.Message != nil && r.Message.MessageID == "m2" {
			if r.ParentID != "m1" {
				t.Errorf("m2.parentId = %q, want m1", r.ParentID)
			}
		}
	}
}

// When the recorder writes message.appended via OnAppend, a follow-up
// Store.Save on the TUI path (OmitMessageWrites=true) must NOT add a second
// copy of the same message under a different ID. Without this guard the
// JSONL would balloon by ~2× and the active chain projection would see
// duplicate messages.
func TestRecorderAndSaveDoNotDoubleWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	fs := store.transcriptStore

	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-dup", Cwd: dir, Provider: "p", Model: "m", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-dup", AgentID: "main", Cwd: dir,
	})

	// Recorder writes the two messages (agent-side path).
	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: core.Message{
		ID: "agent-m1", Role: ai.RoleUser, Content: "hi",
	}})
	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: core.Message{
		ID: "agent-m2", Role: ai.RoleAssistant, Content: "hello",
	}})

	// TUI then calls Save with its own ChatMessage IDs. Without OmitMessageWrites,
	// these would each spawn a second message.appended record.
	snap := &Snapshot{
		Metadata: SessionMetadata{ID: "sess-dup", Cwd: dir},
		Messages: []core.ChatMessage{
			{ID: "tui-m1", Role: core.ChatUser, Content: "hi"},
			{ID: "tui-m2", Role: core.ChatAssistant, Content: "hello"},
		},
		OmitMessageWrites: true,
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	count := 0
	for _, r := range readAllRecords(t, dir, "sess-dup") {
		if r.Type == transcript.MessageAppended {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("message.appended count = %d, want 2 (no duplicates)", count)
	}
}

// permission.required and permission.decided must share the same RequestID
// so audit consumers can join the pair without timestamp heuristics. The ID
// is supplied by the PermissionRules closure and flows through
// PermGateRequest unchanged.
func TestRecorderPermissionRequiredDecidedShareRequestID(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-perm", Cwd: "/tmp", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-perm", AgentID: "main",
	})

	rec.RecordPermissionRequired(transcript.PermissionRecord{
		RequestID: "req-42", Tool: "Bash", Source: "ask", Mode: "normal",
	})
	rec.RecordPermissionDecided(transcript.PermissionRecord{
		RequestID: "req-42", Tool: "Bash", Decision: "permit", Source: "user", Mode: "normal",
	})

	var req, dec transcript.Record
	for _, r := range readAllRecords(t, dir, "sess-perm") {
		switch r.Type {
		case transcript.PermissionRequired:
			req = r
		case transcript.PermissionDecided:
			dec = r
		}
	}
	if req.Permission == nil || dec.Permission == nil {
		t.Fatalf("missing pair: req=%+v dec=%+v", req, dec)
	}
	if req.Permission.RequestID == "" {
		t.Fatalf("required RequestID empty")
	}
	if req.Permission.RequestID != dec.Permission.RequestID {
		t.Errorf("requestId mismatch: required=%q decided=%q",
			req.Permission.RequestID, dec.Permission.RequestID)
	}
}

// On Continue/Resume the agent reloads prior messages via SetMessages
// without firing OnAppend. If the recorder doesn't seed lastMessageID from
// the existing transcript, the first new message lands with parentId=""
// and the replay's leaf walk drops the entire loaded history.
func TestRecorderSeedsLastMessageIDFromExistingTranscript(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-seed")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-seed", Cwd: "/tmp", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Seed the file with two pre-existing messages, simulating a session
	// the user is now resuming.
	rec0 := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-seed", AgentID: "main",
	})
	rec0.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: core.Message{
		ID: "old-m1", Role: ai.RoleUser, Content: "first turn",
	}})
	rec0.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: core.Message{
		ID: "old-m2", Role: ai.RoleAssistant, Content: "first reply",
	}})

	// Simulate process restart with a fresh recorder; without seeding it
	// would write the next message with parentId="" and break the chain.
	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-seed", AgentID: "main",
	})
	rec.seedLastMessageID("old-m2")

	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: core.Message{
		ID: "new-m3", Role: ai.RoleUser, Content: "second turn",
	}})

	var newMsg transcript.Record
	for _, r := range readAllRecords(t, dir, "sess-seed") {
		if r.Type == transcript.MessageAppended && r.Message != nil && r.Message.MessageID == "new-m3" {
			newMsg = r
		}
	}
	if newMsg.Message == nil {
		t.Fatalf("new-m3 not persisted")
	}
	if newMsg.ParentID != "old-m2" {
		t.Errorf("new message ParentID = %q, want old-m2 (seeding failed)", newMsg.ParentID)
	}
}

// messagesFromNodes must propagate the node ID into core.Message.ID. Otherwise
// a resumed session sends PreInfer with only the new message's ID, but the
// replay sees the full chain — triggering a spurious integrity warning on
// every inference after a Continue/Resume.
func TestMessagesFromNodesPropagatesIDToMessage(t *testing.T) {
	nodes := []transcript.Node{
		{
			ID:   "u-1",
			Role: "user",
			Content: []transcript.ContentBlock{
				{Type: "text", Text: "hello"},
			},
		},
		{
			ID:   "a-1",
			Role: "assistant",
			Content: []transcript.ContentBlock{
				{Type: "text", Text: "hi"},
			},
		},
	}
	msgs := messagesFromNodes(nodes)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].ID != "u-1" {
		t.Errorf("msgs[0].ID = %q, want u-1 (loaded message lost its ID)", msgs[0].ID)
	}
	if msgs[1].ID != "a-1" {
		t.Errorf("msgs[1].ID = %q, want a-1", msgs[1].ID)
	}
}

// Permission records must carry the tool input so audit consumers can see
// *what* was being adjudicated (Bash command, file path, etc.) without
// cross-referencing the surrounding tool_use block.
func TestRecorderPermissionRecordsCarryToolInput(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-perm-input")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-pi", Cwd: "/tmp", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-pi", AgentID: "main",
	})

	rec.RecordPermissionRequired(transcript.PermissionRecord{
		RequestID: "rq-1", Tool: "Bash",
		Input:  []byte(`{"command":"git status"}`),
		Source: "ask", Mode: "normal",
	})
	// Scope is an opaque label set by the caller (the approval modal owns
	// the vocabulary); transcript schema treats it as a pass-through string.
	rec.RecordPermissionDecided(transcript.PermissionRecord{
		RequestID: "rq-1", Tool: "Bash",
		Input:    []byte(`{"command":"git status"}`),
		Decision: transcript.PermissionPermit,
		Source:   transcript.PermissionSourceUser,
		Scope:    "session",
		Reason:   "user approved", Mode: "normal",
	})

	var reqRec, decRec transcript.Record
	for _, r := range readAllRecords(t, dir, "sess-pi") {
		switch r.Type {
		case transcript.PermissionRequired:
			reqRec = r
		case transcript.PermissionDecided:
			decRec = r
		}
	}
	if reqRec.Permission == nil || decRec.Permission == nil {
		t.Fatalf("missing pair: req=%+v dec=%+v", reqRec, decRec)
	}
	if got := string(reqRec.Permission.Input); got != `{"command":"git status"}` {
		t.Errorf("required input on disk = %q, want %q", got, `{"command":"git status"}`)
	}
	if decRec.Permission.Scope != "session" {
		t.Errorf("decided scope = %q, want %q", decRec.Permission.Scope, "session")
	}
}

// A blank-content control signal (e.g. SigStop carrier) must not produce a
// message.appended record on the wire — those entries aren't part of the
// model-visible chain.
func TestRecorderSkipsEmptyMessages(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-skip", Cwd: "/tmp", Provider: "p", Model: "m", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec := NewRecorder(RecorderOptions{FileStore: fs, SessionID: "sess-skip", AgentID: "main"})

	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Source: "main", Data: core.Message{
		ID: "empty-1", Role: ai.RoleUser, // no content, no tool result, no images
	}})

	for _, r := range readAllRecords(t, dir, "sess-skip") {
		if r.Type == transcript.MessageAppended {
			t.Fatalf("empty message produced a record: %+v", r)
		}
	}
}
