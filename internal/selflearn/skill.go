package selflearn

import (
	"github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/markdown"
	"github.com/genai-io/san/internal/tool"
	"gopkg.in/yaml.v3"
)

// agentOrigin is the provenance value L1 writes; only skills carrying it are
// mutable by the reviewer (it reads user-created skills but never modifies
// them). See notes/active/l1-background-review.md §5.2.
const agentOrigin = "agent-created"

// skillNameRe enforces class-level kebab names and doubles as a traversal guard
// (no separators, no dots). Session-specific names (PR numbers, error strings)
// should be steered away by the prompt; this just keeps the on-disk name safe.
var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// supportSubdirs are the only places a skill_manage support file may be written.
var supportSubdirs = map[string]struct{}{"references": {}, "templates": {}, "scripts": {}}

// SkillPermissions controls what L1 may do via skill_manage. All actions
// scope to agent-created skills only — user-authored skills are always
// read-only to the reviewer. It appears at three altitudes with one meaning:
// the configured gates (Config.Skills), the per-pass scope the trigger derives
// from them (a skill-use turn keeps update/delete, a skill-free turn keeps
// create), and the SkillManager's hard floor at dispatch.
type SkillPermissions struct {
	AllowCreate bool
	AllowUpdate bool
	AllowDelete bool
}

// Any reports whether at least one action is allowed.
func (p SkillPermissions) Any() bool { return p.AllowCreate || p.AllowUpdate || p.AllowDelete }

// AllowAllSkillActions is the permissive set: every action allowed within
// the agent-created scope; user-created stays read-only regardless.
func AllowAllSkillActions() SkillPermissions {
	return SkillPermissions{AllowCreate: true, AllowUpdate: true, AllowDelete: true}
}

// SkillWriteObserver fires after every successful write (create/patch/
// edit/write_file/remove_file/delete; action is the §5.3 name).
// SetWriteObserver must be called before the first write; the fork is
// single-flight (§6 #8) so the field is lock-free.
type SkillWriteObserver func(action, name, note string)

// SkillManager is the L1-only skill write surface. Skills live directly in
// san's existing user/project scopes — ~/.san/skills/<name>/ and
// ./.san/skills/<name>/ — distinguished by the origin frontmatter field, not a
// subdirectory.
type SkillManager struct {
	userDir    string
	projectDir string
	perms      SkillPermissions
	onWrite    SkillWriteObserver

	mu sync.Mutex
}

// NewSkillManager returns the manager for cwd with the given action
// permissions. The skill dirs are created lazily on first create. When
// the user home directory is unavailable (sandboxed exec, unset HOME),
// userDir is left empty and any user-scope write fails loudly via
// dirFor — better than silently aliasing user-scope onto cwd/.san/skills.
func NewSkillManager(cwd string, perms SkillPermissions) *SkillManager {
	userDir := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		userDir = filepath.Join(confdir.Dir(home), "skills")
	}
	return &SkillManager{
		userDir:    userDir,
		projectDir: filepath.Join(confdir.Dir(cwd), "skills"),
		perms:      perms,
	}
}

// Perms returns the current action permissions (read-only snapshot).
func (m *SkillManager) Perms() SkillPermissions { return m.perms }

// SetWriteObserver registers the callback fired after each successful
// write. Must be called before the first write (see type doc).
func (m *SkillManager) SetWriteObserver(fn SkillWriteObserver) { m.onWrite = fn }

// fireWrite invokes the write observer if one is registered.
func (m *SkillManager) fireWrite(action, name, note string) {
	if m.onWrite != nil {
		m.onWrite(action, name, note)
	}
}

// SkillInfo is a one-line summary of an existing skill, used to brief the
// reviewer so it prefers updating over creating and never re-derives a skill
// that already exists.
type SkillInfo struct {
	Name        string
	Level       string // user | project
	Origin      string // agent-created | user-created
	Description string
}

