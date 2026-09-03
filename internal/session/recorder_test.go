package session

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session/transcript"
)

// One turn through PreInfer + PostInfer must produce one inference.requested
// and one inference.responded record, with the request's MessageIDs/digests
// preserved and the response's usage/stop_reason populated. The turn counter
// links the pair.
func TestRecorderWritesRequestedAndRespondedPerTurn(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-1", Cwd: "/tmp", Provider: "anthropic", Model: "claude-x", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-1", AgentID: "main",
		Provider: "anthropic", Model: "claude-x", MaxTokens: 4096,
	})

	rec.OnAgentEvent(core.Event{Type: core.PreInfer, Source: "main", Data: core.InferenceContext{
		SystemDigest: "sha256:sys", ToolsDigest: "sha256:tools", MessageIDs: []string{"m1", "m2"},
	}})
	rec.OnAgentEvent(core.Event{Type: core.PostInfer, Source: "main", Data: &core.InferResponse{
		StopReason: core.StopEndTurn, Usage: core.Usage{InputTokens: 42, OutputTokens: 8, CacheReadInputTokens: 10},
	}})

	tx, err := fs.Load(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Project() flattens to messages; inspect raw records via a re-load instead.
	// Easiest path: read the file directly via Load and look at the messages —
	// but inference records aren't messages. Use the file to read raw records.
	_ = tx

	// Read raw records from disk to assert on inference records.
	records := readAllRecords(t, dir, "sess-1")

	var reqs, resps []transcript.Record
	for _, r := range records {
		switch r.Type {
		case transcript.InferenceRequested:
			reqs = append(reqs, r)
		case transcript.InferenceResponded:
			resps = append(resps, r)
		}
	}
	if len(reqs) != 1 {
		t.Fatalf("inference.requested count = %d, want 1", len(reqs))
	}
	if len(resps) != 1 {
		t.Fatalf("inference.responded count = %d, want 1", len(resps))
	}

	req := reqs[0].Inference
	if req == nil {
		t.Fatal("request record missing Inference payload")
	}
	if req.Turn != 1 {
		t.Fatalf("requested.Turn = %d, want 1", req.Turn)
	}
	if req.SystemDigest != "sha256:sys" || req.ToolsDigest != "sha256:tools" {
		t.Fatalf("requested digests = %+v", req)
	}
	if len(req.MessageIDs) != 2 || req.MessageIDs[0] != "m1" {
		t.Fatalf("requested.MessageIDs = %+v", req.MessageIDs)
	}

	resp := resps[0].Inference
	if resp == nil {
		t.Fatal("response record missing Inference payload")
	}
	if resp.Turn != 1 {
		t.Fatalf("responded.Turn = %d, want 1 (must match request)", resp.Turn)
	}
	if resp.StopReason != string(core.StopEndTurn) {
		t.Fatalf("responded.StopReason = %q", resp.StopReason)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 42 || resp.Usage.OutputTokens != 8 || resp.Usage.CacheReadInputTokens != 10 {
		t.Fatalf("responded.Usage = %+v", resp.Usage)
	}
}

// System mutations must produce one system.section.added per added/replaced
// section and one system.section.removed per dropped section. Caller info is
// preserved verbatim so trace consumers can answer "who changed this?".
func TestRecorderWritesSystemSectionEvents(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-sys", Cwd: "/tmp", Provider: "p", Model: "m", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-sys", AgentID: "main",
		Provider: "p", Model: "m", MaxTokens: 1,
	})

	rec.OnAgentEvent(core.Event{Type: core.OnSystemChange, Data: core.SystemChange{
		Name: "identity", Slot: 0, Content: "You are X", Caller: "system:init",
	}})
	rec.OnAgentEvent(core.Event{Type: core.OnSystemChange, Data: core.SystemChange{
		Name: "identity", Slot: 0, Content: "You are Y", Caller: "command:/identity",
	}})
	rec.OnAgentEvent(core.Event{Type: core.OnSystemChange, Data: core.SystemChange{
		Name: "policy", Removed: true, Caller: "test:teardown",
	}})

	records := readAllRecords(t, dir, "sess-sys")
	var added, removed []transcript.Record
	for _, r := range records {
		switch r.Type {
		case transcript.SystemSectionAdded:
			added = append(added, r)
		case transcript.SystemSectionRemoved:
			removed = append(removed, r)
		}
	}
	if len(added) != 2 {
		t.Fatalf("system.section.added count = %d, want 2", len(added))
	}
	if len(removed) != 1 {
		t.Fatalf("system.section.removed count = %d, want 1", len(removed))
	}
	if added[1].System.Caller != "command:/identity" || added[1].System.Content != "You are Y" {
		t.Fatalf("replaced section payload = %+v", added[1].System)
	}
	if removed[0].System.Name != "policy" || removed[0].System.Caller != "test:teardown" {
		t.Fatalf("removed section payload = %+v", removed[0].System)
	}
}

