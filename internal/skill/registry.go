package skill

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
)

// NewRegistry creates an empty skill registry.
func NewRegistry() *Registry {
	return &Registry{
		skills: make(map[string]*Skill),
	}
}

// Registry manages loaded skills and their states.
type Registry struct {
	mu           sync.RWMutex
	skills       map[string]*Skill
	userStore    *Store // User-level store (~/.san/skills.json)
	projectStore *Store // Project-level store (.san/skills.json)
	cwd          string // Current working directory for project store

	// onStateChange fires every time a skill transitions between states.
	// Used by the session recorder to emit skill.state.changed records.
	// Called with the read lock NOT held — recorder may do I/O.
	onStateChange func(name, previous, current, caller string)

	// personaSkills records the skills the active persona loaded into the
	// registry (each with the global skill its name displaced, if any), so
	// ClearPersona removes exactly them and restores the shadowed ones without
	// touching skills.json. nil when no persona skills are loaded.
	personaSkills []personaSkill
}

// StateChangeObserver is the callback shape SetStateChangeObserver registers.
// Fires once per accepted SetState call.
type StateChangeObserver func(name, previous, current, caller string)

// SetStateChangeObserver registers a callback for state transitions. nil
// clears it. Replaces any prior observer; the registry supports a single
// recorder consumer at a time, which is enough today (one main session).
func (r *Registry) SetStateChangeObserver(cb StateChangeObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onStateChange = cb
}

// Store handles persistence of skill states to a skills.json file.
type Store struct {
	path   string
	states map[string]SkillState
}

// storeData is the JSON structure for skills.json.
type storeData struct {
	Skills map[string]SkillState `json:"skills"`
}

// NewStore creates a new store for skill state persistence at the given path.
func NewStore(path string) (*Store, error) {
	store := &Store{
		path:   path,
		states: make(map[string]SkillState),
	}

	// Load existing states
	store.load()

	return store, nil
}

// NewUserStore creates a store for user-level settings (~/.san/skills.json).
func NewUserStore() (*Store, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return NewStore(filepath.Join(confdir.Dir(homeDir), "skills.json"))
}

// NewProjectStore creates a store for project-level settings (.san/skills.json).
func NewProjectStore(cwd string) (*Store, error) {
	return NewStore(filepath.Join(confdir.Dir(cwd), "skills.json"))
}

// load reads persisted states from disk.
func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // File doesn't exist or can't be read
	}

	var storeData storeData
	if err := json.Unmarshal(data, &storeData); err != nil {
		return
	}

	if storeData.Skills != nil {
		s.states = storeData.Skills
	}
}

// save writes states to disk.
func (s *Store) save() error {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	storeData := storeData{
		Skills: s.states,
	}

	return atomicfile.WriteJSON(s.path, storeData, 0o644)
}

// GetState returns the persisted state for a skill.
func (s *Store) GetState(name string) (SkillState, bool) {
	state, ok := s.states[name]
	return state, ok
}

// SetState sets and persists the state for a skill.
func (s *Store) SetState(name string, state SkillState) error {
	s.states[name] = state
	return s.save()
}

// Initialize loads all skills and applies persisted states.
// This should be called at application startup.
// Get returns a skill by name.
func (r *Registry) Get(name string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[name]
	return skill, ok
}

// FindByPartialName finds a skill by partial name match.
// It tries exact match first, then checks if name is a suffix (e.g., "commit" matches "git:commit").
func (r *Registry) FindByPartialName(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Exact match first
	if skill, ok := r.skills[name]; ok {
		return skill
	}

	// Try suffix match (e.g., "commit" -> "git:commit")
	name = strings.ToLower(name)
	for fullName, skill := range r.skills {
		// Check if name matches the part after ":"
		if idx := strings.LastIndex(fullName, ":"); idx >= 0 {
			shortName := strings.ToLower(fullName[idx+1:])
			if shortName == name {
				return skill
			}
		}
		// Also try lowercase full match
		if strings.ToLower(fullName) == name {
			return skill
		}
	}

	return nil
}

// List returns all skills sorted by full name (namespace:name).
func (r *Registry) List() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	skills := make([]*Skill, 0, len(r.skills))
	for _, skill := range r.skills {
		skills = append(skills, skill)
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].FullName() < skills[j].FullName()
	})

	return skills
}

// GetEnabled returns all enabled or active skills.
func (r *Registry) GetEnabled() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	skills := make([]*Skill, 0)
	for _, skill := range r.skills {
		if skill.IsEnabled() {
			skills = append(skills, skill)
		}
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].FullName() < skills[j].FullName()
	})

	return skills
}

// GetActive returns all active skills (model-aware).
func (r *Registry) GetActive() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	skills := make([]*Skill, 0)
	for _, skill := range r.skills {
		if skill.IsActive() {
			skills = append(skills, skill)
		}
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].FullName() < skills[j].FullName()
	})

	return skills
}

