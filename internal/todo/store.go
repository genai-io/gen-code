package todo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/log"
)

// Item represents a tracked item
type Item struct {
	ID              string         `json:"id"`
	Subject         string         `json:"subject"`
	Description     string         `json:"description"`
	ActiveForm      string         `json:"activeForm,omitempty"`
	Status          string         `json:"status"`
	Owner           string         `json:"owner,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Blocks          []string       `json:"blocks"`
	BlockedBy       []string       `json:"blockedBy"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	StatusChangedAt time.Time      `json:"statusChangedAt"` // when status last changed (for elapsed time display)
}

// Item status constants
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusDeleted    = "deleted"
)

// Store is a thread-safe item store with optional disk persistence.
// When a storageDir is set, each item is persisted as {id}.json.
type Store struct {
	mu         sync.RWMutex
	items      map[string]*Item
	nextID     int
	storageDir string    // empty = in-memory only
	lastDirMod time.Time // last known dir mtime, for change detection in ReloadFromDisk
}

// NewStore creates a new in-memory Store
func NewStore() *Store {
	return &Store{
		items:  make(map[string]*Item),
		nextID: 1,
	}
}

// SetStorageDir sets the directory for disk persistence and loads existing items.
// If dir is empty, the store operates in memory-only mode.
func (s *Store) SetStorageDir(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.storageDir = dir
	if dir == "" {
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create item storage dir: %w", err)
	}

	// Create lock file
	lockPath := filepath.Join(dir, ".lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			return fmt.Errorf("failed to create item storage lock file: %w", err)
		}
	}

	// Load existing items from disk, then reconcile: this is the moment a
	// fresh process adopts state written by a process that no longer exists.
	if err := s.loadFromDisk(); err != nil {
		return err
	}
	s.demoteOrphanedItems()
	return nil
}

// demoteOrphanedItems demotes items recorded as in_progress that no worker is
// advancing any more. Runs where a store is adopted — SetStorageDir and Import —
// so records written by a process that has since died stop claiming to be live,
// however that process ended. Must be called with s.mu held.
//
// Adoption also happens mid-session (resuming a session re-points the store), so
// liveness is asked of the task manager rather than assumed from the call site:
// a worker still executing keeps its status. Plan items have no worker to ask
// about and fall back to pending, the truthful state of work that was started
// but never finished.
//
// Not called from loadFromDisk — ReloadFromDisk uses that mid-session to pick up
// cross-process writes, where in_progress is expected and genuine.
func (s *Store) demoteOrphanedItems() {
	for _, item := range s.items {
		if item.Status != StatusInProgress || WorkerRunning(item) {
			continue
		}
		if BackgroundTaskID(item) != "" {
			item.Status = StatusCompleted
			if item.Metadata == nil {
				item.Metadata = map[string]any{}
			}
			item.Metadata[metaStatusDetail] = StatusDetailInterrupted
		} else {
			item.Status = StatusPending
		}
		item.StatusChangedAt = time.Now()
		s.persistItem(item)
	}
}

// loadFromDisk reads all {id}.json files from storageDir into memory.
// Must be called with s.mu held.
func (s *Store) loadFromDisk() error {
	items, err := os.ReadDir(s.storageDir)
	if err != nil {
		return err
	}

	s.items = make(map[string]*Item)
	s.nextID = 1

	for _, item := range items {
		name := item.Name()
		if item.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.storageDir, name))
		if err != nil {
			continue
		}

		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			continue
		}

		normalizeItemSlices(&item)
		s.items[item.ID] = &item
		var idNum int
		if _, err := fmt.Sscanf(item.ID, "%d", &idNum); err == nil && idNum >= s.nextID {
			s.nextID = idNum + 1
		}
	}

	return nil
}

// persistItem writes a single item to disk. Must be called with s.mu held.
func (s *Store) persistItem(item *Item) {
	if s.storageDir == "" {
		return
	}
	path := filepath.Join(s.storageDir, item.ID+".json")
	// Report through the logger, not os.Stderr: bubbletea owns the terminal
	// while this runs, and a stray write corrupts the rendered frame.
	if err := atomicfile.WriteJSON(path, item, 0o644); err != nil {
		log.Logger().Error("tracker: persist item",
			zap.String("item", item.ID), zap.Error(err))
	}
}

