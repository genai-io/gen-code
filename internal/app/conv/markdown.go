// Markdown renderer using glamour for styled terminal output.
// Tables are rendered separately with lipgloss/table for full border control,
// since glamour hardcodes outer borders off (ansi/table.go setBorders).
package conv

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/kit"
)

// trimStyledBlankLines drops leading and trailing lines that are blank once
// ANSI styling is removed. glamour pads a block with full-width margin lines
// of styled spaces — plain strings.TrimSpace can't see them because they start
// with a color escape — and committed block-by-block those margins would stack
// into extra vertical gaps between rendered blocks.
func trimStyledBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(xansi.Strip(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(xansi.Strip(lines[end-1])) == "" {
		end--
	}
	if start == 0 && end == len(lines) {
		return s
	}
	return strings.Join(lines[start:end], "\n")
}

// MDRenderer renders markdown content to styled terminal output using
// glamour. Safe for use from a single goroutine only — the TUI's
// rendering path is single-threaded (bubbletea Update/View). The mutex
// only protects the dark/light rebuild path in case a future caller
// reuses the renderer from a background goroutine.
type MDRenderer struct {
	mu       sync.Mutex
	renderer *glamour.TermRenderer
	// blockQuoteRenderer renders blockquote inner content: dimmed, and narrowed by the
	// "│ " prefix so glamour wraps to a width that leaves room for it. We render
	// blockquotes ourselves (see renderBlockQuote) because glamour's native
	// blockquote wrapping drops the │ prefix on CJK continuation lines.
	blockQuoteRenderer *glamour.TermRenderer
	width              int
	darkBg             bool // tracks last known terminal background to detect theme changes
	// cache memoizes Render output keyed by its raw input. The active tail is
	// re-rendered on every frame — every spinner tick, for the whole duration a
	// tool runs — while its content sits unchanged, so without this the full
	// glamour + chroma pipeline re-runs on byte-identical input several times a
	// second. Width is fixed per renderer (a resize builds a fresh MDRenderer via
	// ResizeMDRenderer) and buildRenderers drops the cache on a theme change, so
	// the raw content alone is a sufficient key.
	cache map[string]string
	// agentChild caches the narrower renderer for subagent/task bodies. Reuse
	// preserves its render cache across repaints; rebuild on resize or theme change.
	agentChild *MDRenderer
}

// mdCacheMax bounds the render cache. The live working set (the active tail's
// blocks) is tiny; the cap only guards against unbounded growth from the many
// one-shot renders a long session accumulates (each block is rendered once when
// committed to scrollback). On overflow the whole map is dropped — visible
// content simply re-fills it on the next frame.
const mdCacheMax = 256

// blockQuoteIndentWidth is the visible column cost of the "│ " blockquote prefix.
const blockQuoteIndentWidth = 2

// NewMDRenderer creates a new markdown renderer with the given terminal width.
// The width passed should be the raw terminal column count; the renderer
// subtracts aiIndentWidth internally so glamour wraps exactly at the
// visible boundary after the "● " prompt icon + indent are applied.
func NewMDRenderer(width int) *MDRenderer {
	r := &MDRenderer{width: max(width-4, minWrapWidth)}
	r.buildRenderers(kit.IsDarkBackground())
	return r
}

// buildGlamourRenderer constructs a glamour TermRenderer for the given width and
// background. A non-nil documentColor overrides the document text color — used for
// blockquote inner content, which glamour renders in the muted tone.
func buildGlamourRenderer(width int, dark bool, documentColor *string) *glamour.TermRenderer {
	var style ansi.StyleConfig
	if dark {
		style = styles.DarkStyleConfig
	} else {
		style = styles.LightStyleConfig
	}
	customizeStyle(&style, width)
	if documentColor != nil {
		style.Document.Color = documentColor
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
		glamour.WithChromaFormatter("terminal256"),
	)
	if err != nil {
		fallback := styles.DarkStyle
		if !dark {
			fallback = styles.LightStyle
		}
		r, _ = glamour.NewTermRenderer(glamour.WithStandardStyle(fallback))
	}
	return r
}

// rebuildIfNeeded recreates the glamour renderers when the terminal background changes.
func (r *MDRenderer) rebuildIfNeeded() {
	if dark := kit.IsDarkBackground(); dark != r.darkBg {
		r.buildRenderers(dark)
	}
}

// buildRenderers builds the main and blockquote renderers for the current width
// and background. The blockquote renderer is narrowed by the "│ " prefix width and
// dimmed to the muted tone glamour gives blockquote bodies.
func (r *MDRenderer) buildRenderers(dark bool) {
	textDim := adaptiveColorHex(kit.CurrentTheme.TextDim)
	r.renderer = buildGlamourRenderer(r.width, dark, nil)
	r.blockQuoteRenderer = buildGlamourRenderer(max(r.width-blockQuoteIndentWidth, minWrapWidth), dark, &textDim)
	r.darkBg = dark
	// Rendered output is tied to this width/background; drop any stale entries.
	r.cache = make(map[string]string)
	// Theme changes invalidate the child renderer.
	r.agentChild = nil
}

// agentBodyRenderer returns the cached renderer for indented subagent/task bodies.
func (r *MDRenderer) agentBodyRenderer() *MDRenderer {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuildIfNeeded()
	if r.agentChild == nil {
		r.agentChild = NewMDRenderer(r.width - len(agentContentIndent))
	}
	return r.agentChild
}

// Render parses markdown source and returns styled terminal output.
// Tables are extracted and rendered with lipgloss/table for full border control,
// everything else (including code blocks) goes through glamour natively.
func (r *MDRenderer) Render(content string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuildIfNeeded()
	if cached, ok := r.cache[content]; ok {
		return cached, nil
	}
	key := content
	// Normalize paragraph line breaks: LLMs often hard-wrap at ~80 columns,
	// producing softbreaks that glamour preserves as newlines. Joining them
	// lets glamour re-wrap at the actual terminal width.
	content = normalizeLineBreaks(content)
	segments := splitBlocks(content)

	var parts []string
	for _, seg := range segments {
		switch seg.kind {
		case segTable:
			parts = append(parts, r.renderTable(seg.content))
		case segBlockQuote:
			parts = append(parts, r.renderBlockQuote(seg.content))
		case segList:
			parts = append(parts, r.renderList(seg.content))
		default:
			rendered, err := r.renderer.Render(seg.content)
			if err != nil {
				parts = append(parts, seg.content)
			} else {
				rendered = collapseBlankLines(rendered)
				parts = append(parts, strings.TrimRight(rendered, "\n"))
			}
		}
	}

	result := strings.Join(parts, "")
	// With more than one segment, a table/blockquote/list contributes its own
	// blank-line padding, which can stack against an adjacent block's — collapse
	// across the seams. A single segment is already collapsed per-segment above.
	if len(parts) > 1 {
		result = collapseBlankLines(result)
	}
	out := strings.Trim(result, "\n")
	if len(r.cache) >= mdCacheMax {
		clear(r.cache)
	}
	r.cache[key] = out
	return out, nil
}

// segmentKind identifies what type of markdown block a segment contains.
type segmentKind int

const (
	segPlain segmentKind = iota
	segTable
	segBlockQuote
	segList
)

// listLevelIndent is the column indent glamour applies per list-nesting level;
// we match it so our lists line up with the rest of the rendered markdown.
const listLevelIndent = 2

// segment represents a piece of markdown content.
type segment struct {
	content string
	kind    segmentKind
}

// splitBlocks splits markdown content into segments that need bespoke rendering
// — tables (lipgloss/table for full borders), blockquotes and lists (a CJK-safe
// prefix / hanging indent, see renderBlockQuote and renderList) — and plain
// segments for everything else, which glamour renders natively. Fenced code
// blocks are passed through whole, so a "- " or "1." line inside a code sample
// is never mistaken for a real list or quote.
func splitBlocks(content string) []segment {
	lines := strings.Split(content, "\n")
	var segments []segment
	var plain []string
	flushPlain := func() {
		if len(plain) > 0 {
			segments = append(segments, segment{content: strings.Join(plain, "\n"), kind: segPlain})
			plain = nil
		}
	}

	i := 0
	for i < len(lines) {
		if ch, _ := fenceMarker(lines[i]); ch != 0 {
			fenceEnd := findFenceEnd(lines, i)
			plain = append(plain, lines[i:fenceEnd]...)
			i = fenceEnd
			continue
		}
		if isTableLine(lines[i]) {
			tableEnd := findTableEnd(lines, i)
			if tableEnd > i+1 && hasTableSeparator(lines, i, tableEnd) {
				flushPlain()
				tableLines := strings.Join(lines[i:tableEnd], "\n")
				segments = append(segments, segment{content: tableLines, kind: segTable})
				i = tableEnd
				continue
			}
		}
		// We only take a quote/list away from glamour when it contains CJK text
		// glamour's ASCII-only wrap would mangle. Everything else — and anything
		// our simpler renderer can't lay out (a table embedded in a list) — stays
		// with glamour, which handles structure, numbering and nesting faithfully.
		// A marker indented 4+ spaces is an indented code block, not a real block.
		if isBlockQuoteLine(lines[i]) && leadingSpaces(lines[i]) < codeBlockIndent {
			block := lines[i:findBlockQuoteEnd(lines, i)]
			i += len(block)
			if containsCJK(block) {
				flushPlain()
				segments = append(segments, segment{content: strings.Join(block, "\n"), kind: segBlockQuote})
			} else {
				plain = append(plain, block...)
			}
			continue
		}
		if indent, _, _, _, ok := parseListMarker(lines[i]); ok && indent < codeBlockIndent {
			block := lines[i:findListEnd(lines, i)]
			i += len(block)
			if containsCJK(block) && !containsTableRow(block) {
				flushPlain()
				segments = append(segments, segment{content: strings.Join(block, "\n"), kind: segList})
			} else {
				plain = append(plain, block...)
			}
			continue
		}
		plain = append(plain, lines[i])
		i++
	}

	flushPlain()
	return segments
}

// isBlockQuoteLine reports whether a line opens or continues a blockquote (a
// leading ">", optionally indented). Matches only lines that start the marker,
// so lazy-continuation lines are left to the plain path — LLMs prefix every
// blockquote line with ">" in practice.
func isBlockQuoteLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == ">" || strings.HasPrefix(t, "> ")
}

