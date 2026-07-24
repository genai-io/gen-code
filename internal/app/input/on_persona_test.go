package input

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/persona"
)

// writeUserPersona scaffolds a minimal user-scope persona under $HOME for tests.
func writeUserPersona(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, ".san", "personas", name, "system")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.md"), []byte("You are "+name+"."), 0o644); err != nil {
		t.Fatal(err)
	}
}

func selectByName(s *PersonaSelector, name string) bool {
	for i, it := range s.items {
		if it.Name == name {
			s.nav.Selected = i
			return true
		}
	}
	return false
}

func TestPersonaSelector_EnterMarksCurrentAndSelects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewPersonaSelector(persona.NewRegistry(""), nil) // only the built-in default

	if err := s.EnterSelect(80, 24); err != nil {
		t.Fatal(err)
	}
	if !s.IsActive() {
		t.Fatal("selector should be active after EnterSelect")
	}
	if len(s.items) == 0 {
		t.Fatal("expected at least the default persona")
	}
	// No settings.persona → current resolves to the built-in default, preselected.
	if !s.items[s.nav.Selected].IsCurrent || s.items[s.nav.Selected].Name != persona.DefaultName {
		t.Errorf("initial selection = %+v, want the current default", s.items[s.nav.Selected])
	}

	cmd := s.Select()
	if cmd == nil {
		t.Fatal("Select should return a command")
	}
	sel, ok := cmd().(personaSelectedMsg)
	if !ok {
		t.Fatalf("expected personaSelectedMsg, got %T", cmd())
	}
	if sel.Name != persona.DefaultName {
		t.Errorf("selected = %q, want %q", sel.Name, persona.DefaultName)
	}
}

func TestPersonaSelector_EscCancels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewPersonaSelector(persona.NewRegistry(""), nil)
	_ = s.EnterSelect(80, 24)
	s.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.IsActive() {
		t.Error("Esc should cancel the selector")
	}
}

func TestPersonaSelector_DeleteFlowEmitsMsg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeUserPersona(t, home, "tester")

	s := NewPersonaSelector(persona.NewRegistry(""), nil)
	_ = s.EnterSelect(80, 24)
	if !selectByName(&s, "tester") {
		t.Fatal("tester persona should be listed")
	}

	// Ctrl+D only arms the confirm; the delete fires on "y".
	s.HandleKeypress(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !s.confirmDelete {
		t.Fatal("Ctrl+D should arm the delete confirmation")
	}
	// A non-y key backs out without deleting.
	s.HandleKeypress(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if s.confirmDelete {
		t.Fatal("a non-y key should cancel the confirmation")
	}

	s.HandleKeypress(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	cmd := s.HandleKeypress(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("y should fire the delete")
	}
	msg, ok := cmd().(personaDeleteMsg)
	if !ok || msg.Name != "tester" {
		t.Fatalf("got %#v, want personaDeleteMsg{tester}", cmd())
	}
	if s.IsActive() {
		t.Error("picker should close after confirming delete")
	}
}

func TestPersonaSelector_OpenEmitsMsg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeUserPersona(t, home, "tester")

	s := NewPersonaSelector(persona.NewRegistry(""), nil)
	_ = s.EnterSelect(80, 24)
	if !selectByName(&s, "tester") {
		t.Fatal("tester persona should be listed")
	}
	cmd := s.HandleKeypress(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+O should emit an open message")
	}
	if msg, ok := cmd().(personaOpenMsg); !ok || msg.Name != "tester" {
		t.Fatalf("got %#v, want personaOpenMsg{tester}", cmd())
	}
	if s.IsActive() {
		t.Error("picker should close after opening")
	}
}

func TestPersonaSelector_NoActionsOnBuiltin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewPersonaSelector(persona.NewRegistry(""), nil) // only the built-in default
	_ = s.EnterSelect(80, 24)
	if cmd := s.HandleKeypress(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}); cmd != nil || s.confirmDelete {
		t.Error("Ctrl+D on the built-in default should be a no-op")
	}
	if cmd := s.HandleKeypress(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}); cmd != nil {
		t.Error("Ctrl+O on the built-in default should be a no-op")
	}
}

func TestPersonaSelector_RenderKeepsRowsWithinPanel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewPersonaSelector(persona.NewRegistry(""), nil)
	if err := s.EnterSelect(120, 24); err != nil {
		t.Fatal(err)
	}
	s.items = append(s.items, personaItem{
		Name:        "software-engineer",
		Scope:       "project",
		Description: strings.Repeat("long description ", 20),
	})
	s.nav.Total = len(s.items)

	rendered := s.Render()
	plain := xansi.Strip(rendered)
	row := ""
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "software-engineer") {
			row = strings.TrimSpace(line)
			break
		}
	}
	if row == "" {
		t.Fatal("software-engineer row was not rendered")
	}
	if strings.Contains(row, "long description long description long description long description long") || !strings.HasSuffix(row, "…") {
		t.Fatalf("long persona description was not truncated on its row: %q", row)
	}
}

func TestUpdatePersona_AppliesAndCancels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var got string
	deps := OverlayDeps{
		State:            &Model{},
		SetActivePersona: func(name string) error { got = name; return nil },
	}
	s := NewPersonaSelector(persona.NewRegistry(""), nil)
	_ = s.EnterSelect(80, 24)

	cmd, handled := UpdatePersona(deps, &s, personaSelectedMsg{Name: "ml-researcher"})
	if !handled {
		t.Fatal("UpdatePersona should handle personaSelectedMsg")
	}
	if got != "ml-researcher" {
		t.Errorf("SetActivePersona called with %q, want ml-researcher", got)
	}
	if s.IsActive() {
		t.Error("selector should be cancelled after applying")
	}
	if cmd == nil {
		t.Error("expected a status-timer command")
	}
}
