package conv

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// stripANSI removes ANSI escape sequences (CSI and OSC 8 hyperlinks) from a string.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\]8;;[^\x1b]*\x1b\\`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func TestMDRenderer_AgentBodyRendererReused(t *testing.T) {
	r := NewMDRenderer(80)

	// The child is cached across renders.
	first := r.agentBodyRenderer()
	second := r.agentBodyRenderer()
	if first != second {
		t.Fatalf("agentBodyRenderer built a fresh renderer instead of reusing the cached one")
	}

	// The child width accounts for the indent.
	want := NewMDRenderer(r.width - len(agentContentIndent)).width
	if first.width != want {
		t.Errorf("child width = %d, want %d", first.width, want)
	}

	// Output is unchanged versus a fresh narrow renderer.
	const src = "# Title\n\nBody **text** with a `span`."
	got, err := first.Render(src)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	fresh, err := NewMDRenderer(r.width - len(agentContentIndent)).Render(src)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if got != fresh {
		t.Errorf("cached child output differs from a fresh narrow renderer")
	}
}

func TestMDRenderer_Heading(t *testing.T) {
	r := NewMDRenderer(80)

	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{"h1", "# Hello", "Hello"},
		{"h2", "## World", "World"},
		{"h3", "### Details", "Details"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := r.Render(tt.input)
			if err != nil {
				t.Fatalf("Render error: %v", err)
			}
			if !strings.Contains(out, tt.contains) {
				t.Errorf("output %q should contain %q", out, tt.contains)
			}
		})
	}
}

func TestMDRenderer_Emphasis(t *testing.T) {
	r := NewMDRenderer(80)

	t.Run("bold", func(t *testing.T) {
		out, err := r.Render("**bold text**")
		if err != nil {
			t.Fatalf("Render error: %v", err)
		}
		plain := stripANSI(out)
		if !strings.Contains(plain, "bold text") {
			t.Errorf("output should contain 'bold text', got:\n%s", plain)
		}
	})

	t.Run("italic", func(t *testing.T) {
		out, err := r.Render("*italic text*")
		if err != nil {
			t.Fatalf("Render error: %v", err)
		}
		plain := stripANSI(out)
		if !strings.Contains(plain, "italic text") {
			t.Errorf("output should contain 'italic text', got:\n%s", plain)
		}
	})
}

func TestMDRenderer_CodeSpan(t *testing.T) {
	r := NewMDRenderer(80)

	out, err := r.Render("Use `fmt.Println` here")
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if !strings.Contains(out, "fmt.Println") {
		t.Errorf("output %q should contain 'fmt.Println'", out)
	}
}

func TestMDRenderer_FencedCodeBlock(t *testing.T) {
	r := NewMDRenderer(80)

	input := "```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "func main()") {
		t.Errorf("output should contain 'func main()', got:\n%s", plain)
	}
	// Code block should be padded for visual distinction
	for line := range strings.SplitSeq(plain, "\n") {
		if strings.Contains(line, "func") {
			if !strings.HasPrefix(line, " ") {
				t.Errorf("code line should be padded: %q", line)
			}
			break
		}
	}
}

func TestMDRenderer_UnorderedList(t *testing.T) {
	r := NewMDRenderer(80)

	input := "- item one\n- item two\n- item three"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "item one") {
		t.Errorf("output should contain 'item one', got:\n%s", plain)
	}
	if !strings.Contains(plain, "item two") {
		t.Errorf("output should contain 'item two', got:\n%s", plain)
	}
	if !strings.Contains(plain, "•") {
		t.Errorf("output should contain bullet character '•'")
	}
}

func TestMDRenderer_OrderedList(t *testing.T) {
	r := NewMDRenderer(80)

	input := "1. first\n2. second\n3. third"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "first") {
		t.Errorf("output should contain 'first', got:\n%s", plain)
	}
	if !strings.Contains(plain, "1.") {
		t.Errorf("output should contain '1.'")
	}
}