// removeItemFile deletes an item file from disk. Must be called with s.mu held.
func (s *Store) removeItemFile(id string) {
	if s.storageDir == "" {
		return
	}
	_ = os.Remove(filepath.Join(s.storageDir, id+".json"))
}

// Create adds a new item and returns it
func (s *Store) Create(subject, description, activeForm string, metadata map[string]any) *Item {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("%d", s.nextID)
	s.nextID++

	now := time.Now()
	item := &Item{
		ID:              id,
		Subject:         subject,
		Description:     description,
		ActiveForm:      activeForm,
		Status:          StatusPending,
		Metadata:        metadata,
		Blocks:          []string{},
		BlockedBy:       []string{},
		CreatedAt:       now,
		UpdatedAt:       now,
		StatusChangedAt: now,
	}

	s.items[id] = item
	s.persistItem(item)
	return item
}

// Get retrieves a copy of an item by ID.
func (s *Store) Get(id string) (*Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.items[id]
	if !ok || item.Status == StatusDeleted {
		return nil, false
	}
	cp := *item
	return &cp, true
}

// Update modifies an existing item. Returns error if item not found.
func (s *Store) Update(id string, opts ...UpdateOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if !ok {
		return fmt.Errorf("item %s not found", id)
	}

	for _, opt := range opts {
		opt(item)
	}

	item.UpdatedAt = time.Now()
	s.persistItem(item)
	return nil
}

// List returns copies of all non-deleted items sorted by ID.
func (s *Store) List() []*Item {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]*Item, 0, len(s.items))
	for _, item := range s.items {
		if item.Status != StatusDeleted {
			cp := *item
			items = append(items, &cp)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return compareNumericIDs(items[i].ID, items[j].ID)
	})

	return items
}

// IsBlocked returns true if the item has any uncompleted blockers
func (s *Store) IsBlocked(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.items[id]
	if !ok || item.Status == StatusDeleted {
		return false
	}

	for _, blockerID := range item.BlockedBy {
		blocker, ok := s.items[blockerID]
		if ok && blocker.Status != StatusCompleted && blocker.Status != StatusDeleted {
			return true
		}
	}
	return false
}

// OpenBlockers returns IDs of uncompleted items that block the given item
func (s *Store) OpenBlockers(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.items[id]
	if !ok || item.Status == StatusDeleted {
		return nil
	}

	var open []string
	for _, blockerID := range item.BlockedBy {
		blocker, ok := s.items[blockerID]
		if ok && blocker.Status != StatusCompleted && blocker.Status != StatusDeleted {
			open = append(open, blockerID)
		}
	}
	return open
}

// Delete marks an item as deleted and removes its file from disk
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.items[id]
	if !ok {
		return fmt.Errorf("item %s not found", id)
	}

	item.Status = StatusDeleted
	item.UpdatedAt = time.Now()
	s.removeItemFile(id)
	return nil
}

// AllMarkedCompleted reports whether the store has items and every one of them
// carries StatusCompleted.
//
// The question is what the list records about itself — whether the model closed
// out every item it wrote down — and Status is the only record of that.
// Deriving it from executors would answer a different question and answer it
// wrongly: an item left in_progress with no worker is abandoned work, not
// finished work, and marking it done would wipe the list at turn end and take
// the [stalled] row down with it.
func (s *Store) AllMarkedCompleted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.items) == 0 {
		return false
	}
	for _, t := range s.items {
		if t.Status == StatusDeleted {
			continue
		}
		if t.Status != StatusCompleted {
			return false
		}
	}
	return true
}

// Reset clears all items (for new sessions)
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove all item files from disk
	if s.storageDir != "" {
		for id := range s.items {
			_ = os.Remove(filepath.Join(s.storageDir, id+".json"))
		}
	}

	s.items = make(map[string]*Item)
	s.nextID = 1
}

// Export returns a snapshot of all items (including deleted) for session persistence
func (s *Store) Export() []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]Item, 0, len(s.items))
	for _, t := range s.items {
		items = append(items, *t)
	}
	sort.Slice(items, func(i, j int) bool {
		return compareNumericIDs(items[i].ID, items[j].ID)
	})
	return items
}

// Import restores items from a snapshot (used when loading a session)
func (s *Store) Import(items []Item) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items = make(map[string]*Item, len(items))
	s.nextID = 1
	for i := range items {
		t := items[i]
		normalizeItemSlices(&t)
		s.items[t.ID] = &t
		var idNum int
		if _, err := fmt.Sscanf(t.ID, "%d", &idNum); err == nil && idNum >= s.nextID {
			s.nextID = idNum + 1
		}
		s.persistItem(&t)
	}
	s.demoteOrphanedItems()
}

