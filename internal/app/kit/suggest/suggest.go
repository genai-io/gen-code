package suggest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

type Type int

const (
	typeCommand Type = iota
	TypeFile
)

type Suggestion struct {
	Name        string
	Description string
}

type Matcher func(query string) []Suggestion

func suggestionBoxStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(kit.CurrentTheme.Border).
		Padding(0, 1)
}

func selectedSuggestionStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(kit.CurrentTheme.TextBright).
		Bold(true)
}

func normalSuggestionStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(kit.CurrentTheme.Muted)
}

func commandNameStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(kit.CurrentTheme.Primary)
}

func commandDescStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(kit.CurrentTheme.Muted)
}

type fileSuggestion struct {
	Path        string
	DisplayName string
	IsDir       bool
}

type State struct {
	visible         bool
	suggestionType  Type
	suggestions     []Suggestion
	fileSuggestions []fileSuggestion
	selectedIdx     int
	viewStart       int
	cwd             string
	atQuery         string
	cmdMatcher      Matcher

	allFiles    []fileSuggestion
	allFilesCwd string
}

func NewState(matcher Matcher) State {
	return State{
		visible:    false,
		cmdMatcher: matcher,
	}
}

func (s *State) Reset() {
	s.visible = false
	s.suggestions = nil
	s.fileSuggestions = nil
	s.selectedIdx = 0
	s.viewStart = 0
	s.atQuery = ""
}

func (s *State) UpdateSuggestions(input string) {
	input = strings.TrimSpace(input)

	if atIdx := strings.LastIndex(input, "@"); atIdx >= 0 {
		query := input[atIdx+1:]
		if atIdx == len(input)-1 || !strings.ContainsAny(query, " \t\n") {
			if s.atQuery != query {
				s.selectedIdx = 0
				s.viewStart = 0
			}
			s.atQuery = query
			s.updatefileSuggestions(query)
			return
		}
	}

	if strings.HasPrefix(input, "/") {
		s.suggestionType = typeCommand
		s.suggestions = s.cmdMatcher(input)
		s.fileSuggestions = nil
		s.visible = len(s.suggestions) > 0
		s.atQuery = ""

		if s.selectedIdx >= len(s.suggestions) {
			s.selectedIdx = 0
		}
		return
	}

	s.visible = false
	s.suggestions = nil
	s.fileSuggestions = nil
	s.selectedIdx = 0
	s.atQuery = ""
}

const (
	fileScanMaxResults        = 500
	fileScanMaxDirsVisited    = 2000
	fileScanMaxDepth          = 6
	fileSuggestionViewSize    = 8
	commandSuggestionViewSize = 8
)

func (s *State) updatefileSuggestions(query string) {
	s.suggestionType = TypeFile
	s.suggestions = nil
	s.fileSuggestions = nil

	if s.cwd == "" {
		s.visible = false
		return
	}

	if s.allFilesCwd != s.cwd {
		s.allFiles = s.scanAllFiles()
		s.allFilesCwd = s.cwd
	}

	s.fileSuggestions = filterFiles(s.allFiles, query)
	s.sortSuggestions()

	s.visible = len(s.fileSuggestions) > 0
	if s.selectedIdx >= len(s.fileSuggestions) {
		s.selectedIdx = 0
	}
	s.clampViewStart()
}

func (s *State) scanAllFiles() []fileSuggestion {
	seen := make(map[string]bool)
	var results []fileSuggestion

	type queueItem struct {
		dir   string
		depth int
	}
	queue := []queueItem{{s.cwd, 0}}
	dirsVisited := 0

	// Load gitignore patterns for filtering
	gi := loadGitignore(s.cwd)

	for len(queue) > 0 && len(results) < fileScanMaxResults && dirsVisited < fileScanMaxDirsVisited {
		item := queue[0]
		queue = queue[1:]

		if item.depth > fileScanMaxDepth {
			continue
		}

		entries, err := os.ReadDir(item.dir)
		if err != nil {
			continue
		}
		dirsVisited++

		for _, entry := range entries {
			if len(results) >= fileScanMaxResults {
				break
			}

			name := entry.Name()
			fullPath := filepath.Join(item.dir, name)

			// Check gitignore before adding directories or files
			relPath, err := filepath.Rel(s.cwd, fullPath)
			if err == nil && gi != nil && gi.Matches(relPath, entry.IsDir()) {
				// If this is a directory that's gitignored, skip it entirely
				if entry.IsDir() {
					// Still descend into .san directory even if gitignored
					if name != ".san" {
						continue
					}
				} else {
					continue
				}
			}

			if entry.IsDir() {
				if !shouldSkipDirectory(name) && item.depth < fileScanMaxDepth {
					queue = append(queue, queueItem{fullPath, item.depth + 1})
				}
				continue
			}

			relPath, err = filepath.Rel(s.cwd, fullPath)
			if err != nil || seen[relPath] {
				continue
			}
			seen[relPath] = true

			results = append(results, fileSuggestion{
				Path:        relPath,
				DisplayName: relPath,
				IsDir:       false,
			})
		}
	}

	return results
}