// Editable reports whether the reviewer may modify this skill (agent-created).
// User-created skills are read-only to the reviewer at every setting.
func (i SkillInfo) Editable() bool { return i.Origin == agentOrigin }

// Inventory lists existing skills across both scopes (project entries shadow
// user entries of the same name, matching loader precedence).
//
// Why the disk scan instead of consulting skill.Registry: the registry is
// initialized once at startup and only refreshed on
// ReloadAfterPluginChange. The L1 reviewer mutates ~/.san/skills and
// ./.san/skills mid-session via skill_manage; a registry-backed Inventory
// would not see skills the reviewer just created in earlier passes, which
// would cause it to re-create duplicates. Reading the on-disk state
// directly is the correct freshness for this caller.
func (m *SkillManager) Inventory() []SkillInfo {
	seen := make(map[string]bool)
	var out []SkillInfo
	for _, scope := range []struct {
		dir   string
		level string
	}{{m.projectDir, "project"}, {m.userDir, "user"}} {
		if scope.dir == "" {
			continue
		}
		entries, err := os.ReadDir(scope.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			path := filepath.Join(scope.dir, e.Name(), "SKILL.md")
			fm, _, err := markdown.ParseFrontmatterFile(path)
			if err != nil {
				continue
			}
			var meta struct {
				Description string `yaml:"description"`
				Origin      string `yaml:"origin"`
			}
			_ = yaml.Unmarshal([]byte(fm), &meta)
			origin := meta.Origin
			if origin == "" {
				origin = "user-created"
			}
			seen[e.Name()] = true
			out = append(out, SkillInfo{
				Name:        e.Name(),
				Level:       scope.level,
				Origin:      origin,
				Description: meta.Description,
			})
		}
	}
	return out
}

func (m *SkillManager) dirFor(level string) (string, error) {
	switch strings.TrimSpace(level) {
	case "", "user":
		if m.userDir == "" {
			return "", fmt.Errorf("user home directory unavailable; user-scope skills cannot be written")
		}
		return m.userDir, nil
	case "project":
		return m.projectDir, nil
	default:
		return "", fmt.Errorf("invalid level %q; use user or project", level)
	}
}

// resolve finds an existing skill's SKILL.md by name, project scope first
// (higher priority), then user. Returns the path or an error if absent.
func (m *SkillManager) resolve(name string) (string, error) {
	// Validate before touching the filesystem: every action except create
	// reaches the disk through here, and only create validated the name
	// itself. Without this guard a name with a separator or ".." would be
	// joined straight into a path (patch/edit/delete/write_file/remove_file
	// all flow through resolve), escaping the skills directory. skillNameRe
	// forbids both, doubling as the traversal guard its doc claims to be.
	if !skillNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	for _, dir := range []string{m.projectDir, m.userDir} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name, "SKILL.md")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no skill named %q", name)
}

// Read returns the full SKILL.md content for name, project scope first, for
// read-only display (the /evolve panel's skill preview). resolve enforces the
// same name/traversal guard used by every other on-disk action.
func (m *SkillManager) Read(name string) (string, error) {
	path, err := m.resolve(name)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parsed is the result of locating + parsing a skill's SKILL.md once. It
// is the return type of the two guards below so callers (Edit, Patch)
// don't have to re-parse the same file.
type parsed struct {
	path   string
	origin string
	fm     string
	body   string
}

// parseSkill resolves name and parses its SKILL.md frontmatter exactly
// once. Returns the path, the origin field (empty ⇒ user-created), the
// raw frontmatter block, and the body.
func (m *SkillManager) parseSkill(name string) (parsed, error) {
	path, err := m.resolve(name)
	if err != nil {
		return parsed{}, err
	}
	fm, body, err := markdown.ParseFrontmatterFile(path)
	if err != nil {
		return parsed{}, err
	}
	var meta struct {
		Origin string `yaml:"origin"`
	}
	if fm != "" {
		if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
			return parsed{}, err
		}
	}
	return parsed{path: path, origin: meta.Origin, fm: fm, body: body}, nil
}