// findBlockQuoteEnd returns the end index (exclusive) of the consecutive run of
// blockquote lines starting at start.
func findBlockQuoteEnd(lines []string, start int) int {
	i := start
	for i < len(lines) && isBlockQuoteLine(lines[i]) {
		i++
	}
	return i
}

// codeBlockIndent is the leading-space count at which a line becomes an indented
// code block (CommonMark): a list/quote marker indented this far is code, not a
// real block.
const codeBlockIndent = 4

// leadingSpaces counts the leading spaces on a line.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// containsCJK reports whether any line holds a CJK character — the case glamour's
// ASCII-only wrap can't break, and the only reason we render a block ourselves.
func containsCJK(lines []string) bool {
	for _, line := range lines {
		if strings.ContainsFunc(line, isCJK) {
			return true
		}
	}
	return false
}

// containsTableRow reports whether any line is a table row, marking a list whose
// item embeds a table — structure our list renderer can't lay out, so glamour
// takes it instead.
func containsTableRow(lines []string) bool {
	return slices.ContainsFunc(lines, isTableLine)
}

// fenceMarker returns the fence character ('`' or '~') and its run length if the
// line opens a fenced code block (an info string may follow), else (0, 0).
func fenceMarker(line string) (ch byte, length int) {
	t := strings.TrimSpace(line)
	if len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return 0, 0
	}
	ch = t[0]
	for length < len(t) && t[length] == ch {
		length++
	}
	if length < 3 {
		return 0, 0
	}
	return ch, length
}

