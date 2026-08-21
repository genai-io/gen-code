package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/log"
	"go.uber.org/zap"
)

const transcriptIndexFile = "transcripts-index.json"

type FileStore struct {
	mu        sync.RWMutex
	baseDir   string
	projectID string

	// sessions holds per-transcript caches keyed by session ID. Populated
	// lazily on first access via warmupCachesLocked, which does one forward
	// scan and fills every field together so resumed sessions pay one O(N)
	// scan per process. Delete clears the entry to evict all derived state
	// at once.
	sessions map[string]*sessionCache

	// cachedIndex holds the parsed transcript index so hot writes (every
	// message append and every state patch) stop re-reading and re-parsing the
	// whole transcripts-index.json each call. It is populated by saveIndexLocked
	// (the sole writer of the file) and read through by loadIndexLocked; writes
	// stay write-through, so on-disk durability is unchanged. nil until the
	// first load or save.
	cachedIndex *fileIndex

	// indexDirty marks that cachedIndex holds hot-path mutations not yet written
	// to disk. Every message append and state patch used to re-serialize and
	// rewrite the whole index file — O(sessions) work, ~7-12 times per turn.
	// Those mutations now stage in memory (cachedIndex stays authoritative for
	// in-process reads) and flush once at a turn boundary / on shutdown via
	// FlushIndex. The index is a pure derived cache — a crash that loses an
	// unflushed update is recovered by rebuildIndexLocked on the next List.
	indexDirty bool
}

// sessionCache holds the derived state we keep in memory to avoid re-scanning
// the transcript on every write. All fields are mu-guarded.
type sessionCache struct {
	warmed        bool                // forward scan completed
	persistedIDs  map[string]struct{} // dedup for AppendMessage
	lastGitBranch string              // sparse-emit base
	leafMessageID string              // parent pointer for next message
	latestState   State               // projected state (cold-Save diff base)
}

type fileIndex struct {
	Version   int              `json:"version"`
	ProjectID string           `json:"projectId"`
	Entries   []fileIndexEntry `json:"entries"`
}

