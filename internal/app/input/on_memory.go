// Memory file selector, state, runtime, and commands (/init, /memory) for
// project memory file management. Flattened from internal/app/user/memory/.
package input

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
)

// ── State ───────────────────────────────────────────────────────────────

// MemoryState holds memory selector UI state for the TUI model.
// Cached instructions (User, Project) live on the parent app model, not here.
type MemoryState struct {
	Selector    MemorySelector
	EditingFile string
}

// MemoryEditorFinishedMsg is sent when the external memory editor closes.
type MemoryEditorFinishedMsg struct {
	Err error
}

// ── Selector model ──────────────────────────────────────────────────────

// memoryItem represents a memory file option in the kit.
type memoryItem struct {
	Label       string
	Description string
	Path        string
	Exists      bool
	Size        int64
	Level       string
	CreateHint  string
}

// MemorySelector holds the state for the memory kit.
type MemorySelector struct {
	active      bool
	items       []memoryItem
	selectedIdx int
	width       int
	height      int
	cwd         string
}

// memorySelectedMsg is sent when a memory file is selected for editing.
type memorySelectedMsg struct {
	Path  string
	Level string
}

// NewMemorySelector creates a new memory selector.
func NewMemorySelector() MemorySelector {
	return MemorySelector{
		active:      false,
		items:       []memoryItem{},
		selectedIdx: 0,
	}
}

// EnterSelect enters memory selection mode.
func (m *MemorySelector) EnterSelect(cwd string, width, height int) {
	m.cwd = cwd
	m.width = width
	m.height = height
	m.active = true
	m.selectedIdx = 0

	paths := system.GetAllMemoryPaths(cwd)
	m.items = []memoryItem{
		m.buildMemoryItem("Global", "global", []string{paths.Global}, cwd,
			fmt.Sprintf("Saved in %s", kit.ShortenPath(paths.Global)),
			"Will be created on edit"),

		m.buildMemoryItem("Project", "project", paths.Project, cwd,
			"Checked in at AGENTS.md",
			"Use /init to create"),

		m.buildMemoryItem("Local", "local", []string{paths.Local}, cwd,
			"Not committed (git-ignored)",
			"Use /init local to create"),
	}
}

// buildMemoryItem describes one memory level. searchPaths is a root-first
// chain, so the nearest existing file is the one the agent actually obeys;
// when none exists the first path is where the file would be created.
func (m *MemorySelector) buildMemoryItem(label, level string, searchPaths []string, cwd, defaultDesc, createHint string) memoryItem {
	foundPath := system.FindNearestMemoryFile(searchPaths)
	exists := foundPath != ""

	path := foundPath
	if !exists {
		path = searchPaths[0]
	}

	description := defaultDesc
	if exists && level == "project" {
		description = fmt.Sprintf("Checked in at %s", kit.ShortenPathForProject(foundPath, cwd))
	}

	return memoryItem{
		Label:       label,
		Description: description,
		Path:        path,
		Exists:      exists,
		Size:        system.GetFileSize(path),
		Level:       level,
		CreateHint:  createHint,
	}
}

// IsActive returns whether the selector is active.
func (m *MemorySelector) IsActive() bool {
	return m.active
}

// Cancel cancels the kit.
func (m *MemorySelector) Cancel() {
	m.active = false
	m.items = []memoryItem{}
	m.selectedIdx = 0
}

// HandleKeypress handles a keypress and returns a command if selection is made.
func (m *MemorySelector) HandleKeypress(key tea.KeyMsg) tea.Cmd {
	keyStr := key.String()

	switch keyStr {
	case "up", "ctrl+p", "k":
		if m.selectedIdx > 0 {
			m.selectedIdx--
		}
	case "down", "ctrl+n", "j":
		if m.selectedIdx < len(m.items)-1 {
			m.selectedIdx++
		}
	case "enter", "right", "l":
		return m.selectMemoryItem()
	case "esc", "left", "h":
		return m.cancelMemoryWithMsg()
	case "1", "2", "3":
		idx := int(keyStr[0] - '1')
		if idx < len(m.items) {
			m.selectedIdx = idx
			return m.selectMemoryItem()
		}
	}

	return nil
}

func (m *MemorySelector) selectMemoryItem() tea.Cmd {
	if m.selectedIdx >= len(m.items) {
		return nil
	}

	selected := m.items[m.selectedIdx]
	m.active = false

	return func() tea.Msg {
		return memorySelectedMsg{
			Path:  selected.Path,
			Level: selected.Level,
		}
	}
}

