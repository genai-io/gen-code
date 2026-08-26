package system

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/log"
	"go.uber.org/zap"
)

const (
	maxImportDepth = 5

	// InstructionFile is the industry-standard agent instruction file
	// (https://agents.md). San reads it at the user level and at every
	// directory from the project root down to the working directory.
	InstructionFile = "AGENTS.md"

	// LocalInstructionFile holds project instructions that stay out of git.
	LocalInstructionFile = "AGENTS.local.md"

	// instructionsByteCap bounds the combined size of the instruction files
	// injected at session start, matching Codex's 32 KiB project_doc_max_bytes
	// so a deep directory chain cannot crowd out the conversation.
	instructionsByteCap = 32 * 1024

	// AutoMemoryIndexName is the index file of the agent-written auto-memory
	// store. Topic files (loaded on demand by the agent) live beside it.
	AutoMemoryIndexName = "MEMORY.md"

	// autoMemoryByteCap bounds how much of the auto-memory index is injected at
	// session start, mirroring Claude Code's index cap. Topic files are never
	// injected — the agent reads them on demand.
	autoMemoryByteCap = 25 * 1024
)

// AutoMemoryDir is the project-partitioned directory backing the agent-written
// auto-memory store: ~/.san/projects/<encoded-cwd>/memory/. It shares the
// project partitioning used by the session transcript store, so worktrees and
// subdirectories of one repo resolve to the same store.
func AutoMemoryDir(cwd string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(confdir.Dir(cwd), "memory")
	}
	return filepath.Join(confdir.Dir(homeDir), "projects", encodeProjectPath(cwd), "memory")
}

// encodeProjectPath mirrors internal/session.EncodePath: replaces path
// separators with "-" so the cwd can stand alone as a subdirectory name.
// Duplicated (5 lines) to keep core layer-pure rather than importing the
// session feature package; the two functions must stay in lockstep so
// memory and transcript stores resolve to the same project partition.
func encodeProjectPath(path string) string {
	path = strings.TrimRight(path, "/")
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, ":", "-")
		path = strings.ReplaceAll(path, "\\", "-")
	}
	return strings.ReplaceAll(path, "/", "-")
}

// AutoMemoryIndexPath is the auto-memory index file for cwd's project.
func AutoMemoryIndexPath(cwd string) string {
	return filepath.Join(AutoMemoryDir(cwd), AutoMemoryIndexName)
}

// ResolveAutoMemoryDir returns the directory backing the auto-memory store,
// honoring a user override (the /evolve Memory storage path). An empty override
// falls back to the project-partitioned default; a "~" prefix expands to the
// home dir; a relative override resolves against cwd.
func ResolveAutoMemoryDir(cwd, override string) string {
	override = strings.TrimSpace(override)
	if override == "" {
		return AutoMemoryDir(cwd)
	}
	if override == "~" || strings.HasPrefix(override, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			override = filepath.Join(home, strings.TrimPrefix(override, "~"))
		}
	}
	if !filepath.IsAbs(override) {
		override = filepath.Join(cwd, override)
	}
	return override
}

// LoadAutoMemoryAt reads the agent-written auto-memory index from dir —
// resolve it with ResolveAutoMemoryDir so a user-configured memory path is
// honored (the reviewer prompt and the main-agent memory reminder both do).
// Capped at autoMemoryByteCap. It is a distinct source from LoadMemoryFiles:
// agent-written memory and user-authored AGENTS.md instructions are
// injected as separate blocks and never mixed. Returns ("", false) when the
// store is empty or absent. When the index exceeds the cap it is truncated on
// a line boundary with a marker — topic files are read on demand and never
// injected.
func LoadAutoMemoryAt(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, AutoMemoryIndexName))
	if err != nil {
		return "", false
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", false
	}
	if len(content) > autoMemoryByteCap {
		content = truncateOnLineBoundary(content, autoMemoryByteCap) +
			"\n\n<!-- auto-memory truncated; read topic files on demand -->"
	}
	return content, true
}

// truncateOnLineBoundary trims s to at most max bytes, cutting at the last
// newline within the budget so a partial line is never injected.
func truncateOnLineBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return cut[:i]
	}
	return cut
}