// findFenceEnd returns the index just past the closing fence of the code block
// opened at start. A closing fence must use the same character as the opener and
// be at least as long, so a "~~~" line inside a "```" block doesn't end it early.
// An unterminated fence runs to the end of the input.
func findFenceEnd(lines []string, start int) int {
	ch, length := fenceMarker(lines[start])
	for i := start + 1; i < len(lines); i++ {
		if closesFence(lines[i], ch, length) {
			return i + 1
		}
	}
	return len(lines)
}

// closesFence reports whether line closes a fence opened with the given character
// and length: nothing but a run of that character, at least as long (a closing
// fence carries no info string).
func closesFence(line string, ch byte, length int) bool {
	t := strings.TrimSpace(line)
	if len(t) < length {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != ch {
			return false
		}
	}
	return true
}

// renderBlockQuote renders a blockquote with a themed "│ " prefix on every
// wrapped line. glamour's native blockquote wrapping loses the prefix on CJK
// continuation lines: its ansi.Wrap breakpoints are ASCII-only, so an
// unbreakable CJK run overflows the indent width and the margin writer drops the
// token. Rendering the inner markdown ourselves (dimmed, narrowed by the prefix)
// and prefixing each line keeps the block intact.
func (r *MDRenderer) renderBlockQuote(content string) string {
	inner := stripBlockQuoteMarkers(content)
	rendered, err := r.blockQuoteRenderer.Render(inner)
	if err != nil {
		rendered = inner
	} else {
		rendered = collapseBlankLines(rendered)
	}
	rendered = strings.Trim(rendered, "\n")

	prefix := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Render("│ ")
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	// Blank lines above and below match glamour's native block spacing so the
	// quote stays visually separated from adjacent paragraphs; leading/trailing
	// blanks are trimmed by the Render join when the quote sits at an edge.
	return "\n\n" + strings.Join(lines, "\n") + "\n\n"
}

