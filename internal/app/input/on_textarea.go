package input

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit/history"
	"github.com/genai-io/san/internal/app/kit/suggest"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/image"
)

const (
	minTextareaHeight    = 1
	defaultMaxHeight     = 10
	fixedChromeLines     = 6 // separators(2) + status(1) + prompt overhead(2) + image/warning(1)
	maxHeightScreenRatio = 2 // use up to 1/2 of terminal height
)

// imageRefPattern matches @path/to/image.ext references (case-insensitive extension).
var imageRefPattern = regexp.MustCompile(`(?i)@([^\s]+\.(png|jpg|jpeg|gif|webp))`)

// bareImagePathRe matches standalone image file paths (absolute or relative with a
// directory separator) that are NOT prefixed with @. This catches drag-drop paste
// of an image path into the terminal. Only paths containing a separator (/ or \)
// are matched to avoid treating bare filenames or coincidental text as images.
var bareImagePathRe = regexp.MustCompile(`(?i)((?:/[^\s]+|[^\s]+[/\\][^\s]*)\.(png|jpg|jpeg|gif|webp))`)

// ImageTokenMatch describes an inline image token found in the textarea value.
type ImageTokenMatch struct {
	PendingIdx int
	ID         int
	Label      string
	Start      int
	End        int
}

// maxTextareaHeight returns the dynamic max height based on terminal size.
func (m *Model) maxTextareaHeight() int {
	if m.terminalHeight <= 0 {
		return defaultMaxHeight
	}
	dynMax := m.terminalHeight/maxHeightScreenRatio - fixedChromeLines
	if dynMax < defaultMaxHeight {
		return defaultMaxHeight
	}
	return dynMax
}

// SetTerminalHeight records the terminal size and re-caps the input box, which
// is allowed half the screen. Growing and shrinking to fit the content is the
// textarea's own job (DynamicHeight); this only moves its ceiling.
func (m *Model) SetTerminalHeight(height int) {
	m.terminalHeight = height
	m.Textarea.MaxHeight = m.maxTextareaHeight()
}

// imageLabel returns the display label for a pending image token.
func imageLabel(id int) string {
	return fmt.Sprintf("[Image #%d]", id)
}

// AddPendingImage appends a new inline image token and returns its label.
func (m *Model) AddPendingImage(img core.Image) string {
	m.Images.NextID++
	m.Images.Pending = append(m.Images.Pending, PendingImage{
		ID:   m.Images.NextID,
		Data: img,
	})
	return imageLabel(m.Images.NextID)
}

// ClearImages resets all inline image state.
func (m *Model) ClearImages() {
	m.Images.Pending = nil
	m.Images.NextID = 0
	m.Images.Selection = ImageSelection{}
}

// CursorIndex returns the absolute rune cursor position in the textarea value.
func (m *Model) CursorIndex() int {
	lines := strings.Split(m.Textarea.Value(), "\n")
	row := m.Textarea.Line()
	if row >= len(lines) {
		row = len(lines) - 1
	}
	if row < 0 {
		return 0
	}

	col := m.Textarea.LineInfo().StartColumn + m.Textarea.LineInfo().ColumnOffset
	idx := 0
	for i := 0; i < row; i++ {
		idx += len([]rune(lines[i])) + 1
	}
	return idx + col
}

// SetCursorIndex moves the cursor to the absolute rune position by replaying
// horizontal cursor movement from the current position.
func (m *Model) SetCursorIndex(target int) {
	valueLen := len([]rune(m.Textarea.Value()))
	target = max(0, min(target, valueLen))
	current := m.CursorIndex()
	if target == current {
		return
	}

	code := tea.KeyRight
	steps := target - current
	if target < current {
		code = tea.KeyLeft
		steps = current - target
	}

	for i := 0; i < steps; i++ {
		m.stepCursor(code)
	}
}

// PendingImageMatches returns inline image token matches in display order.
func (m *Model) PendingImageMatches() []ImageTokenMatch {
	return m.PendingImageMatchesIn(m.Textarea.Value())
}

// PendingImageMatchesIn returns inline image token matches for the provided
// buffer rather than the live textarea contents.
func (m *Model) PendingImageMatchesIn(value string) []ImageTokenMatch {
	valueRunes := []rune(value)
	matches := make([]ImageTokenMatch, 0, len(m.Images.Pending))

	for idx, pending := range m.Images.Pending {
		label := imageLabel(pending.ID)
		start := indexRunes(valueRunes, label, 0)
		if start < 0 {
			continue
		}
		matches = append(matches, ImageTokenMatch{
			PendingIdx: idx,
			ID:         pending.ID,
			Label:      label,
			Start:      start,
			End:        start + len([]rune(label)),
		})
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Start < matches[j].Start
	})
	return matches
}