// A "- " or "1." line inside a code block — fenced, tilde-fenced, or 4-space
// indented — is a code sample, not a list. splitBlocks must pass it through to
// glamour instead of extracting it.
func TestMDRenderer_CodeBlockNotList(t *testing.T) {
	tests := map[string]string{
		"fenced":            "before\n\n```\n- not a list\n1. also not\n> not a quote\n```\n\nafter",
		"indented":          "before\n\n    - not a list\n    1. also not\n\nafter",
		"tilde in backtick": "```\ncode\n~~~\n- not a list\n```\n\nafter",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := NewMDRenderer(80).Render(input)
			if err != nil {
				t.Fatalf("Render error: %v", err)
			}
			plain := stripANSI(out)
			if strings.Contains(plain, "•") {
				t.Errorf("code content must not become a bullet list, got:\n%s", plain)
			}
			if !strings.Contains(plain, "not a list") {
				t.Errorf("code sample should render verbatim, got:\n%s", plain)
			}
		})
	}
}

// A list whose item embeds a table must not be folded into one raw bullet line
// (our list renderer can't lay out a table). It goes to glamour whole, which
// keeps the rows on separate lines.
func TestMDRenderer_TableInListNotFolded(t *testing.T) {
	out, err := NewMDRenderer(80).Render("* | 组件 | 类比 |\n  |---|---|\n  | kube-apiserver | 前台 |\n  | etcd | 账本 |")
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	for line := range strings.SplitSeq(stripANSI(out), "\n") {
		if strings.Contains(line, "kube-apiserver") && strings.Contains(line, "etcd") {
			t.Errorf("table rows were folded onto one line:\n%s", stripANSI(out))
		}
	}
}

// A "~~~" line inside a "```" block must not end it, so its lines stay verbatim
// code rather than being reflowed into a paragraph.
func TestNormalizeLineBreaks_MismatchedFence(t *testing.T) {
	src := "```\n~~~\nline one\nline two\n```"
	if got := normalizeLineBreaks(src); got != src {
		t.Errorf("code block must be preserved verbatim:\n got %q\nwant %q", got, src)
	}
}

// In a CJK list (which we render ourselves), an indented continuation line folds
// into its item rather than becoming a bare paragraph at column 0. ASCII lists go
// to glamour, whose own continuation handling we don't second-guess.
func TestMDRenderer_ListContinuation(t *testing.T) {
	out, err := NewMDRenderer(80).Render("- 第一项内容\n  这是续行文字\n- 第二项内容")
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	for line := range strings.SplitSeq(out, "\n") {
		plain := strings.TrimRight(stripANSI(line), " ")
		if plain == "" {
			continue
		}
		// Every rendered line belongs to an item: it starts with a bullet or the
		// hanging indent — never bare continuation text at column 0.
		if !strings.HasPrefix(plain, "•") && !strings.HasPrefix(plain, " ") {
			t.Errorf("continuation should stay within its item, got bare line %q in:\n%s", plain, stripANSI(out))
		}
	}
	if !strings.Contains(strings.ReplaceAll(stripANSI(out), " ", ""), "续行文字") {
		t.Errorf("continuation text should survive, got:\n%s", stripANSI(out))
	}
}

// Ordered lists anchor on the first item's number: an intentional start (5., 6.)
// is preserved, while a repeated 1./1./1. is repaired to 1., 2., 3.
func TestMDRenderer_OrderedListNumbering(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"preserves start", "5. five\n6. six\n7. seven", []string{"5. five", "6. six", "7. seven"}},
		{"repairs repeated", "1. a\n1. b\n1. c", []string{"1. a", "2. b", "3. c"}},
		// Skips are normalized to sequential, matching glamour/CommonMark, which
		// renumber from the first item's number and disregard the rest.
		{"normalizes skips", "5. five\n7. seven\n9. nine", []string{"5. five", "6. seven", "7. nine"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := NewMDRenderer(80).Render(tt.input)
			if err != nil {
				t.Fatalf("Render error: %v", err)
			}
			plain := stripANSI(out)
			for _, want := range tt.want {
				if !strings.Contains(plain, want) {
					t.Errorf("want %q in output, got:\n%s", want, plain)
				}
			}
		})
	}
}

