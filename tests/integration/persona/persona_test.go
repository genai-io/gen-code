// Package persona_test covers persona registry loading, parsing, scope
// priority, and the --persona CLI flag.
package persona_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/persona"
)

// ============================================================
// Helpers
// ============================================================

// writePersonaFile writes a file inside a persona directory.
func writePersonaFile(t *testing.T, root, personaName, relPath, content string) {
	t.Helper()
	full := filepath.Join(root, personaName, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// writePersonaSkill creates a persona-bundled skill directory with a SKILL.md.
func writePersonaSkill(t *testing.T, root, personaName, skillName, content string) {
	t.Helper()
	writePersonaFile(t, root, personaName, filepath.Join("skills", skillName, "SKILL.md"), content)
}

// newUserPersona creates a persona under the user-level personas directory.
func newUserPersona(t *testing.T, home, name string) string {
	t.Helper()
	root := filepath.Join(confdir.Dir(home), "personas")
	return filepath.Join(root, name)
}

// newProjectPersona creates a persona under the project-level personas directory.
func newProjectPersona(t *testing.T, cwd, name string) string {
	t.Helper()
	root := filepath.Join(confdir.Dir(cwd), "personas")
	return filepath.Join(root, name)
}

// ============================================================
// Registry tests
// ============================================================

func TestRegistry_BuiltinDefault(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	r := persona.NewRegistry("")
	all := r.List()

	if len(all) != 1 {
		t.Fatalf("expected 1 persona (built-in), got %d", len(all))
	}
	if all[0].Name != persona.DefaultName {
		t.Errorf("expected default persona, got %q", all[0].Name)
	}
	if !all[0].IsBuiltin() {
		t.Error("default persona should be built-in")
	}

	p, ok := r.Get(persona.DefaultName)
	if !ok {
		t.Fatal("Get(default) should succeed")
	}
	if p.Name != persona.DefaultName {
		t.Errorf("expected 'default', got %q", p.Name)
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) should return false")
	}
}

func TestRegistry_LoadUserPersonas(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create a user-level persona with identity and skills.
	dir := newUserPersona(t, tmpHome, "backend-dev")
	writePersonaFile(t, dir, "", "system/identity.md", "You are a backend engineer.")
	writePersonaFile(t, dir, "", "system/behavior.md", "Write tests first.")
	writePersonaFile(t, dir, "", "settings.json", `{"description": "Backend development persona"}`)
	writePersonaSkill(t, dir, "", "deploy", "---\nname: deploy\n---\n\nDeploy the app.")

	r := persona.NewRegistry("")
	all := r.List()

	// Should have default + backend-dev
	if len(all) != 2 {
		t.Fatalf("expected 2 personas, got %d", len(all))
	}

	p, ok := r.Get("backend-dev")
	if !ok {
		t.Fatal("Get(backend-dev) should succeed")
	}
	if p.Scope != persona.ScopeUser {
		t.Errorf("expected ScopeUser, got %v", p.Scope)
	}
	if p.Description != "Backend development persona" {
		t.Errorf("unexpected description: %q", p.Description)
	}
	if p.Identity != "You are a backend engineer." {
		t.Errorf("unexpected identity: %q", p.Identity)
	}
	if p.Behavior != "Write tests first." {
		t.Errorf("unexpected behavior: %q", p.Behavior)
	}
	if len(p.SkillDirs) != 1 {
		t.Errorf("expected 1 skill dir, got %d", len(p.SkillDirs))
	}
}

func TestRegistry_LoadProjectPersonas(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpCwd := t.TempDir()

	dir := newProjectPersona(t, tmpCwd, "project-x")
	writePersonaFile(t, dir, "", "system/identity.md", "You work on project X.")
	writePersonaFile(t, dir, "", "settings.json", `{"description": "Project X persona"}`)

	r := persona.NewRegistry(tmpCwd)
	all := r.List()

	if len(all) != 2 {
		t.Fatalf("expected 2 personas (default + project-x), got %d", len(all))
	}

	p, ok := r.Get("project-x")
	if !ok {
		t.Fatal("Get(project-x) should succeed")
	}
	if p.Scope != persona.ScopeProject {
		t.Errorf("expected ScopeProject, got %v", p.Scope)
	}
	if p.Identity != "You work on project X." {
		t.Errorf("unexpected identity: %q", p.Identity)
	}
}

func TestRegistry_ProjectOverridesUser(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpCwd := t.TempDir()

	// Same name at both scopes.
	userDir := newUserPersona(t, tmpHome, "fullstack")
	writePersonaFile(t, userDir, "", "system/identity.md", "User-level fullstack.")
	writePersonaFile(t, userDir, "", "settings.json", `{"description": "user"}`)

	projDir := newProjectPersona(t, tmpCwd, "fullstack")
	writePersonaFile(t, projDir, "", "system/identity.md", "Project-level fullstack.")
	writePersonaFile(t, projDir, "", "settings.json", `{"description": "project"}`)

	r := persona.NewRegistry(tmpCwd)
	p, ok := r.Get("fullstack")
	if !ok {
		t.Fatal("Get(fullstack) should succeed")
	}

	// Project scope wins over user scope.
	if p.Scope != persona.ScopeProject {
		t.Errorf("expected ScopeProject (override), got %v", p.Scope)
	}
	if p.Identity != "Project-level fullstack." {
		t.Errorf("expected project-level identity, got %q", p.Identity)
	}
	if p.Description != "project" {
		t.Errorf("expected project description, got %q", p.Description)
	}
}

func TestRegistry_DisplayOrder(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpCwd := t.TempDir()

	// Project personas (should appear after default, before user).
	projDir1 := newProjectPersona(t, tmpCwd, "zulu-proj")
	writePersonaFile(t, projDir1, "", "settings.json", `{"description": "p"}`)
	projDir2 := newProjectPersona(t, tmpCwd, "alpha-proj")
	writePersonaFile(t, projDir2, "", "settings.json", `{"description": "p"}`)

	// User personas (should appear after project).
	userDir1 := newUserPersona(t, tmpHome, "zulu-user")
	writePersonaFile(t, userDir1, "", "settings.json", `{"description": "u"}`)
	userDir2 := newUserPersona(t, tmpHome, "alpha-user")
	writePersonaFile(t, userDir2, "", "settings.json", `{"description": "u"}`)

	r := persona.NewRegistry(tmpCwd)
	all := r.List()

	if len(all) != 5 {
		t.Fatalf("expected 5 personas, got %d", len(all))
	}

	// Expected order: default, alpha-proj (project A-Z), zulu-proj, alpha-user (user A-Z), zulu-user
	expected := []struct {
		name  string
		scope persona.Scope
	}{
		{persona.DefaultName, persona.ScopeBuiltin},
		{"alpha-proj", persona.ScopeProject},
		{"zulu-proj", persona.ScopeProject},
		{"alpha-user", persona.ScopeUser},
		{"zulu-user", persona.ScopeUser},
	}

	for i, exp := range expected {
		if all[i].Name != exp.name {
			t.Errorf("[%d] expected %q, got %q", i, exp.name, all[i].Name)
		}
		if all[i].Scope != exp.scope {
			t.Errorf("[%d] expected scope %v, got %v", i, exp.scope, all[i].Scope)
		}
	}
}

func TestRegistry_Reload(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := newUserPersona(t, tmpHome, "reload-test")
	writePersonaFile(t, dir, "", "settings.json", `{"description": "before reload"}`)

	r := persona.NewRegistry("")
	if len(r.List()) != 2 {
		t.Fatalf("expected 2 personas before reload, got %d", len(r.List()))
	}

	// Add another persona on disk.
	dir2 := newUserPersona(t, tmpHome, "new-persona")
	writePersonaFile(t, dir2, "", "settings.json", `{"description": "added after init"}`)

	r.Reload()
	all := r.List()
	if len(all) != 3 {
		t.Fatalf("expected 3 personas after reload, got %d", len(all))
	}
	p, ok := r.Get("new-persona")
	if !ok {
		t.Fatal("Get(new-persona) should succeed after reload")
	}
	if p.Description != "added after init" {
		t.Errorf("unexpected description: %q", p.Description)
	}
}

// ============================================================
// Parsing tests
// ============================================================

func TestPersona_AllParts(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := newUserPersona(t, tmpHome, "full")
	writePersonaFile(t, dir, "", "system/identity.md", "Identity content.")
	writePersonaFile(t, dir, "", "system/behavior.md", "Behavior content.")
	writePersonaFile(t, dir, "", "system/rules.md", "Rules content.")
	writePersonaFile(t, dir, "", "settings.json", `{"description": "Full persona"}`)
	writePersonaSkill(t, dir, "", "skill-a", "skill a content")
	writePersonaSkill(t, dir, "", "skill-b", "skill b content")

	r := persona.NewRegistry("")
	p, ok := r.Get("full")
	if !ok {
		t.Fatal("Get(full) should succeed")
	}

	if p.Identity != "Identity content." {
		t.Errorf("identity mismatch: %q", p.Identity)
	}
	if p.Behavior != "Behavior content." {
		t.Errorf("behavior mismatch: %q", p.Behavior)
	}
	if p.Rules != "Rules content." {
		t.Errorf("rules mismatch: %q", p.Rules)
	}
	if p.Description != "Full persona" {
		t.Errorf("description mismatch: %q", p.Description)
	}
	if len(p.SkillDirs) != 2 {
		t.Errorf("expected 2 skill dirs, got %d", len(p.SkillDirs))
	}
}

func TestPersona_SettingsOnly(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := newUserPersona(t, tmpHome, "settings-only")
	writePersonaFile(t, dir, "", "settings.json", `{"description": "Just settings"}`)

	r := persona.NewRegistry("")
	p, ok := r.Get("settings-only")
	if !ok {
		t.Fatal("Get(settings-only) should succeed — settings.json alone is enough")
	}
	if p.Description != "Just settings" {
		t.Errorf("description mismatch: %q", p.Description)
	}
}

func TestPersona_SkillsOnly(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := newUserPersona(t, tmpHome, "skills-only")
	writePersonaSkill(t, dir, "", "mytool", "mytool content")

	r := persona.NewRegistry("")
	p, ok := r.Get("skills-only")
	if !ok {
		t.Fatal("Get(skills-only) should succeed — skills alone is enough")
	}
	if len(p.SkillDirs) != 1 {
		t.Errorf("expected 1 skill dir, got %d", len(p.SkillDirs))
	}
}

func TestPersona_IdentityOnly(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := newUserPersona(t, tmpHome, "identity-only")
	writePersonaFile(t, dir, "", "system/identity.md", "Minimal persona.")

	r := persona.NewRegistry("")
	p, ok := r.Get("identity-only")
	if !ok {
		t.Fatal("Get(identity-only) should succeed — identity.md alone is enough")
	}
	if p.Identity != "Minimal persona." {
		t.Errorf("identity mismatch: %q", p.Identity)
	}
}

func TestPersona_EmptyDirIgnored(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create a directory that is NOT a persona (no system files, no skills, no settings.json).
	dir := newUserPersona(t, tmpHome, "not-a-persona")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	r := persona.NewRegistry("")
	_, ok := r.Get("not-a-persona")
	if ok {
		t.Error("empty directory should not be treated as a persona")
	}
}

func TestPersona_DefaultNameSkipped(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// The reserved "default" directory name should be skipped.
	dir := newUserPersona(t, tmpHome, "default")
	writePersonaFile(t, dir, "", "settings.json", `{"description": "should be skipped"}`)

	r := persona.NewRegistry("")
	all := r.List()
	for _, p := range all {
		if p.Name == "default" && p.Scope != persona.ScopeBuiltin {
			t.Error("user-level 'default' directory should be skipped")
		}
	}
}

func TestPersona_PartsAreTrimmed(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := newUserPersona(t, tmpHome, "trimmed")
	writePersonaFile(t, dir, "", "system/identity.md", "\n\n  Trimmed content.  \n\n")

	r := persona.NewRegistry("")
	p, ok := r.Get("trimmed")
	if !ok {
		t.Fatal("Get(trimmed) should succeed")
	}
	if p.Identity != "Trimmed content." {
		t.Errorf("identity should be trimmed, got %q", p.Identity)
	}
}

func TestPersona_SettingsWithSkillStates(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := newUserPersona(t, tmpHome, "with-skills")
	writePersonaFile(t, dir, "", "settings.json", `{
		"description": "Has skill states",
		"skills": {
			"skill-a": "active",
			"skill-b": "disable"
		}
	}`)

	r := persona.NewRegistry("")
	p, ok := r.Get("with-skills")
	if !ok {
		t.Fatal("Get(with-skills) should succeed")
	}
	if p.Settings == nil {
		t.Fatal("settings should not be nil")
	}
	if s, ok := p.Settings.Skills["skill-a"]; !ok || s != "active" {
		t.Errorf("skill-a should be 'active', got %q", s)
	}
	if s, ok := p.Settings.Skills["skill-b"]; !ok || s != "disable" {
		t.Errorf("skill-b should be 'disable', got %q", s)
	}
}

// ============================================================
// CLI --persona flag tests
// ============================================================

// buildBinary compiles the san binary into a temp file.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "san-test")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/san")
	cmd.Dir = projectRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return bin
}