// MemoryFile represents a loaded memory file with metadata.
type MemoryFile struct {
	Path    string
	Size    int64
	Content string
	Level   string // "global", "project", or "local"
}

// LoadInstructions loads user-level and project-level instructions separately.
func LoadInstructions(cwd string) (user, project string) {
	files := LoadMemoryFiles(cwd)
	var userParts, projectParts []string
	for _, f := range files {
		switch f.Level {
		case "global":
			userParts = append(userParts, f.Content)
		case "project", "local":
			projectParts = append(projectParts, f.Content)
		}
	}
	return strings.Join(userParts, "\n\n"), strings.Join(projectParts, "\n\n")
}

// LoadMemoryFiles loads every instruction file that applies to cwd, in the
// order they are injected: user-level AGENTS.md, user rules, the project chain
// from the repository root down to cwd, project rules, then the git-ignored
// local override. Files closer to cwd are injected last so their instructions
// win, mirroring the AGENTS.md convention shared by Codex and Cursor.
func LoadMemoryFiles(cwd string) []MemoryFile {
	homeDir, _ := os.UserHomeDir()
	l := &loader{seen: make(map[string]bool), remaining: instructionsByteCap}

	userDir := confdir.Dir(homeDir)
	l.add(filepath.Join(userDir, InstructionFile), "global")
	l.addRulesDirectory(filepath.Join(userDir, "rules"), "global")

	for _, path := range ProjectInstructionChain(cwd) {
		l.add(path, "project")
	}
	l.addRulesDirectory(filepath.Join(confdir.Dir(cwd), "rules"), "project")

	l.add(filepath.Join(ProjectRoot(cwd), LocalInstructionFile), "local")

	return l.files
}

// ProjectRoot returns the repository root at or above cwd, falling back to cwd
// when cwd is not inside a repository. Instruction discovery starts here.
func ProjectRoot(cwd string) string {
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// ProjectInstructionChain returns the AGENTS.md path for every directory from
// the project root down to cwd, root first. Every file in the chain is loaded;
// the ones nearer cwd are injected last and therefore take precedence.
func ProjectInstructionChain(cwd string) []string {
	root := ProjectRoot(cwd)
	paths := []string{filepath.Join(root, InstructionFile)}

	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return paths
	}
	dir := root
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		dir = filepath.Join(dir, segment)
		paths = append(paths, filepath.Join(dir, InstructionFile))
	}
	return paths
}

// loader accumulates instruction files while enforcing the combined byte cap.
type loader struct {
	seen      map[string]bool
	remaining int
	files     []MemoryFile
}

// add loads one instruction file, resolves its @imports, and appends it unless
// the file is missing, empty, already loaded, or the byte cap is exhausted.
func (l *loader) add(path, level string) {
	info, err := os.Stat(path)
	if err != nil || l.seen[path] {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return
	}
	l.seen[path] = true
	content = resolveImports(content, filepath.Dir(path), 0, l.seen)

	if l.remaining <= 0 {
		log.Logger().Warn("Skipped instruction file: byte cap reached",
			zap.String("path", path),
			zap.Int("cap", instructionsByteCap))
		return
	}
	if len(content) > l.remaining {
		log.Logger().Warn("Truncated instruction file: byte cap reached",
			zap.String("path", path),
			zap.Int("cap", instructionsByteCap))
		content = truncateOnLineBoundary(content, l.remaining) +
			"\n\n<!-- instructions truncated: combined 32KB cap reached -->"
	}
	l.remaining -= len(content)

	log.Logger().Info("Loaded instruction file",
		zap.String("path", path),
		zap.Int64("bytes", info.Size()),
		zap.String("level", level))

	l.files = append(l.files, MemoryFile{
		Path:    path,
		Size:    info.Size(),
		Content: fmt.Sprintf("<!-- Source: %s -->\n%s", path, content),
		Level:   level,
	})
}

// addRulesDirectory loads every .md file in dir, alphabetically.
func (l *loader) addRulesDirectory(dir, level string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	for _, path := range paths {
		l.add(path, level)
	}
}

// importRe matches @import directives in memory files (e.g., @file.md).
var importRe = regexp.MustCompile(`(?m)^@([^\s@]+\.md)\s*$`)