func TestMDRenderer_Link(t *testing.T) {
	r := NewMDRenderer(80)

	input := "Visit [Go](https://golang.org) for info"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "Go") {
		t.Errorf("output should contain link text 'Go', got:\n%s", plain)
	}
}

func TestMDRenderer_ThematicBreak(t *testing.T) {
	r := NewMDRenderer(80)

	input := "above\n\n---\n\nbelow"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "above") || !strings.Contains(plain, "below") {
		t.Errorf("output should contain text above and below the rule, got:\n%s", plain)
	}
	if !strings.Contains(plain, "---") && !strings.Contains(plain, "─") {
		t.Errorf("output should contain horizontal rule, got:\n%s", plain)
	}
}

func TestMDRenderer_Blockquote(t *testing.T) {
	r := NewMDRenderer(80)

	input := "> This is a quote"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "This is a quote") {
		t.Errorf("output should contain quote text, got:\n%s", plain)
	}
}

func TestMDRenderer_Paragraph(t *testing.T) {
	r := NewMDRenderer(80)

	input := "Hello world, this is a test paragraph."
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if !strings.Contains(out, "Hello world") {
		t.Errorf("output should contain paragraph text")
	}
}

func TestMDRenderer_WordWrap(t *testing.T) {
	r := NewMDRenderer(40) // narrow width

	input := "This is a long paragraph that should wrap at the specified width boundary for proper terminal display."
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Errorf("expected word wrap to produce multiple lines, got %d", len(lines))
	}
}

func TestMDRenderer_MixedContent(t *testing.T) {
	r := NewMDRenderer(80)

	input := `# Title

Some **bold** and *italic* text with ` + "`code`" + `.

- item 1
- item 2

` + "```go\nfmt.Println(\"hi\")\n```"

	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	plain := stripANSI(out)
	checks := []string{"Title", "bold", "italic", "code", "item 1", "item 2", "Println"}
	for _, check := range checks {
		if !strings.Contains(plain, check) {
			t.Errorf("mixed content output should contain %q, got:\n%s", check, plain)
		}
	}
}

func Test_renderMarkdownContent(t *testing.T) {
	r := NewMDRenderer(80)

	result := renderMarkdownContent(r, "# Hello\n\nWorld")
	if !strings.Contains(result, "Hello") {
		t.Errorf("result should contain 'Hello'")
	}
	if !strings.Contains(result, "World") {
		t.Errorf("result should contain 'World'")
	}
	// Should be trimmed
	if strings.HasPrefix(result, "\n") || strings.HasSuffix(result, "\n") {
		t.Errorf("result should be trimmed, got: %q", result)
	}
}

