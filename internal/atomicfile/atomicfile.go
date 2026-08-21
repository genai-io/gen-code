// Package atomicfile replaces a file's contents in one observable step.
//
// The content is written to a temporary file in the destination directory and
// renamed over the target, so a concurrent reader sees either the old contents
// or the new ones — never a half-written file — and a failed write leaves the
// previous version intact.
//
// The temporary name is unique per call. A fixed "<path>.tmp" would have two
// concurrent writers of the same path share one scratch file and interleave
// their bytes into it, which is exactly the corruption the rename is meant to
// prevent.
package atomicfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// dirPerm is the mode used for directories created on the way to the target.
const dirPerm os.FileMode = 0o755

// Write replaces the file at path with data, creating parent directories as
// needed. The temporary file is removed on every failure path, so a failed
// write leaves nothing behind.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	// Armed until the rename succeeds, so every failure below — including a
	// panic — leaves nothing behind. Both calls are no-ops once they have run.
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	// CreateTemp always makes the file 0600; widen or narrow it to the caller's
	// mode before it becomes visible under the target name.
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// WriteJSON marshals v as indented JSON and writes it through Write.
func WriteJSON(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	return Write(path, data, perm)
}

// ReadJSON decodes the JSON file at path into v as the read half of a
// read-modify-write cycle that ends in WriteJSON.
//
// A file that is missing — or present but empty — is not an error: v is left as
// the caller initialized it, which is the correct base for creating the file.
//
// A file that exists with content it cannot parse IS an error, and callers must
// abort the write rather than continue from an empty base. WriteJSON replaces
// the whole file, so continuing would overwrite everything the user had in it
// with just the block being saved: one stray comma in settings.json, and the
// next persona switch takes model, permissions and env down with it.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
