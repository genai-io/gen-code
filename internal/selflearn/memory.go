package selflearn

import (
	"github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool"
)

// memoryEntryDelimiter separates entries inside a memory file. The
// standalone "§" line lets entries span multiple lines without ambiguity
// and is rare enough in prose to be safe for substring matching.
const memoryEntryDelimiter = "\n§\n"

// rejectEmbeddedDelimiter blocks content whose delimiter would re-split
// entries on read — both the full "\n§\n" and bare-§ lines, which fuse
// with the join newlines into a delimiter when stored next to a
// neighbour (e.g. ["a\n§","b"] reads back as ["a","§\nb"]). A re-split
// store also bypasses the scanner, which only saw the joined blob.
func rejectEmbeddedDelimiter(content string) error {
	if strings.Contains(content, memoryEntryDelimiter) {
		return fmt.Errorf("content cannot contain the entry delimiter (a standalone § line); this would silently split into multiple entries on read")
	}
	if slices.Contains(strings.Split(content, "\n"), "§") {
		return fmt.Errorf("content cannot contain a standalone § line; it collides with the entry delimiter and would corrupt the store on read")
	}
	return nil
}

// DefaultMemoryFileCharLimit is the fallback per-file cap when the
// constructor gets 0. Matches the injection cap so a file that fits on
// write also fits when read (§4.2).
const DefaultMemoryFileCharLimit = setting.SelfLearnMaxMemoryKB * 1024

// MemoryStore is the project-partitioned durable memory written by the L1
// fork and read back via system.LoadMemoryFiles. Lives under
// ~/.san/projects/<encoded-cwd>/memory/ — machine-local, out of the repo
// (§4). Entries are delimited; add/replace/remove locate one by a unique
// substring. Writes are atomic and re-read under the mutex.
// Cross-process safety is best-effort (atomic rename only).
//
// MemoryWriteObserver fires after every successful write. action is the
// tool action ("add"/"replace"/"remove"); file is the basename ("" ⇒
// MEMORY.md); note is the LLM-supplied short description of WHAT was
// changed (e.g. "added 3 entries about race conditions"). The note
// reaches the recap row so the user sees what changed at a glance.
// SetWriteObserver must be called before the first write; the fork is
// single-flight (§6 #8) so the field is lock-free.
type MemoryWriteObserver func(action, file, note string)

type MemoryStore struct {
	dir     string
	maxFile int // per-file char cap, always > 0 (constructor normalizes)
	onWrite MemoryWriteObserver

	mu sync.Mutex
}

// NewMemoryStore returns the store for cwd's project partition, or for
// dirOverride when non-empty (the /evolve Memory storage path). maxCharsPerFile
// caps each memory file; pass <= 0 for DefaultMemoryFileCharLimit. The
// directory is created lazily on the first write.
func NewMemoryStore(cwd string, maxCharsPerFile int, dirOverride string) *MemoryStore {
	if maxCharsPerFile <= 0 {
		maxCharsPerFile = DefaultMemoryFileCharLimit
	}
	return &MemoryStore{dir: system.ResolveAutoMemoryDir(cwd, dirOverride), maxFile: maxCharsPerFile}
}

// MaxKB returns the per-file cap in kilobytes, rounded down. Used by the
// review prompt so the model sees the actual configured cap rather than a
// hardcoded string (the cap is configurable via memory.maxKB).
func (s *MemoryStore) MaxKB() int { return s.maxFile / 1024 }

// SetWriteObserver registers the callback fired after each successful
// write. Must be called before the first write (see type doc).
func (s *MemoryStore) SetWriteObserver(fn MemoryWriteObserver) { s.onWrite = fn }

// Dir is the on-disk directory backing the store.
func (s *MemoryStore) Dir() string { return s.dir }

// resolveFile maps a caller-supplied file name to an absolute path inside the
// store, rejecting traversal and non-markdown names. An empty name defaults to
// the index file.
func (s *MemoryStore) resolveFile(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = system.AutoMemoryIndexName
	}
	if name != filepath.Base(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid memory file %q: must be a bare file name", name)
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		return "", fmt.Errorf("invalid memory file %q: must end in .md", name)
	}
	return filepath.Join(s.dir, name), nil
}

// Add appends a new entry to file (default index). Exact duplicates are a no-op.
func (s *MemoryStore) Add(file, content, note string) (string, error) {
	content = strings.TrimSpace(content)
	if err := scanContent(content); err != nil {
		return "", err
	}
	if err := rejectEmbeddedDelimiter(content); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.resolveFile(file)
	if err != nil {
		return "", err
	}
	entries := readEntries(path)
	if slices.Contains(entries, content) {
		return "Entry already present; nothing added.", nil
	}
	entries = append(entries, content)
	if n := joinedLen(entries); n > s.maxFile {
		return "", fmt.Errorf("entry would put %s at %d/%d chars; replace or remove entries first",
			filepath.Base(path), n, s.maxFile)
	}
	if err := writeEntries(path, entries); err != nil {
		return "", err
	}
	if s.onWrite != nil {
		s.onWrite("add", file, note)
	}
	return "Entry added.", nil
}