func TestNormalizeLineBreaks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "joins paragraph lines",
			input: "This is line one.\nThis is line two.\nThis is line three.",
			want:  "This is line one. This is line two. This is line three.",
		},
		{
			name:  "preserves paragraph breaks",
			input: "Paragraph one.\n\nParagraph two.",
			want:  "Paragraph one.\n\nParagraph two.",
		},
		{
			name:  "preserves headers",
			input: "# Header\nSome text.\nMore text.",
			want:  "# Header\nSome text. More text.",
		},
		{
			name:  "preserves list items",
			input: "- item one\n- item two\n- item three",
			want:  "- item one\n- item two\n- item three",
		},
		{
			name:  "preserves ordered list",
			input: "1. first\n2. second\n3. third",
			want:  "1. first\n2. second\n3. third",
		},
		{
			name:  "preserves code blocks",
			input: "```go\nfunc main() {\n  fmt.Println(\"hello\")\n}\n```",
			want:  "```go\nfunc main() {\n  fmt.Println(\"hello\")\n}\n```",
		},
		{
			name:  "preserves blockquotes",
			input: "> quote line one\n> quote line two",
			want:  "> quote line one\n> quote line two",
		},
		{
			name:  "preserves hard breaks (trailing spaces)",
			input: "line one  \nline two",
			want:  "line one  \nline two",
		},
		{
			name:  "preserves indented code blocks",
			input: "    code line 1\n    code line 2",
			want:  "    code line 1\n    code line 2",
		},
		{
			name:  "LLM-style wrapped paragraph",
			input: "This is a long description that the LLM wrapped at 80\ncolumns, but the terminal is much wider so it should\nbe reflowed.",
			want:  "This is a long description that the LLM wrapped at 80 columns, but the terminal is much wider so it should be reflowed.",
		},
		{
			name:  "mixed content",
			input: "# Title\n\nFirst paragraph that wraps\nat 80 columns.\n\n- list item\n- another item\n\nAnother paragraph\nthat also wraps.",
			want:  "# Title\n\nFirst paragraph that wraps at 80 columns.\n\n- list item\n- another item\n\nAnother paragraph that also wraps.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeLineBreaks(tt.input)
			if got != tt.want {
				t.Errorf("normalizeLineBreaks()\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestMDRenderer_Table(t *testing.T) {
	r := NewMDRenderer(80)

	input := "| Name | Value |\n|------|-------|\n| foo  | bar   |\n| baz  | qux   |"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)

	// Should contain table content
	if !strings.Contains(plain, "Name") {
		t.Errorf("table should contain 'Name', got:\n%s", plain)
	}
	if !strings.Contains(plain, "foo") {
		t.Errorf("table should contain 'foo', got:\n%s", plain)
	}
	// Should have internal separators
	if !strings.Contains(plain, "│") {
		t.Errorf("table should have column separators │, got:\n%s", plain)
	}
	if !strings.Contains(plain, "─") {
		t.Errorf("table should have row separators ─, got:\n%s", plain)
	}
}

func TestMDRenderer_TableWithLinks(t *testing.T) {
	r := NewMDRenderer(80)

	input := "| Name | Link |\n|------|------|\n| Go | [Go](https://golang.org) |\n| Rust | [Rust](https://rust-lang.org) |"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)

	if !strings.Contains(plain, "Go") {
		t.Errorf("table should contain link text 'Go', got:\n%s", plain)
	}
	if !strings.Contains(plain, "Rust") {
		t.Errorf("table should contain link text 'Rust', got:\n%s", plain)
	}
	if strings.Contains(plain, "[Go]") {
		t.Errorf("table should not contain raw markdown link syntax '[Go]', got:\n%s", plain)
	}
	if strings.Contains(plain, "https://golang.org") {
		t.Errorf("table should not display URL as plain text, got:\n%s", plain)
	}
	if !strings.Contains(out, "\x1b]8;;https://golang.org\x1b\\") {
		t.Errorf("table should contain OSC 8 hyperlink escape for golang.org, got:\n%s", out)
	}
}

func TestRenderInlineMarkdown_Link(t *testing.T) {
	out := renderInlineMarkdown("[example](https://example.com)")
	plain := stripANSI(out)

	if !strings.Contains(plain, "example") {
		t.Errorf("should contain link text 'example', got: %s", plain)
	}
	if strings.Contains(plain, "https://example.com") {
		t.Errorf("should not display URL as plain text, got: %s", plain)
	}
	if strings.Contains(plain, "[example]") {
		t.Errorf("should not contain raw markdown syntax, got: %s", plain)
	}
	if !strings.Contains(out, "\x1b]8;;https://example.com\x1b\\") {
		t.Errorf("should contain OSC 8 hyperlink escape, got: %s", out)
	}
}

