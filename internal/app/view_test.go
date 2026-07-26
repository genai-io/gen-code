package app

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
)

// The composer prints the "❭ " prompt once, on the first row, while inputCursor
// offsets the cursor by that prompt's width on every row. hangComposerRows is
// what keeps the two in step: without the gutter, wrapped and newline-split rows
// start at column 0 and the cursor floats two columns right of the text.
func TestComposerCursorAlignsWithEveryRow(t *testing.T) {
	const width = 80

	tests := []struct {
		name  string
		value string
	}{
		{"hard newline", "first line\nsecond"},
		{"soft wrap", strings.TrimSpace(strings.Repeat("ab/cd-ef ", 12))},
		{"cjk soft wrap", strings.Repeat("看看当前分支对 readme 的更改，", 6)},
		{"single row", "short"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &model{
				env:       env{Width: width, Height: 24, Ready: true},
				conv:      conv.NewModel(width),
				userInput: input.New("", width, nil, input.SelectorDeps{}),
			}
			m.userInput.Textarea.SetWidth(width - 4 - 2)
			m.userInput.Textarea.SetValue(tt.value)
			m.userInput.Textarea.CursorEnd()

			lines := strings.Split(m.renderInputView(), "\n")
			cursor := m.inputCursor(0)
			if cursor == nil {
				t.Fatal("composer reported no cursor")
			}
			if cursor.Position.Y != len(lines)-1 {
				t.Fatalf("cursor on row %d, but the value ends on row %d", cursor.Position.Y, len(lines)-1)
			}

			// The cursor sits at the end of the value, so it must land exactly
			// where the drawn row's text stops.
			row := strings.TrimRight(ansi.Strip(lines[cursor.Position.Y]), " ")
			if got := lipgloss.Width(row); got != cursor.Position.X {
				t.Fatalf("cursor at column %d, but row %q ends at column %d", cursor.Position.X, row, got)
			}

			// Every row has to fit the terminal, gutter included, or the
			// terminal rewraps the composer under the cursor.
			for i, line := range lines {
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("row %d is %d columns wide, exceeds terminal width %d", i, w, width)
				}
			}
		})
	}
}

func TestTailLines(t *testing.T) {
	five := "L0\nL1\nL2\nL3\nL4"

	tests := []struct {
		name     string
		in       string
		maxLines int
		want     string
	}{
		{"no room drops content", five, 0, ""},
		{"negative room drops content", five, -3, ""},
		{"fewer lines than max returns input", "a\nb", 5, "a\nb"},
		{"exact fit returns input", five, 5, five},
		{"truncates to last N (latest)", five, 2, "L3\nL4"},
		{"single line cap keeps latest", five, 1, "L4"},
		{"empty string", "", 3, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tailLines(tt.in, tt.maxLines)
			if got != tt.want {
				t.Fatalf("tailLines(%q, %d) = %q, want %q", tt.in, tt.maxLines, got, tt.want)
			}
			// The result must never exceed maxLines rows when capping applies.
			if tt.maxLines > 0 {
				if n := strings.Count(got, "\n") + 1; n > tt.maxLines && got != tt.in {
					t.Fatalf("tailLines(%q, %d) returned %d lines, exceeds cap", tt.in, tt.maxLines, n)
				}
			}
		})
	}
}
