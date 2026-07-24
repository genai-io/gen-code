package command

import (
	"os"
	"path/filepath"
	"testing"
)

// saveAndRestore saves current singleton state and restores it on cleanup.
func saveAndRestore(t *testing.T) {
	t.Helper()
	prev := defaultRegistry
	t.Cleanup(func() { SetDefaultRegistry(prev) })
}

// initTestService creates a fresh service and sets it as the singleton.
func initTestService(t *testing.T, cwd string, opts ...func(*Registry)) *Registry {
	t.Helper()
	s := &Registry{cwd: cwd}
	for _, opt := range opts {
		opt(s)
	}
	SetDefaultRegistry(s)
	return s
}

func withDynamicProviders(providers ...func() []Info) func(*Registry) {
	return func(s *Registry) {
		s.dynamicInfoProviders = append([]func() []Info(nil), providers...)
	}
}

func withPluginCommandPaths(fn func() []PluginCommandPath) func(*Registry) {
	return func(s *Registry) {
		s.pluginCommandPaths = fn
	}
}

func TestBuiltinCommandsUseModelsAndExcludeRemovedCommands(t *testing.T) {
	commands := (&Registry{}).BuiltinNames()
	if _, ok := commands["models"]; !ok {
		t.Fatal("builtin commands should include /models")
	}
	for _, removed := range []string{"model", "glob"} {
		if _, ok := commands[removed]; ok {
			t.Fatalf("builtin commands should not include removed /%s", removed)
		}
	}
}

func TestGetMatchingCommands_IncludesDynamicProviders(t *testing.T) {
	saveAndRestore(t)

	initTestService(t, "", withDynamicProviders(func() []Info {
		return []Info{
			{Name: "search", Description: "Search files <pattern>"},
			{Name: "review", Description: "Review code"},
		}
	}))

	matches := Default().GetMatching("sea")
	if len(matches) != 1 {
		t.Fatalf("expected 1 matching dynamic command, got %d", len(matches))
	}
	if matches[0].Name != "search" {
		t.Fatalf("unexpected dynamic command %q", matches[0].Name)
	}
}

func TestLoadCustomCommandFile_WithFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	cmdFile := filepath.Join(tmpDir, "deploy.md")
	content := "---\nname: deploy\ndescription: Deploy to production\n---\nRun the deploy pipeline.\n"
	if err := os.WriteFile(cmdFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pc := loadCustomCommandFile(cmdFile, "myplug")
	if pc == nil {
		t.Fatal("expected plugin command to be loaded")
	}
	if pc.Name != "deploy" {
		t.Errorf("name = %q, want %q", pc.Name, "deploy")
	}
	if pc.Description != "Deploy to production" {
		t.Errorf("description = %q, want %q", pc.Description, "Deploy to production")
	}
	if pc.Namespace != "myplug" {
		t.Errorf("namespace = %q, want %q", pc.Namespace, "myplug")
	}
	if pc.FullName() != "myplug:deploy" {
		t.Errorf("fullName = %q, want %q", pc.FullName(), "myplug:deploy")
	}
	inst := pc.GetInstructions()
	if inst != "Run the deploy pipeline." {
		t.Errorf("instructions = %q, want %q", inst, "Run the deploy pipeline.")
	}
}