func TestRender_Markdown_NestedList(t *testing.T) {
	r := NewMDRenderer(80)

	input := "- parent one\n  - child a\n  - child b\n- parent two"
	out, err := r.Render(input)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := stripANSI(out)

	if !strings.Contains(plain, "parent one") {
		t.Errorf("output should contain 'parent one', got:\n%s", plain)
	}
	if !strings.Contains(plain, "parent two") {
		t.Errorf("output should contain 'parent two', got:\n%s", plain)
	}
	if !strings.Contains(plain, "child a") {
		t.Errorf("output should contain nested item 'child a', got:\n%s", plain)
	}
	if !strings.Contains(plain, "child b") {
		t.Errorf("output should contain nested item 'child b', got:\n%s", plain)
	}
}

func TestRender_EmptyMessage_NoOutput(t *testing.T) {
	r := NewMDRenderer(80)

	out, err := r.Render("")
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	plain := strings.TrimSpace(stripANSI(out))
	if plain != "" {
		t.Errorf("expected empty output for empty input, got %q", plain)
	}
}

func TestMDRenderer_NoLeadingBlankLine(t *testing.T) {
	r := NewMDRenderer(80)
	tests := []struct {
		name  string
		input string
	}{
		{"paragraph", "Hello world."},
		{"heading", "## Title"},
		{"list", "- item 1\n- item 2"},
		{"code block", "```go\nfunc main() {}\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := r.Render(tt.input)
			if err != nil {
				t.Fatalf("Render error: %v", err)
			}
			if strings.HasPrefix(out, "\n") {
				t.Errorf("output should not start with blank line, got:\n%q", out)
			}
		})
	}
}

func TestMDRenderer_NoConsecutiveBlankLines(t *testing.T) {
	r := NewMDRenderer(80)
	tests := []struct {
		name  string
		input string
	}{
		{"heading + paragraph", "## Title\n\nSome content here."},
		{"paragraph + list", "Here are items:\n\n- Item 1\n- Item 2"},
		{"paragraph + code", "Code:\n\n```go\nfmt.Println(\"hi\")\n```"},
		{"heading + list", "## Summary\n\n- Item 1\n- Item 2"},
		{"multi section", "## S1\n\nContent 1.\n\n## S2\n\nContent 2."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := r.Render(tt.input)
			if err != nil {
				t.Fatalf("Render error: %v", err)
			}
			if strings.Contains(out, "\n\n\n") {
				plain := stripANSI(out)
				t.Errorf("output should not contain consecutive blank lines, got:\n%s", plain)
			}
		})
	}
}

// The active tail is re-rendered on every frame while its content sits
// unchanged (through a whole tool execution), so Render memoizes its output
// keyed by raw input to avoid re-running glamour on byte-identical content.
func TestMDRendererCachesRenders(t *testing.T) {
	r := NewMDRenderer(80)
	const src = "# Title\n\nSome **bold** text and `code`.\n"

	first, err := r.Render(src)
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	if _, ok := r.cache[src]; !ok {
		t.Fatal("expected render output to be cached under its raw input")
	}

	second, err := r.Render(src)
	if err != nil {
		t.Fatalf("Render() second: %v", err)
	}
	if second != first {
		t.Fatalf("cached render differs from first:\n first=%q\nsecond=%q", first, second)
	}

	// A distinct input caches separately without evicting the first.
	if _, err := r.Render("a plain paragraph"); err != nil {
		t.Fatalf("Render(other): %v", err)
	}
	if len(r.cache) != 2 {
		t.Fatalf("cache size = %d, want 2", len(r.cache))
	}
}

// The cache is bounded so a long session's one-shot renders can't grow it
// without limit.
func TestMDRendererCacheBounded(t *testing.T) {
	r := NewMDRenderer(80)
	for i := range mdCacheMax + 50 {
		if _, err := r.Render(fmt.Sprintf("paragraph number %d", i)); err != nil {
			t.Fatalf("Render(#%d): %v", i, err)
		}
	}
	if len(r.cache) > mdCacheMax {
		t.Fatalf("cache size = %d, want <= %d", len(r.cache), mdCacheMax)
	}
}