// stripBlockQuoteMarkers removes one leading ">" (and its following space) from
// each blockquote line, yielding the inner markdown to render.
func stripBlockQuoteMarkers(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		t := strings.TrimPrefix(strings.TrimSpace(line), ">")
		lines[i] = strings.TrimPrefix(t, " ")
	}
	return strings.Join(lines, "\n")
}

// listItem is one parsed list entry: its nesting depth, whether its parent list
// is ordered, its 1-based number within that list, and its inline text.
type listItem struct {
	depth   int
	ordered bool
	number  int
	content string
}

// parseListMarker splits a line into its leading indent and, if it opens a list
// item, the item's inline text. It recognizes "-", "*", "+" bullets and
// "N." / "N)" ordered markers, each followed by a space. ok is false for any
// other line (blank lines, continuations, non-list text).
func parseListMarker(line string) (indent int, ordered bool, number int, content string, ok bool) {
	body := strings.TrimLeft(line, " ")
	indent = len(line) - len(body)

	if len(body) >= 2 && (body[0] == '-' || body[0] == '*' || body[0] == '+') && body[1] == ' ' {
		return indent, false, 0, strings.TrimSpace(body[2:]), true
	}

	digits := 0
	for digits < len(body) && body[digits] >= '0' && body[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(body) && (body[digits] == '.' || body[digits] == ')') && body[digits+1] == ' ' {
		num, _ := strconv.Atoi(body[:digits])
		return indent, true, num, strings.TrimSpace(body[digits+2:]), true
	}
	return 0, false, 0, "", false
}

// isListLine reports whether a line opens a list item.
func isListLine(line string) bool {
	_, _, _, _, ok := parseListMarker(line)
	return ok
}

// isListContinuation reports whether a line continues the previous list item's
// text — an indented, non-blank line that isn't itself a new marker (e.g. the
// second line of "- first\n  more").
func isListContinuation(line string) bool {
	return strings.TrimSpace(line) != "" && line != strings.TrimLeft(line, " \t") && !isListLine(line)
}

// findListEnd returns the end index (exclusive) of the list block starting at
// start: a run of item lines and their indented continuation lines, tolerating a
// single blank line within the list (a "loose" list) but stopping at the first
// non-indented, non-item line.
func findListEnd(lines []string, start int) int {
	i := start
	for i < len(lines) {
		switch {
		case isListLine(lines[i]), isListContinuation(lines[i]):
			i++
		case strings.TrimSpace(lines[i]) == "" && i+1 < len(lines) &&
			(isListLine(lines[i+1]) || isListContinuation(lines[i+1])):
			i++
		default:
			return i
		}
	}
	return i
}

// listLevel tracks one active nesting level while parsing: the source indent that
// opened it, the marker type of its current run, the display number its first
// item carried, and how many items that run has seen.
type listLevel struct {
	indent  int
	ordered bool
	start   int
	count   int
}

// parseListItems turns a list block into items, assigning each a nesting depth
// (from its source indent) and a display number. Numbering is anchored at each
// run's first item, so an intentional start (5., 6.) is preserved while a
// repeated 1./1./1. is repaired — matching how glamour renumbers from Start.
func parseListItems(content string) []listItem {
	var items []listItem
	var stack []listLevel // one entry per active nesting level, outermost first
	for line := range strings.SplitSeq(content, "\n") {
		indent, ordered, number, text, ok := parseListMarker(line)
		if !ok {
			mergeContinuation(items, line)
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].indent > indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 || stack[len(stack)-1].indent < indent {
			stack = append(stack, listLevel{indent: indent})
		}
		lvl := &stack[len(stack)-1]
		if lvl.count == 0 || lvl.ordered != ordered {
			// First item at this level, or the marker type flipped (bullets ↔
			// numbers, i.e. a new list): anchor numbering at this item's number.
			*lvl = listLevel{indent: lvl.indent, ordered: ordered, start: number}
		}
		lvl.count++
		items = append(items, listItem{depth: len(stack) - 1, ordered: ordered, number: lvl.start + lvl.count - 1, content: text})
	}
	return items
}

// mergeContinuation appends a continuation line's text to the last parsed item, so
// "- first\n  more" renders as one wrapped item. Joined without a space between
// CJK runs, matching normalizeLineBreaks.
func mergeContinuation(items []listItem, line string) {
	text := strings.TrimSpace(line)
	if text == "" || len(items) == 0 {
		return
	}
	last := &items[len(items)-1]
	sep := " "
	if endsWithCJK(last.content) || startsWithCJK(text) {
		sep = ""
	}
	last.content += sep + text
}

// listMarker returns the marker to print for an item and the item's remaining
// content. Ordered items use "N. "; task items ("[ ] " / "[x] ") keep their
// checkbox — glamour rendered these specially, so we preserve them rather than
// prefixing a redundant bullet; everything else gets the "• " bullet.
func listMarker(it listItem) (marker, content string) {
	switch {
	case it.ordered:
		return strconv.Itoa(it.number) + ". ", it.content
	case strings.HasPrefix(it.content, "[ ] "):
		return "[ ] ", it.content[len("[ ] "):]
	case strings.HasPrefix(it.content, "[x] "), strings.HasPrefix(it.content, "[X] "):
		return "[✓] ", it.content[len("[x] "):]
	default:
		return "• ", it.content
	}
}

// renderList renders a list with a hanging indent so wrapped continuation lines
// align under the item text instead of falling back to column 0. glamour's
// native list wrapping drops the hanging indent on continuation lines (and, like
// blockquotes, its ASCII-only wrap breakpoints can't split CJK runs), so we lay
// the items out ourselves: marker on the first line, matching indent on the rest.
//
// Unlike renderBlockQuote — which narrows glamour and re-prefixes each line — a
// list can't reuse that trick: every item has its own marker width, so there's
// no single prefix to hang. We wrap each item's inline content directly instead.
func (r *MDRenderer) renderList(content string) string {
	items := parseListItems(content)
	if len(items) == 0 {
		return content
	}

	var lines []string
	for _, it := range items {
		indent := strings.Repeat(" ", it.depth*listLevelIndent)
		marker, itemText := listMarker(it)
		markerWidth := lipgloss.Width(marker)
		hang := strings.Repeat(" ", markerWidth)

		contentWidth := max(r.width-len(indent)-markerWidth, minWrapWidth/2)
		styled := renderInlineMarkdown(itemText)
		wrapped := strings.Split(xansi.Wrap(styled, contentWidth, " ,.;-+|"), "\n")

		for i, line := range wrapped {
			if i == 0 {
				lines = append(lines, indent+marker+line)
			} else {
				lines = append(lines, indent+hang+line)
			}
		}
	}

	// Blank lines above and below match glamour's native block spacing; edge
	// blanks are trimmed by the Render join.
	return "\n\n" + strings.Join(lines, "\n") + "\n\n"
}

// isTableLine checks if a line looks like a markdown table line (starts with |).
func isTableLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.Contains(trimmed[1:], "|")
}