// SelectedImageMatch returns the selected inline image token, if any.
func (m *Model) SelectedImageMatch() (ImageTokenMatch, bool) {
	if !m.Images.Selection.Active {
		return ImageTokenMatch{}, false
	}
	for _, match := range m.PendingImageMatches() {
		if match.PendingIdx == m.Images.Selection.PendingIdx {
			return match, true
		}
	}
	return ImageTokenMatch{}, false
}

// MatchAdjacentToCursor returns the inline image token adjacent to the cursor.
func (m *Model) MatchAdjacentToCursor(cursor int, wantStart bool) (ImageTokenMatch, bool) {
	for _, match := range m.PendingImageMatches() {
		if wantStart && cursor == match.Start {
			return match, true
		}
		if !wantStart && cursor == match.End {
			return match, true
		}
	}
	return ImageTokenMatch{}, false
}

// RemoveImageToken removes the inline token from the textarea and pending list.
func (m *Model) RemoveImageToken(match ImageTokenMatch, cursor int) {
	valueRunes := []rune(m.Textarea.Value())
	nextValue := string(valueRunes[:match.Start]) + string(valueRunes[match.End:])
	m.Textarea.SetValue(nextValue)
	m.Images.RemoveAt(match.PendingIdx)
	m.Images.Selection = ImageSelection{}
	m.SetCursorIndex(cursor)
}

// ExtractInlineImages removes inline image tokens from content and returns the
// ordered images based on their appearance in the text.
func (m *Model) ExtractInlineImages(input string) (string, []core.Image) {
	matches := m.PendingImageMatchesIn(input)
	if len(matches) == 0 {
		return strings.TrimSpace(input), nil
	}

	var images []core.Image
	valueRunes := []rune(input)
	var sb strings.Builder
	last := 0
	for _, match := range matches {
		if match.Start > len(valueRunes) || match.End > len(valueRunes) || match.PendingIdx >= len(m.Images.Pending) {
			continue
		}
		sb.WriteString(string(valueRunes[last:match.Start]))
		images = append(images, m.Images.Pending[match.PendingIdx].Data)
		last = match.End
	}
	sb.WriteString(string(valueRunes[last:]))
	return strings.TrimSpace(sb.String()), images
}

func (m *Model) stepCursor(code rune) {
	var cmd tea.Cmd
	m.Textarea, cmd = m.Textarea.Update(tea.KeyPressMsg{Code: code})
	_ = cmd
}

func indexRunes(haystack []rune, needle string, start int) int {
	s := string(haystack)
	if start > 0 {
		// Convert rune offset to byte offset
		byteStart := len(string(haystack[:start]))
		idx := strings.Index(s[byteStart:], needle)
		if idx < 0 {
			return -1
		}
		// Convert byte position back to rune offset
		return start + len([]rune(s[byteStart:byteStart+idx]))
	}
	idx := strings.Index(s, needle)
	if idx < 0 {
		return -1
	}
	return len([]rune(s[:idx]))
}

// HistoryUp navigates to the previous history entry.
func (m *Model) HistoryUp() {
	if len(m.History.Items) == 0 {
		return
	}
	if m.History.Index == -1 {
		m.History.Stashed = m.Textarea.Value()
		m.History.Index = len(m.History.Items) - 1
	} else if m.History.Index > 0 {
		m.History.Index--
	}
	m.Textarea.SetValue(m.History.Items[m.History.Index])
	m.Textarea.CursorEnd()
}

// HistoryDown navigates to the next history entry.
func (m *Model) HistoryDown() {
	if m.History.Index == -1 {
		return
	}
	if m.History.Index < len(m.History.Items)-1 {
		m.History.Index++
		m.Textarea.SetValue(m.History.Items[m.History.Index])
	} else {
		m.History.Index = -1
		m.Textarea.SetValue(m.History.Stashed)
	}
	m.Textarea.CursorEnd()
}

// LeadingImagePath returns the first whitespace-delimited token of input when
// that token is a path to an existing image file (absolute, or relative to
// cwd), and "" otherwise. It is used to keep a leading image path in a prompt
// from being misread as a slash command — /home/user/photo.png is a file, not
// a command — and to decide whether that leading path should be consumed as an
// image reference rather than kept as text.
func LeadingImagePath(cwd, input string) string {
	trimmed := strings.TrimSpace(input)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	// Only tokens that look like an image path (never a bare filename), and
	// only when the whole token is the path — trailing punctuation like a
	// comma must disqualify it.
	m := bareImagePathRe.FindStringSubmatch(first)
	if m == nil || m[1] != first {
		return ""
	}
	absPath := first
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(cwd, absPath)
	}
	if _, err := os.Stat(absPath); err != nil {
		return ""
	}
	return first
}

