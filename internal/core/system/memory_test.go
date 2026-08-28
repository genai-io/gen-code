package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveImports(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "san-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mainContent := `# Main File
@imported.md
Some content after import`

	importedContent := `## Imported Content
This was imported from another file.`

	if err := os.WriteFile(filepath.Join(tmpDir, "main.md"), []byte(mainContent), 0o644); err != nil {
		t.Fatalf("Failed to write main.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "imported.md"), []byte(importedContent), 0o644); err != nil {
		t.Fatalf("Failed to write imported.md: %v", err)
	}

	seen := make(map[string]bool)
	result := resolveImports(mainContent, tmpDir, 0, seen)

	if !strings.Contains(result, "<!-- Imported: imported.md -->") {
		t.Errorf("Expected import comment, got: %s", result)
	}
	if !strings.Contains(result, "This was imported from another file.") {
		t.Errorf("Expected imported content, got: %s", result)
	}
	if !strings.Contains(result, "Some content after import") {
		t.Errorf("Expected content after import, got: %s", result)
	}
}

func TestResolveImportsCycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "san-test-cycle")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	file1Content := `# File 1
@file2.md`

	file2Content := `# File 2
@file1.md`

	if err := os.WriteFile(filepath.Join(tmpDir, "file1.md"), []byte(file1Content), 0o644); err != nil {
		t.Fatalf("Failed to write file1.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.md"), []byte(file2Content), 0o644); err != nil {
		t.Fatalf("Failed to write file2.md: %v", err)
	}

	seen := make(map[string]bool)
	seen[filepath.Join(tmpDir, "file1.md")] = true
	result := resolveImports(file1Content, tmpDir, 0, seen)

	if !strings.Contains(result, "# File 2") {
		t.Errorf("Expected file2 content, got: %s", result)
	}
	if !strings.Contains(result, "Skipped (cycle)") {
		t.Errorf("Expected cycle skip comment, got: %s", result)
	}
}

func TestResolveImportsNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "san-test-notfound")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	content := `# Test
@nonexistent.md`

	seen := make(map[string]bool)
	result := resolveImports(content, tmpDir, 0, seen)

	if !strings.Contains(result, "Import not found") {
		t.Errorf("Expected not found comment, got: %s", result)
	}
}

func TestResolveImportsMaxDepth(t *testing.T) {
	content := `@deep.md`

	seen := make(map[string]bool)
	result := resolveImports(content, "/tmp", maxImportDepth, seen)

	if result != content {
		t.Errorf("Expected unchanged content at max depth, got: %s", result)
	}
}

func TestLoadRulesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	rulesDir := filepath.Join(tmpDir, "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatalf("Failed to create rules dir: %v", err)
	}

	writeFile(t, filepath.Join(rulesDir, "coding.md"), "# Coding Rules")
	writeFile(t, filepath.Join(rulesDir, "security.md"), "# Security Rules")
	writeFile(t, filepath.Join(rulesDir, "readme.txt"), "Ignore me")

	l := &loader{seen: make(map[string]bool), remaining: instructionsByteCap}
	l.addRulesDirectory(rulesDir, "project")

	if len(l.files) != 2 {
		t.Fatalf("Expected 2 rule files, got %d", len(l.files))
	}
	if !strings.Contains(l.files[0].Path, "coding.md") {
		t.Errorf("Expected coding.md first (alphabetical), got: %s", l.files[0].Path)
	}
	if !strings.Contains(l.files[1].Path, "security.md") {
		t.Errorf("Expected security.md second, got: %s", l.files[1].Path)
	}
}

func TestGetAllMemoryPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	paths := GetAllMemoryPaths(cwd)

	if want := filepath.Join(home, ".san", InstructionFile); paths.Global != want {
		t.Errorf("Global = %q, want %q", paths.Global, want)
	}
	if len(paths.Project) != 1 || paths.Project[0] != filepath.Join(cwd, InstructionFile) {
		t.Errorf("Project = %v, want [%s]", paths.Project, filepath.Join(cwd, InstructionFile))
	}
	if want := filepath.Join(cwd, LocalInstructionFile); paths.Local != want {
		t.Errorf("Local = %q, want %q", paths.Local, want)
	}
	if want := filepath.Join(cwd, ".san", "rules"); paths.ProjectRules != want {
		t.Errorf("ProjectRules = %q, want %q", paths.ProjectRules, want)
	}
}

func TestProjectRootAndInstructionChain(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "gitdir: elsewhere")
	sub := filepath.Join(root, "packages", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if got := ProjectRoot(sub); got != root {
		t.Errorf("ProjectRoot(%s) = %q, want %q", sub, got, root)
	}

	chain := ProjectInstructionChain(sub)
	want := []string{
		filepath.Join(root, InstructionFile),
		filepath.Join(root, "packages", InstructionFile),
		filepath.Join(sub, InstructionFile),
	}
	if len(chain) != len(want) {
		t.Fatalf("chain = %v, want %v", chain, want)
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Errorf("chain[%d] = %q, want %q", i, chain[i], want[i])
		}
	}

	loose := t.TempDir()
	if got := ProjectInstructionChain(loose); len(got) != 1 || got[0] != filepath.Join(loose, InstructionFile) {
		t.Errorf("outside a repository the chain should be the cwd file alone, got %v", got)
	}
}

func TestLoadMemoryFiles_NestedChainNearestLast(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "gitdir: elsewhere")
	writeFile(t, filepath.Join(root, InstructionFile), "root instructions")

	sub := filepath.Join(root, "packages", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(sub, InstructionFile), "package instructions")

	files := LoadMemoryFiles(sub)
	if len(files) != 2 {
		t.Fatalf("expected the root and package files, got %d: %v", len(files), files)
	}
	if !strings.Contains(files[0].Content, "root instructions") {
		t.Errorf("expected the root file first, got: %s", files[0].Path)
	}
	if !strings.Contains(files[1].Content, "package instructions") {
		t.Errorf("expected the package file last so it wins, got: %s", files[1].Path)
	}
}

func TestLoadMemoryFiles_SectionOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()

	writeFile(t, filepath.Join(home, ".san", InstructionFile), "user instructions")
	writeFile(t, filepath.Join(home, ".san", "rules", "01-global.md"), "global rule")
	writeFile(t, filepath.Join(root, InstructionFile), "project instructions")
	writeFile(t, filepath.Join(root, ".san", "rules", "01-project.md"), "project rule")
	writeFile(t, filepath.Join(root, LocalInstructionFile), "local instructions")

	files := LoadMemoryFiles(root)
	if len(files) != 5 {
		t.Fatalf("expected 5 instruction files, got %d", len(files))
	}

	wantLevels := []string{"global", "global", "project", "project", "local"}
	wantPaths := []string{
		filepath.Join(home, ".san", InstructionFile),
		filepath.Join(home, ".san", "rules", "01-global.md"),
		filepath.Join(root, InstructionFile),
		filepath.Join(root, ".san", "rules", "01-project.md"),
		filepath.Join(root, LocalInstructionFile),
	}
	for i := range wantPaths {
		if files[i].Level != wantLevels[i] || files[i].Path != wantPaths[i] {
			t.Errorf("files[%d] = (%s, %s), want (%s, %s)", i, files[i].Level, files[i].Path, wantLevels[i], wantPaths[i])
		}
	}
}

func TestLoadMemoryFiles_ByteCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "gitdir: elsewhere")
	writeFile(t, filepath.Join(root, InstructionFile), strings.Repeat("root line\n", instructionsByteCap/5))

	sub := filepath.Join(root, "packages")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(sub, InstructionFile), "package instructions")

	files := LoadMemoryFiles(sub)
	if len(files) != 1 {
		t.Fatalf("expected the oversized root file to exhaust the cap, got %d files", len(files))
	}
	if !strings.Contains(files[0].Content, "instructions truncated") {
		t.Error("expected a truncation marker in the capped file")
	}
	if len(files[0].Content) > instructionsByteCap+len(files[0].Path)+200 {
		t.Errorf("capped content is %d bytes, want at most ~%d", len(files[0].Content), instructionsByteCap)
	}
}

func TestFindNearestMemoryFile(t *testing.T) {
	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "root.md")
	nested := filepath.Join(tmpDir, "nested.md")
	writeFile(t, root, "root")
	writeFile(t, nested, "nested")

	tests := []struct {
		name     string
		paths    []string
		expected string
	}{
		{"nearest wins", []string{root, filepath.Join(tmpDir, "gone.md"), nested}, nested},
		{"falls back to the only existing file", []string{root, filepath.Join(tmpDir, "gone.md")}, root},
		{"no files exist", []string{filepath.Join(tmpDir, "a.md")}, ""},
		{"empty chain", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindNearestMemoryFile(tc.paths); got != tc.expected {
				t.Errorf("FindNearestMemoryFile() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestExistingMemoryFiles(t *testing.T) {
	tmpDir := t.TempDir()
	first := filepath.Join(tmpDir, "first.md")
	second := filepath.Join(tmpDir, "second.md")
	writeFile(t, first, "first")
	writeFile(t, second, "second")

	got := ExistingMemoryFiles([]string{first, filepath.Join(tmpDir, "gone.md"), second})
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Errorf("ExistingMemoryFiles() = %v, want [%s %s]", got, first, second)
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		size     int64
		expected string
	}{
		{500, "500B"},
		{1024, "1.0KB"},
		{2048, "2.0KB"},
		{1024 * 1024, "1.0MB"},
		{1536 * 1024, "1.5MB"},
	}

	for _, tc := range tests {
		result := FormatFileSize(tc.size)
		if result != tc.expected {
			t.Errorf("FormatFileSize(%d) = %s, expected %s", tc.size, result, tc.expected)
		}
	}
}

func TestResolveImportsNested(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "san-test-nested")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	aContent := `# Level A
@b.md
After B import`

	bContent := `## Level B
@c.md
After C import`

	cContent := `### Level C
Deepest content`

	if err := os.WriteFile(filepath.Join(tmpDir, "a.md"), []byte(aContent), 0o644); err != nil {
		t.Fatalf("Failed to write a.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.md"), []byte(bContent), 0o644); err != nil {
		t.Fatalf("Failed to write b.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "c.md"), []byte(cContent), 0o644); err != nil {
		t.Fatalf("Failed to write c.md: %v", err)
	}

	seen := make(map[string]bool)
	result := resolveImports(aContent, tmpDir, 0, seen)

	if !strings.Contains(result, "<!-- Imported: b.md -->") {
		t.Errorf("Expected b.md import comment, got: %s", result)
	}
	if !strings.Contains(result, "<!-- Imported: c.md -->") {
		t.Errorf("Expected c.md import comment, got: %s", result)
	}
	if !strings.Contains(result, "Deepest content") {
		t.Errorf("Expected deepest content from c.md, got: %s", result)
	}
	if !strings.Contains(result, "After C import") {
		t.Errorf("Expected content after C import from b.md, got: %s", result)
	}
	if !strings.Contains(result, "After B import") {
		t.Errorf("Expected content after B import from a.md, got: %s", result)
	}
}

func TestResolveImportsRelativePath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "san-test-relative")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	mainContent := `# Main
@./subdir/nested.md`

	nestedContent := `## Nested
Nested content here`

	if err := os.WriteFile(filepath.Join(tmpDir, "main.md"), []byte(mainContent), 0o644); err != nil {
		t.Fatalf("Failed to write main.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.md"), []byte(nestedContent), 0o644); err != nil {
		t.Fatalf("Failed to write nested.md: %v", err)
	}

	seen := make(map[string]bool)
	result := resolveImports(mainContent, tmpDir, 0, seen)

	if !strings.Contains(result, "<!-- Imported: ./subdir/nested.md -->") {
		t.Errorf("Expected nested import comment, got: %s", result)
	}
	if !strings.Contains(result, "Nested content here") {
		t.Errorf("Expected nested content, got: %s", result)
	}
}

func TestLoadMemoryFilesWithImports(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	writeFile(t, filepath.Join(root, InstructionFile), "# Project Memory\n@extra.md\nEnd of memory")
	writeFile(t, filepath.Join(root, "extra.md"), "## Extra Content\nThis was imported")

	projectFile := findProjectFile(t, LoadMemoryFiles(root))

	if !strings.Contains(projectFile.Content, "<!-- Imported: extra.md -->") {
		t.Errorf("Expected import comment in content, got: %s", projectFile.Content)
	}
	if !strings.Contains(projectFile.Content, "This was imported") {
		t.Errorf("Expected imported content, got: %s", projectFile.Content)
	}
}

func TestLoadInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()

	writeFile(t, filepath.Join(home, ".san", InstructionFile), "User instructions here")
	writeFile(t, filepath.Join(root, InstructionFile), "Project instructions here")
	writeFile(t, filepath.Join(root, LocalInstructionFile), "Local instructions here")

	user, project := LoadInstructions(root)

	if !strings.Contains(user, "User instructions here") {
		t.Errorf("user instructions should contain the user AGENTS.md content, got: %s", user)
	}
	if !strings.Contains(project, "Project instructions here") {
		t.Errorf("project instructions should contain the AGENTS.md content, got: %s", project)
	}
	if !strings.Contains(project, "Local instructions here") {
		t.Errorf("project instructions should contain the AGENTS.local.md content, got: %s", project)
	}
}

func TestMemory_ImportChain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	writeFile(t, filepath.Join(root, InstructionFile), "# Root\n@a.md")
	writeFile(t, filepath.Join(root, "a.md"), "## Level A\n@b.md")
	writeFile(t, filepath.Join(root, "b.md"), "### Level B\nFinal content from B")

	projectFile := findProjectFile(t, LoadMemoryFiles(root))

	for _, want := range []string{"Level A", "Final content from B", "<!-- Imported: a.md -->", "<!-- Imported: b.md -->"} {
		if !strings.Contains(projectFile.Content, want) {
			t.Errorf("Expected %q in resolved output; got: %s", want, projectFile.Content)
		}
	}
}

func TestMemory_MissingFile_NoError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	files := LoadMemoryFiles(root)
	for _, f := range files {
		if f.Level == "project" && strings.Contains(f.Path, root) {
			t.Errorf("Did not expect a project instruction file when AGENTS.md is absent, got: %s", f.Path)
		}
	}

	if _, project := LoadInstructions(root); project != "" {
		t.Errorf("Expected no project instructions, got: %s", project)
	}
}

// writeFile writes content to path, creating parent directories as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// findProjectFile returns the single project-level instruction file.
func findProjectFile(t *testing.T, files []MemoryFile) MemoryFile {
	t.Helper()
	for _, f := range files {
		if f.Level == "project" {
			return f
		}
	}
	t.Fatal("Expected a project-level AGENTS.md in the loaded files")
	return MemoryFile{}
}

func TestResolveAutoMemoryDir(t *testing.T) {
	cwd := "/work/project-x"
	// Empty override → the project-partitioned default.
	if got := ResolveAutoMemoryDir(cwd, ""); got != AutoMemoryDir(cwd) {
		t.Fatalf("empty override: got %q, want default %q", got, AutoMemoryDir(cwd))
	}
	// Absolute override is used verbatim.
	if got := ResolveAutoMemoryDir(cwd, "/srv/mem"); got != "/srv/mem" {
		t.Fatalf("absolute override: got %q", got)
	}
	// Relative override resolves against cwd.
	if got := ResolveAutoMemoryDir(cwd, "notes/mem"); got != filepath.Join(cwd, "notes/mem") {
		t.Fatalf("relative override: got %q", got)
	}
	// "~" expands to the home dir.
	if home, err := os.UserHomeDir(); err == nil {
		if got := ResolveAutoMemoryDir(cwd, "~/mem"); got != filepath.Join(home, "mem") {
			t.Fatalf("tilde override: got %q, want %q", got, filepath.Join(home, "mem"))
		}
	}
}