type fileIndexEntry struct {
	SessionID    string    `json:"sessionId"`
	FullPath     string    `json:"fullPath"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Title        string    `json:"title,omitempty"`
	LastPrompt   string    `json:"lastPrompt,omitempty"`
	MessageCount int       `json:"messageCount"`
	GitBranch    string    `json:"gitBranch,omitempty"`
	IsSidechain  bool      `json:"isSidechain,omitempty"`
}

// indexPreviewMaxRunes bounds the Title/LastPrompt copies stored in the index.
// The index is a derived preview cache for the session picker — the authoritative
// full text lives in the transcript records (LoadState), and the picker only ever
// shows the first line truncated to the viewport width. Capping the stored copy
// therefore loses nothing on screen while stopping a long pasted prompt from
// bloating an entry that saveIndexLocked re-serializes on every turn.
const indexPreviewMaxRunes = 200

// indexPreview caps s to indexPreviewMaxRunes runes, cutting on a rune boundary so
// multibyte (e.g. CJK) text is never split mid-character.
func indexPreview(s string) string {
	// Fast path: byte length within the cap ⇒ rune count is too (runes are ≥ 1
	// byte each), so no []rune allocation is needed for the common short case.
	if len(s) <= indexPreviewMaxRunes {
		return s
	}
	runes := []rune(s)
	if len(runes) <= indexPreviewMaxRunes {
		return s
	}
	return string(runes[:indexPreviewMaxRunes])
}

func NewFileStore(baseDir, projectID string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Join(baseDir, "transcripts"), 0o755); err != nil {
		return nil, fmt.Errorf("create transcripts dir: %w", err)
	}
	return &FileStore{baseDir: baseDir, projectID: projectID}, nil
}

func (s *FileStore) Start(ctx context.Context, cmd StartCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	path := s.transcriptPath(cmd.SessionID)
	exists, err := fileExists(path)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	rec := Record{
		ID:        cmd.SessionID + ":start",
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      SessionStarted,
		Cwd:       cmd.Cwd,
		Version:   SchemaVersion,
		Session: &SessionRecord{
			Provider:  cmd.Provider,
			Model:     cmd.Model,
			MaxTokens: cmd.MaxTokens,
			AgentID:   cmd.AgentID,
			ParentID:  cmd.ParentID,
		},
	}
	if err := s.appendRecord(path, rec, true); err != nil {
		return err
	}
	if err := s.upsertIndexEntryLocked(cmd.SessionID, func(e *fileIndexEntry, fresh bool) {
		e.FullPath = path
		if fresh {
			e.CreatedAt = cmd.Time
		}
		if e.UpdatedAt.Before(cmd.Time) {
			e.UpdatedAt = cmd.Time
		}
	}); err != nil {
		return err
	}
	// Write a new session's first index entry eagerly. Start runs once per
	// session (it returns above for one that already exists), not on the hot
	// per-message path, so this costs one write per session while guaranteeing
	// the picker — even in another process, even after a crash mid-first-turn —
	// sees the session immediately. Only the per-message field updates that
	// follow defer to the turn-boundary flush.
	return s.flushIndexLocked()
}

func (s *FileStore) AppendMessage(ctx context.Context, cmd AppendMessageCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	path := s.transcriptPath(cmd.SessionID)
	seen, err := s.persistedIDsLocked(cmd.SessionID)
	if err != nil {
		return err
	}
	if _, ok := seen[cmd.MessageID]; ok {
		return nil
	}

	branch, err := s.lastBranchLocked(cmd.SessionID)
	if err != nil {
		return err
	}
	// gitBranch is sparse: only stamp when it changes from the last persisted
	// value. lastGitBranch(messages) on the read side scans for the most recent
	// non-empty entry, so unchanged branches stay correctly reported.
	emitBranch := ""
	if cmd.GitBranch != "" && cmd.GitBranch != branch {
		emitBranch = cmd.GitBranch
	}

	rec := Record{
		ID:          cmd.SessionID + ":" + cmd.MessageID,
		SessionID:   cmd.SessionID,
		Time:        cmd.Time,
		Type:        MessageAppended,
		ParentID:    cmd.ParentID,
		GitBranch:   emitBranch,
		AgentID:     cmd.AgentID,
		IsSidechain: cmd.IsSidechain,
		Message: &MessageRecord{
			MessageID: cmd.MessageID,
			Role:      cmd.Role,
			Content:   append([]ContentBlock(nil), cmd.Content...),
		},
	}
	if err := s.appendRecord(path, rec, true); err != nil {
		return err
	}
	seen[cmd.MessageID] = struct{}{}
	if emitBranch != "" {
		s.setLastBranchLocked(cmd.SessionID, emitBranch)
	}
	userText := ""
	if cmd.Role == "user" {
		userText = firstTextBlock(cmd.Content)
	}
	return s.upsertIndexEntryLocked(cmd.SessionID, func(e *fileIndexEntry, fresh bool) {
		e.FullPath = path
		if fresh || e.CreatedAt.IsZero() {
			e.CreatedAt = cmd.Time
		}
		if e.UpdatedAt.Before(cmd.Time) {
			e.UpdatedAt = cmd.Time
		}
		e.MessageCount++
		if userText != "" {
			if e.Title == "" {
				e.Title = indexPreview(userText)
			}
			e.LastPrompt = indexPreview(userText)
		}
		if emitBranch != "" {
			e.GitBranch = emitBranch
		}
		if cmd.IsSidechain {
			e.IsSidechain = true
		}
	})
}

func (s *FileStore) PatchState(ctx context.Context, cmd PatchStateCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	if len(cmd.Ops) == 0 {
		// Nothing changed; do not emit an empty patch record.
		return nil
	}
	rec := Record{
		ID:        fmt.Sprintf("%s:state:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      SessionStatePatched,
		State: &StateRecord{
			Ops: append([]PatchOp(nil), cmd.Ops...),
		},
	}
	if err := s.appendRecord(s.transcriptPath(cmd.SessionID), rec, false); err != nil {
		return err
	}
	// Keep the in-memory state cache live so LoadState reflects writes from
	// this process without re-reading the file. Warmup is required so we
	// don't lose the prior projected state when applying the new ops.
	if err := s.warmupCachesLocked(cmd.SessionID); err != nil {
		return err
	}
	c := s.sessions[cmd.SessionID]
	_ = applyStatePatch(&c.latestState, rec.State)
	return s.upsertIndexEntryLocked(cmd.SessionID, func(e *fileIndexEntry, fresh bool) {
		if fresh {
			e.FullPath = s.transcriptPath(cmd.SessionID)
			e.CreatedAt = cmd.Time
		}
		if e.UpdatedAt.Before(cmd.Time) {
			e.UpdatedAt = cmd.Time
		}
		// Only mirror fields the patch actually touched — otherwise we'd
		// clobber the AppendMessage-derived firstUserText title fallback
		// whenever an unrelated state op (Tag, Mode, Tasks) flows through.
		for _, op := range cmd.Ops {
			switch op.Path {
			case PatchPathTitle:
				e.Title = indexPreview(c.latestState.Title)
			case PatchPathLastPrompt:
				e.LastPrompt = indexPreview(c.latestState.LastPrompt)
			}
		}
	})
}

func (s *FileStore) AppendTool(ctx context.Context, cmd AppendToolCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.Type != ToolAdded && cmd.Type != ToolRemoved {
		return fmt.Errorf("append tools: unexpected type %q", cmd.Type)
	}

	rec := cmd.Record
	full := Record{
		ID:        fmt.Sprintf("%s:tool:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      cmd.Type,
		Tool:      &rec,
	}
	return s.appendRecord(s.transcriptPath(cmd.SessionID), full, false)
}

func (s *FileStore) AppendSystemSection(ctx context.Context, cmd AppendSystemSectionCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.Type != SystemSectionAdded && cmd.Type != SystemSectionRemoved {
		return fmt.Errorf("append system section: unexpected type %q", cmd.Type)
	}

	rec := cmd.Record
	full := Record{
		ID:        fmt.Sprintf("%s:system:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      cmd.Type,
		System:    &rec,
	}
	return s.appendRecord(s.transcriptPath(cmd.SessionID), full, false)
}

// AppendHook records one hook.fired event. Non-fsync — audit telemetry that
// rolls up to disk at the next sync boundary (inference.responded or message).
func (s *FileStore) AppendHook(ctx context.Context, cmd AppendHookCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	rec := cmd.Record
	full := Record{
		ID:        fmt.Sprintf("%s:hook:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      HookFired,
		Hook:      &rec,
	}
	return s.appendRecord(s.transcriptPath(cmd.SessionID), full, false)
}

// AppendPermission records one permission.required or permission.decided
// event. Non-fsync for the same reason as AppendHook.
func (s *FileStore) AppendPermission(ctx context.Context, cmd AppendPermissionCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.Type != PermissionRequired && cmd.Type != PermissionDecided {
		return fmt.Errorf("append permission: unexpected type %q", cmd.Type)
	}
	rec := cmd.Record
	full := Record{
		ID:         fmt.Sprintf("%s:perm:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID:  cmd.SessionID,
		Time:       cmd.Time,
		Type:       cmd.Type,
		Permission: &rec,
	}
	return s.appendRecord(s.transcriptPath(cmd.SessionID), full, false)
}

// AppendSkillState records one skill.state.changed event. Non-fsync — audit
// telemetry that rolls up at the next sync boundary.
func (s *FileStore) AppendSkillState(ctx context.Context, cmd AppendSkillStateCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	rec := cmd.Record
	full := Record{
		ID:        fmt.Sprintf("%s:skill:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      SkillStateChanged,
		Skill:     &rec,
	}
	return s.appendRecord(s.transcriptPath(cmd.SessionID), full, false)
}

func (s *FileStore) AppendInference(ctx context.Context, cmd AppendInferenceCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.Type != InferenceRequested && cmd.Type != InferenceResponded {
		return fmt.Errorf("append inference: unexpected type %q", cmd.Type)
	}

	rec := cmd.Record
	full := Record{
		ID:        fmt.Sprintf("%s:inference:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      cmd.Type,
		Inference: &rec,
	}
	// inference.responded sync-flushes the file so any preceding telemetry
	// (system.section.*, tools.*, inference.requested) from this turn lands
	// on disk together; inference.requested itself stays in the page cache.
	return s.appendRecord(s.transcriptPath(cmd.SessionID), full, cmd.Type == InferenceResponded)
}

func (s *FileStore) Compact(ctx context.Context, cmd CompactCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	rec := Record{
		ID:        fmt.Sprintf("%s:compact:%d", cmd.SessionID, cmd.Time.UnixNano()),
		SessionID: cmd.SessionID,
		Time:      cmd.Time,
		Type:      SessionCompacted,
		Session:   &SessionRecord{SummaryMessageID: cmd.SummaryMessageID},
	}
	if err := s.appendRecord(s.transcriptPath(cmd.SessionID), rec, true); err != nil {
		return err
	}
	return s.refreshIndexLocked(cmd.SessionID)
}

func (s *FileStore) Fork(ctx context.Context, cmd ForkCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	sourcePath := s.transcriptPath(cmd.SourceSessionID)
	records, err := s.loadRecordsLocked(sourcePath)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("source transcript not found: %s", cmd.SourceSessionID)
	}

	destPath := s.transcriptPath(cmd.NewSessionID)
	tmpPath := destPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open fork transcript: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, rec := range records {
		rec.SessionID = cmd.NewSessionID
		rec.ID = rewriteRecordSessionID(rec.ID, cmd.SourceSessionID, cmd.NewSessionID)
		if rec.Version == "" {
			rec.Version = SchemaVersion
		}
		rec.Time = cmd.Time
		if err := enc.Encode(rec); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("write fork record: %w", err)
		}
	}
	forkRec := Record{
		ID:        fmt.Sprintf("%s:fork:%d", cmd.NewSessionID, cmd.Time.UnixNano()),
		SessionID: cmd.NewSessionID,
		Time:      cmd.Time,
		Type:      SessionForked,
		Version:   SchemaVersion,
		Session: &SessionRecord{
			ParentID: cmd.SourceSessionID,
		},
	}
	if err := enc.Encode(forkRec); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write fork marker: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close fork transcript: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename fork transcript: %w", err)
	}
	return s.refreshIndexLocked(cmd.NewSessionID)
}

func rewriteRecordSessionID(id, oldSessionID, newSessionID string) string {
	prefix := oldSessionID + ":"
	if after, ok := strings.CutPrefix(id, prefix); ok {
		return newSessionID + ":" + after
	}
	return id
}

func (s *FileStore) Load(ctx context.Context, transcriptID string) (*Transcript, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	records, err := s.loadRecordsLocked(s.transcriptPath(transcriptID))
	if err != nil {
		return nil, err
	}
	return Project(records)
}

func (s *FileStore) List(ctx context.Context, projectID string, opts ListOptions) ([]ListItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if projectID != "" && s.projectID != "" && projectID != s.projectID {
		return []ListItem{}, nil
	}

	// Try read-only path first; upgrade to write lock only for index rebuild.
	entries, err := s.listIndexEntries()
	if err != nil {
		return nil, err
	}

	items := make([]ListItem, 0, len(entries))
	for _, entry := range entries {
		if !opts.IncludeSidechain && entry.IsSidechain {
			continue
		}
		items = append(items, ListItem{
			SessionID:    entry.SessionID,
			FullPath:     entry.FullPath,
			CreatedAt:    entry.CreatedAt,
			UpdatedAt:    entry.UpdatedAt,
			Title:        entry.Title,
			LastPrompt:   entry.LastPrompt,
			MessageCount: entry.MessageCount,
			GitBranch:    entry.GitBranch,
			IsSidechain:  entry.IsSidechain,
		})
	}

	slices.SortFunc(items, func(a, b ListItem) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	if opts.Limit > 0 && len(items) > opts.Limit {
		items = items[:opts.Limit]
	}
	return items, nil
}

// listIndexEntries returns a snapshot of the index entries, safely holding
// the lock during the copy to prevent data races with concurrent writers.
func (s *FileStore) listIndexEntries() ([]fileIndexEntry, error) {
	s.mu.RLock()
	index, err := s.loadIndexLocked()
	if err == nil {
		entries := make([]fileIndexEntry, len(index.Entries))
		copy(entries, index.Entries)
		s.mu.RUnlock()
		return entries, nil
	}
	s.mu.RUnlock()

	// Upgrade to write lock for index rebuild.
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err = s.loadIndexLocked()
	if err != nil {
		if rbErr := s.rebuildIndexLocked(); rbErr != nil {
			return nil, rbErr
		}
		index, err = s.loadIndexLocked()
	}
	if err != nil {
		return nil, err
	}
	entries := make([]fileIndexEntry, len(index.Entries))
	copy(entries, index.Entries)
	return entries, nil
}

func (s *FileStore) Delete(ctx context.Context, transcriptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(s.transcriptPath(transcriptID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete transcript file: %w", err)
	}
	delete(s.sessions, transcriptID)

	index, err := s.loadIndexLocked()
	if err == nil {
		filtered := make([]fileIndexEntry, 0, len(index.Entries))
		for _, entry := range index.Entries {
			if entry.SessionID != transcriptID {
				filtered = append(filtered, entry)
			}
		}
		index.Entries = filtered
		return s.saveIndexLocked(index)
	}
	return nil
}

func (s *FileStore) transcriptPath(transcriptID string) string {
	return filepath.Join(s.baseDir, "transcripts", transcriptID+".jsonl")
}

func (s *FileStore) TranscriptPath(transcriptID string) string {
	return s.transcriptPath(transcriptID)
}

func (s *FileStore) indexPath() string {
	return filepath.Join(s.baseDir, transcriptIndexFile)
}

// appendRecord writes one record to the JSONL. fsync is gated by sync so
// hot-path telemetry (system.section.*, tools.*, inference.requested) can be
// buffered in the OS page cache and rolled up to disk at turn boundaries.
//
// Durability classes:
//   - sync=true: user input (message.appended), turn-completion
//     (inference.responded), lifecycle events (session.started, session.compacted).
//     A crash after these must not lose them.
//   - sync=false: pure telemetry. Worst case on crash: the in-flight turn's
//     telemetry is lost, but the message/state of preceding turns is intact
//     because their write fsynced.
//
// Single-process per file: the append+close pair preserves order regardless
// of fsync; durability is the only thing the flag toggles.
func (s *FileStore) appendRecord(path string, rec Record, sync bool) error {
	// NewFileStore creates transcripts/ at construction; no per-record MkdirAll.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open transcript file: %w", err)
	}

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		f.Close()
		return fmt.Errorf("append transcript record: %w", err)
	}
	if sync {
		if err := f.Sync(); err != nil {
			f.Close()
			return fmt.Errorf("sync transcript file: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close transcript file: %w", err)
	}
	return nil
}

// cacheLocked returns the per-session cache, creating an empty (unwarmed)
// entry on first access. Callers must hold s.mu (write).
func (s *FileStore) cacheLocked(transcriptID string) *sessionCache {
	if s.sessions == nil {
		s.sessions = make(map[string]*sessionCache)
	}
	c, ok := s.sessions[transcriptID]
	if !ok {
		c = &sessionCache{persistedIDs: make(map[string]struct{})}
		s.sessions[transcriptID] = c
	}
	return c
}

// warmupCachesLocked performs a single forward scan of the transcript and
// populates persistedIDs, lastGitBranch, leafMessageID, and latestState. On
// resumed sessions this replaces four independent O(N) scans (one for each
// cache) with one. Idempotent.
func (s *FileStore) warmupCachesLocked(transcriptID string) error {
	c := s.cacheLocked(transcriptID)
	if c.warmed {
		return nil
	}
	f, err := os.Open(s.transcriptPath(transcriptID))
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("open transcript file: %w", err)
		}
		// Missing file → empty caches; still mark warmed so we don't retry.
		c.warmed = true
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var rec Record
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			continue
		}
		if rec.GitBranch != "" {
			c.lastGitBranch = rec.GitBranch
		}
		if rec.Type == MessageAppended && rec.Message != nil {
			c.persistedIDs[rec.Message.MessageID] = struct{}{}
			c.leafMessageID = rec.Message.MessageID
		}
		if rec.Type == SessionStatePatched && rec.State != nil {
			_ = applyStatePatch(&c.latestState, rec.State)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan transcript file: %w", err)
	}
	c.warmed = true
	return nil
}

// persistedIDsLocked returns the cached set of message IDs already written to
// the given transcript. The returned map is owned by the store; callers update
// it after a successful append.
func (s *FileStore) persistedIDsLocked(transcriptID string) (map[string]struct{}, error) {
	if err := s.warmupCachesLocked(transcriptID); err != nil {
		return nil, err
	}
	return s.sessions[transcriptID].persistedIDs, nil
}

// lastBranchLocked returns the most recently emitted git branch for the
// transcript. Used to keep gitBranch emission sparse.
func (s *FileStore) lastBranchLocked(transcriptID string) (string, error) {
	if err := s.warmupCachesLocked(transcriptID); err != nil {
		return "", err
	}
	return s.sessions[transcriptID].lastGitBranch, nil
}

func (s *FileStore) setLastBranchLocked(transcriptID, branch string) {
	s.cacheLocked(transcriptID).lastGitBranch = branch
}

// LastMessageID returns the active-chain leaf message ID for an existing
// transcript, or "" if the transcript doesn't exist or has no messages. Fast
// path uses a read lock when the cache is warm; cold path upgrades to a
// write lock to run warmupCachesLocked. One O(N) scan per session per
// process, then O(1).
func (s *FileStore) LastMessageID(ctx context.Context, transcriptID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	if c, ok := s.sessions[transcriptID]; ok && c.warmed {
		leaf := c.leafMessageID
		s.mu.RUnlock()
		return leaf, nil
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.warmupCachesLocked(transcriptID); err != nil {
		return "", err
	}
	return s.sessions[transcriptID].leafMessageID, nil
}

// LoadState returns the projected session State (Title, LastPrompt, Tag,
// Mode, Tasks, Worktree) without materializing the active message chain.
// Used by Save's first-write cold path to seed the diff base.
func (s *FileStore) LoadState(ctx context.Context, transcriptID string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	s.mu.RLock()
	if c, ok := s.sessions[transcriptID]; ok && c.warmed {
		state := c.latestState
		s.mu.RUnlock()
		return state, nil
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.warmupCachesLocked(transcriptID); err != nil {
		return State{}, err
	}
	return s.sessions[transcriptID].latestState, nil
}

func (s *FileStore) loadRecordsLocked(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("transcript file not found: %w", err)
		}
		return nil, fmt.Errorf("open transcript file: %w", err)
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// The file is append-only and appendRecord fsyncs only on turn
			// boundaries, so a crash mid-append leaves a partial final line.
			// That is the one place a torn record can be, and rejecting the
			// whole file for it means an interrupted turn costs the user the
			// entire session — every record before it is intact.
			//
			// A malformed line anywhere else is not a crash, and silently
			// dropping it would leave a hole in the replayed conversation with
			// nothing to show for it, so that still fails loudly.
			if scanner.Scan() {
				return nil, fmt.Errorf("decode transcript record: %w", err)
			}
			log.Logger().Warn("transcript: dropping torn final record",
				zap.String("path", path), zap.Error(err))
			break
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript file: %w", err)
	}
	return records, nil
}

// loadIndexLocked returns the transcript index, served from the in-memory
// cache when populated. On a cache miss it reads and parses the file but does
// NOT populate s.cachedIndex — populating is left to saveIndexLocked so this
// stays a pure read, safe to call while holding only the read lock
// (listIndexEntries). A read miss (e.g. no index file yet) returns the error
// for the caller to handle (rebuild, or start a fresh index).
func (s *FileStore) loadIndexLocked() (*fileIndex, error) {
	if s.cachedIndex != nil {
		return s.cachedIndex, nil
	}
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return nil, err
	}
	var index fileIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return &index, nil
}

// saveIndexLocked writes the index through to disk and caches it. It is the
// sole writer of the index file, so caching here keeps s.cachedIndex coherent
// with disk. Callers hold the write lock.
func (s *FileStore) saveIndexLocked(index *fileIndex) error {
	// Compact (not indented): the index is a machine-read derived cache rewritten
	// every turn, so pretty-print whitespace is pure write amplification.
	data, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("marshal transcript index: %w", err)
	}
	if err := atomicfile.Write(s.indexPath(), data, 0o644); err != nil {
		return fmt.Errorf("write transcript index: %w", err)
	}
	s.cachedIndex = index
	s.indexDirty = false
	return nil
}

// stageIndexLocked records that cachedIndex diverges from disk without writing
// it, so a burst of hot-path appends (a turn's tool results, a subagent's
// message dump) collapses to a single index write at the next FlushIndex
// instead of one full re-serialization per message. The caller has already
// mutated the entry in place; `index` must be (or become) s.cachedIndex.
// Callers hold the write lock.
func (s *FileStore) stageIndexLocked(index *fileIndex) {
	s.cachedIndex = index
	s.indexDirty = true
}

// flushIndexLocked writes staged index mutations to disk, if any. Callers hold
// the write lock. A dirty index always has a non-nil cachedIndex — stageIndexLocked
// sets both together — so the dirty flag alone gates the write.
func (s *FileStore) flushIndexLocked() error {
	if !s.indexDirty {
		return nil
	}
	return s.saveIndexLocked(s.cachedIndex)
}

// FlushIndex writes any index mutations the hot append path staged in memory
// out to disk. Callers invoke it at turn boundaries (end of Save) and on
// shutdown; between flushes the in-memory index stays authoritative for
// in-process reads, so the picker is never stale within the process.
func (s *FileStore) FlushIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushIndexLocked()
}

func (s *FileStore) rebuildIndexLocked() error {
	entries, err := os.ReadDir(filepath.Join(s.baseDir, "transcripts"))
	if err != nil {
		return fmt.Errorf("read transcripts dir: %w", err)
	}

	index := &fileIndex{
		Version:   1,
		ProjectID: s.projectID,
		Entries:   make([]fileIndexEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		transcriptID := strings.TrimSuffix(entry.Name(), ".jsonl")
		item, err := s.buildListItemLocked(transcriptID)
		if err != nil {
			continue
		}
		index.Entries = append(index.Entries, fileIndexEntry{
			SessionID:    item.SessionID,
			FullPath:     item.FullPath,
			CreatedAt:    item.CreatedAt,
			UpdatedAt:    item.UpdatedAt,
			Title:        indexPreview(item.Title),
			LastPrompt:   indexPreview(item.LastPrompt),
			MessageCount: item.MessageCount,
			GitBranch:    item.GitBranch,
			IsSidechain:  item.IsSidechain,
		})
	}
	return s.saveIndexLocked(index)
}

// upsertIndexEntryLocked applies an incremental mutation to the index entry
// for the given session. The fresh flag tells the mutator whether it's
// initializing a brand new entry — used for CreatedAt seeding. This replaces
// the legacy full-rebuild path (loadRecordsLocked + Project) for hot writes,
// which made every append O(transcript size). The Compact/Fork paths still
// use refreshIndexLocked because they can change the projected active chain
// length, which incremental updates can't track.
func (s *FileStore) upsertIndexEntryLocked(transcriptID string, mutate func(e *fileIndexEntry, fresh bool)) error {
	index, err := s.loadIndexLocked()
	if err != nil {
		// Rebuild from the transcripts on disk before giving up on them.
		// Start runs at the first turn of every new session, so an empty index
		// written here reaches disk before anything reads it — and
		// listIndexEntries' own recovery then loads a perfectly valid
		// one-entry file and never rebuilds. Every earlier session becomes
		// permanently invisible in /resume while its .jsonl sits untouched
		// beside it. A genuinely fresh store has no transcripts dir, so the
		// rebuild fails and the empty index below is the right answer.
		if rbErr := s.rebuildIndexLocked(); rbErr == nil {
			index, err = s.loadIndexLocked()
		}
		if err != nil {
			index = &fileIndex{Version: 1, ProjectID: s.projectID}
		}
	}
	for i := range index.Entries {
		if index.Entries[i].SessionID == transcriptID {
			mutate(&index.Entries[i], false)
			s.stageIndexLocked(index)
			return nil
		}
	}
	entry := fileIndexEntry{SessionID: transcriptID}
	mutate(&entry, true)
	index.Entries = append(index.Entries, entry)
	s.stageIndexLocked(index)
	return nil
}

func (s *FileStore) refreshIndexLocked(transcriptID string) error {
	index, err := s.loadIndexLocked()
	if err != nil {
		index = &fileIndex{
			Version:   1,
			ProjectID: s.projectID,
		}
	}

	item, err := s.buildListItemLocked(transcriptID)
	if err != nil {
		return err
	}

	entry := fileIndexEntry{
		SessionID:    item.SessionID,
		FullPath:     item.FullPath,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
		Title:        indexPreview(item.Title),
		LastPrompt:   indexPreview(item.LastPrompt),
		MessageCount: item.MessageCount,
		GitBranch:    item.GitBranch,
		IsSidechain:  item.IsSidechain,
	}

	for i := range index.Entries {
		if index.Entries[i].SessionID == transcriptID {
			index.Entries[i] = entry
			return s.saveIndexLocked(index)
		}
	}
	index.Entries = append(index.Entries, entry)
	return s.saveIndexLocked(index)
}

func (s *FileStore) buildListItemLocked(transcriptID string) (ListItem, error) {
	records, err := s.loadRecordsLocked(s.transcriptPath(transcriptID))
	if err != nil {
		return ListItem{}, err
	}
	transcript, err := Project(records)
	if err != nil {
		return ListItem{}, err
	}

	title := transcript.State.Title
	if title == "" {
		title = firstUserText(transcript.Messages)
	}

	return ListItem{
		SessionID:    transcriptID,
		FullPath:     s.transcriptPath(transcriptID),
		CreatedAt:    transcript.CreatedAt,
		UpdatedAt:    transcript.UpdatedAt,
		Title:        title,
		LastPrompt:   coalesce(transcript.State.LastPrompt, lastUserText(transcript.Messages)),
		MessageCount: len(transcript.Messages),
		GitBranch:    lastGitBranch(transcript.Messages),
		IsSidechain:  anySidechain(transcript.Messages),
	}, nil
}

func firstUserText(messages []Node) string {
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		if text := firstTextBlock(msg.Content); text != "" {
			return text
		}
	}
	return ""
}

func lastUserText(messages []Node) string {
	for _, message := range slices.Backward(messages) {
		if message.Role != "user" {
			continue
		}
		if text := firstTextBlock(message.Content); text != "" {
			return text
		}
	}
	return ""
}

func firstTextBlock(content []ContentBlock) string {
	for _, block := range content {
		if block.Type == "tool_result" {
			return ""
		}
		if block.Type == "text" && block.Text != "" {
			return block.Text
		}
	}
	return ""
}

func lastGitBranch(messages []Node) string {
	for _, message := range slices.Backward(messages) {
		if message.GitBranch != "" {
			return message.GitBranch
		}
	}
	return ""
}

func anySidechain(messages []Node) bool {
	for _, msg := range messages {
		if msg.IsSidechain {
			return true
		}
	}
	return false
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