// ProcessImageRefs extracts @image.png references and bare image file paths
// from input. Returns the cleaned text content and any loaded images.
// Only processes references where the file actually exists on disk;
// non-existent file references are left in the text as-is.
//
// @-prefixed references are removed from the content after loading, and a
// failed load aborts the entire send — both are right because the user
// deliberately typed @. Bare paths (drag-drop, absolute paths) are treated
// more leniently: the text stays in the message, and a failed load is
// silently skipped so a mere mention of a path doesn't block the send.
// One exception: when the bare image path is the first token of the prompt
// (an "upload first" paste), it is consumed as an image reference and removed
// from the text, mirroring @-prefixed behavior — otherwise the leading path
// would reach the agent as text that reads like an unknown command.
//
// An error still comes with usable content: the text and images returned are
// what survived the failure. A caller with somewhere to hand the turn back to
// can ignore both and abort; one that has to send something anyway sends what
// resolved instead of the raw text.
func ProcessImageRefs(cwd, input string) (string, []core.Image, error) {
	content := input
	var images []core.Image

	// Step 1: Process @-prefixed image references — remove from text,
	// abort on load failure.
	matches := imageRefPattern.FindAllStringSubmatch(content, -1)
	var loadedRefs []string
	for _, match := range matches {
		path := match[1]
		absPath := path
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(cwd, absPath)
		}
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			continue
		}
		img, err := image.Load(absPath)
		if err != nil {
			return strings.TrimSpace(stripRefs(content, loadedRefs)), images, fmt.Errorf("loading image %s: %w", absPath, err)
		}
		images = append(images, img)
		loadedRefs = append(loadedRefs, match[0])
	}
	content = stripRefs(content, loadedRefs)

	// Step 2: Process bare image file paths (drag-drop, absolute paths, etc.)
	// Keep the text in the message, skip silently on load failure, unless
	// the path is the first token — then it gets consumed as an image reference
	// (mirroring @-prefixed behavior) with a notice on load failure.
	bareMatches := bareImagePathRe.FindAllStringSubmatch(content, -1)
	leadPath := LeadingImagePath(cwd, content)
	leadingConsumed := false
	for _, match := range bareMatches {
		path := match[1]
		absPath := path
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(cwd, absPath)
		}
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			continue
		}
		img, err := image.Load(absPath)
		if err != nil {
			if !leadingConsumed && leadPath == path {
				// The leading image path exists (stat passed) but failed to load
				// (e.g. oversized or corrupt). Consume it from the text anyway so
				// the agent doesn't see the bare path as an unknown command, and
				// hand the caller the surviving text alongside the error.
				return stripLeadingPath(content, leadPath), images, fmt.Errorf("loading image %s: %w", absPath, err)
			}
			continue
		}
		images = append(images, img)
		if !leadingConsumed && leadPath == path {
			leadingConsumed = true
		}
	}
	if leadingConsumed {
		// The image came first in the prompt: consume the leading path as an
		// image reference so the agent receives the image plus the rest of the
		// prompt, not a leading path that reads like an unknown command.
		content = stripLeadingPath(content, leadPath)
	}

	return strings.TrimSpace(content), images, nil
}

// stripLeadingPath drops the leading image-path token from a prompt, leaving
// the rest of it.
func stripLeadingPath(content, leadPath string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), leadPath))
}

// stripRefs drops the @-references that were consumed as images, so the text a
// caller gets back — on the error path too — is the text that survived.
func stripRefs(content string, refs []string) string {
	for _, ref := range refs {
		content = strings.ReplaceAll(content, ref, "")
	}
	return content
}

// PastePlaceholder returns the placeholder text displayed in the textarea for a pasted chunk.
func PastePlaceholder(index, lineCount int) string {
	return fmt.Sprintf("[Pasted text #%d +%d lines]", index, lineCount)
}

// FullValue returns the textarea value with paste placeholders expanded to the original pasted text.
func (m *Model) FullValue() string {
	val := m.Textarea.Value()
	for i, chunk := range m.PastedChunks {
		placeholder := PastePlaceholder(i+1, chunk.LineCount)
		val = strings.Replace(val, placeholder, chunk.Text, 1)
	}
	return val
}

// ClearPaste resets the pasted chunks state.
func (m *Model) ClearPaste() {
	m.PastedChunks = nil
}

func (m *Model) Reset() {
	m.Textarea.Reset() // shrinks back to MinHeight on its own
	m.ClearPaste()
	m.ClearImages()
	m.Queue.ResetSelection()
}

