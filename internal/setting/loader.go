package setting

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/log"
)

// Loader handles loading and merging settings from multiple sources.
type Loader struct {
	userDir      string
	projectDir   string
	projectRoot  string
	claudeCompat bool
}

// NewLoader creates a loader with default paths (~/.san, .san) and Claude compatibility enabled.
func NewLoader() *Loader {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Logger().Warn("failed to determine home directory, user-level settings will be unavailable", zap.Error(err))
	}
	userDir := ""
	if homeDir != "" {
		userDir = confdir.Dir(homeDir)
	}
	return &Loader{
		userDir:      userDir,
		projectDir:   confdir.Dir("."),
		projectRoot:  ".",
		claudeCompat: true,
	}
}

// NewLoaderWithOptions creates a loader with custom options.
func NewLoaderWithOptions(userDir, projectDir string, claudeCompat bool) *Loader {
	return &Loader{
		userDir:      userDir,
		projectDir:   projectDir,
		projectRoot:  filepath.Dir(projectDir),
		claudeCompat: claudeCompat,
	}
}

// NewLoaderForCwd creates a loader rooted at the provided working directory.
func NewLoaderForCwd(cwd string) *Loader {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Logger().Warn("failed to determine home directory, user-level settings will be unavailable", zap.Error(err))
	}
	userDir := ""
	if homeDir != "" {
		userDir = confdir.Dir(homeDir)
	}
	return &Loader{
		userDir:      userDir,
		projectDir:   confdir.Dir(cwd),
		projectRoot:  cwd,
		claudeCompat: true,
	}
}

// Load loads and merges settings from all sources in priority order (lowest to highest):
//  1. ~/.claude/settings.json
//  2. ~/.san/settings.json
//  3. .claude/settings.json
//  4. .san/settings.json
//  5. .claude/settings.local.json
//  6. .san/settings.local.json
func (l *Loader) Load() (*Data, error) {
	homeDir, _ := os.UserHomeDir()

	// Two-phase loading: first load Claude-compat settings, then San-native.
	// For hooks, San-native settings override Claude-compat settings per event
	// to prevent incompatible hooks (e.g., Claude Code's interactive protocol)
	// from blocking San's own hooks.

	type source struct {
		path         string
		claudeCompat bool
	}

	var sources []source
	if l.claudeCompat && homeDir != "" {
		sources = append(sources, source{filepath.Join(homeDir, ".claude", "settings.json"), true})
	}
	if l.userDir != "" {
		sources = append(sources, source{filepath.Join(l.userDir, "settings.json"), false})
	}
	if l.claudeCompat {
		sources = append(sources, source{filepath.Join(l.projectRoot, ".claude", "settings.json"), true})
	}
	sources = append(sources, source{filepath.Join(l.projectDir, "settings.json"), false})
	if l.claudeCompat {
		sources = append(sources, source{filepath.Join(l.projectRoot, ".claude", "settings.local.json"), true})
	}
	sources = append(sources, source{filepath.Join(l.projectDir, "settings.local.json"), false})

	// Collect hooks separately: Claude-compat hooks and San-native hooks.
	// For native hooks, higher-priority sources REPLACE lower-priority sources
	// per event (project overrides user, local overrides project).
	claudeHooks := make(map[string][]Hook)
	nativeHooks := make(map[string][]Hook) // last write wins per event

	settings := NewData()
	for _, src := range sources {
		data, err := os.ReadFile(src.path)
		if err != nil {
			continue
		}
		var s Data
		if err := json.Unmarshal(data, &s); err != nil {
			log.Logger().Warn("failed to parse config file", zap.String("path", src.path), zap.Error(err))
			continue
		}

		// Extract hooks before merging — we'll merge hooks manually
		srcHooks := s.Hooks
		s.Hooks = nil
		settings = mergeSettings(settings, &s)

		// Accumulate hooks by source type.
		// Native hooks: higher-priority sources replace lower-priority per event.
		// This means project .san/settings.json can set "PermissionRequest": []
		// to disable user-level PermissionRequest hooks.
		for event, hooks := range srcHooks {
			if src.claudeCompat {
				claudeHooks[event] = append(claudeHooks[event], hooks...)
			} else {
				nativeHooks[event] = hooks
			}
		}
	}

	// Merge hooks: for each event, use native hooks if available, otherwise use Claude-compat hooks.
	// PermissionRequest hooks are NEVER inherited from Claude-compat sources because
	// Claude Code's interactive permission protocol (e.g., vibe-island-bridge) is
	// incompatible with San's TUI-based approval flow and can cause hangs.
	merged := make(map[string][]Hook)
	for event, hooks := range claudeHooks {
		if event == "PermissionRequest" {
			continue // skip — incompatible protocol
		}
		if _, hasNative := nativeHooks[event]; !hasNative {
			merged[event] = hooks
		}
	}
	for event, hooks := range nativeHooks {
		if len(hooks) > 0 {
			merged[event] = hooks
		}
		// Empty hooks array means explicitly disabled — don't add to merged
	}
	settings.Hooks = merged

	return settings, nil
}

