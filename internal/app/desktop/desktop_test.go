package desktop

import (
	"strings"
	"testing"
)

func newSurface(w, h int, footer string) Manager {
	mgr := New()
	mgr.Resize(w, h)
	mgr.SetFooter(footer)
	return mgr
}

func pane(sig, body string) Pane {
	return Pane{ID: "conversation", Sig: sig, Render: func(int, int) string { return body }}
}

// The frame is exactly the surface height, so it fills the alt-screen without
// pushing rows off it, and the footer starts on the row ContentHeight reports —
// which is what the app uses to place the terminal cursor.
func TestRenderFillsSurfaceHeight(t *testing.T) {
	footer := "───\n❭ prompt\n───"
	mgr := newSurface(40, 12, footer)
	mgr.SetContent(pane("a", "one\ntwo"))

	frame := mgr.Render()
	if got := strings.Count(frame, "\n") + 1; got != 12 {
		t.Fatalf("frame height = %d, want 12", got)
	}
	if got := mgr.ContentHeight(); got != 12-3 {
		t.Fatalf("ContentHeight = %d, want %d", got, 12-3)
	}
	lines := strings.Split(frame, "\n")
	if !strings.HasPrefix(lines[mgr.ContentHeight()], "───") {
		t.Fatalf("footer does not start at row %d: %q", mgr.ContentHeight(), lines[mgr.ContentHeight()])
	}
}

// The signature gates the expensive rebuild: same signature, no re-render.
func TestSetContentRebuildsOnlyOnChange(t *testing.T) {
	mgr := newSurface(40, 12, "───")
	renders := 0
	build := func(sig string) Pane {
		return Pane{ID: "conversation", Sig: sig, Render: func(int, int) string {
			renders++
			return "body"
		}}
	}

	mgr.SetContent(build("a"))
	mgr.SetContent(build("a"))
	if renders != 1 {
		t.Fatalf("renders = %d after an unchanged signature, want 1", renders)
	}

	mgr.SetContent(build("b"))
	if renders != 2 {
		t.Fatalf("renders = %d after a changed signature, want 2", renders)
	}

	// A resize invalidates too — the content has to re-wrap.
	mgr.Resize(60, 12)
	mgr.SetContent(build("b"))
	if renders != 3 {
		t.Fatalf("renders = %d after a resize, want 3", renders)
	}
}

// A reader parked at the bottom keeps following streaming output; one that has
// scrolled up stays put, and GotoBottom re-pins it.
func TestScrollPinning(t *testing.T) {
	tall := strings.Repeat("line\n", 200)
	mgr := newSurface(40, 12, "───")
	mgr.SetContent(pane("a", tall))

	bottom := mgr.Render()
	mgr.SetContent(pane("b", tall+"more\n"))
	if mgr.Render() == bottom {
		t.Fatal("pinned reader did not follow new content")
	}

	mgr.PageUp()
	scrolled := mgr.Render()
	mgr.SetContent(pane("c", tall+"more\nand more\n"))
	if mgr.Render() != scrolled {
		t.Fatal("scrolled-up reader was yanked to the bottom by new content")
	}

	mgr.GotoBottom()
	if mgr.Render() == scrolled {
		t.Fatal("GotoBottom did not re-pin the reader")
	}
}

// A surface with no size yet renders nothing rather than a stray frame.
func TestRenderWithoutSizeIsEmpty(t *testing.T) {
	mgr := New()
	mgr.SetFooter("───")
	if got := mgr.Render(); got != "" {
		t.Fatalf("Render() = %q, want empty", got)
	}
}
