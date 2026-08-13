package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestPermissionsPanelDefaultsAllowed confirms an unset gate reads as open —
// allowBypass is opt-out, so the cursor parks on "Allowed" and stays clean
// until it moves.
func TestPermissionsPanelDefaultsAllowed(t *testing.T) {
	p := newPermissionsPanel(nil)
	p.Enter()

	if !p.baseline {
		t.Fatalf("YOLO mode should default allowed (allowBypass is opt-out)")
	}
	if got := yoloOptions[p.cursor]; !got.allowed {
		t.Fatalf("cursor should park on the Allowed row, got %q", got.label)
	}
	if p.Dirty() {
		t.Fatalf("fresh panel parked on the effective value should not be dirty")
	}
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Allowed → Locked
	if !p.Dirty() {
		t.Fatalf("after moving off the baseline the panel should be dirty")
	}
}

// TestPermissionsPanelLockPersistsExplicitFalse is the load-bearing case for
// an opt-out setting: locking must write allowBypass=false, not drop the
// field. A dropped field means "unset", which reads back as allowed — the
// user would lock the gate and find it open again next launch.
func TestPermissionsPanelLockPersistsExplicitFalse(t *testing.T) {
	home := tempHome(t)

	p := newPermissionsPanel(nil)
	p.Enter()
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Allowed → Locked

	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatalf("enter should dismiss the popup (done=true)")
	}
	if p.baseline {
		t.Fatalf("baseline should be false after locking")
	}
	if p.saveErr != nil {
		t.Fatalf("unexpected saveErr: %v", p.saveErr)
	}
	msg, ok := cmd().(AllowBypassSavedMsg)
	if !ok || msg.Allowed {
		t.Fatalf("expected AllowBypassSavedMsg{Allowed:false}, got %#v", cmd())
	}

	data := readPersistedAllowBypass(t, home)
	if data == nil {
		t.Fatalf("allowBypass should persist as an explicit false, got null")
	}
	if *data {
		t.Fatalf("persisted allowBypass = true, want false")
	}
}

// TestPermissionsPanelReallowPersistsTrue confirms the switch works in both
// directions: re-allowing after a lock writes true rather than leaving the
// explicit false behind.
func TestPermissionsPanelReallowPersistsTrue(t *testing.T) {
	home := tempHome(t)

	p := newPermissionsPanel(nil)
	p.Enter()
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Allowed → Locked
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp}) // Locked → Allowed
	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatalf("enter should dismiss the popup (done=true)")
	}
	if msg, ok := cmd().(AllowBypassSavedMsg); !ok || !msg.Allowed {
		t.Fatalf("expected AllowBypassSavedMsg{Allowed:true}, got %#v", cmd())
	}

	data := readPersistedAllowBypass(t, home)
	if data == nil || !*data {
		t.Fatalf("persisted allowBypass = %v, want true", data)
	}
}

// TestPermissionsPanelSaveFailureSurfacesError confirms a failed persist keeps
// the popup open, leaves the baseline untouched, and shows the error inline.
func TestPermissionsPanelSaveFailureSurfacesError(t *testing.T) {
	home := tempHome(t)
	// Block the write: a regular file where the .san dir must be makes the
	// loader's MkdirAll fail.
	if err := os.WriteFile(filepath.Join(home, ".san"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newPermissionsPanel(nil)
	p.Enter()
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Allowed → Locked

	cmd, done := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done {
		t.Fatalf("a failed save should keep the popup open (done=false)")
	}
	if cmd != nil {
		t.Fatalf("a failed save should not emit AllowBypassSavedMsg, got %#v", cmd())
	}
	if p.saveErr == nil {
		t.Fatalf("a failed save should set saveErr")
	}
	if !p.baseline {
		t.Fatalf("baseline should be untouched on failure")
	}
	if out := p.Render(80, 24); !strings.Contains(out, "couldn't save") {
		t.Fatalf("Render should surface the save error, got:\n%s", out)
	}
}

// tempHome points the settings loader at a throwaway home directory.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// readPersistedAllowBypass reads back the allowBypass field from the user
// settings file the panel writes to. A nil return means the field is absent.
func readPersistedAllowBypass(t *testing.T, home string) *bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".san", "settings.json"))
	if err != nil {
		t.Fatalf("settings file not written: %v", err)
	}
	var data struct {
		AllowBypass *bool `json:"allowBypass"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("settings file not valid JSON: %v\n%s", err, raw)
	}
	return data.AllowBypass
}
