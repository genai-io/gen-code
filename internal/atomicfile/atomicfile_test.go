package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestWriteCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c.json")
	if err := Write(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("contents = %q, want %q", got, "hello")
	}
}

// CreateTemp always makes 0600, so the caller's mode has to be applied before
// the file becomes visible under the target name — secrets stay 0600, shared
// config becomes 0644.
func TestWriteAppliesRequestedPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	dir := t.TempDir()
	for _, perm := range []os.FileMode{0o600, 0o644} {
		path := filepath.Join(dir, perm.String()+".json")
		if err := Write(path, []byte("{}"), perm); err != nil {
			t.Fatalf("Write: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.Mode().Perm() != perm {
			t.Errorf("mode = %v, want %v", info.Mode().Perm(), perm)
		}
	}
}

// A failed write must leave no scratch file behind — the secrets store used to
// leak one containing API keys when the rename failed.
func TestWriteLeavesNoTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	// Renaming onto a path whose parent is a directory entry that already
	// exists as a non-empty directory fails on every platform.
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := Write(target, []byte("data"), 0o644); err == nil {
		t.Fatal("Write onto a non-empty directory should fail")
	}
	assertNoTempLeft(t, dir)
}

// Two writers racing on the same path must not share one scratch file. With a
// fixed "<path>.tmp" they interleave into it; unique names keep each write's
// bytes to itself, so the winner's content is intact rather than a blend.
func TestConcurrentWritesLeaveIntactContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contended.json")
	a := strings.Repeat("a", 4096)
	b := strings.Repeat("b", 4096)

	var wg sync.WaitGroup
	for _, payload := range []string{a, b} {
		wg.Go(func() {
			for range 50 {
				if err := Write(path, []byte(payload), 0o644); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != a && string(got) != b {
		t.Errorf("contents are a blend of both writers (len=%d), want exactly one payload", len(got))
	}
	assertNoTempLeft(t, dir)
}

func TestWriteJSONRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.json")
	if err := WriteJSON(path, map[string]string{"k": "v"}, 0o644); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := "{\n  \"k\": \"v\"\n}"; string(got) != want {
		t.Errorf("contents = %q, want indented %q", got, want)
	}
}

func assertNoTempLeft(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReadJSONTreatsMissingAndEmptyAsNoContent(t *testing.T) {
	dir := t.TempDir()
	seeds := map[string]*string{
		"missing.json":    nil,
		"empty.json":      ptr(""),
		"whitespace.json": ptr("\n\t \n"),
	}
	for name, seed := range seeds {
		path := filepath.Join(dir, name)
		if seed != nil {
			if err := os.WriteFile(path, []byte(*seed), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		v := map[string]string{"kept": "yes"}
		if err := ReadJSON(path, &v); err != nil {
			t.Errorf("%s: ReadJSON = %v, want nil", name, err)
		}
		if v["kept"] != "yes" {
			t.Errorf("%s: caller's value was clobbered: %v", name, v)
		}
	}
}

func TestReadJSONReportsUnparsableContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"model": "opus",}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	err := ReadJSON(path, &v)
	if err == nil {
		t.Fatal("ReadJSON = nil, want a parse error")
	}
	if !strings.Contains(err.Error(), "settings.json") {
		t.Errorf("error should name the file, got: %v", err)
	}
}

func TestReadJSONDecodesValidContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := WriteJSON(path, map[string]int{"n": 7}, 0o644); err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if err := ReadJSON(path, &v); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if v["n"] != 7 {
		t.Errorf("v = %v, want n=7", v)
	}
}

func ptr(s string) *string { return &s }