// requireAgentOwned parses name and confirms it is agent-created. Used for
// actions where user-created remains off-limits at every config setting
// (delete, edit, write_file, remove_file).
func (m *SkillManager) requireAgentOwned(name string) (parsed, error) {
	p, err := m.parseSkill(name)
	if err != nil {
		return parsed{}, err
	}
	if p.origin != agentOrigin {
		return parsed{}, fmt.Errorf("skill %q is user-created and must not be modified by the reviewer", name)
	}
	return p, nil
}

func (m *SkillManager) Create(name, description, body, level, note string) (string, error) {
	if !m.perms.AllowCreate {
		return "", errActionDenied("create", "allowCreate=false")
	}
	if !skillNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q; use a class-level kebab-case name (e.g. go-table-tests)", name)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("skill content cannot be empty")
	}
	// Description is the line Inventory exposes to future reviewers for
	// UPDATE/CREATE disambiguation (§5.1). An empty description silently
	// degrades the "prefer update over create" policy to substring-on-name.
	description = strings.TrimSpace(description)
	if description == "" {
		return "", fmt.Errorf("skill description cannot be empty")
	}
	// Skill bodies and descriptions are loaded into a future system prompt, so
	// they carry the same stored-injection risk as memory entries.
	if err := scanContent(body); err != nil {
		return "", err
	}
	if err := scanForThreats(description); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.resolve(name); err == nil {
		return "", fmt.Errorf("skill %q already exists; use patch or edit", name)
	}
	dir, err := m.dirFor(level)
	if err != nil {
		return "", err
	}
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", err
	}
	content := buildSkillMD(name, description, agentOrigin, body)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		return "", err
	}
	m.fireWrite("create", name, note)
	return fmt.Sprintf("Created skill %q.", name), nil
}

func (m *SkillManager) Edit(name, body, note string) (string, error) {
	if !m.perms.AllowUpdate {
		return "", errActionDenied("edit", "allowUpdate=false")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("skill content cannot be empty")
	}
	if err := scanContent(body); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Edit is a full-body rewrite — agent-created only, like every other
	// skill_manage action.
	p, err := m.requireAgentOwned(name)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p.path, []byte(joinFrontmatter(p.fm, body)), 0o644); err != nil {
		return "", err
	}
	m.fireWrite("edit", name, note)
	return fmt.Sprintf("Rewrote skill %q.", name), nil
}

func (m *SkillManager) Patch(name, oldText, newText string, replaceAll bool, note string) (string, error) {
	if !m.perms.AllowUpdate {
		return "", errActionDenied("patch", "allowUpdate=false")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Every skill_manage action is agent-created only — the reviewer never
	// modifies user-authored skills.
	p, err := m.requireAgentOwned(name)
	if err != nil {
		return "", err
	}
	patched, err := applyPatch(p.body, oldText, newText, replaceAll)
	if err != nil {
		return "", err
	}
	// Only block patches that INTRODUCE a new threat pattern; preserving a
	// pattern already in the original (a legitimate injection-defense
	// example, a quoted attacker payload, etc.) must not brick the skill.
	// scanForThreats(patched) would have refused every later edit of such
	// a skill, including ones removing the very text that tripped the scan.
	if err := scanNewThreats(p.body, patched); err != nil {
		return "", err
	}
	if err := os.WriteFile(p.path, []byte(joinFrontmatter(p.fm, patched)), 0o644); err != nil {
		return "", err
	}
	m.fireWrite("patch", name, note)
	return fmt.Sprintf("Patched skill %q.", name), nil
}

func (m *SkillManager) WriteFile(name, file, content, note string) (string, error) {
	if !m.perms.AllowUpdate {
		return "", errActionDenied("write_file", "allowUpdate=false")
	}
	// Support files (references/templates/scripts) are read or executed by the
	// agent, so they get the same threat scan as skill bodies.
	if err := scanForThreats(content); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Support-file writes don't extend to user-created skills — adding
	// references/scripts to someone's authored skill is a structural change,
	// not a targeted patch, so it stays out of allowUpdateUserCreated.
	p, err := m.requireAgentOwned(name)
	if err != nil {
		return "", err
	}
	rel, err := safeSupportFile(file)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(filepath.Dir(p.path), rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return "", err
	}
	m.fireWrite("write_file", name, note)
	return fmt.Sprintf("Wrote %s to skill %q.", rel, name), nil
}

func (m *SkillManager) RemoveFile(name, file, note string) (string, error) {
	if !m.perms.AllowUpdate {
		return "", errActionDenied("remove_file", "allowUpdate=false")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Same scope as WriteFile — agent-created only.
	p, err := m.requireAgentOwned(name)
	if err != nil {
		return "", err
	}
	rel, err := safeSupportFile(file)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(filepath.Dir(p.path), rel)
	if err := os.Remove(dest); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no such support file %q", rel)
		}
		return "", err
	}
	m.fireWrite("remove_file", name, note)
	return fmt.Sprintf("Removed %s from skill %q.", rel, name), nil
}