// projectRoot returns the repository root by walking up from the working directory.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

// filteredEnv returns os.Environ() with the specified keys removed.
func filteredEnv(removeKeys ...string) []string {
	remove := make(map[string]bool, len(removeKeys))
	for _, k := range removeKeys {
		remove[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		key := kv
		if before, _, ok := strings.Cut(kv, "="); ok {
			key = before
		}
		if !remove[key] {
			env = append(env, kv)
		}
	}
	return env
}

func TestCLI_HelpShowsPersonaFlag(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "help")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("san help exited with error: %v", err)
	}

	output := string(out)
	if !strings.Contains(output, "--persona") {
		t.Errorf("help output missing --persona flag\nfull output:\n%s", output)
	}
}

func TestCLI_NonexistentPersona(t *testing.T) {
	bin := buildBinary(t)

	isolatedHome := t.TempDir()
	isolatedCwd := t.TempDir()

	env := filteredEnv(
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"MOONSHOT_API_KEY", "ALIBABA_API_KEY", "BIGMODEL_API_KEY",
		"HOME",
	)
	env = append(env, "HOME="+isolatedHome)

	// Pass a nonexistent persona — should get a clear error.
	cmd := exec.Command(bin, "--persona", "nonexistent")
	cmd.Env = env
	cmd.Dir = isolatedCwd
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected non-zero exit for nonexistent persona")
	}
	output := string(out)
	if !strings.Contains(strings.ToLower(output), "not found") {
		t.Errorf("error should say persona not found, got: %q", output)
	}
	if strings.Contains(strings.ToLower(output), "unknown flag") {
		t.Errorf("--persona flag should be recognized, not an unknown flag: %q", output)
	}
}