func (m *MemorySelector) cancelMemoryWithMsg() tea.Cmd {
	m.Cancel()
	return func() tea.Msg {
		return kit.DismissedMsg{}
	}
}

// Render renders the kit.
func (m *MemorySelector) Render() string {
	if !m.active {
		return ""
	}

	var sb strings.Builder

	sb.WriteString(kit.SelectorTitleStyle().Render("Select memory to edit:"))
	sb.WriteString("\n\n")

	for i, item := range m.items {
		var statusIcon string
		var statusStyle lipgloss.Style

		if item.Exists {
			statusIcon = "●"
			statusStyle = kit.SelectorStatusConnected()
		} else {
			statusIcon = "○"
			statusStyle = kit.SelectorStatusNone()
		}

		numKey := fmt.Sprintf("%d.", i+1)
		sizeStr := ""
		if item.Exists && item.Size > 0 {
			sizeStr = fmt.Sprintf(" (%s)", system.FormatFileSize(item.Size))
		}

		line := fmt.Sprintf("%s %s %s",
			statusStyle.Render(statusIcon),
			item.Label,
			kit.SelectorHintStyle().Render(item.Description+sizeStr),
		)

		sb.WriteString(kit.RenderSelectableRow(numKey+" "+line, i == m.selectedIdx))
		sb.WriteString("\n")

		if !item.Exists && i == m.selectedIdx {
			sb.WriteString(kit.SelectorItemStyle().Render("      " + kit.SelectorHintStyle().Render(item.CreateHint)))
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n")
	sb.WriteString(kit.SelectorHintStyle().Render("↑/↓ navigate · Enter edit · 1-3 quick select · Esc cancel"))

	content := sb.String()
	box := kit.SelectorBorderStyle().Width(kit.CalculateBoxWidth(m.width)).Render(content)

	return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, box)
}

// ── Runtime interface & Update ──────────────────────────────────────────

// UpdateMemory routes memory selection and editor messages.
func UpdateMemory(deps OverlayDeps, state *MemoryState, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case memorySelectedMsg:
		return handleMemorySelected(deps, state, msg), true
	case MemoryEditorFinishedMsg:
		return handleMemoryEditorFinished(deps, state, msg), true
	}
	return nil, false
}

func handleMemorySelected(deps OverlayDeps, state *MemoryState, msg memorySelectedMsg) tea.Cmd {
	filePath := msg.Path

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if err := CreateMemoryFile(filePath, msg.Level, deps.Cwd); err != nil {
			deps.Conv.Append(core.ChatMessage{
				Role:    core.RoleNotice,
				Content: fmt.Sprintf("Error: %v", err),
			})
			return tea.Batch(deps.CommitMessages()...)
		}
	}

	state.EditingFile = filePath

	displayPath := FormatMemoryDisplayPath(filePath, msg.Level, deps.Cwd)

	deps.Conv.Append(core.ChatMessage{
		Role:    core.RoleNotice,
		Content: fmt.Sprintf("Opening %s memory: %s", msg.Level, displayPath),
	})

	commitCmds := deps.CommitMessages()
	commitCmds = append(commitCmds, startExternalEditorForMemory(filePath))
	return tea.Batch(commitCmds...)
}

func handleMemoryEditorFinished(deps OverlayDeps, state *MemoryState, msg MemoryEditorFinishedMsg) tea.Cmd {
	filePath := state.EditingFile
	state.EditingFile = ""

	deps.ClearCachedInstructions()

	content := fmt.Sprintf("Saved: %s", filePath)
	if msg.Err != nil {
		content = fmt.Sprintf("Editor error: %v", msg.Err)
	} else {
		deps.RefreshMemoryContext(deps.Cwd, "memory_edit")
		deps.FireFileChanged(filePath, "memory_editor")
	}

	deps.Conv.Append(core.ChatMessage{Role: core.RoleNotice, Content: content})
	return tea.Batch(deps.CommitMessages()...)
}

// startExternalEditorForMemory launches the external editor for a memory file.
func startExternalEditorForMemory(filePath string) tea.Cmd {
	return kit.StartExternalEditor(filePath, func(err error) tea.Msg {
		return MemoryEditorFinishedMsg{Err: err}
	})
}

// ── Commands (/init, /memory) ───────────────────────────────────────────