func (m *SkillManager) Delete(name, note string) (string, error) {
	if !m.perms.AllowDelete {
		return "", errActionDenied("delete", "allowDelete=false")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Delete is always restricted to agent-created — no config setting
	// (including allowUpdateUserCreated) opens user-created deletion (§5.5).
	p, err := m.requireAgentOwned(name)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(filepath.Dir(p.path)); err != nil {
		return "", err
	}
	m.fireWrite("delete", name, note)
	return fmt.Sprintf("Deleted skill %q.", name), nil
}

// errActionDenied builds a uniform "permission denied" error for actions the
// configured SkillPermissions reject. Used as the early-return in the four
// action entry points so the model sees a consistent shape on the
// permission-veto path (§5.5).
func errActionDenied(action, reason string) error {
	return fmt.Errorf("skill_manage(%s) denied: %s (see selfLearn.skills permissions in §5.5)", action, reason)
}

// safeSupportFile validates a support-file path: <subdir>/<file>, where subdir
// is references/templates/scripts and file is a bare name.
func safeSupportFile(file string) (string, error) {
	file = strings.TrimSpace(strings.TrimPrefix(file, "./"))
	if file == "" || strings.Contains(file, "..") {
		return "", fmt.Errorf("invalid support file %q", file)
	}
	parts := strings.Split(filepath.ToSlash(file), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("support file must be <references|templates|scripts>/<name>, got %q", file)
	}
	if _, ok := supportSubdirs[parts[0]]; !ok {
		return "", fmt.Errorf("support subdir must be references, templates, or scripts; got %q", parts[0])
	}
	if parts[1] != filepath.Base(parts[1]) || parts[1] == "" {
		return "", fmt.Errorf("invalid support file name %q", parts[1])
	}
	return filepath.Join(parts[0], parts[1]), nil
}

func buildSkillMD(name, description, origin, body string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + name + "\n")
	if description != "" {
		b.WriteString("description: " + yamlScalar(description) + "\n")
	}
	b.WriteString("origin: " + origin + "\n")
	b.WriteString("---\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

// joinFrontmatter reattaches existing frontmatter (as returned by
// ParseFrontmatterFile, newline-terminated per line) to a new body. When
// the input had no frontmatter at all (fm empty), the body is returned
// alone — fabricating an empty `---\n\n---\n\n` block here would silently
// mutate a user-authored SKILL.md into a different on-disk shape.
func joinFrontmatter(fm, body string) string {
	fm = strings.TrimRight(fm, "\n")
	if fm == "" {
		return strings.TrimSpace(body) + "\n"
	}
	return "---\n" + fm + "\n---\n\n" + strings.TrimSpace(body) + "\n"
}

// yamlScalar renders a description as a YAML scalar that always round-trips.
// A hand-rolled "quote only these chars" rule under-quotes values that open a
// YAML indicator (e.g. "fix [bug"), producing frontmatter that parses as a
// flow sequence — or fails to parse at all, which would make every later
// parseSkill on that file error and leave the skill permanently un-editable.
// Delegating to the YAML encoder guarantees a valid scalar for any input.
func yamlScalar(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	scalar := strings.TrimRight(string(out), "\n")
	// A multi-line value marshals to a block scalar, which cannot be inlined
	// after "description: ". Fall back to a double-quoted single line (valid
	// YAML, \n-escaped) for that case.
	if strings.Contains(scalar, "\n") {
		return strconv.Quote(s)
	}
	return scalar
}

// skillManageTool is the L1-only skill write surface.
type skillManageTool struct {
	mgr *SkillManager
}

func newSkillManageTool(mgr *SkillManager) *skillManageTool {
	return &skillManageTool{mgr: mgr}
}

func (t *skillManageTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "skill_manage",
		Description: "Create or maintain an agent-created skill (a reusable, class-level technique). " +
			"Prefer updating an existing skill over creating a new one. Actions: " +
			"create (new class-level skill), patch (targeted find-and-replace), edit (full body rewrite — rare), " +
			"write_file/remove_file (references|templates|scripts support files), delete. " +
			"Only skills with origin: agent-created may be modified.",
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"create", "patch", "edit", "write_file", "remove_file", "delete"},
				},
				"name":        map[string]any{"type": "string", "description": "Class-level kebab-case skill name."},
				"description": map[string]any{"type": "string", "description": "One-line skill description (create)."},
				"content":     map[string]any{"type": "string", "description": "Body for create/edit, or support-file content for write_file."},
				"level":       map[string]any{"type": "string", "enum": []string{"user", "project"}, "description": "Scope for create (default user)."},
				"old_text":    map[string]any{"type": "string", "description": "Text to find (patch)."},
				"new_text":    map[string]any{"type": "string", "description": "Replacement text (patch)."},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every match (patch)."},
				"file":        map[string]any{"type": "string", "description": "Support file as <references|templates|scripts>/<name>."},
				"note":        map[string]any{"type": "string", "description": "Required. One short clause (≤80 chars) describing what this single change accomplished — surfaced in the post-review recap. Examples: \"trimmed examples section by 1.8KB\", \"removed vague tooling guidance\"."},
			},
			"required": []string{"action", "name", "note"},
		},
	}
}

