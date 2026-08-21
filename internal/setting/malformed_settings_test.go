package setting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// malformed is a settings.json a user could plausibly end up with: real
// content, one stray trailing comma.
const malformed = `{
  "model": "claude-opus-5",
  "permissions": {"allow": ["Bash(git:*)"]},
  "env": {"TOKEN": "secret"},
}`

// Every save path used to read the file, ignore the parse error, and continue
// from an empty base — then write that base back over the whole file. A single
// typo in settings.json therefore cost the user their model, permissions and
// env the next time anything touched settings. Each save must now refuse.
func TestSavePathsRefuseToClobberMalformedSettings(t *testing.T) {
	saves := map[string]func(t *testing.T, home string) error{
		"SaveToUser": func(t *testing.T, home string) error {
			d := NewData()
			d.Model = "new-model"
			return NewLoaderWithOptions(filepath.Join(home, ".san"), "", true).SaveToUser(d)
		},
		"UpdateSelfLearnAt": func(t *testing.T, home string) error {
			return UpdateSelfLearnAt(SelfLearnSettings{Memory: SelfLearnMemory{Enabled: true}}, true)
		},
		"SavePersonaAt": func(t *testing.T, home string) error {
			return SavePersonaAt("", "reviewer", true)
		},
	}

	for name, save := range saves {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			path := userSettingsFile(home)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(malformed), 0o644); err != nil {
				t.Fatal(err)
			}

			err := save(t, home)
			if err == nil {
				t.Fatal("save succeeded on a malformed settings.json; it must report the parse error")
			}
			if !strings.Contains(err.Error(), "settings.json") {
				t.Errorf("error should name the offending file, got: %v", err)
			}

			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read back: %v", readErr)
			}
			if string(after) != malformed {
				t.Errorf("the user's file was rewritten:\n%s", after)
			}
		})
	}
}

// A settings.json that does not exist yet, or exists but is empty, is a normal
// starting state — not corruption — so saving must still work.
func TestSaveWorksOnMissingOrEmptySettings(t *testing.T) {
	for _, tc := range []struct{ name, seed string }{
		{"missing", ""},
		{"empty", ""},
		{"whitespace", "\n  \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			path := userSettingsFile(home)
			if tc.name != "missing" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.seed), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if err := SavePersonaAt("", "reviewer", true); err != nil {
				t.Fatalf("SavePersonaAt: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !strings.Contains(string(after), `"persona": "reviewer"`) {
				t.Errorf("persona was not written:\n%s", after)
			}
		})
	}
}
