// Package setting owns the merged user+project settings and the
// central permission decision gate. Exposes *Settings directly.
//
// *Settings is the live, mutex-protected handle (controller). *Data is
// the on-disk schema (struct with the actual setting fields). The
// controller wraps a *Data under a mutex and exposes mutex-protected
// views.
package setting

import (
	"sync"

	"github.com/genai-io/san/internal/tool/perm"
)

// Options holds configuration for Initialize.
type Options struct {
	CWD string
}

// Initialize loads settings for cwd and installs the package-level *Settings.
func Initialize(opts Options) {
	d := InitForApp(opts.CWD)
	defaultSettings = &Settings{data: d}
}

// Default returns the package-level *Settings.
func Default() *Settings {
	return defaultSettings
}

// New wraps a *Data in a live *Settings handle, independent of the package-level
// default. Intended for tests and any caller that needs an isolated instance.
func New(d *Data) *Settings {
	return &Settings{data: d}
}

// DefaultIfInit returns the package-level *Settings, or nil if it
// has not been initialized with real settings yet.
func DefaultIfInit() *Settings {
	if defaultSettings == nil || defaultSettings.data == nil {
		return nil
	}
	return defaultSettings
}

// SetDefaultSettings replaces the package-level *Settings. Intended for
// tests. A nil argument restores a fresh empty *Settings.
func SetDefaultSettings(s *Settings) {
	if s == nil {
		defaultSettings = &Settings{}
		return
	}
	defaultSettings = s
}

// ResetDefaultSettings restores a fresh empty *Settings. Intended for
// tests.
func ResetDefaultSettings() {
	defaultSettings = &Settings{}
}

var defaultSettings = &Settings{}

// Settings is the live handle: a *Data under a mutex. Methods are
// mutex-protected views over the underlying *Data.
type Settings struct {
	mu   sync.RWMutex
	data *Data
}

func (s *Settings) Snapshot() *Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Clone()
}

// AllowBypass reports whether Bypass Permissions mode is reachable (in the
// Shift+Tab cycle and as a settings defaultMode). It is opt-out: enabled
// unless the user explicitly sets "allowBypass": false to lock it out.
func (s *Settings) AllowBypass() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data == nil || s.data.AllowBypass == nil || *s.data.AllowBypass
}

func (s *Settings) IsGitRepo(cwd string) bool {
	return IsGitRepo(cwd)
}

func (s *Settings) Reload(cwd string) error {
	var (
		newData *Data
		err     error
	)
	if cwd != "" {
		newData, err = LoadForCwd(cwd)
	} else {
		newData, err = Load()
	}
	if err != nil {
		return err
	}
	if newData == nil {
		newData = NewData()
	}
	mergeProviderPreferences(newData)
	cloned := newData.Clone()

	s.mu.Lock()
	s.data = cloned
	s.mu.Unlock()

	return nil
}

func (s *Settings) DisabledTools() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return WithDefaultDisabledTools(nil)
	}
	return WithDefaultDisabledTools(s.data.DisabledTools)
}

func (s *Settings) SearchProvider() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return ""
	}
	return s.data.SearchProvider
}

func (s *Settings) SetSearchProvider(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data != nil {
		s.data.SearchProvider = provider
	}
}

func (s *Settings) StreamFirstChunkTimeout() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return ""
	}
	return s.data.StreamFirstChunkTimeout
}

func (s *Settings) StreamIdleTimeout() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return ""
	}
	return s.data.StreamIdleTimeout
}

func (s *Settings) HookUITimeout() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return ""
	}
	return s.data.HookUITimeout
}

// SelfLearn returns just the self-learning settings, by value. Unlike
// Snapshot() it does not deep-clone the whole Data — SelfLearnSettings is a
// value type (no shared maps/pointers), so a plain copy under the read lock is
// safe. Used by the per-turn capability-drift check, which would otherwise
// clone every permission map and hook to read this one field.
func (s *Settings) SelfLearn() SelfLearnSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return SelfLearnSettings{}
	}
	return s.data.SelfLearn
}

func (s *Settings) Hooks() map[string][]Hook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return nil
	}
	return s.data.Hooks
}

func (s *Settings) CheckPermission(toolName string, args map[string]any, session *SessionPermissions) perm.Decision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return perm.Prompt
	}
	return s.data.CheckPermission(toolName, args, session)
}

func (s *Settings) HasPermissionToUseTool(toolName string, args map[string]any, session *SessionPermissions) PermissionDecision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return decide(perm.Prompt, "default: no settings loaded")
	}
	return s.data.HasPermissionToUseTool(toolName, args, session)
}

func (s *Settings) ResolveHookAllow(toolName string, args map[string]any, session *SessionPermissions) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		// No settings loaded means no deny rules, no ask rules and no
		// confirmation tiers to check the hook's allow against, so the waiver
		// cannot be certified: refuse it and let the call reach the gate, which
		// prompts for the same reason.
		return false
	}
	return s.data.ResolveHookAllow(toolName, args, session)
}

func (s *Settings) GetDisabledToolsAt(userLevel bool) map[string]bool {
	return GetDisabledToolsAt(userLevel)
}

func (s *Settings) UpdateDisabledToolsAt(disabledTools map[string]bool, userLevel bool) error {
	return UpdateDisabledToolsAt(disabledTools, userLevel)
}
