// Package task tracks background bash and subagent tasks for the TUI's
// task panel and the agent's TaskGet tool. Exposes *Manager directly.
package task

// Options holds all dependencies for initialization.
type Options struct {
	OutputDir string
}

// Initialize creates the package-level *Manager and configures it. It returns
// the error from configuring the output directory: the manager is installed
// either way, so a caller that cannot use the directory still gets a working
// in-memory manager and can decide what to say about the lost persistence.
func Initialize(opts Options) error {
	m := NewManager()
	var err error
	if opts.OutputDir != "" {
		err = m.SetOutputDir(opts.OutputDir)
	}
	defaultManager = m
	return err
}

// Default returns the package-level *Manager.
func Default() *Manager {
	return defaultManager
}

// SetDefaultTracker replaces the package-level *Manager. Intended for
// tests. A nil argument restores a fresh empty *Manager.
func SetDefaultTracker(m *Manager) {
	if m == nil {
		defaultManager = NewManager()
		return
	}
	defaultManager = m
}

// ResetDefaultTracker restores a fresh empty *Manager. Intended for
// tests.
func ResetDefaultTracker() {
	defaultManager = NewManager()
}

var defaultManager = NewManager()

// SetOutputDir on *Manager delegates to the package-level setOutputDir.
func (m *Manager) SetOutputDir(dir string) error {
	return setOutputDir(dir)
}