// LoadFile loads settings from a specific file.
func (l *Loader) LoadFile(path string) (*Data, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Data
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveToProject saves settings to the project-level settings file, merging with existing.
func (l *Loader) SaveToProject(settings *Data) error {
	return l.saveToFile(filepath.Join(l.projectDir, "settings.json"), settings)
}

// SaveToUser saves settings to the user-level settings file, merging with existing.
func (l *Loader) SaveToUser(settings *Data) error {
	return l.saveToFile(filepath.Join(l.userDir, "settings.json"), settings)
}

func (l *Loader) saveToFile(path string, settings *Data) error {
	toSave := settings
	if data, err := os.ReadFile(path); err == nil {
		existing := NewData()
		if err := json.Unmarshal(data, existing); err == nil {
			toSave = mergeSettings(existing, settings)
		}
	}
	return atomicfile.WriteJSON(path, toSave, 0o644)
}

var (
	loadedSettings   *Data
	loadedSettingsMu sync.Mutex
)

// Load loads settings using the default loader (cached after first call).
func Load() (*Data, error) {
	loadedSettingsMu.Lock()
	defer loadedSettingsMu.Unlock()
	if loadedSettings != nil {
		return loadedSettings, nil
	}
	s, err := NewLoader().Load()
	if err != nil {
		return nil, err
	}
	loadedSettings = s
	return s, nil
}

// Reload clears the settings cache and reloads from disk.
func Reload() (*Data, error) {
	loadedSettingsMu.Lock()
	defer loadedSettingsMu.Unlock()
	s, err := NewLoader().Load()
	if err != nil {
		return nil, err
	}
	loadedSettings = s
	return s, nil
}

// LoadForCwd loads settings for the provided working directory without using
// the process-global cache. This is used when the session cwd changes.
func LoadForCwd(cwd string) (*Data, error) {
	return NewLoaderForCwd(cwd).Load()
}

// defaultData returns default settings without loading from disk.
func defaultData() *Data {
	return NewData()
}

// UpdateDisabledToolsAt updates disabled tools at user level (true) or project
// level (false). It replaces the whole disabledTools block rather than routing
// through the merging save: the caller passes the complete desired map for the
// level, and merging would OR it with the file's previous keys so a re-enable
// (a removed key) or a default-disabled tool toggled back off could never
// persist. Cross-level layering still ORs on Load by design.
func UpdateDisabledToolsAt(disabledTools map[string]bool, userLevel bool) error {
	return updateSettingsFile(userLevel, func(d *Data) {
		d.DisabledTools = disabledTools
	})
}

// updateSettingsFile reads the settings file at the requested level (true =
// user-wide, false = project-local), lets mutate replace a single block, and
// writes it back — leaving every other setting in the file untouched. It
// deliberately does NOT route through SaveToUser/SaveToProject (which merge):
// those mergers OR the boolean fields, so reusing them would OR the new config
// with the file's own previous value and a true→false toggle (e.g. disabling an
// arm from /config) could never be persisted. Replacing the block lets the
// off-toggle stick. (Cross-level layering still ORs on Load by design —
// disabling at a lower-priority level cannot override an enable at a
// higher-priority one.)
func updateSettingsFile(userLevel bool, mutate func(*Data)) error {
	loader := NewLoader()
	path := filepath.Join(loader.projectDir, "settings.json")
	if userLevel {
		path = filepath.Join(loader.userDir, "settings.json")
	}

	existing := NewData()
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, existing)
	}
	mutate(existing)
	if err := atomicfile.WriteJSON(path, existing, 0o644); err != nil {
		return err
	}

	loadedSettingsMu.Lock()
	loadedSettings = nil
	loadedSettingsMu.Unlock()
	return nil
}

// UpdateSelfLearnAt persists the L1 self-learning config at the requested
// settings level, rewriting only the selfLearn block. Returns Validate's error
// verbatim if the new config is illegal (§3.1) so the caller can surface it
// inline before touching disk.
func UpdateSelfLearnAt(cfg SelfLearnSettings, userLevel bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return updateSettingsFile(userLevel, func(d *Data) { d.SelfLearn = cfg })
}