// findTableEnd finds the end index (exclusive) of consecutive table lines.
func findTableEnd(lines []string, start int) int {
	i := start
	for i < len(lines) && isTableLine(lines[i]) {
		i++
	}
	return i
}

// hasTableSeparator checks if there's a separator line (|---|) in the range.
func hasTableSeparator(lines []string, start, end int) bool {
	for i := start; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		// A separator line contains |, -, and optionally : for alignment
		cleaned := strings.NewReplacer("|", "", "-", "", ":", "", " ", "").Replace(trimmed)
		if cleaned == "" && strings.Contains(trimmed, "-") {
			return true
		}
	}
	return false
}

// renderTable renders a markdown table using lipgloss/table with full borders.
func (r *MDRenderer) renderTable(content string) string {
	headers, rows := parseMarkdownTable(content)
	if len(headers) == 0 {
		return content
	}

	borderColor := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Separator)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(kit.CurrentTheme.TextBright)

	t := table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderColor).
		BorderTop(true).
		BorderBottom(true).
		BorderLeft(true).
		BorderRight(true).
		BorderHeader(true).
		BorderColumn(true).
		BorderRow(true).
		Width(r.width).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextBright)
		})

	return "\n" + t.String() + "\n"
}

// parseMarkdownTable extracts headers and rows from a markdown table string.
func parseMarkdownTable(content string) ([]string, [][]string) {
	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		return nil, nil
	}

	var headers []string
	var rows [][]string
	headerParsed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Check if this is a separator line (|---|---|)
		cleaned := strings.NewReplacer("|", "", "-", "", ":", "", " ", "").Replace(trimmed)
		if cleaned == "" && strings.Contains(trimmed, "-") {
			headerParsed = true
			continue
		}

		cells := parseTableRow(trimmed)
		if !headerParsed {
			headers = cells
		} else {
			rows = append(rows, cells)
		}
	}

	return headers, rows
}

