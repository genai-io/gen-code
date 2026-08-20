package conv

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// glamour's ansi.Wrap breakpoints are ASCII-only, so it can't split long CJK
// runs. In indented blocks (blockquotes, lists) that overflow drops the block's
// prefix/hanging-indent on continuation lines — they land at column 0. We render
// those blocks ourselves; these tests lock in that every wrapped line keeps its
// structure and stays within the wrap width.

func nonEmptyLines(out string) []string {
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(xansi.Strip(line)) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestBlockquoteCJKKeepsPrefixAndWidth(t *testing.T) {
	src := "> 最近 San 的几次更新，表面上看是 Persona、Self-Learning、Subscription、Autopilot。但放在一起看，它们其实都指向同一个问题：一个 Agent Harness，应该怎样帮助开发者把 Agent 真正带进工作流？"

	for _, termWidth := range []int{80, 100, 120, 160} {
		r := NewMDRenderer(termWidth)
		out, err := r.Render(src)
		if err != nil {
			t.Fatalf("width=%d: render: %v", termWidth, err)
		}

		lines := nonEmptyLines(out)
		if len(lines) < 2 {
			t.Fatalf("width=%d: expected the blockquote to wrap onto multiple lines, got %d", termWidth, len(lines))
		}
		for i, line := range lines {
			plain := xansi.Strip(line)
			if !strings.HasPrefix(plain, "│ ") {
				t.Errorf("width=%d line %d missing │ prefix: %q", termWidth, i, plain)
			}
			if w := lipgloss.Width(line); w > r.width {
				t.Errorf("width=%d line %d width %d exceeds wrap width %d: %q", termWidth, i, w, r.width, plain)
			}
		}
	}
}

func TestBlockquoteCJKPreservesContent(t *testing.T) {
	src := "> 最近 San 的几次更新，指向同一个问题：一个 Agent Harness 应该怎样帮助开发者把 Agent 真正带进工作流？"

	out, err := NewMDRenderer(100).Render(src)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var got strings.Builder
	for line := range strings.SplitSeq(out, "\n") {
		plain := strings.TrimRight(xansi.Strip(line), " ")
		plain = strings.TrimPrefix(plain, "│ ")
		got.WriteString(plain)
	}
	joined := strings.ReplaceAll(got.String(), " ", "")
	for _, want := range []string{"Agent", "Harness", "开发者", "工作流", "同一个问题"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rendered blockquote dropped %q; got %q", want, joined)
		}
	}
}

// lineStartsWithListMarker reports whether a plain (ANSI-stripped) line begins,
// after its leading indent, with a bullet or ordered marker.
func lineStartsWithListMarker(plain string) bool {
	t := strings.TrimLeft(plain, " ")
	if strings.HasPrefix(t, "• ") {
		return true
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(t) && (t[i] == '.' || t[i] == ')') && t[i+1] == ' '
}

// A wrapped CJK list item must keep a hanging indent: every line is either an
// item's marker line or an indented continuation — never bare content at column
// 0 — and no line exceeds the wrap width.
func TestListCJKHangingIndentAndWidth(t *testing.T) {
	srcs := map[string]string{
		"unordered": "- 第一项，这一项比较长需要换行显示所以会触发我们的悬挂缩进逻辑来对齐后续行的内容。\n- 第二项也一样长，同样需要换行来验证每一项都能正确地对齐到文字下方的位置。",
		"ordered":   "1. 第一项内容比较长需要换行才能看到悬挂缩进是否把续行对齐到了数字后面的文字位置。\n2. 第二项。",
		"nested":    "- 顶层项目内容也可以很长从而换行来测试顶层的悬挂缩进对齐效果究竟如何。\n    - 子项目同样很长需要换行以验证嵌套层级的悬挂缩进对齐是否正确无误。",
	}
	for name, src := range srcs {
		for _, termWidth := range []int{60, 90, 130} {
			r := NewMDRenderer(termWidth)
			out, err := r.Render(src)
			if err != nil {
				t.Fatalf("%s width=%d: render: %v", name, termWidth, err)
			}
			lines := nonEmptyLines(out)
			sawContinuation := false
			for i, line := range lines {
				plain := xansi.Strip(line)
				if !strings.HasPrefix(plain, " ") && !lineStartsWithListMarker(plain) {
					t.Errorf("%s width=%d line %d is bare content at column 0 (lost hanging indent): %q", name, termWidth, i, plain)
				}
				if !lineStartsWithListMarker(plain) {
					sawContinuation = true
				}
				if w := lipgloss.Width(line); w > r.width {
					t.Errorf("%s width=%d line %d width %d exceeds wrap width %d: %q", name, termWidth, i, w, r.width, plain)
				}
			}
			if termWidth == 60 && !sawContinuation {
				t.Errorf("%s width=%d: expected at least one wrapped continuation line", name, termWidth)
			}
		}
	}
}

func TestListCJKPreservesContentAndNumbering(t *testing.T) {
	src := "1. 苹果需要认真对待\n2. 香蕉也要认真对待\n3. 橘子同样重要"

	out, err := NewMDRenderer(80).Render(src)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	plain := xansi.Strip(out)
	for _, want := range []string{"1. 苹果", "2. 香蕉", "3. 橘子"} {
		if !strings.Contains(plain, want) {
			t.Errorf("ordered list dropped or misnumbered %q; got:\n%s", want, plain)
		}
	}
}