// SetState sets the state for a skill and persists it to the specified level.
// The name should be the full name (namespace:name or just name).
// If userLevel is true, saves to ~/.san/skills.json, otherwise to .san/skills.json.
func (r *Registry) SetState(name string, state SkillState, userLevel bool) error {
	r.mu.Lock()
	skill, ok := r.skills[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("skill not found: %s", name)
	}
	previous := skill.State
	skill.State = state
	observer := r.onStateChange
	fullName := skill.FullName()
	r.mu.Unlock()

	// Persist to the appropriate store
	var err error
	if userLevel {
		err = r.userStore.SetState(fullName, state)
	} else {
		err = r.projectStore.SetState(fullName, state)
	}

	// Fire observer after the write so the recorder sees the durable state
	// transition, not a no-op or rollback-on-error.
	if err == nil && observer != nil && previous != state {
		level := "project"
		if userLevel {
			level = "user"
		}
		observer(fullName, string(previous), string(state), "user:/skills:"+level)
	}
	return err
}

// GetStatesAt returns a copy of skill states from the specified level.
func (r *Registry) GetStatesAt(userLevel bool) map[string]SkillState {
	var src map[string]SkillState
	if userLevel {
		src = r.userStore.states
	} else {
		src = r.projectStore.states
	}
	result := make(map[string]SkillState, len(src))
	maps.Copy(result, src)
	return result
}

// GetSkillsSection generates the body of the skills directory for the system
// prompt. Only includes active skills (progressive loading — full instructions
// arrive only when the Skill tool is invoked).
//
// Returns plain body text without the outer XML tag; the system catalog
// wraps it in <skills>…</skills>.
func (r *Registry) GetSkillsSection() string {
	active := r.GetActive()
	if len(active) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Use the Skill tool to invoke these capabilities:\n\n")

	for _, skill := range active {
		// Only include name and description (progressive loading)
		sb.WriteString(fmt.Sprintf("- %s: %s", skill.FullName(), skill.Description))
		if skill.ArgumentHint != "" {
			sb.WriteString(fmt.Sprintf(" %s", skill.ArgumentHint))
		}
		if skill.HasResources() {
			resources := []string{}
			if len(skill.Scripts) > 0 {
				resources = append(resources, fmt.Sprintf("%d scripts", len(skill.Scripts)))
			}
			if len(skill.References) > 0 {
				resources = append(resources, fmt.Sprintf("%d refs", len(skill.References)))
			}
			sb.WriteString(fmt.Sprintf(" [%s]", strings.Join(resources, ", ")))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nInvoke with: Skill(skill=\"name\", args=\"optional args\")")
	return sb.String()
}

// GetSkillInvocationPrompt returns the full skill content wrapped in XML for injection.
// The name should be the full name (namespace:name or just name).
func (r *Registry) GetSkillInvocationPrompt(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	skill, ok := r.skills[name]
	if !ok {
		return ""
	}

	instructions := skill.GetInstructions()
	if instructions == "" {
		return ""
	}

	var sb strings.Builder
	// Use FullName in the XML tag
	fmt.Fprintf(&sb, "<skill-invocation name=\"%s\">\n", skill.FullName())

	// Include script and reference paths so LLM knows correct locations
	if skill.SkillDir != "" {
		if len(skill.Scripts) > 0 {
			sb.WriteString("Available scripts (use Bash to execute):\n")
			for _, script := range skill.Scripts {
				fmt.Fprintf(&sb, "  - %s/scripts/%s\n", skill.SkillDir, script)
			}
			sb.WriteString("\n")
		}
		if len(skill.References) > 0 {
			sb.WriteString("Reference files (use Read when needed):\n")
			for _, ref := range skill.References {
				fmt.Fprintf(&sb, "  - %s/references/%s\n", skill.SkillDir, ref)
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(instructions)
	sb.WriteString("\n</skill-invocation>")

	return sb.String()
}

// Count returns the total number of loaded skills.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.skills)
}

// IsEnabled returns true if the named skill exists and is enabled or active.
func (r *Registry) IsEnabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[name]
	if !ok {
		return false
	}
	return skill.IsEnabled()
}

// SetEnabled sets the enabled state for a skill and persists it.
// When enabled is true the skill moves to StateEnable; when false it moves to StateDisable.
func (r *Registry) SetEnabled(name string, enabled bool, userLevel bool) error {
	state := StateEnable
	if !enabled {
		state = StateDisable
	}
	return r.SetState(name, state, userLevel)
}

// GetDisabledAt returns a map of skill names that are disabled at the given level.
func (r *Registry) GetDisabledAt(userLevel bool) map[string]bool {
	states := r.GetStatesAt(userLevel)
	result := make(map[string]bool)
	for name, state := range states {
		if state == StateDisable {
			result[name] = true
		}
	}
	return result
}

// PromptSection returns the rendered skills section for the system prompt.
// This is an alias for GetSkillsSection to satisfy the Service interface.
func (r *Registry) PromptSection() string {
	return r.GetSkillsSection()
}

// NewRegistryForTest creates a Registry with pre-populated skills and stores.
// Intended for testing only.
func NewRegistryForTest(skills map[string]*Skill, userStore, projectStore *Store) *Registry {
	return &Registry{
		skills:       skills,
		userStore:    userStore,
		projectStore: projectStore,
	}
}