// parseTableRow splits a markdown table row into cells and renders inline markdown.
func parseTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")

	parts := strings.Split(trimmed, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = renderInlineMarkdown(strings.TrimSpace(p))
	}
	return cells
}

// renderInlineMarkdown renders inline markdown elements: `code`, **bold**, *italic*, [text](url).
func renderInlineMarkdown(text string) string {
	var result strings.Builder
	i := 0
	for i < len(text) {
		// Inline code: `...`
		if text[i] == '`' {
			end := strings.Index(text[i+1:], "`")
			if end != -1 {
				code := text[i+1 : i+1+end]
				codeStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Accent)
				result.WriteString(codeStyle.Render(code))
				i += end + 2
				continue
			}
		}
		// Link: [text](url)
		if text[i] == '[' {
			closeBracket := strings.Index(text[i+1:], "](")
			if closeBracket != -1 {
				linkText := text[i+1 : i+1+closeBracket]
				urlStart := i + 1 + closeBracket + 2
				closeParen := strings.Index(text[urlStart:], ")")
				if closeParen != -1 {
					url := text[urlStart : urlStart+closeParen]
					linkStyle := lipgloss.NewStyle().
						Foreground(kit.CurrentTheme.Primary).
						Underline(true)
					styled := linkStyle.Render(linkText)
					result.WriteString("\x1b]8;;" + url + "\x1b\\" + styled + "\x1b]8;;\x1b\\")
					i = urlStart + closeParen + 1
					continue
				}
			}
		}
		// Bold: **...**
		if i+1 < len(text) && text[i] == '*' && text[i+1] == '*' {
			end := strings.Index(text[i+2:], "**")
			if end != -1 {
				bold := text[i+2 : i+2+end]
				boldStyle := lipgloss.NewStyle().Bold(true)
				result.WriteString(boldStyle.Render(bold))
				i += end + 4
				continue
			}
		}
		// Italic: *...*
		if text[i] == '*' {
			end := strings.Index(text[i+1:], "*")
			if end != -1 {
				italic := text[i+1 : i+1+end]
				italicStyle := lipgloss.NewStyle().Italic(true)
				result.WriteString(italicStyle.Render(italic))
				i += end + 2
				continue
			}
		}
		result.WriteByte(text[i])
		i++
	}
	return result.String()
}

