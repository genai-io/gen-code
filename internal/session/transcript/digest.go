package transcript

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// What an inference record claims it saw, as one number each.
//
// These live here because a digest is a property of the record: the writer and
// the reader both import this package, and one function is the only way two
// sides can be made to hash the same bytes. They used to be a copy each, kept
// in step by the coincidence that core.ToolSchema serialised like
// ToolSchemaView — which stopped the day core.ToolSchema became the SDK's
// ai.Schema, and every inference began failing its integrity check.

// DigestSystem hashes the rendered system prompt. A caller that holds sections
// rather than a prompt renders them first, because rendering is its own answer
// to give.
func DigestSystem(rendered string) string { return digest([]byte(rendered)) }

// DigestTools hashes a toolset, in name order — the same tools assembled in a
// different order are the same toolset.
func DigestTools(views []ToolSchemaView) string {
	if len(views) == 0 {
		return digest(nil)
	}
	sorted := make([]ToolSchemaView, len(views))
	copy(sorted, views)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	b, err := json.Marshal(sorted)
	if err != nil {
		// Marshal can only fail on unsupported types, and this one is
		// JSON-safe. Digest the names so the value is stable rather than empty.
		names := make([]string, len(sorted))
		for i, v := range sorted {
			names[i] = v.Name
		}
		b, _ = json.Marshal(names)
	}
	return digest(b)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