func resolveImports(content string, basePath string, depth int, seen map[string]bool) string {
	if depth >= maxImportDepth {
		return content
	}
	return importRe.ReplaceAllStringFunc(content, func(match string) string {
		importPath := strings.TrimPrefix(strings.TrimSpace(match), "@")
		fullPath := filepath.Clean(filepath.Join(basePath, importPath))

		// Path traversal guard: resolved path must stay under basePath.
		// Use trailing separator to prevent prefix collisions (e.g., /tmp/project vs /tmp/projectile).
		baseWithSep := basePath + string(filepath.Separator)
		if fullPath != basePath && !strings.HasPrefix(fullPath, baseWithSep) {
			return fmt.Sprintf("<!-- Import blocked (outside base): @%s -->", importPath)
		}

		// Symlink guard: resolve symlinks and re-check to prevent escapes
		// via symlinks that point outside the base directory.
		if realPath, err := filepath.EvalSymlinks(fullPath); err == nil {
			realBase, _ := filepath.EvalSymlinks(basePath)
			if realBase != "" {
				realBaseWithSep := realBase + string(filepath.Separator)
				if realPath != realBase && !strings.HasPrefix(realPath, realBaseWithSep) {
					return fmt.Sprintf("<!-- Import blocked (symlink escape): @%s -->", importPath)
				}
			}
		}

		if seen[fullPath] {
			return fmt.Sprintf("<!-- Skipped (cycle): @%s -->", importPath)
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Sprintf("<!-- Import not found: @%s -->", importPath)
		}
		seen[fullPath] = true
		importedContent := strings.TrimSpace(string(data))

		log.Logger().Info("Resolved import",
			zap.String("import", importPath),
			zap.String("fullPath", fullPath),
			zap.Int("depth", depth))

		importedContent = resolveImports(importedContent, filepath.Dir(fullPath), depth+1, seen)
		return fmt.Sprintf("<!-- Imported: %s -->\n%s", importPath, importedContent)
	})
}

// MemoryPaths holds the instruction paths San consults, for display and edit.
type MemoryPaths struct {
	Global       string   // ~/.san/AGENTS.md
	GlobalRules  string   // ~/.san/rules/
	Project      []string // <root>/AGENTS.md ... <cwd>/AGENTS.md, root first
	ProjectRules string   // <cwd>/.san/rules/
	Local        string   // <root>/AGENTS.local.md
}

// GetAllMemoryPaths returns all instruction paths organized by category.
func GetAllMemoryPaths(cwd string) MemoryPaths {
	homeDir, _ := os.UserHomeDir()
	userDir := confdir.Dir(homeDir)
	return MemoryPaths{
		Global:       filepath.Join(userDir, InstructionFile),
		GlobalRules:  filepath.Join(userDir, "rules"),
		Project:      ProjectInstructionChain(cwd),
		ProjectRules: filepath.Join(confdir.Dir(cwd), "rules"),
		Local:        filepath.Join(ProjectRoot(cwd), LocalInstructionFile),
	}
}

// FindNearestMemoryFile returns the existing path closest to the working
// directory -- the last entry of a root-first chain -- since the instructions
// nearest cwd are the ones that win.
func FindNearestMemoryFile(paths []string) string {
	for i := len(paths) - 1; i >= 0; i-- {
		if _, err := os.Stat(paths[i]); err == nil {
			return paths[i]
		}
	}
	return ""
}

// ExistingMemoryFiles returns the paths that exist, preserving chain order.
func ExistingMemoryFiles(paths []string) []string {
	var found []string
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	return found
}

// ListRulesFiles returns all .md files in a rules directory.
func ListRulesFiles(rulesDir string) []string {
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".md") {
			files = append(files, filepath.Join(rulesDir, name))
		}
	}
	sort.Strings(files)
	return files
}

// GetFileSize returns the size of a file in bytes, or 0 if not found.
func GetFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// FormatFileSize formats a file size for display.
func FormatFileSize(size int64) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%.1fMB", float64(size)/(1024*1024))
	}
	if size >= 1024 {
		return fmt.Sprintf("%.1fKB", float64(size)/1024)
	}
	return fmt.Sprintf("%dB", size)
}