// Replace swaps the single entry containing oldText for newContent. It errors if
// oldText matches zero or multiple distinct entries.
func (s *MemoryStore) Replace(file, oldText, newContent, note string) (string, error) {
	oldText = strings.TrimSpace(oldText)
	newContent = strings.TrimSpace(newContent)
	if oldText == "" {
		return "", fmt.Errorf("old_text cannot be empty")
	}
	if newContent == "" {
		return "", fmt.Errorf("content cannot be empty; use remove to delete an entry")
	}
	if err := scanContent(newContent); err != nil {
		return "", err
	}
	if err := rejectEmbeddedDelimiter(newContent); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.resolveFile(file)
	if err != nil {
		return "", err
	}
	entries := readEntries(path)
	idx, err := uniqueMatch(entries, oldText)
	if err != nil {
		return "", err
	}
	entries[idx] = newContent
	if n := joinedLen(entries); n > s.maxFile {
		return "", fmt.Errorf("replacement would put %s at %d/%d chars; shorten it or remove other entries",
			filepath.Base(path), n, s.maxFile)
	}
	if err := writeEntries(path, entries); err != nil {
		return "", err
	}
	if s.onWrite != nil {
		s.onWrite("replace", file, note)
	}
	return "Entry replaced.", nil
}

// Remove deletes the single entry containing oldText.
func (s *MemoryStore) Remove(file, oldText, note string) (string, error) {
	oldText = strings.TrimSpace(oldText)
	if oldText == "" {
		return "", fmt.Errorf("old_text cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.resolveFile(file)
	if err != nil {
		return "", err
	}
	entries := readEntries(path)
	idx, err := uniqueMatch(entries, oldText)
	if err != nil {
		return "", err
	}
	entries = slices.Delete(entries, idx, idx+1)
	if err := writeEntries(path, entries); err != nil {
		return "", err
	}
	if s.onWrite != nil {
		s.onWrite("remove", file, note)
	}
	return "Entry removed.", nil
}

// uniqueMatch returns the index of the one entry containing sub. Multiple
// distinct matches are an error (ambiguous); identical duplicates resolve to the
// first.
func uniqueMatch(entries []string, sub string) (int, error) {
	first := -1
	distinct := make(map[string]struct{})
	for i, e := range entries {
		if strings.Contains(e, sub) {
			if first == -1 {
				first = i
			}
			distinct[e] = struct{}{}
		}
	}
	if first == -1 {
		return 0, fmt.Errorf("no entry matched %q", sub)
	}
	if len(distinct) > 1 {
		return 0, fmt.Errorf("multiple entries matched %q; be more specific", sub)
	}
	return first, nil
}

// joinedLen returns len(strings.Join(entries, memoryEntryDelimiter))
// without allocating the joined string — Add/Replace call this on every
// write just to check the cap, so doing real arithmetic keeps a
// near-cap file from rebuilding a 25 KB string on each call.
func joinedLen(entries []string) int {
	if len(entries) == 0 {
		return 0
	}
	total := (len(entries) - 1) * len(memoryEntryDelimiter)
	for _, e := range entries {
		total += len(e)
	}
	return total
}

// readEntries parses a memory file into trimmed, non-empty entries. A missing or
// empty file yields no entries.
func readEntries(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}
	var out []string
	for e := range strings.SplitSeq(raw, memoryEntryDelimiter) {
		if t := strings.TrimSpace(e); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// writeEntries persists entries atomically (temp file + rename) so a concurrent
// reader sees either the old or the new complete file, never a truncated one.
// An empty entry list removes the file.
func writeEntries(path string, entries []string) error {
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mem-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	content := strings.Join(entries, memoryEntryDelimiter) + "\n"
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// memoryWriteTool is the L1-only write surface over a MemoryStore. It is granted
// solely to the reviewer fork; the main agent never sees it.
type memoryWriteTool struct {
	store *MemoryStore
}

func newMemoryWriteTool(store *MemoryStore) *memoryWriteTool {
	return &memoryWriteTool{store: store}
}

func (t *memoryWriteTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "memory_write",
		Description: "Persist a durable fact to project memory (survives across sessions). " +
			"Actions: add (new entry), replace (update — old_text identifies it), remove (delete — old_text identifies it). " +
			"Save user preferences, project conventions, and build/debug insights — never one-off task state or session narratives. " +
			"old_text is a short unique substring of the existing entry. " +
			"file defaults to the MEMORY.md index; spill long detail into a topic file (e.g. debugging.md).",
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"add", "replace", "remove"},
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Entry text. Required for add and replace.",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Short unique substring of the entry to replace or remove.",
				},
				"file": map[string]any{
					"type":        "string",
					"description": "Target file name (bare, .md). Defaults to MEMORY.md.",
				},
				"note": map[string]any{
					"type":        "string",
					"description": "Required. One short clause (≤80 chars) describing what this single write changed — surfaced in the post-review recap. Examples: \"added 3 race-condition entries\", \"removed vague tooling note\".",
				},
			},
			"required": []string{"action", "note"},
		},
	}
}

func (t *memoryWriteTool) Run(_ context.Context, call ai.ToolCall) (agent.Result, error) {
	input, _ := core.ParseToolInput(call.Input)
	action := strings.TrimSpace(tool.GetString(input, "action"))
	file := tool.GetString(input, "file")
	content := tool.GetString(input, "content")
	oldText := tool.GetString(input, "old_text")
	note := tool.GetString(input, "note")

	var (
		msg string
		err error
	)
	switch action {
	case "add":
		msg, err = t.store.Add(file, content, note)
	case "replace":
		msg, err = t.store.Replace(file, oldText, content, note)
	case "remove":
		msg, err = t.store.Remove(file, oldText, note)
	default:
		return agent.Result{}, fmt.Errorf("unknown action %q; use add, replace, or remove", action)
	}
	if err != nil {
		return agent.Result{}, err
	}
	out, _ := json.Marshal(map[string]string{"status": "ok", "message": msg})
	return agent.TextResult(string(out)), nil
}