// adaptiveColorHex resolves an AdaptiveColor to its hex string based on the
// current terminal background. Used for glamour StyleConfig which requires *string.
func adaptiveColorHex(c kit.AdaptiveColor) string {
	if kit.IsDarkBackground() {
		return c.Dark
	}
	return c.Light
}

// customizeStyle adjusts glamour's default style for a clean, unified look.
func customizeStyle(s *ansi.StyleConfig, width int) {
	blue := adaptiveColorHex(kit.CurrentTheme.Primary)
	text := adaptiveColorHex(kit.CurrentTheme.Text)
	textDim := adaptiveColorHex(kit.CurrentTheme.TextDim)

	// Document: set foreground color, no margin (paragraph spacing handled by glamour block prefix/suffix)
	margin := uint(0)
	s.Document.Margin = &margin
	s.Document.Color = &text
	s.Document.BlockPrefix = ""
	s.Document.BlockSuffix = ""

	// Headings: themed blue, bold, no extra prefix/suffix markers
	s.H1.Prefix = ""
	s.H1.Suffix = ""
	s.H1.Color = &blue
	s.H1.BackgroundColor = nil
	s.H1.Bold = boolPtr(true)
	s.H2.Prefix = ""
	s.H2.Color = &blue
	s.H2.Bold = boolPtr(true)
	s.H3.Prefix = ""
	s.H3.Color = &blue
	s.H3.Bold = boolPtr(true)
	s.Heading.BlockSuffix = "\n"
	s.H4.Prefix = ""
	s.H5.Prefix = ""
	s.H6.Prefix = ""

	// BlockQuote: muted color with standard │ indent token
	s.BlockQuote.Color = &textDim
	s.BlockQuote.Indent = uintPtr(1)
	s.BlockQuote.IndentToken = stringPtr("│ ")

	// Horizontal rule: full-width thin line. Kept on the faint Border tone (not
	// muted) so a section divider recedes instead of competing with the prose —
	// lighter on the light theme, subtler on the dark one.
	border := adaptiveColorHex(kit.CurrentTheme.Border)
	hr := strings.Repeat("─", width)
	s.HorizontalRule.Format = "\n" + hr + "\n"
	s.HorizontalRule.Color = &border

	// Inline code: no background, accent color
	accent := adaptiveColorHex(kit.CurrentTheme.Accent)
	s.Code.BackgroundColor = nil
	s.Code.Prefix = ""
	s.Code.Suffix = ""
	s.Code.Color = &accent

	// Code blocks: remove Chroma background color for cleaner look
	if s.CodeBlock.Chroma != nil {
		s.CodeBlock.Chroma.Background = ansi.StylePrimitive{}
		s.CodeBlock.Chroma.Error = ansi.StylePrimitive{}
	}
}

func boolPtr(b bool) *bool { return &b }

var reTripleNewlines = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string {
	return reTripleNewlines.ReplaceAllString(s, "\n\n")
}
func uintPtr(u uint) *uint       { return &u }
func stringPtr(s string) *string { return &s }

// CompletedBlockBoundary returns the byte offset in content up to which the
// text forms complete markdown blocks that are safe to render and commit to
// scrollback. Content after the boundary is the still-streaming block and must
// stay in the live view until more arrives. Two things close a block: a blank
// line outside a fenced code block (it terminates the preceding blocks), and a
// closing code fence (the block is complete the moment it closes). The trailing
// block is never included — the turn-end commit flushes whatever remains.
func CompletedBlockBoundary(content string) int {
	boundary := 0
	offset := 0
	var fenceCh byte // the open fence's character while inside a code block, else 0
	var fenceLen int
	for _, line := range strings.SplitAfter(content, "\n") {
		offset += len(line)
		// A line without a trailing newline is the last, still-streaming line:
		// never a boundary, whatever it contains.
		if !strings.HasSuffix(line, "\n") {
			continue
		}
		if fenceCh != 0 {
			// Inside a code block: it completes only on a fence matching the
			// opener's character and length (see fenceMarker / closesFence).
			if closesFence(line, fenceCh, fenceLen) {
				fenceCh, fenceLen = 0, 0
				boundary = offset
			}
			continue
		}
		if ch, n := fenceMarker(line); ch != 0 {
			fenceCh, fenceLen = ch, n
			continue
		}
		if strings.TrimSpace(line) == "" {
			boundary = offset // a blank line terminates the preceding blocks
		}
	}
	return boundary
}