func (t *skillManageTool) Run(_ context.Context, call ai.ToolCall) (agent.Result, error) {
	in, _ := core.ParseToolInput(call.Input)
	action := strings.TrimSpace(tool.GetString(in, "action"))
	name := strings.TrimSpace(tool.GetString(in, "name"))
	if name == "" {
		return agent.Result{}, fmt.Errorf("name is required")
	}
	note := tool.GetString(in, "note")

	var (
		msg string
		err error
	)
	switch action {
	case "create":
		msg, err = t.mgr.Create(name, tool.GetString(in, "description"), tool.GetString(in, "content"), tool.GetString(in, "level"), note)
	case "patch":
		msg, err = t.mgr.Patch(name, tool.GetString(in, "old_text"), tool.GetString(in, "new_text"), tool.GetBool(in, "replace_all"), note)
	case "edit":
		msg, err = t.mgr.Edit(name, tool.GetString(in, "content"), note)
	case "write_file":
		msg, err = t.mgr.WriteFile(name, tool.GetString(in, "file"), tool.GetString(in, "content"), note)
	case "remove_file":
		msg, err = t.mgr.RemoveFile(name, tool.GetString(in, "file"), note)
	case "delete":
		msg, err = t.mgr.Delete(name, note)
	default:
		return agent.Result{}, fmt.Errorf("unknown action %q", action)
	}
	if err != nil {
		return agent.Result{}, err
	}
	out, _ := json.Marshal(map[string]string{"status": "ok", "message": msg})
	return agent.TextResult(string(out)), nil
}