func TestLoadCustomCommandFile_WithoutFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	cmdFile := filepath.Join(tmpDir, "check.md")
	if err := os.WriteFile(cmdFile, []byte("Run health checks.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pc := loadCustomCommandFile(cmdFile, "ops")
	if pc == nil {
		t.Fatal("expected plugin command to be loaded")
	}
	if pc.Name != "check" {
		t.Errorf("name = %q, want %q", pc.Name, "check")
	}
	if pc.FullName() != "ops:check" {
		t.Errorf("fullName = %q, want %q", pc.FullName(), "ops:check")
	}
}

func TestLoadCustomCommandFile_NamespaceInFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	cmdFile := filepath.Join(tmpDir, "test.md")
	content := "---\nname: test\nnamespace: ci\ndescription: Run tests\n---\nExecute test suite.\n"
	if err := os.WriteFile(cmdFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pc := loadCustomCommandFile(cmdFile, "fallback")
	if pc.Namespace != "ci" {
		t.Errorf("namespace = %q, want %q (frontmatter should override default)", pc.Namespace, "ci")
	}
	if pc.FullName() != "ci:test" {
		t.Errorf("fullName = %q, want %q", pc.FullName(), "ci:test")
	}
}

func setupPluginCommandPaths(t *testing.T) (string, func() []PluginCommandPath) {
	t.Helper()
	tmpDir := t.TempDir()

	cmdsDir := filepath.Join(tmpDir, "myplugin", "commands")
	if err := os.MkdirAll(cmdsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd1 := "---\nname: greet\ndescription: Say hello\n---\nGreet the user warmly.\n"
	if err := os.WriteFile(filepath.Join(cmdsDir, "greet.md"), []byte(cmd1), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd2 := "---\nname: build\ndescription: Build project\n---\nBuild all targets.\n"
	if err := os.WriteFile(filepath.Join(cmdsDir, "build.md"), []byte(cmd2), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := func() []PluginCommandPath {
		return []PluginCommandPath{
			{Path: filepath.Join(cmdsDir, "greet.md"), Namespace: "myplugin", IsProject: true},
			{Path: filepath.Join(cmdsDir, "build.md"), Namespace: "myplugin", IsProject: true},
		}
	}
	return tmpDir, provider
}

func TestIsCustomCommand_MatchesCustomCommands(t *testing.T) {
	saveAndRestore(t)

	_, provider := setupPluginCommandPaths(t)
	initTestService(t, "", withPluginCommandPaths(provider))

	svc := Default()

	pc, ok := svc.IsCustomCommand("myplugin:greet")
	if !ok {
		cmds := svc.GetCustomCommands()
		t.Logf("available plugin commands: %+v", cmds)
		t.Fatal("expected myplugin:greet to be found as plugin command")
	}
	if pc.Description != "Say hello" {
		t.Errorf("description = %q, want %q", pc.Description, "Say hello")
	}

	_, ok = svc.IsCustomCommand("greet")
	if !ok {
		t.Fatal("expected short name 'greet' to match plugin command")
	}

	_, ok = svc.IsCustomCommand("nonexistent")
	if ok {
		t.Fatal("nonexistent command should not match")
	}
}

func TestGetMatchingCommands_IncludesCustomCommands(t *testing.T) {
	saveAndRestore(t)

	_, provider := setupPluginCommandPaths(t)
	initTestService(t, "", withPluginCommandPaths(provider))

	matches := Default().GetMatching("gre")
	found := false
	for _, m := range matches {
		if m.Name == "myplugin:greet" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected myplugin:greet in matching commands for 'gre', got %+v", matches)
	}
}

func TestLoadCommandsFromDir(t *testing.T) {
	tmpDir := t.TempDir()
	cmdsDir := filepath.Join(tmpDir, "commands")
	if err := os.MkdirAll(cmdsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd1 := "---\nname: lint\ndescription: Run linter\n---\nLint all files.\n"
	if err := os.WriteFile(filepath.Join(cmdsDir, "lint.md"), []byte(cmd1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdsDir, "readme.txt"), []byte("not a command"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds := loadCommandsFromDir(cmdsDir, "", scopeUser)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "lint" {
		t.Errorf("name = %q, want %q", cmds[0].Name, "lint")
	}
	if cmds[0].Scope != scopeUser {
		t.Errorf("scope = %d, want %d (scopeUser)", cmds[0].Scope, scopeUser)
	}
	if cmds[0].Namespace != "" {
		t.Errorf("namespace = %q, want empty (user-level has no namespace)", cmds[0].Namespace)
	}
}

func TestLoadCommandsFromDir_NonexistentDir(t *testing.T) {
	cmds := loadCommandsFromDir("/nonexistent/path", "", scopeProject)
	if len(cmds) != 0 {
		t.Fatalf("expected 0 commands from nonexistent dir, got %d", len(cmds))
	}
}

func TestProjectCommandOverridesUser(t *testing.T) {
	saveAndRestore(t)

	root := t.TempDir()

	homeDir := filepath.Join(root, "home")
	userCmds := filepath.Join(homeDir, ".san", "commands")
	if err := os.MkdirAll(userCmds, 0o755); err != nil {
		t.Fatal(err)
	}
	userCmd := "---\nname: deploy\ndescription: User deploy\n---\nUser-level deploy.\n"
	if err := os.WriteFile(filepath.Join(userCmds, "deploy.md"), []byte(userCmd), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(root, "project")
	projectCmds := filepath.Join(projectDir, ".san", "commands")
	if err := os.MkdirAll(projectCmds, 0o755); err != nil {
		t.Fatal(err)
	}
	projectCmd := "---\nname: deploy\ndescription: Project deploy\n---\nProject-level deploy.\n"
	if err := os.WriteFile(filepath.Join(projectCmds, "deploy.md"), []byte(projectCmd), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := initTestService(t, projectDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// The list also carries builtin prompt commands, so assert on the deploy
	// entries specifically: exactly one, and the project copy wins.
	var deploys []CustomCommand
	for _, c := range svc.loadAllCustomCommands() {
		if c.Name == "deploy" {
			deploys = append(deploys, c)
		}
	}
	if len(deploys) != 1 {
		t.Fatalf("expected 1 deploy command (project overrides user), got %d: %+v", len(deploys), deploys)
	}
	if deploys[0].Description != "Project deploy" {
		t.Errorf("description = %q, want %q (project should override user)", deploys[0].Description, "Project deploy")
	}
	if deploys[0].Scope != scopeProject {
		t.Errorf("scope = %d, want %d (scopeProject)", deploys[0].Scope, scopeProject)
	}
}

func TestUserCommandWithoutNamespace(t *testing.T) {
	root := t.TempDir()
	cmdsDir := filepath.Join(root, "commands")
	if err := os.MkdirAll(cmdsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := "---\nname: review\ndescription: Code review\n---\nReview code.\n"
	if err := os.WriteFile(filepath.Join(cmdsDir, "review.md"), []byte(cmd), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds := loadCommandsFromDir(cmdsDir, "", scopeUser)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].FullName() != "review" {
		t.Errorf("fullName = %q, want %q (user commands have no namespace)", cmds[0].FullName(), "review")
	}
}

func TestIsCustomCommand_MatchesUserAndProjectCommands(t *testing.T) {
	saveAndRestore(t)

	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	projectCmds := filepath.Join(projectDir, ".san", "commands")
	if err := os.MkdirAll(projectCmds, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := "---\nname: format\ndescription: Format code\n---\nFormat all source files.\n"
	if err := os.WriteFile(filepath.Join(projectCmds, "format.md"), []byte(cmd), 0o644); err != nil {
		t.Fatal(err)
	}

	initTestService(t, projectDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", filepath.Join(root, "empty-home"))
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	svc := Default()
	pc, ok := svc.IsCustomCommand("format")
	if !ok {
		t.Fatal("expected project-level 'format' command to be found")
	}
	if pc.Description != "Format code" {
		t.Errorf("description = %q, want %q", pc.Description, "Format code")
	}
	if pc.Scope != scopeProject {
		t.Errorf("scope = %d, want %d (scopeProject)", pc.Scope, scopeProject)
	}
}

func TestPluginCommandScope(t *testing.T) {
	saveAndRestore(t)

	_, provider := setupPluginCommandPaths(t)
	initTestService(t, "", withPluginCommandPaths(provider))

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "empty-home"))
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	svc := Default()
	pc, ok := svc.IsCustomCommand("myplugin:greet")
	if !ok {
		t.Fatal("expected plugin command to be found")
	}
	if pc.Scope != scopeProjectPlugin {
		t.Errorf("scope = %d, want %d (scopeProjectPlugin for IsProject=true)", pc.Scope, scopeProjectPlugin)
	}
}
