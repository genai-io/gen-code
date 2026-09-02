// Package desktop implements San's full-screen surface: an alt-screen reader
// that shows the same conversation transcript and input strip as the inline
// view, but owns the whole screen so the entire history scrolls inside the
// managed frame — where the inline view instead hands finished output to the
// terminal's native scrollback and can no longer touch it.
//
// It is deliberately chrome-light: one scrollable content viewport above the
// footer (the app's input bar). Nothing here renders conversation content: the
// app supplies a Pane, the manager gives it a width and scrolls the result. The
// package holds no reference to the root model and imports no bubbletea — it is
// a plain view component, driven entirely from internal/app/desktop_surface.go.
package desktop

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// Pane is the content the app supplies each frame — today the conversation
// transcript, rendered at the surface width. Sig is a cheap change signature
// that gates the expensive rebuild: same signature, same size, no re-render.
// A second window is a second Pane, not a change to this file.
type Pane struct {
	ID     string
	Sig    string
	Render func(w, h int) string
}

// Manager owns the alt-screen reader: a single scrollable content viewport plus
// the footer. It is a value held by the root model.
type Manager struct {
	w, h   int
	vp     viewport.Model
	footer string
	sig    string
	cw, ch int
	ready  bool
}

func New() Manager { return Manager{vp: viewport.New()} }

func (mgr *Manager) Resize(w, h int) { mgr.w, mgr.h = w, h }

// SetFooter sets the bottom strip (the input bar), drawn below the transcript.
// Call it before SetContent: the footer's height is what's left for the
// transcript.
func (mgr *Manager) SetFooter(s string) { mgr.footer = s }

// ContentHeight reports how many rows the transcript occupies, which is also the
// row the footer starts on — the app needs it to place the terminal cursor
// inside the composer. Valid after SetContent.
func (mgr *Manager) ContentHeight() int { return mgr.ch }

// SetContent syncs the scrollable transcript, rebuilding only when the signature
// or the content size changes — so a markdown re-render happens on real change,
// not every frame. The reader stays pinned to the bottom when it was already
// there, so streaming output keeps the latest line in view; once the reader has
// scrolled up, new content no longer yanks it back down.
func (mgr *Manager) SetContent(p Pane) {
	h := max(mgr.h-footerLines(mgr.footer), 1)
	if mgr.ready && mgr.sig == p.Sig && mgr.cw == mgr.w && mgr.ch == h {
		return
	}
	atBottom := !mgr.ready || mgr.vp.AtBottom()
	mgr.sig, mgr.cw, mgr.ch, mgr.ready = p.Sig, mgr.w, h, true
	mgr.vp.SetWidth(mgr.w)
	mgr.vp.SetHeight(h)
	mgr.vp.SetContent(p.Render(mgr.w, h))
	if atBottom {
		mgr.vp.GotoBottom()
	}
}

// Render composes the scrollable transcript above the footer — the full-screen
// counterpart of the inline view. The result is exactly the surface height, so
// it fills the alt-screen without pushing rows off it.
func (mgr *Manager) Render() string {
	if mgr.w < 1 || mgr.h < 1 {
		return ""
	}
	if footerLines(mgr.footer) > 0 {
		return lipgloss.JoinVertical(lipgloss.Left, mgr.vp.View(), mgr.footer)
	}
	return mgr.vp.View()
}

// Scrolling is exposed as explicit methods rather than by forwarding keys into
// the viewport's own Update: its default keymap claims bare letters (f, b, j, k,
// space) and ctrl+u/ctrl+d, which belong to the composer and to the app's own
// shortcuts. The app decides which keys the reader gets; see handleDesktopKey.

func (mgr *Manager) PageUp()   { mgr.vp.ScrollUp(mgr.vp.Height()) }
func (mgr *Manager) PageDown() { mgr.vp.ScrollDown(mgr.vp.Height()) }
func (mgr *Manager) GotoTop()  { mgr.vp.GotoTop() }

// GotoBottom jumps to the latest output and re-pins the reader, so streaming
// resumes following the tail.
func (mgr *Manager) GotoBottom() { mgr.vp.GotoBottom() }

func footerLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
