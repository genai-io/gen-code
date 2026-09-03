package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session/transcript"
	"github.com/genai-io/san/internal/todo"
)

type Store struct {
	mu              sync.RWMutex
	cwd             string
	projectID       string
	projectDir      string
	transcriptStore *transcript.FileStore

	// lastEmittedState caches the projected state most recently written for each
	// session so Save emits only changed paths. Cold-start after a process
	// restart pays one full snapshot per session (no item in the cache means
	// every non-default field looks "changed" vs the zero state, which is what
	// we want — the projector still applies last-wins).
	lastEmittedState map[string]transcript.State
}

type Snapshot struct {
	Metadata SessionMetadata
	Messages []core.ChatMessage
	Tasks    []todo.Item

	// OmitMessageWrites skips the per-item AppendMessage loop in Save. Used
	// by the main TUI path where the agent's Recorder already persists every
	// message in causal order via the OnAppend event — running Save's loop on
	// top would write a second copy under a different ID (the TUI
	// ChatMessage.ID, which doesn't match the agent's core.Message.ID).
	//
	// The subagent path leaves this false: its agent has no Recorder wired,
	// so Save is the only writer for those messages.
	//
	// Messages are still used to derive title / lastPrompt / messageCount in
	// NormalizeMetadata regardless of this flag.
	OmitMessageWrites bool
}

func NewStore(cwd string) (*Store, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	projectID := encodePath(cwd)
	projectDir := filepath.Join(confdir.Dir(homeDir), "projects", projectID)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create project directory: %w", err)
	}

	txStore, err := transcript.NewFileStore(projectDir, projectID)
	if err != nil {
		return nil, err
	}

	return &Store{
		cwd:             cwd,
		projectID:       projectID,
		projectDir:      projectDir,
		transcriptStore: txStore,
	}, nil
}

func NewStoreWithDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	txStore, err := transcript.NewFileStore(dir, encodePath(dir))
	if err != nil {
		return nil, fmt.Errorf("create transcript store: %w", err)
	}
	return &Store{
		cwd:             dir,
		projectID:       encodePath(dir),
		projectDir:      dir,
		transcriptStore: txStore,
	}, nil
}

func (s *Store) SessionPath(sessionID string) string {
	if s.transcriptStore != nil {
		return s.transcriptStore.TranscriptPath(sessionID)
	}
	return filepath.Join(s.projectDir, "transcripts", sessionID+".jsonl")
}

func (s *Store) List() ([]*SessionMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, err := s.transcriptStore.List(context.Background(), s.projectID, transcript.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := make([]*SessionMetadata, 0, len(items))
	for _, item := range items {
		meta := transcript.MetadataFromListItem(item, s.cwd)
		out = append(out, &meta)
	}
	return out, nil
}

func (s *Store) GetLatest() (*Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, err := s.transcriptStore.List(context.Background(), s.projectID, transcript.ListOptions{})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}
	meta := transcript.MetadataFromListItem(items[0], s.cwd)
	return s.loadSnapshot(context.Background(), meta.ID)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.transcriptStore.Delete(context.Background(), id); err != nil {
		return err
	}
	_ = os.RemoveAll(s.toolResultsDir(id))
	return nil
}

