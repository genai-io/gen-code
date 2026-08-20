package trigger

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/genai-io/san/internal/hook"
)

const DefaultFileWatcherInterval = 500 * time.Millisecond

type FileWatcher struct {
	engine    hook.Handler
	onOutcome func(hook.HookOutcome)
	interval  time.Duration

	mu      sync.Mutex
	started bool
	stopCh  chan struct{}
	done    chan struct{} // closed when loop() returns
	paths   map[string]fileSnapshot
}

type fileSnapshot struct {
	exists  bool
	size    int64
	modTime time.Time
}

func NewFileWatcher(engine hook.Handler, onOutcome func(hook.HookOutcome)) *FileWatcher {
	return &FileWatcher{
		engine:    engine,
		onOutcome: onOutcome,
		interval:  DefaultFileWatcherInterval,
		stopCh:    make(chan struct{}),
		paths:     make(map[string]fileSnapshot),
	}
}

func (w *FileWatcher) SetPaths(paths []string) {
	if w == nil {
		return
	}

	normalized := make(map[string]fileSnapshot)
	for _, path := range paths {
		if !filepath.IsAbs(path) || path == "" {
			continue
		}
		clean := filepath.Clean(path)
		normalized[clean] = snapshotFile(clean)
	}

	w.mu.Lock()
	w.paths = normalized
	shouldStart := len(normalized) > 0 && !w.started
	var stopCh chan struct{}
	var done chan struct{}
	if shouldStart {
		w.started = true
		w.done = make(chan struct{})
		stopCh = w.stopCh
		done = w.done
	}
	w.mu.Unlock()

	if shouldStart {
		go w.loop(stopCh, done)
	}
}

func (w *FileWatcher) CurrentPaths() []string {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	paths := make([]string, 0, len(w.paths))
	for path := range w.paths {
		paths = append(paths, path)
	}
	return paths
}

func (w *FileWatcher) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	stopCh := w.stopCh
	done := w.done
	w.started = false
	w.stopCh = make(chan struct{})
	w.mu.Unlock()
	close(stopCh)
	if done != nil {
		<-done // wait for loop goroutine to exit
	}
}

func (w *FileWatcher) loop(stopCh, done chan struct{}) {
	if done != nil {
		defer close(done)
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.poll()
		case <-stopCh:
			return
		}
	}
}

func (w *FileWatcher) poll() {
	type change struct {
		path  string
		event string
		next  fileSnapshot
	}

	w.mu.Lock()
	paths := make(map[string]fileSnapshot, len(w.paths))
	maps.Copy(paths, w.paths)
	w.mu.Unlock()

	var changes []change
	for path, prev := range paths {
		next := snapshotFile(path)
		if event, ok := detectFileEvent(prev, next); ok {
			changes = append(changes, change{path: path, event: event, next: next})
		}
	}

	if len(changes) == 0 {
		return
	}

	w.mu.Lock()
	for _, change := range changes {
		if _, ok := w.paths[change.path]; ok {
			w.paths[change.path] = change.next
		}
	}
	w.mu.Unlock()

	for _, change := range changes {
		if w.engine == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		outcome := w.engine.Execute(ctx, hook.FileChanged, hook.HookInput{
			FilePath: change.path,
			Event:    change.event,
		})
		cancel()
		if len(outcome.WatchPaths) > 0 {
			w.SetPaths(outcome.WatchPaths)
		}
		if w.onOutcome != nil {
			w.onOutcome(outcome)
		}
	}
}

func snapshotFile(path string) fileSnapshot {
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}
	}
	return fileSnapshot{
		exists:  true,
		size:    info.Size(),
		modTime: info.ModTime(),
	}
}

func detectFileEvent(prev, next fileSnapshot) (string, bool) {
	switch {
	case !prev.exists && next.exists:
		return "add", true
	case prev.exists && !next.exists:
		return "unlink", true
	case prev.exists && next.exists && (prev.size != next.size || !prev.modTime.Equal(next.modTime)):
		return "change", true
	default:
		return "", false
	}
}