// GetStorageDir returns the current storage directory.
func (s *Store) GetStorageDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageDir
}

// ReloadFromDisk re-reads all item files from the storage directory.
// This picks up changes made by other processes (e.g., background agents).
// No-op if no storage directory is configured or if directory hasn't been modified.
func (s *Store) ReloadFromDisk() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.storageDir == "" {
		return
	}

	// Check if any item file has been modified since last reload.
	// We can't rely on directory mtime alone because modifying existing
	// files in-place doesn't update the directory mtime on macOS/most
	// filesystems.
	items, err := os.ReadDir(s.storageDir)
	if err != nil {
		return
	}

	changed := false
	for _, item := range items {
		if item.IsDir() || filepath.Ext(item.Name()) != ".json" {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(s.lastDirMod) {
			changed = true
			break
		}
	}

	if !changed && !s.lastDirMod.IsZero() {
		return
	}

	s.lastDirMod = time.Now()
	_ = s.loadFromDisk()
}

// UpdateOption is a functional option for updating an item
type UpdateOption func(*Item)

// WithStatus sets the item status and records the status change timestamp.
func WithStatus(status string) UpdateOption {
	return func(t *Item) {
		if t.Status != status {
			t.StatusChangedAt = time.Now()
		}
		t.Status = status
	}
}

// WithSubject sets the item subject
func WithSubject(subject string) UpdateOption {
	return func(t *Item) {
		t.Subject = subject
	}
}

// WithDescription sets the item description
func WithDescription(description string) UpdateOption {
	return func(t *Item) {
		t.Description = description
	}
}

// WithActiveForm sets the item activeForm
func WithActiveForm(activeForm string) UpdateOption {
	return func(t *Item) {
		t.ActiveForm = activeForm
	}
}

// WithOwner sets the item owner
func WithOwner(owner string) UpdateOption {
	return func(t *Item) {
		t.Owner = owner
	}
}

// WithMetadata merges metadata (nil values delete keys)
func WithMetadata(metadata map[string]any) UpdateOption {
	return func(t *Item) {
		if t.Metadata == nil {
			t.Metadata = make(map[string]any)
		}
		for k, v := range metadata {
			if v == nil {
				delete(t.Metadata, k)
			} else {
				t.Metadata[k] = v
			}
		}
	}
}

// WithAddBlocks adds item IDs that this item blocks
func WithAddBlocks(ids []string) UpdateOption {
	return func(t *Item) {
		t.Blocks = appendUnique(t.Blocks, ids)
	}
}

// WithAddBlockedBy adds item IDs that block this item
func WithAddBlockedBy(ids []string) UpdateOption {
	return func(t *Item) {
		t.BlockedBy = appendUnique(t.BlockedBy, ids)
	}
}

// normalizeItemSlices ensures Blocks and BlockedBy are non-nil slices.
func normalizeItemSlices(t *Item) {
	if t.Blocks == nil {
		t.Blocks = []string{}
	}
	if t.BlockedBy == nil {
		t.BlockedBy = []string{}
	}
}

// FindByMetadata returns a copy of the first non-deleted item whose metadata[key] equals want.
// Returns nil if no match is found.
func (s *Store) FindByMetadata(key, want string) *Item {
	if want == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.items {
		if t.Status == StatusDeleted {
			continue
		}
		if t.Metadata == nil {
			continue
		}
		if v, ok := t.Metadata[key]; ok {
			if str, ok := v.(string); ok && str == want {
				cp := *t
				return &cp
			}
		}
	}
	return nil
}

// compareNumericIDs compares two item IDs numerically (e.g. "2" < "10").
// Falls back to lexicographic comparison if parsing fails.
func compareNumericIDs(a, b string) bool {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	if errA == nil && errB == nil {
		return na < nb
	}
	return a < b
}

// appendUnique appends ids to slice, skipping duplicates
func appendUnique(slice, ids []string) []string {
	existing := make(map[string]bool, len(slice))
	for _, id := range slice {
		existing[id] = true
	}
	for _, id := range ids {
		if !existing[id] {
			slice = append(slice, id)
			existing[id] = true
		}
	}
	return slice
}