func TestCLI_NonexistentPersonaWithPrint(t *testing.T) {
	bin := buildBinary(t)

	isolatedHome := t.TempDir()
	isolatedCwd := t.TempDir()

	env := filteredEnv(
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"MOONSHOT_API_KEY", "ALIBABA_API_KEY", "BIGMODEL_API_KEY",
		"HOME",
	)
	env = append(env, "HOME="+isolatedHome)

	// --persona nonexistent with -p: validation must happen before print mode,
	// so this should fail with a persona-not-found error, not a provider error.
	cmd := exec.Command(bin, "--persona", "nonexistent", "-p", "hello")
	cmd.Env = env
	cmd.Dir = isolatedCwd
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected non-zero exit for nonexistent persona with -p")
	}
	output := string(out)
	if !strings.Contains(strings.ToLower(output), "not found") {
		t.Errorf("error should say persona not found, got: %q", output)
	}
	if strings.Contains(strings.ToLower(output), "no provider") {
		t.Errorf("--persona validation should happen before print mode; got provider error: %q", output)
	}
}

func TestCLI_PersonaFlagAccepted(t *testing.T) {
	bin := buildBinary(t)

	isolatedHome := t.TempDir()
	isolatedCwd := t.TempDir()

	// Create a persona in HOME so it exists at startup.
	personaDir := filepath.Join(confdir.Dir(isolatedHome), "personas", "test-p")
	if err := os.MkdirAll(filepath.Join(personaDir, "system"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(personaDir, "settings.json"), []byte(`{"description": "test"}`), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	env := filteredEnv(
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"MOONSHOT_API_KEY", "ALIBABA_API_KEY", "BIGMODEL_API_KEY",
		"HOME",
	)
	env = append(env, "HOME="+isolatedHome)

	// Valid persona in non-interactive mode — should not error about the persona.
	cmd := exec.Command(bin, "--persona", "test-p", "-p", "hello")
	cmd.Env = env
	cmd.Dir = isolatedCwd
	out, err := cmd.CombinedOutput()

	// Should fail with a provider error, not a persona error.
	if err == nil {
		t.Fatalf("expected non-zero exit due to missing provider")
	}
	output := string(out)
	if strings.Contains(strings.ToLower(output), "not found") {
		t.Errorf("persona should be found, but got: %q", output)
	}
	if strings.Contains(strings.ToLower(output), "unknown flag") {
		t.Errorf("--persona flag should be recognized, got: %q", output)
	}
}

// TestCLI_PersonaWithPrintFullContent verifies that when --persona and -p are
// combined, the persona's identity/behavior/rules are loaded via system.Build,
// and its model and disabledTools settings are applied — not ignored.
func TestCLI_PersonaWithPrintFullContent(t *testing.T) {
	bin := buildBinary(t)

	isolatedHome := t.TempDir()
	isolatedCwd := t.TempDir()

	personaDir := filepath.Join(confdir.Dir(isolatedHome), "personas", "full-p")
	writePersonaFile(t, personaDir, "", "system/identity.md", "You are a Go backend engineer.")
	writePersonaFile(t, personaDir, "", "system/behavior.md", "Write tests first.")
	writePersonaFile(t, personaDir, "", "system/rules.md", "Follow standard Go conventions.")
	writePersonaFile(t, personaDir, "", "settings.json", `{
		"description": "Full persona for print mode",
		"model": "claude-sonnet-4-6",
		"disabledTools": {"bash": true, "write": true}
	}`)

	env := filteredEnv(
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"MOONSHOT_API_KEY", "ALIBABA_API_KEY", "BIGMODEL_API_KEY",
		"HOME",
	)
	env = append(env, "HOME="+isolatedHome)

	cmd := exec.Command(bin, "--persona", "full-p", "-p", "hello")
	cmd.Env = env
	cmd.Dir = isolatedCwd
	out, err := cmd.CombinedOutput()

	// Without a configured provider the binary should exit non-zero, but the
	// error must NOT be about the persona (it exists and has valid content).
	if err == nil {
		t.Fatalf("expected non-zero exit due to missing provider")
	}
	output := string(out)
	if strings.Contains(strings.ToLower(output), "not found") {
		t.Errorf("persona should be found, got: %q", output)
	}
	if strings.Contains(strings.ToLower(output), "panic") {
		t.Errorf("should not panic, got: %q", output)
	}
	if strings.Contains(strings.ToLower(output), "unknown flag") {
		t.Errorf("--persona flag should be recognized, got: %q", output)
	}
}