func filterFiles(all []fileSuggestion, query string) []fileSuggestion {
	if query == "" {
		out := make([]fileSuggestion, len(all))
		copy(out, all)
		return out
	}
	queryLower := strings.ToLower(query)
	var filtered []fileSuggestion
	for _, f := range all {
		if kit.FuzzyMatch(strings.ToLower(f.Path), queryLower) {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

func (s *State) sortSuggestions() {
	sort.SliceStable(s.fileSuggestions, func(i, j int) bool {
		depthI := strings.Count(s.fileSuggestions[i].Path, "/")
		depthJ := strings.Count(s.fileSuggestions[j].Path, "/")
		if depthI != depthJ {
			return depthI < depthJ
		}
		return len(s.fileSuggestions[i].Path) < len(s.fileSuggestions[j].Path)
	})
}

func shouldSkipDirectory(name string) bool {
	if strings.HasPrefix(name, ".") && name != ".san" {
		return true
	}

	switch name {
	case "node_modules", "vendor", ".git", "__pycache__", "dist", "build",
		"target", "DerivedData", "Pods", "coverage":
		return true
	}
	return false
}

// --- .gitignore support ---

// gitignore holds compiled gitignore patterns for a directory.
type gitignore struct {
	patterns []gitignorePattern
}

type gitignorePattern struct {
	raw     string
	negate  bool
	dirOnly bool
	rooted  bool
	glob    string // the glob to match against
}

// loadGitignore reads and parses .gitignore from dir. Returns nil when
// the file doesn't exist or is empty.
func loadGitignore(dir string) *gitignore {
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	gi := &gitignore{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := gitignorePattern{raw: line}

		// Negation
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = line[1:]
		}

		// Trailing slash = directory-only
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		// Leading slash = anchored to root
		if strings.HasPrefix(line, "/") {
			p.rooted = true
			line = line[1:]
		}

		p.glob = line
		gi.patterns = append(gi.patterns, p)
	}
	if len(gi.patterns) == 0 {
		return nil
	}
	return gi
}

// Matches reports whether the given relative path (from repo root) matches
// any gitignore pattern. isDir indicates whether the path refers to a directory.
func (gi *gitignore) Matches(relPath string, isDir bool) bool {
	if gi == nil {
		return false
	}
	matched := false
	for _, p := range gi.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if matchGitignorePattern(relPath, p) {
			matched = !p.negate
		}
	}
	return matched
}

// matchGitignorePattern checks whether relPath matches a single gitignore pattern.
func matchGitignorePattern(relPath string, p gitignorePattern) bool {
	glob := p.glob

	// Convert ** to a temp placeholder for filepath.Match compatibility
	// ** matches everything including path separators
	if strings.Contains(glob, "**") {
		return matchDoubleStar(relPath, glob)
	}

	if p.rooted {
		// Anchored: match against the full relative path
		ok, _ := filepath.Match(glob, relPath)
		return ok
	}

	// Not anchored: match against any path component (basename or subpath)
	if !strings.Contains(glob, "/") {
		// No slash in pattern = match basename only
		base := filepath.Base(relPath)
		ok, _ := filepath.Match(glob, base)
		if ok {
			return true
		}
	}

	// Match against the full relative path
	ok, _ := filepath.Match(glob, relPath)
	if ok {
		return true
	}

	// Match against any subpath (e.g., pattern "foo/bar" matches "a/foo/bar")
	for p := relPath; p != "." && p != "/"; p = filepath.Dir(p) {
		parent := filepath.Dir(p)
		if parent == "." || parent == "/" {
			break
		}
		sub := strings.TrimPrefix(relPath, parent+string(filepath.Separator))
		if sub == relPath {
			continue
		}
		ok, _ := filepath.Match(glob, sub)
		if ok {
			return true
		}
	}

	return false
}

// matchDoubleStar handles ** globs by splitting on ** and matching segments.
func matchDoubleStar(relPath string, glob string) bool {
	parts := strings.SplitN(glob, "**", 2)
	// ** can appear at start, middle, or end
	if parts[0] != "" {
		// Prefix must match
		if !strings.HasPrefix(relPath, parts[0]) {
			return false
		}
		relPath = relPath[len(parts[0]):]
	}
	if len(parts) == 2 && parts[1] != "" {
		// Suffix must match
		suffix := strings.TrimPrefix(parts[1], "/")
		if !strings.HasSuffix(relPath, suffix) {
			return false
		}
	}
	return true
}

func (s *State) SetCwd(cwd string) {
	if s.cwd != cwd {
		s.cwd = cwd
		s.allFiles = nil
		s.allFilesCwd = ""
	}
}

func (s *State) MoveUp() {
	if s.selectedIdx > 0 {
		s.selectedIdx--
		s.clampViewStart()
	}
}

func (s *State) MoveDown() {
	maxIdx := s.maxSelectedIdx()
	if s.selectedIdx < maxIdx {
		s.selectedIdx++
		s.clampViewStart()
	}
}

func (s *State) MovePageUp() {
	pageSize := s.suggestionViewSize()
	s.selectedIdx = max(s.selectedIdx-pageSize, 0)
	s.clampViewStart()
}

func (s *State) MovePageDown() {
	pageSize := s.suggestionViewSize()
	maxIdx := s.maxSelectedIdx()
	s.selectedIdx += pageSize
	if s.selectedIdx > maxIdx {
		s.selectedIdx = maxIdx
	}
	if s.selectedIdx < 0 {
		s.selectedIdx = 0
	}
	s.clampViewStart()
}

func (s *State) MoveToTop() {
	s.selectedIdx = 0
	s.viewStart = 0
}

func (s *State) MoveToEnd() {
	s.selectedIdx = max(s.maxSelectedIdx(), 0)
	s.clampViewStart()
}

func (s *State) maxSelectedIdx() int {
	if s.suggestionType == TypeFile {
		return len(s.fileSuggestions) - 1
	}
	return len(s.suggestions) - 1
}

func (s *State) clampViewStart() {
	viewSize := s.suggestionViewSize()
	total := s.totalSuggestions()
	if total == 0 {
		s.viewStart = 0
		return
	}
	maxStart := max(total-viewSize, 0)
	if s.viewStart > maxStart {
		s.viewStart = maxStart
	}
	if s.viewStart < 0 {
		s.viewStart = 0
	}
	if s.selectedIdx < s.viewStart {
		s.viewStart = s.selectedIdx
	} else if s.selectedIdx >= s.viewStart+viewSize {
		s.viewStart = s.selectedIdx - viewSize + 1
	}
}

func (s *State) suggestionViewSize() int {
	if s.suggestionType == TypeFile {
		return fileSuggestionViewSize
	}
	return commandSuggestionViewSize
}

func (s *State) totalSuggestions() int {
	if s.suggestionType == TypeFile {
		return len(s.fileSuggestions)
	}
	return len(s.suggestions)
}

func (s *State) GetSelected() string {
	if !s.visible {
		return ""
	}

	if s.suggestionType == TypeFile {
		if len(s.fileSuggestions) == 0 || s.selectedIdx >= len(s.fileSuggestions) {
			return ""
		}
		return s.fileSuggestions[s.selectedIdx].Path
	}

	if len(s.suggestions) == 0 || s.selectedIdx >= len(s.suggestions) {
		return ""
	}
	return "/" + s.suggestions[s.selectedIdx].Name
}

func (s *State) GetSuggestionType() Type {
	return s.suggestionType
}

func (s *State) Hide() {
	s.visible = false
}

func (s *State) IsVisible() bool {
	if s.suggestionType == TypeFile {
		return s.visible && len(s.fileSuggestions) > 0
	}
	return s.visible && len(s.suggestions) > 0
}

func (s *State) Render(width int) string {
	if !s.IsVisible() {
		return ""
	}
	if s.suggestionType == TypeFile {
		return s.renderfileSuggestions(width)
	}
	return s.renderCommandSuggestions(width)
}

func (s *State) renderfileSuggestions(width int) string {
	total := len(s.fileSuggestions)
	viewSize := min(fileSuggestionViewSize, total)

	start := s.viewStart
	if start+viewSize > total {
		start = total - viewSize
	}
	if start < 0 {
		start = 0
	}
	end := min(start+viewSize, total)
	items := s.fileSuggestions[start:end]

	boxWidth := clampInt(width*60/100, 40, 60)

	var lines []string
	headerStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Bold(true)
	header := "Import file"
	if total > viewSize {
		header = fmt.Sprintf("Import file  %d/%d", s.selectedIdx+1, total)
	}
	lines = append(lines, headerStyle.Render(header), "")

	maxPathLen := boxWidth - 8
	for i, file := range items {
		suffix := ""
		if file.IsDir {
			suffix = "/"
		}
		displayPath := truncateFromLeft(file.DisplayName, maxPathLen) + suffix

		if start+i == s.selectedIdx {
			bar := kit.FocusBarStyle().Render(kit.FocusBar)
			lines = append(lines, bar+" "+selectedSuggestionStyle().Render(displayPath))
		} else {
			lines = append(lines, "  "+normalSuggestionStyle().Render(displayPath))
		}
	}

	hint := "Tab/Enter to select · Esc to cancel"
	if total > viewSize {
		hint = "↑/↓ scroll · Tab/Enter · Esc"
	}
	lines = append(lines, "", commandDescStyle().Render(hint))

	content := strings.Join(lines, "\n")
	return suggestionBoxStyle().Width(boxWidth).Render(content)
}

func (s *State) renderCommandSuggestions(width int) string {
	total := len(s.suggestions)
	viewSize := min(commandSuggestionViewSize, total)

	start := s.viewStart
	if start+viewSize > total {
		start = total - viewSize
	}
	if start < 0 {
		start = 0
	}
	end := min(start+viewSize, total)
	items := s.suggestions[start:end]

	boxWidth := max(width-2, 40)
	contentWidth := max(boxWidth-2, 20)

	var lines []string
	headerStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Bold(true)
	header := "Commands"
	if total > viewSize {
		header = fmt.Sprintf("Commands  %d/%d", s.selectedIdx+1, total)
	}
	lines = append(lines, headerStyle.Render(header), "")

	// Align descriptions into a column: pad every command name to the widest
	// one visible, then a 2-space gutter, so the descriptions read as a list.
	nameWidth := 0
	for _, cmd := range items {
		if w := lipgloss.Width(cmd.Name) + 1; w > nameWidth { // +1 for the leading "/"
			nameWidth = w
		}
	}
	// Budget: 2 (bar/indent prefix) + nameWidth + 2 (gutter) + desc, with 2
	// cols of right margin so the focused row never wraps.
	maxDescLen := max(contentWidth-nameWidth-6, 10)
	for i, cmd := range items {
		cmdName := "/" + cmd.Name
		pad := strings.Repeat(" ", max(0, nameWidth-lipgloss.Width(cmdName)))
		desc := kit.TruncateText(cmd.Description, maxDescLen)

		if start+i == s.selectedIdx {
			bar := kit.FocusBarStyle().Render(kit.FocusBar)
			lines = append(lines, bar+" "+selectedSuggestionStyle().Render(cmdName+pad+"  "+desc))
		} else {
			row := commandNameStyle().Render(cmdName) + commandDescStyle().Render(pad+"  "+desc)
			lines = append(lines, "  "+row)
		}
	}

	hint := "Tab/Enter to select · Esc to cancel"
	if total > viewSize {
		hint = "↑/↓ scroll · Tab/Enter · Esc"
	}
	lines = append(lines, "", commandDescStyle().Render(hint))

	content := strings.Join(lines, "\n")
	return suggestionBoxStyle().Width(boxWidth).Render(content)
}

func clampInt(value, minVal, maxVal int) int {
	return max(minVal, min(value, maxVal))
}

func truncateFromLeft(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[len(runes)-maxLen:])
	}
	return "…" + string(runes[len(runes)-maxLen+1:])
}
