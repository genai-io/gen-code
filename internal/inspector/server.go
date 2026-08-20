// Package inspector serves the san session transcripts to a local web
// UI. The inspector is read-only, single-process, localhost-only. It exposes
// a small HTTP API that mirrors the JSONL on disk — the wire format IS the
// file format — plus an SSE endpoint for live tail. UI assets are embedded
// so the binary is self-contained.
package inspector

import (
	"container/list"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/inspector/ui"
	"github.com/genai-io/san/internal/session/transcript"
)

// replayCacheCapacity is the upper bound on cached replay states across all
// sessions. Each entry is a fully materialized replayState (system sections,
// tool schemas, active message chain at that point in time). A few dozen
// entries cover sequential timeline navigation comfortably while keeping
// peak memory in the low single-digit MB.
const replayCacheCapacity = 64

// Server hosts the trace viewer for a single project directory.
type Server struct {
	projectDir string // .../.san/projects/<encoded-cwd>
	mux        *http.ServeMux
	store      *transcript.FileStore // shared with the writer; reads use the index

	mu          sync.Mutex
	watchers    map[string]*tailer // sessionID -> active tailer
	replayCache *replayLRU
}

func New(projectDir string) *Server {
	s := &Server{
		projectDir:  projectDir,
		mux:         http.NewServeMux(),
		watchers:    make(map[string]*tailer),
		replayCache: newReplayLRU(replayCacheCapacity),
	}
	// Best-effort: the viewer falls back to a directory walk if the index
	// isn't constructable (missing project dir, permission error, etc.).
	if store, err := transcript.NewFileStore(projectDir, ""); err == nil {
		s.store = store
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// UI assets — strip the "assets/" prefix to expose them at the root.
	assets, err := fs.Sub(ui.Assets, "assets")
	if err == nil {
		s.mux.Handle("/", http.FileServer(http.FS(assets)))
	}

	s.mux.HandleFunc("/api/sessions", s.handleListSessions)
	// Per-session routes: /api/sessions/{id}/records, /api/sessions/{id}/stream,
	// /api/sessions/{id}/state/{recordID}
	s.mux.HandleFunc("/api/sessions/", s.handleSessionRoute)
}

// transcriptsDir is the JSONL location: <projectDir>/transcripts.
func (s *Server) transcriptsDir() string {
	return filepath.Join(s.projectDir, "transcripts")
}

type sessionListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updatedAt"`
	Size      int64     `json:"size"`
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		items, err := s.listFromIndex(r.Context())
		if err == nil {
			writeJSON(w, items)
			return
		}
		// Fall through to the directory walk on index error — the index is
		// best-effort and the writer might not have created it yet.
	}
	items, err := s.listFromDir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

// listFromIndex serves the session list out of transcripts-index.json. Title
// and UpdatedAt come from the index entry the writer maintains. Size is a
// cheap per-file Stat — we still surface it because the UI shows KB.
func (s *Server) listFromIndex(ctx context.Context) ([]sessionListItem, error) {
	entries, err := s.store.List(ctx, "", transcript.ListOptions{IncludeSidechain: true})
	if err != nil {
		return nil, err
	}
	items := make([]sessionListItem, 0, len(entries))
	for _, e := range entries {
		var size int64
		if info, err := os.Stat(e.FullPath); err == nil {
			size = info.Size()
		}
		items = append(items, sessionListItem{
			ID:        e.SessionID,
			Title:     e.Title,
			UpdatedAt: e.UpdatedAt,
			Size:      size,
		})
	}
	return items, nil
}

// listFromDir is the fallback path used when the FileStore couldn't be
// constructed or the index is unreadable. Slower (opens every transcript for
// the title preview) but self-contained.
func (s *Server) listFromDir() ([]sessionListItem, error) {
	entries, err := os.ReadDir(s.transcriptsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []sessionListItem{}, nil
		}
		return nil, err
	}
	items := make([]sessionListItem, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(s.transcriptsDir(), e.Name())
		items = append(items, sessionListItem{
			ID:        id,
			UpdatedAt: info.ModTime(),
			Size:      info.Size(),
			Title:     previewTitle(path),
		})
	}
	sortByUpdatedDesc(items)
	return items, nil
}

