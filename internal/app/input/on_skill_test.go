package input

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

// Regression for the /skills overflow: a row whose description contains CJK
// (2-column) glyphs must still render on a single line. Rune-count truncation
// undercounted the display width and let the voiceover row wrap.
func TestSkillRowDoesNotWrapWithCJKDescription(t *testing.T) {
	s := NewSkillSelector(nil)
	s.list.load([]skillItem{{
		Name: "voiceover",
		Description: "配音 / voiceover / narration / dub — generate dynamically-paced " +
			"TTS narration (节奏 + 抑扬顿挫) from a script, with an optional synthesized " +
			"ambient music bed and sidechain ducking",
	}}, 190, 40)
	s.list.nav.MaxVisible = 10
	s.list.nav.EnsureVisible()

	panel := kit.Panel{Width: 190, Height: 40}
	var sb strings.Builder
	s.renderItemList(&sb, panel)

	var lines []string
	for l := range strings.SplitSeq(sb.String(), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}

	if len(lines) != 1 {
		t.Fatalf("CJK skill row rendered on %d lines, want 1 (it wrapped):\n%s", len(lines), sb.String())
	}
	if w, rowWidth := lipgloss.Width(lines[0]), max(20, panel.ContentWidth()-4); w > rowWidth {
		t.Fatalf("row display width %d exceeds the %d-column slot", w, rowWidth)
	}
}