// normalizeLineBreaks joins single-newline breaks within plain paragraphs so
// that glamour's word-wrap can reflow text to the terminal width. Structural
// markdown lines (headers, lists, blockquotes, code blocks, tables) and blank
// lines (paragraph separators) are preserved as-is.
func normalizeLineBreaks(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	var fenceCh byte // the open fence's character while inside a code block, else 0
	var fenceLen int

	for i, line := range lines {
		// Inside a fenced code block: keep every line verbatim. The block ends only
		// on a fence matching the opener's character and length, so a "~~~" inside a
		// "```" block stays code (see fenceMarker / closesFence).
		if fenceCh != 0 {
			result = append(result, line)
			if closesFence(line, fenceCh, fenceLen) {
				fenceCh, fenceLen = 0, 0
			}
			continue
		}
		if ch, n := fenceMarker(line); ch != 0 {
			fenceCh, fenceLen = ch, n
			result = append(result, line)
			continue
		}

		trimmed := strings.TrimSpace(line)

		// Blank line = paragraph separator
		if trimmed == "" {
			result = append(result, line)
			continue
		}

		// Indented code block (4+ spaces or tab)
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			result = append(result, line)
			continue
		}

		// Structural markdown lines: preserve as-is
		if isMarkdownStructural(trimmed) {
			result = append(result, line)
			continue
		}

		// Try to join with the previous line if it was a plain paragraph line
		if i > 0 && len(result) > 0 {
			prev := result[len(result)-1]
			prevTrimmed := strings.TrimSpace(prev)
			if prevTrimmed != "" &&
				!strings.HasPrefix(prevTrimmed, "```") && !strings.HasPrefix(prevTrimmed, "~~~") &&
				!strings.HasPrefix(prev, "    ") && !strings.HasPrefix(prev, "\t") &&
				!isMarkdownStructural(prevTrimmed) &&
				!strings.HasSuffix(prev, "  ") {
				// Don't insert a space between CJK lines — Chinese/Japanese/Korean
				// text doesn't use spaces between words.
				sep := " "
				if endsWithCJK(prevTrimmed) || startsWithCJK(trimmed) {
					sep = ""
				}
				result[len(result)-1] = prev + sep + trimmed
				continue
			}
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

// isMarkdownStructural returns true for lines that start markdown block structures
// (headers, list items, blockquotes, table rows, thematic breaks).
func isMarkdownStructural(line string) bool {
	// Headers
	if strings.HasPrefix(line, "#") {
		return true
	}
	// Blockquotes
	if strings.HasPrefix(line, "> ") || line == ">" {
		return true
	}
	// Table rows
	if strings.HasPrefix(line, "|") {
		return true
	}
	// Thematic breaks (---, ***, ___)
	if isThematicBreak(line) {
		return true
	}
	// Ordered and unordered list items — same grammar splitBlocks/renderList use,
	// so normalizeLineBreaks and block extraction always agree on what's a list.
	return isListLine(line)
}

// isThematicBreak checks if line is a markdown thematic break (---, ***, ___).
func isThematicBreak(line string) bool {
	cleaned := strings.ReplaceAll(line, " ", "")
	if len(cleaned) < 3 {
		return false
	}
	return strings.Count(cleaned, "-") == len(cleaned) ||
		strings.Count(cleaned, "*") == len(cleaned) ||
		strings.Count(cleaned, "_") == len(cleaned)
}

// isCJK reports whether r is a CJK (Chinese/Japanese/Korean) character.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Halfwidth/Fullwidth Forms
}

// endsWithCJK reports whether s ends with a CJK character.
func endsWithCJK(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r != utf8.RuneError && isCJK(r)
}

// startsWithCJK reports whether s starts with a CJK character.
func startsWithCJK(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r != utf8.RuneError && isCJK(r)
}
