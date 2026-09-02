package conv

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// A tool that draws progress in place ends each update with a carriage return.
// Rendering the whole line puts that carriage return inside the "┊" gutter and
// the terminal obeys it — the row restarts at column 0 and the connector is
// gone. git rebase is the one people see: "Rebasing (1/3)\rAuto-merging f.go".
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