// HandleInitCommand handles the /init command.
// cwd is the current working directory.
func HandleInitCommand(cwd, args string) (string, error) {
	args = strings.TrimSpace(args)
	parts := strings.Fields(args)

	subCmd := ""
	if len(parts) > 0 && !strings.HasPrefix(parts[0], "--") {
		subCmd = strings.ToLower(parts[0])
	}

	switch subCmd {
	case "local":
		return handleInitLocal(cwd)
	case "rules":
		return handleInitRules(cwd)
	default:
		return handleInitProject(cwd)
	}
}

func handleInitProject(cwd string) (string, error) {
	root := system.ProjectRoot(cwd)
	filePath := filepath.Join(root, system.InstructionFile)

	if _, err := os.Stat(filePath); err == nil {
		return fmt.Sprintf("File already exists: %s\nUse /memory edit to modify it.", filePath), nil
	}

	if err := os.WriteFile(filePath, []byte(getMemoryProjectTemplate(root)), 0o644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", filePath, err)
	}

	return fmt.Sprintf("Created %s\n\nEdit with: /memory edit", filePath), nil
}

func handleInitLocal(cwd string) (string, error) {
	root := system.ProjectRoot(cwd)
	filePath := filepath.Join(root, system.LocalInstructionFile)

	if _, err := os.Stat(filePath); err == nil {
		return fmt.Sprintf("File already exists: %s\nUse /memory edit local to modify it.", filePath), nil
	}

	if err := os.WriteFile(filePath, []byte(getMemoryLocalTemplate()), 0o644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", filePath, err)
	}

	memoryAddToGitignore(root, system.LocalInstructionFile)

	return fmt.Sprintf("Created %s (added to .gitignore)\n\nEdit with: /memory edit local", filePath), nil
}

func handleInitRules(cwd string) (string, error) {
	rulesDir := filepath.Join(confdir.Dir(cwd), "rules")

	if _, err := os.Stat(rulesDir); err == nil {
		return fmt.Sprintf("Directory already exists: %s", rulesDir), nil
	}

	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", rulesDir, err)
	}

	examplePath := filepath.Join(rulesDir, "example.md")
	if err := os.WriteFile(examplePath, []byte(getMemoryRulesTemplate()), 0o644); err != nil {
		return "", fmt.Errorf("failed to write example rule: %w", err)
	}

	return fmt.Sprintf("Created %s\n\nAdd .md files to this directory to define rules.\nExample created: %s", rulesDir, examplePath), nil
}

// memoryAddToGitignore adds an entry to .gitignore in the given directory if not already present.
// Creates the file if it doesn't exist.
func memoryAddToGitignore(cwd, entry string) {
	gitignorePath := filepath.Join(cwd, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return
	}

	content := string(data)
	// Check line-by-line to avoid substring false positives
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(line) == entry {
			return
		}
	}

	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += entry + "\n"
	_ = os.WriteFile(gitignorePath, []byte(content), 0o644)
}

// HandleMemoryCommand handles the /memory command.
// selector is the memory selector model. cwd, width, height are from the app model.
// Returns (result string, editFilePath string, error).
// When editFilePath is non-empty, the caller should open an external editor for that file.
func HandleMemoryCommand(selector *MemorySelector, cwd string, width, height int, args string) (string, string, error) {
	args = strings.TrimSpace(args)
	parts := strings.Fields(args)

	if len(parts) == 0 {
		selector.EnterSelect(cwd, width, height)
		return "", "", nil
	}

	subCmd := strings.ToLower(parts[0])

	scope := "project"
	if len(parts) > 1 {
		scope = strings.ToLower(parts[1])
	}

	switch subCmd {
	case "list":
		result, err := handleMemoryList(cwd)
		return result, "", err
	case "show":
		result, err := handleMemoryShow(cwd)
		return result, "", err
	case "edit":
		editPath, err := handleMemoryEdit(cwd, scope)
		if err != nil {
			return "", "", err
		}
		if editPath == "" {
			return "No project memory file found.\n\nCreate with: /init", "", nil
		}
		return "", editPath, nil
	default:
		return "Usage: /memory [list|show|edit] [global|project|local]", "", nil
	}
}

type memoryListState struct {
	cwd        string
	totalFiles int
	totalSize  int64
}

const (
	memoryBoxWidth = 53
	memoryMaxPath  = 36
)

