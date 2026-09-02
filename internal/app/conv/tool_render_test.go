package conv

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// A carriage return left in the line lands inside the "┊" gutter, and the
// terminal restarts the row at column 0. git rebase is where people see it.
func TestNestedToolBodyDropsOverwrittenProgress(t *testing.T) {
	body := renderNestedToolBody("Rebasing (1/1)\rAuto-merging f.txt\nCONFLICT (content): Merge conflict")

	if strings.Contains(body, "\r") {
		t.Fatalf("a carriage return reached the frame: %q", body)
	}
	if strings.Contains(body, "Rebasing (1/1)") {
		t.Errorf("overwritten progress was rendered: %q", body)
	}
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if !strings.HasPrefix(xansi.Strip(line), "  ┊ ") && strings.TrimSpace(xansi.Strip(line)) != "" {
			t.Errorf("line escaped the gutter: %q", line)
		}
	}
	for _, want := range []string{"Auto-merging f.txt", "CONFLICT (content): Merge conflict"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lost %q: %q", want, body)
		}
	}
}

// CRLF is a line ending, not an overwrite: the text before it is what was shown.
func TestNestedToolBodyKeepsCRLFContent(t *testing.T) {
	body := renderNestedToolBody("first line\r\nsecond line\r")
	for _, want := range []string{"first line", "second line"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lost %q: %q", want, body)
		}
	}
}