// handleSessionRoute routes /api/sessions/{id}/{action} to the matching handler.
func (s *Server) handleSessionRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	sessionID, action := parts[0], parts[1]
	if sessionID == "" {
		http.NotFound(w, r)
		return
	}

	switch {
	case action == "records":
		s.handleRecords(w, r, sessionID)
	case action == "stream":
		s.handleStream(w, r, sessionID)
	case strings.HasPrefix(action, "state/"):
		recordID := strings.TrimPrefix(action, "state/")
		s.handleState(w, r, sessionID, recordID)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request, sessionID, recordID string) {
	path, ok := s.sessionPath(sessionID)
	if !ok || recordID == "" || strings.ContainsAny(recordID, "/\\") {
		http.NotFound(w, r)
		return
	}
	key := sessionID + "\x00" + recordID
	if cached, ok := s.replayCache.get(key); ok {
		writeJSON(w, cached)
		return
	}
	state, found, err := replayFile(path, recordID)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	s.replayCache.put(key, state)
	writeJSON(w, state)
}

// replayLRU caches the results of replayFile by (sessionID, recordID).
// Replay is deterministic for append-only transcripts, so a (sessionID,
// recordID) result never changes for the life of the file. Bounded to keep
// peak memory in check on long-running viewers; eviction is plain LRU.
type replayLRU struct {
	mu       sync.Mutex
	capacity int
	order    *list.List // front = most recently used
	entries  map[string]*list.Element
}

type replayCacheEntry struct {
	key   string
	state replayState
}

func newReplayLRU(capacity int) *replayLRU {
	return &replayLRU{
		capacity: capacity,
		order:    list.New(),
		entries:  make(map[string]*list.Element),
	}
}

func (c *replayLRU) get(key string) (replayState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*replayCacheEntry).state, true
	}
	return replayState{}, false
}

func (c *replayLRU) put(key string, state replayState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*replayCacheEntry).state = state
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&replayCacheEntry{key: key, state: state})
	c.entries[key] = el
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*replayCacheEntry).key)
	}
}

func (s *Server) sessionPath(id string) (string, bool) {
	// Defense against path traversal — sessionID must look like a single
	// filename segment.
	if strings.ContainsAny(id, "/\\") || id == "" {
		return "", false
	}
	p := filepath.Join(s.transcriptsDir(), id+".jsonl")
	return p, true
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request, sessionID string) {
	path, ok := s.sessionPath(sessionID)
	if !ok {
		http.NotFound(w, r)
		return
	}

	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	data, nextOffset, err := readFromOffset(path, after)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Next-Offset", strconv.FormatInt(nextOffset, 10))
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// readFromOffset reads bytes from the file starting at offset. Returns the
// bytes plus the new end-of-file offset. Always returns at line boundaries —
// trailing partial line is left for the next read.
func readFromOffset(path string, offset int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	end := info.Size()
	if offset >= end {
		return nil, end, nil
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, 0, err
	}
	buf := make([]byte, end-offset)
	n, err := f.Read(buf)
	if err != nil {
		return nil, 0, err
	}
	buf = buf[:n]
	// Trim trailing partial line.
	if i := lastNewline(buf); i >= 0 {
		return buf[:i+1], offset + int64(i+1), nil
	}
	// No newline yet — leave the bytes for next poll.
	return nil, offset, nil
}

func lastNewline(b []byte) int {
	for i, v := range slices.Backward(b) {
		if v == '\n' {
			return i
		}
	}
	return -1
}

// previewTitle returns a short prefix of the first user-role message's text
// content, suitable for a list view. Best-effort; returns "" on any error.
func previewTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	for dec.More() {
		var rec struct {
			Type    string `json:"type"`
			Message *struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := dec.Decode(&rec); err != nil {
			return ""
		}
		if rec.Type != "message.appended" || rec.Message == nil || rec.Message.Role != "user" {
			continue
		}
		for _, c := range rec.Message.Content {
			if c.Type == "text" {
				t := strings.TrimSpace(c.Text)
				if t == "" {
					continue
				}
				if len(t) > 80 {
					t = t[:80] + "…"
				}
				return t
			}
		}
	}
	return ""
}

func sortByUpdatedDesc(items []sessionListItem) {
	// Insertion sort — list size is small (tens of sessions per project).
	for i := 1; i < len(items); i++ {
		j := i
		for j > 0 && items[j].UpdatedAt.After(items[j-1].UpdatedAt) {
			items[j-1], items[j] = items[j], items[j-1]
			j--
		}
	}
}