func (m *Model) HandleCwdChange(newCwd string) {
	m.Suggestions.SetCwd(newCwd)
	if m.Suggestions.GetSuggestionType() == suggest.TypeFile {
		m.Suggestions.Hide()
	}
}

func (m *Model) RecordSubmission(cwd, input string) {
	if input == "" {
		return
	}
	m.History.Items = append(m.History.Items, input)
	m.History.Index = -1
	m.History.Stashed = ""
	history.Save(cwd, m.History.Items)
}

func (m *Model) RestoreImages(images []core.Image) {
	m.Images.Pending = nil
	m.Images.Selection = ImageSelection{}
	for i, img := range images {
		id := m.Images.NextID + i + 1
		m.Images.Pending = append(m.Images.Pending, PendingImage{ID: id, Data: img})
	}
	m.Images.NextID += len(images)
}

func (m *Model) HasContent() bool {
	return strings.TrimSpace(m.Textarea.Value()) != "" || len(m.Images.Pending) > 0
}

func (m *Model) PendingImages() []core.Image {
	images := make([]core.Image, len(m.Images.Pending))
	for i, p := range m.Images.Pending {
		images[i] = p.Data
	}
	return images
}

// HandleTextareaUpdate forwards a message to the textarea and applies user-input
// side effects such as paste placeholder expansion, height updates, and suggestions.
// It returns the resulting tea.Cmd and whether the textarea value changed.
func (m *Model) HandleTextareaUpdate(msg tea.Msg) (tea.Cmd, bool) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	_, isPaste := msg.(tea.PasteMsg)

	prevValue := m.Textarea.Value()
	m.Textarea, cmd = m.Textarea.Update(msg)
	cmds = append(cmds, cmd)

	if isPaste {
		newValue := m.Textarea.Value()
		pastedText := ExtractPastedText(prevValue, newValue)
		lines := strings.Split(pastedText, "\n")
		if len(lines) > 1 {
			chunk := PastedChunk{
				Text:      pastedText,
				LineCount: len(lines),
			}
			m.PastedChunks = append(m.PastedChunks, chunk)
			placeholder := PastePlaceholder(len(m.PastedChunks), chunk.LineCount)
			m.Textarea.SetValue(prevValue)
			m.Textarea.CursorEnd()
			m.Textarea.InsertString(placeholder)
		} else {
			trimmed := strings.TrimSpace(newValue)
			if trimmed != newValue {
				m.Textarea.SetValue(trimmed)
				m.Textarea.CursorEnd()
			}
		}
	}

	changed := m.Textarea.Value() != prevValue
	if changed {
		m.Suggestions.UpdateSuggestions(m.Textarea.Value())
	}

	return tea.Batch(cmds...), changed
}

// ExtractPastedText derives the pasted content by comparing the textarea
// value before and after the paste event.
func ExtractPastedText(prevValue, newValue string) string {
	if strings.HasPrefix(newValue, prevValue) {
		return strings.TrimSpace(newValue[len(prevValue):])
	}
	return strings.TrimSpace(newValue)
}

// HandleSuggestionKey handles keys while the autocomplete suggestion list is visible.
// Returns (cmd, true) if the key was consumed, (nil, false) otherwise.
func (m *Model) HandleSuggestionKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if !m.Suggestions.IsVisible() {
		return nil, false
	}
	switch msg.String() {
	case "up", "ctrl+p":
		m.Suggestions.MoveUp()
		return nil, true
	case "down", "ctrl+n":
		m.Suggestions.MoveDown()
		return nil, true
	case "pgup":
		m.Suggestions.MovePageUp()
		return nil, true
	case "pgdown":
		m.Suggestions.MovePageDown()
		return nil, true
	case "home":
		m.Suggestions.MoveToTop()
		return nil, true
	case "end":
		m.Suggestions.MoveToEnd()
		return nil, true
	case "tab", "enter":
		if selected := m.Suggestions.GetSelected(); selected != "" {
			if m.Suggestions.GetSuggestionType() == suggest.TypeFile {
				currentValue := m.Textarea.Value()
				if atIdx := strings.LastIndex(currentValue, "@"); atIdx >= 0 {
					newValue := currentValue[:atIdx] + "@" + selected
					m.Textarea.SetValue(newValue)
					m.Textarea.CursorEnd()
				}
			} else {
				m.Textarea.SetValue(selected + " ")
				m.Textarea.CursorEnd()
			}
			m.Suggestions.Hide()
		}
		return nil, true
	case "esc":
		m.Suggestions.Hide()
		return nil, true
	}
	return nil, false
}