// handleMemoryList lists all memory files.
func handleMemoryList(cwd string) (string, error) {
	paths := system.GetAllMemoryPaths(cwd)
	state := &memoryListState{cwd: cwd}

	var sb strings.Builder

	sb.WriteString("╭─ Memory Files ─────────────────────────────────────╮\n")
	sb.WriteString(memoryFormatBoxLine(""))

	state.writeMemorySection(&sb, "Global", []string{paths.Global}, paths.GlobalRules, paths.Global, false)
	state.writeMemorySection(&sb, "Project", paths.Project, paths.ProjectRules, "/init", true)
	state.writeMemoryLocalSection(&sb, paths.Local)

	sb.WriteString("╰────────────────────────────────────────────────────╯\n")

	if state.totalFiles > 0 {
		fmt.Fprintf(&sb, "  Total: %d file(s) loaded (%s)\n", state.totalFiles, system.FormatFileSize(state.totalSize))
	} else {
		sb.WriteString("  No memory files loaded. Create with /init\n")
	}

	sb.WriteString("\n  Tip: Use @path/to/file.md in memory files to import other files.\n")

	return sb.String(), nil
}

func (s *memoryListState) writeMemorySection(sb *strings.Builder, label string, mainPaths []string, rulesDir, createHint string, isProject bool) {
	mainFound := system.ExistingMemoryFiles(mainPaths)
	rulesFiles := system.ListRulesFiles(rulesDir)

	if len(mainFound) > 0 || len(rulesFiles) > 0 {
		sb.WriteString(memoryFormatBoxLine(fmt.Sprintf(" ● %s", label)))
		for _, mf := range mainFound {
			s.writeMemoryFileLine(sb, mf, isProject)
		}
		for _, rf := range rulesFiles {
			s.writeMemoryFileLine(sb, rf, isProject)
		}
	} else {
		sb.WriteString(memoryFormatBoxLine(fmt.Sprintf(" ○ %s (not found)", label)))
		sb.WriteString(memoryFormatBoxLine(fmt.Sprintf("   Create: %s", createHint)))
	}
	sb.WriteString(memoryFormatBoxLine(""))
}

func (s *memoryListState) writeMemoryLocalSection(sb *strings.Builder, localPath string) {
	if _, err := os.Stat(localPath); err == nil {
		sb.WriteString(memoryFormatBoxLine(" ● Local (git-ignored)"))
		s.writeMemoryFileLine(sb, localPath, true)
	} else {
		sb.WriteString(memoryFormatBoxLine(" ○ Local (not found)"))
		sb.WriteString(memoryFormatBoxLine("   Create: /init local"))
	}
	sb.WriteString(memoryFormatBoxLine(""))
}

func (s *memoryListState) writeMemoryFileLine(sb *strings.Builder, path string, isProject bool) {
	size := system.GetFileSize(path)
	s.totalFiles++
	s.totalSize += size

	displayPath := memoryShortenPathForDisplay(path, s.cwd, isProject)
	displayPath = memoryTruncatePathKeepFilename(displayPath, memoryMaxPath)
	sizeStr := fmt.Sprintf("(%s)", system.FormatFileSize(size))
	sb.WriteString(memoryFormatBoxLine(fmt.Sprintf("   %s %s", memoryPadRight(displayPath, memoryMaxPath), sizeStr)))
}

func memoryFormatBoxLine(content string) string {
	// Display columns, not runes: a CJK rune occupies two, so a rune count
	// under-pads and pushes the closing border past the box corner.
	visibleLen := lipgloss.Width(content)
	padding := max(memoryBoxWidth-visibleLen-2, 0)
	return fmt.Sprintf("│ %s%s│\n", content, strings.Repeat(" ", padding))
}

func memoryShortenPathForDisplay(path, cwd string, isProject bool) string {
	if isProject {
		if rel, err := filepath.Rel(cwd, path); err == nil {
			return rel
		}
	}
	return kit.ShortenPath(path)
}

// memoryTruncatePathKeepFilename trims a path to maxLen display columns,
// spending the budget on the filename first. All measurements are columns, and
// every cut goes through kit.TruncateText so it lands on a rune boundary — a
// path like ~/项目/笔记/说明.md would otherwise be sliced mid-character.
func memoryTruncatePathKeepFilename(path string, maxLen int) string {
	if lipgloss.Width(path) <= maxLen {
		return path
	}

	base := filepath.Base(path)
	if lipgloss.Width(base) >= maxLen-3 {
		return kit.TruncateText(base, maxLen)
	}

	remaining := maxLen - lipgloss.Width(base) - 4
	if remaining > 0 {
		dir := kit.TruncateKeepEnd(filepath.Dir(path), remaining)
		return "..." + dir + "/" + base
	}
	return base
}