// Tool registry changes must produce one tool.added per Add and one
// tool.removed per Remove. Schema/Name and Caller are preserved.
func TestRecorderWritesToolsChangeEvents(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-tools", Cwd: "/tmp", Provider: "p", Model: "m", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-tools", AgentID: "main",
		Provider: "p", Model: "m", MaxTokens: 1,
	})

	rec.OnAgentEvent(core.Event{Type: core.OnToolsChange, Data: core.ToolsChange{
		Schema: core.ToolSchema{Name: "Read", Description: "read a file"},
		Caller: "tools:init",
	}})
	rec.OnAgentEvent(core.Event{Type: core.OnToolsChange, Data: core.ToolsChange{
		Schema: core.ToolSchema{Name: "Bash", Description: "run shell"},
		Caller: "mcp:Bash",
	}})
	rec.OnAgentEvent(core.Event{Type: core.OnToolsChange, Data: core.ToolsChange{
		Name: "Bash", Removed: true, Caller: "mode:plan",
	}})

	records := readAllRecords(t, dir, "sess-tools")
	var added, removed []transcript.Record
	for _, r := range records {
		switch r.Type {
		case transcript.ToolAdded:
			added = append(added, r)
		case transcript.ToolRemoved:
			removed = append(removed, r)
		}
	}
	if len(added) != 2 {
		t.Fatalf("tool.added count = %d, want 2", len(added))
	}
	if len(removed) != 1 {
		t.Fatalf("tool.removed count = %d, want 1", len(removed))
	}
	if added[0].Tool.Schema == nil || added[0].Tool.Schema.Name != "Read" || added[0].Tool.Caller != "tools:init" {
		t.Fatalf("first added record = %+v", added[0].Tool)
	}
	if removed[0].Tool.Name != "Bash" || removed[0].Tool.Caller != "mode:plan" {
		t.Fatalf("removed record = %+v", removed[0].Tool)
	}
}

// Compaction must persist the summary as a message.appended and a
// session.compacted record whose SummaryMessageID is that summary's ID, so replay
// reconstructs the compacted chain instead of the summarized-away history.
func TestRecorderWritesCompactionBoundary(t *testing.T) {
	dir := t.TempDir()
	fs, err := transcript.NewFileStore(dir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Start(context.Background(), transcript.StartCommand{
		SessionID: "sess-c", Cwd: "/tmp", Provider: "anthropic", Model: "claude-x", Time: time.Now(),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	rec := NewRecorder(RecorderOptions{
		FileStore: fs, SessionID: "sess-c", AgentID: "main",
		Provider: "anthropic", Model: "claude-x", MaxTokens: 4096,
	})

	// A pre-compaction message, then the summary append + compaction boundary.
	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Data: core.Message{ID: "m1", Role: ai.RoleUser, Content: "hi"}})
	rec.OnAgentEvent(core.Event{Type: core.OnAppend, Data: core.Message{ID: "sum-1", Role: ai.RoleUser, Content: "Previous context:\nsummary"}})
	rec.OnAgentEvent(core.Event{Type: core.OnCompact, Data: core.CompactInfo{Summary: "summary", OriginalCount: 5, SummaryMessageID: "sum-1"}})

	records := readAllRecords(t, dir, "sess-c")
	var summaryAppended bool
	var boundary string
	for _, r := range records {
		if r.Type == transcript.MessageAppended && r.Message != nil && r.Message.MessageID == "sum-1" {
			summaryAppended = true
		}
		if r.Type == transcript.SessionCompacted && r.Session != nil {
			boundary = r.Session.SummaryMessageID
		}
	}
	if !summaryAppended {
		t.Fatal("summary message.appended was not recorded")
	}
	if boundary != "sum-1" {
		t.Fatalf("compaction boundary = %q, want sum-1", boundary)
	}
}

// A nil-safe recorder (no FileStore) must accept events without panicking.
func TestRecorderNilSafe(t *testing.T) {
	var rec *Recorder
	rec.OnAgentEvent(core.Event{Type: core.PreInfer})

	empty := NewRecorder(RecorderOptions{})
	empty.OnAgentEvent(core.Event{Type: core.PreInfer})
}

func readAllRecords(t *testing.T, baseDir, sessionID string) []transcript.Record {
	t.Helper()
	fs, err := transcript.NewFileStore(baseDir, "proj-1")
	if err != nil {
		t.Fatalf("NewFileStore (reread): %v", err)
	}
	path := fs.TranscriptPath(sessionID)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()

	var out []transcript.Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r transcript.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode record: %v", err)
		}
		out = append(out, r)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
	return out
}