func (s *Store) Load(id string) (*Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, err := s.loadSnapshot(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// Save persists the snapshot via append-only writes:
//
//  1. Start (idempotent; no-op if the transcript already exists)
//  2. AppendMessage per item (deduped by message ID — cheap re-saves)
//  3. PatchState with the full projected state (last-wins on read)
//
// No file rewrites; each call writes only the records that didn't already
// exist plus a single state-patch record.
func (s *Store) Save(sess *Snapshot) error {
	if sess == nil {
		return fmt.Errorf("session is nil")
	}

	// Shell out before acquiring the lock to avoid blocking other store ops.
	gitBranch := getGitBranch(s.cwd)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.transcriptStore == nil {
		return fmt.Errorf("transcript store not configured")
	}

	now := time.Now()
	NormalizeMetadata(&sess.Metadata, sess.Messages, s.cwd, now)

	ctx := context.Background()
	id := sess.Metadata.ID

	if err := s.transcriptStore.Start(ctx, transcript.StartCommand{
		SessionID: id,
		ProjectID: s.projectID,
		Cwd:       sess.Metadata.Cwd,
		Provider:  sess.Metadata.Provider,
		Model:     sess.Metadata.Model,
		ParentID:  sess.Metadata.ParentSessionID,
		Time:      sess.Metadata.CreatedAt,
	}); err != nil {
		return err
	}

	if !sess.OmitMessageWrites {
		nodes := messagesToNodes(sess.Messages, sess.Metadata.Cwd, sess.Metadata.CreatedAt, gitBranch)
		for _, n := range nodes {
			if err := s.transcriptStore.AppendMessage(ctx, transcript.AppendMessageCommand{
				SessionID: id,
				MessageID: n.ID,
				ParentID:  n.ParentID,
				Time:      n.Time,
				GitBranch: n.GitBranch,
				Role:      n.Role,
				Content:   n.Content,
			}); err != nil {
				return err
			}
		}
	}

	next := transcript.State{
		Title:      sess.Metadata.Title,
		LastPrompt: sess.Metadata.LastPrompt,
		Tag:        sess.Metadata.Tag,
		Mode:       sess.Metadata.Mode,
		AutoPilot:  sess.Metadata.AutoPilot,
		Tasks:      transcript.TrackerItemViewsFromItems(sess.Tasks),
	}
	if s.lastEmittedState == nil {
		s.lastEmittedState = make(map[string]transcript.State)
	}
	// On the first save in this process the cache is cold; without
	// rehydrating prev from disk, a clear (e.g. title "Foo" → "") would
	// look like zero→zero and emit no op, leaving the stale value on disk.
	prev, hadPrev := s.lastEmittedState[id]
	if !hadPrev {
		if loaded, err := s.transcriptStore.LoadState(ctx, id); err == nil {
			prev = loaded
		}
	}
	ops := transcript.StateOpsDiff(prev, next)
	if err := s.transcriptStore.PatchState(ctx, transcript.PatchStateCommand{
		SessionID: id,
		Time:      now,
		Ops:       ops,
	}); err != nil {
		return err
	}
	s.lastEmittedState[id] = next
	// End of a turn (main path) or of a subagent's message dump: flush the
	// index mutations the appends above staged in memory to disk in one write.
	if err := s.transcriptStore.FlushIndex(); err != nil {
		return err
	}
	return nil
}

// FlushIndex writes any transcript-index mutations the hot append path staged
// in memory out to disk. Callers invoke it on shutdown; Save already flushes at
// each turn boundary. The in-memory index stays authoritative for in-process
// reads, and a crash that loses an unflushed update is recovered by the
// rebuild-from-transcripts path on the next List.
func (s *Store) FlushIndex() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.transcriptStore == nil {
		return nil
	}
	return s.transcriptStore.FlushIndex()
}

func (s *Store) Fork(sourceID string) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	newID := generateSessionID()
	if err := s.transcriptStore.Fork(context.Background(), transcript.ForkCommand{
		SourceSessionID: sourceID,
		NewSessionID:    newID,
		Time:            time.Now(),
	}); err != nil {
		return nil, err
	}
	forked, err := s.loadSnapshot(context.Background(), newID)
	if err != nil {
		return nil, fmt.Errorf("failed to load forked session: %w", err)
	}
	return forked, nil
}

func (s *Store) PersistToolResult(sessionID, toolCallID, content string) error {
	// Sanitize both sessionID and toolCallID to prevent path traversal
	safeSessionID := filepath.Base(sessionID)
	if safeSessionID == "." || safeSessionID == "/" || safeSessionID == "" {
		return fmt.Errorf("invalid session ID: %q", sessionID)
	}
	safeName := filepath.Base(toolCallID)
	if safeName == "." || safeName == "/" || safeName == "" {
		return fmt.Errorf("invalid tool call ID: %q", toolCallID)
	}
	dir := s.toolResultsDir(safeSessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create tool result dir: %w", err)
	}
	return atomicfile.Write(filepath.Join(dir, safeName), []byte(content), 0o644)
}

func (s *Store) SaveSubagentConversation(parentSessionID, title, modelID, cwd string, messages []core.Message) (string, string, error) {
	if len(messages) == 0 {
		return "", "", nil
	}
	if title == "" {
		title = "Subagent"
	}
	if cwd == "" {
		cwd = s.cwd
	}

	sess := &Snapshot{
		Metadata: SessionMetadata{
			Title:           title,
			Model:           modelID,
			Cwd:             cwd,
			ParentSessionID: parentSessionID,
		},
		// A subagent has no interface, so its transcript is its conversation
		// with nothing drawn on it — which is what ToChat gives.
		Messages: chatOf(messages),
	}
	if err := s.Save(sess); err != nil {
		return "", "", err
	}
	return sess.Metadata.ID, s.SessionPath(sess.Metadata.ID), nil
}

func (s *Store) toolResultsDir(sessionID string) string {
	return filepath.Join(s.projectDir, "blobs", "tool-result", sessionID)
}

func (s *Store) loadSnapshot(ctx context.Context, sessionID string) (*Snapshot, error) {
	if s.transcriptStore == nil || sessionID == "" {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	tx, err := s.transcriptStore.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load transcript %s: %w", sessionID, err)
	}
	if tx == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	transcript.HydrateToolResultNodes(tx.ID, tx.Messages, func(toolCallID string) (string, error) {
		data, err := os.ReadFile(filepath.Join(s.toolResultsDir(tx.ID), toolCallID))
		if err != nil {
			return "", err
		}
		return string(data), nil
	})
	sess := &Snapshot{
		Metadata: transcript.MetadataFromTranscript(tx),
		Messages: messagesFromNodes(tx.Messages),
		Tasks:    transcript.TrackerItemsFromView(tx.State.Tasks),
	}

	if sess.Metadata.Title == "" {
		sess.Metadata.Title = GenerateTitle(sess.Messages)
	}
	if sess.Metadata.LastPrompt == "" {
		sess.Metadata.LastPrompt = ExtractLastUserText(sess.Messages)
	}
	return sess, nil
}

// chatOf views a conversation as a transcript with no display state set.
func chatOf(msgs []core.Message) []core.ChatMessage {
	out := make([]core.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ToChat())
	}
	return out
}