func memoryPadRight(s string, length int) string {
	if w := lipgloss.Width(s); w >= length {
		return kit.TruncateText(s, length)
	}
	return s + strings.Repeat(" ", length-lipgloss.Width(s))
}

// handleMemoryShow shows the current loaded memory content.
func handleMemoryShow(cwd string) (string, error) {
	files := system.LoadMemoryFiles(cwd)
	if len(files) == 0 {
		return "No memory files loaded.\n\nCreate project memory with: /init", nil
	}
	var parts []string
	for _, f := range files {
		parts = append(parts, f.Content)
	}
	content := strings.Join(parts, "\n\n")

	const maxShow = 2000
	if len(content) > maxShow {
		content = content[:maxShow] + "\n\n... (truncated)"
	}

	return fmt.Sprintf("Current Memory:\n\n%s", content), nil
}

// handleMemoryEdit resolves the file to edit for the given scope.
// Returns the file path to edit, or an empty string with a message if no file was found.
func handleMemoryEdit(cwd, scope string) (string, error) {
	paths := system.GetAllMemoryPaths(cwd)

	switch scope {
	case "global", "user":
		filePath, err := ensureMemoryFile(paths.Global, getMemoryGlobalTemplate())
		if err != nil {
			return "", err
		}
		return filePath, nil

	case "local":
		filePath, err := ensureMemoryFile(paths.Local, getMemoryLocalTemplate())
		if err != nil {
			return "", err
		}
		memoryAddToGitignore(system.ProjectRoot(cwd), system.LocalInstructionFile)
		return filePath, nil

	default:
		filePath := system.FindNearestMemoryFile(paths.Project)
		if filePath == "" {
			// Return empty path; caller should display the message.
			return "", nil
		}
		return filePath, nil
	}
}

// ensureMemoryFile returns filePath, creating it from template when absent.
func ensureMemoryFile(filePath, template string) (string, error) {
	if _, err := os.Stat(filePath); err == nil {
		return filePath, nil
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(filePath, []byte(template), 0o644); err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	return filePath, nil
}

func getMemoryProjectTemplate(root string) string {
	projectName := filepath.Base(root)
	return fmt.Sprintf(`# AGENTS.md

This file provides guidance to San and other coding agents working in this
repository.

## Project Overview

%s - Describe what this project does.

## Build & Run

`+"`"+`bash
# Add your build commands here
`+"`"+`

## Architecture

<!-- Key directories and their purpose -->

## Key Patterns

<!-- Important conventions to follow -->
`, projectName)
}

func getMemoryGlobalTemplate() string {
	return `# AGENTS.md

Global instructions for San (applies to all projects).

## Coding Preferences

<!-- Your preferred coding style -->

## Security

<!-- Security practices to follow -->
`
}

func getMemoryLocalTemplate() string {
	return `# AGENTS.local.md

Local instructions for this project (not committed to git).

Use this file for:
- Personal notes and reminders
- Environment-specific settings
- Credentials and secrets (keep these safe!)
- Work-in-progress ideas

## Notes

<!-- Your local notes here -->
`
}

func getMemoryRulesTemplate() string {
	return `# Example Rule

This file defines specific rules for San to follow.

## Guidelines

- Add specific guidelines here
- Each rule file should focus on one topic
- Rules are loaded alphabetically by filename

## Example

<!-- Remove this example and add your actual rules -->
`
}

// CreateMemoryFile creates a memory file if it doesn't exist.
func CreateMemoryFile(filePath, level, cwd string) error {
	template := getMemoryTemplateForLevel(level, cwd)
	if _, err := ensureMemoryFile(filePath, template); err != nil {
		return err
	}
	if level == "local" {
		memoryAddToGitignore(system.ProjectRoot(cwd), system.LocalInstructionFile)
	}
	return nil
}

// getMemoryTemplateForLevel returns the template content for a given memory level.
func getMemoryTemplateForLevel(level, cwd string) string {
	switch level {
	case "global":
		return getMemoryGlobalTemplate()
	case "project":
		return getMemoryProjectTemplate(system.ProjectRoot(cwd))
	case "local":
		return getMemoryLocalTemplate()
	default:
		return ""
	}
}

// FormatMemoryDisplayPath formats a memory file path for display.
func FormatMemoryDisplayPath(filePath, level, cwd string) string {
	if level == "project" || level == "local" {
		if rel, err := filepath.Rel(cwd, filePath); err == nil {
			return rel
		}
	}
	return kit.ShortenPath(filePath)
}