// UpdateAutoPilotAt persists the autopilot config at the requested settings
// level, rewriting only the autoPilot block.
func UpdateAutoPilotAt(cfg AutoPilotSettings, userLevel bool) error {
	return updateSettingsFile(userLevel, func(d *Data) { d.AutoPilot = cfg })
}

// UpdateLastOperationMode persists the user-wide mode restored when a new
// session starts.
func UpdateLastOperationMode(mode OperationMode) error {
	return updateSettingsFile(true, func(d *Data) { d.LastOperationMode = mode.PersistenceName() })
}

// AutoPilotPresetDir is the folder where /autopilot Export saves named configs
// and Import reads them — a shared, non-session space for reusable presets.
func AutoPilotPresetDir() string {
	return filepath.Join(NewLoader().userDir, "autopilot")
}

// ExportAutoPilot writes cfg as a named preset (<presetDir>/<name>.json),
// returning the path. The name is reduced to a bare filename and must be
// non-empty.
func ExportAutoPilot(name string, cfg AutoPilotSettings) (string, error) {
	base := sanitizePresetName(name)
	if base == "" {
		return "", fmt.Errorf("preset name required")
	}
	dir := AutoPilotPresetDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, base+".json")
	if err := atomicfile.WriteJSON(path, cfg, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ListAutoPilotPresets returns the saved preset names (without extension),
// sorted. A missing directory yields an empty list, not an error.
func ListAutoPilotPresets() ([]string, error) {
	entries, err := os.ReadDir(AutoPilotPresetDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names, nil
}

// ImportAutoPilot reads a named preset back into an AutoPilotSettings.
func ImportAutoPilot(name string) (AutoPilotSettings, error) {
	var cfg AutoPilotSettings
	base := sanitizePresetName(name)
	if base == "" {
		return cfg, fmt.Errorf("preset name required")
	}
	data, err := os.ReadFile(filepath.Join(AutoPilotPresetDir(), base+".json"))
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

// sanitizePresetName reduces a user-entered name to a safe bare filename so a
// preset can never escape the preset directory.
func sanitizePresetName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".json")
	name = filepath.Base(name) // strip any path components
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// defaultDisabledTools are tools that ship disabled: rarely-used features
// whose schemas should not cost every conversation context. The /tool panel
// enables one by writing an explicit "false" entry, which overrides the
// default here.
var defaultDisabledTools = map[string]bool{
	"Cron": true,
	// Task tracker: the plan panel and its tools ship off by default. The
	// workflow guidance lives in the tools' own schemas, so re-enabling them
	// from the /tool panel brings the guidance back with them.
	"TaskCreate": true,
	"TaskGet":    true,
	"TaskUpdate": true,
	// Inter-agent messaging: steering a still-running background worker. Never
	// exercised in practice (subagents run to completion and report back on
	// their own), so it ships off; the tool's own schema documents it when a
	// user re-enables it from the /tool panel.
	"SendMessage": true,
	// Stopping a background agent is uncommon and destructive. It remains
	// available through /tool when a user wants the model to manage workers.
	"AgentStop": true,
}

// IsDefaultDisabledTool reports whether the tool ships disabled. The /tool
// panel uses this to write an explicit enable entry instead of deleting the
// key (deletion would fall back to the default).
func IsDefaultDisabledTool(name string) bool {
	return defaultDisabledTools[name]
}

// WithDefaultDisabledTools overlays the factory defaults onto explicit
// settings entries: an explicit entry (true or false) always wins; absent
// keys fall back to the default.
func WithDefaultDisabledTools(explicit map[string]bool) map[string]bool {
	result := make(map[string]bool, len(explicit)+len(defaultDisabledTools))
	for name, disabled := range defaultDisabledTools {
		result[name] = disabled
	}
	maps.Copy(result, explicit)
	for name, disabled := range result {
		if !disabled {
			delete(result, name)
		}
	}
	return result
}

// GetDisabledTools returns the merged disabled tools map from loaded settings,
// with factory defaults applied. Returns a copy so callers cannot mutate the
// cached settings.
func GetDisabledTools() map[string]bool {
	s, err := Load()
	if err != nil {
		return WithDefaultDisabledTools(nil)
	}
	return WithDefaultDisabledTools(s.DisabledTools)
}

// GetDisabledToolsAt returns disabled tools from a single settings file (not merged).
// userLevel=true reads from ~/.san/settings.json; false reads from .san/settings.json.
func GetDisabledToolsAt(userLevel bool) map[string]bool {
	loader := NewLoader()
	path := filepath.Join(loader.projectDir, "settings.json")
	if userLevel {
		path = filepath.Join(loader.userDir, "settings.json")
	}
	s, err := loader.LoadFile(path)
	if err != nil || s.DisabledTools == nil {
		return make(map[string]bool)
	}
	result := make(map[string]bool, len(s.DisabledTools))
	maps.Copy(result, s.DisabledTools)
	return result
}

// PersonaAt returns the persona pinned in a single settings file (not merged).
// userLevel=true reads ~/.san/settings.json; false reads <cwd>/.san/settings.json.
func PersonaAt(cwd string, userLevel bool) string {
	loader := NewLoader()
	if cwd != "" {
		loader = NewLoaderForCwd(cwd)
	}
	dir := loader.projectDir
	if userLevel {
		dir = loader.userDir
	}
	if dir == "" {
		return ""
	}
	s, err := loader.LoadFile(filepath.Join(dir, "settings.json"))
	if err != nil || s == nil {
		return ""
	}
	return s.Persona
}

// AddAllowRuleAt appends a permission allow rule to project settings rooted at
// the provided cwd.
func AddAllowRuleAt(toolName string, args map[string]any, cwd string) error {
	return AddAllowRuleDirectlyAt(BuildRule(toolName, args), cwd)
}

// AddAllowRuleDirectlyAt appends a pre-built allow rule string to the project
// settings associated with cwd. When cwd is empty, it uses the process cwd.
func AddAllowRuleDirectlyAt(rule, cwd string) error {
	if rule == "" {
		return nil
	}

	loader := NewLoader()
	if cwd != "" {
		loader = NewLoaderForCwd(cwd)
	}
	path := filepath.Join(loader.projectDir, "settings.json")

	// Load existing to check for duplicates
	existing, _ := loader.LoadFile(path)
	if existing != nil && slices.Contains(existing.Permissions.Allow, rule) {
		return nil // already exists
	}

	settings := &Data{
		Permissions: PermissionSettings{
			Allow: []string{rule},
		},
	}
	if err := loader.SaveToProject(settings); err != nil {
		return err
	}
	loadedSettingsMu.Lock()
	loadedSettings = nil
	loadedSettingsMu.Unlock()
	return nil
}

// LoadTheme returns the configured theme string, or "" if none is set.
func LoadTheme() string {
	s, err := Load()
	if err != nil || s == nil {
		return ""
	}
	return s.Theme
}

// SaveTheme persists the chosen theme to ~/.san/settings.json.
func SaveTheme(t string) error {
	if err := NewLoader().SaveToUser(&Data{Theme: t}); err != nil {
		return err
	}
	loadedSettingsMu.Lock()
	loadedSettings = nil
	loadedSettingsMu.Unlock()
	return nil
}

// SaveContextBar persists the context-usage bar on/off choice to
// ~/.san/settings.json. The value is written as an explicit pointer so an
// "off" choice overrides any inherited "on" instead of being dropped as a
// zero value (see Data.ContextBar).
func SaveContextBar(on bool) error {
	if err := NewLoader().SaveToUser(&Data{ContextBar: &on}); err != nil {
		return err
	}
	loadedSettingsMu.Lock()
	loadedSettings = nil
	loadedSettingsMu.Unlock()
	return nil
}

// SaveAllowBypass persists whether YOLO mode (bypassPermissions) is reachable
// to ~/.san/settings.json. Written as an explicit pointer for the same reason
// as ContextBar: an "off" choice must override an inherited "on" rather than
// being dropped as a zero value.
func SaveAllowBypass(on bool) error {
	if err := NewLoader().SaveToUser(&Data{AllowBypass: &on}); err != nil {
		return err
	}
	loadedSettingsMu.Lock()
	loadedSettings = nil
	loadedSettingsMu.Unlock()
	return nil
}

// SavePersonaAt persists the chosen persona name at the given scope: the
// project file (.san/settings.json under cwd) when userLevel is false, or the
// user file (~/.san/settings.json) when true. An empty name clears the field.
// It bypasses mergeSettings so an empty value can actually clear it on disk.
//
// Scope matters: a project-scoped persona should save project-level so the
// selection lives with the persona (and doesn't leak to other projects, where
// it wouldn't resolve); user-scoped personas save user-level to become the
// default everywhere.
func SavePersonaAt(cwd, name string, userLevel bool) error {
	loader := NewLoader()
	if cwd != "" {
		loader = NewLoaderForCwd(cwd)
	}
	dir := loader.projectDir
	if userLevel {
		dir = loader.userDir
	}
	if dir == "" {
		return os.ErrNotExist
	}
	path := filepath.Join(dir, "settings.json")

	existing := NewData()
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, existing)
	}
	existing.Persona = name

	if err := atomicfile.WriteJSON(path, existing, 0o644); err != nil {
		return err
	}

	loadedSettingsMu.Lock()
	loadedSettings = nil
	loadedSettingsMu.Unlock()
	return nil
}
