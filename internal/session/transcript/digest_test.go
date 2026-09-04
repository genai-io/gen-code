package transcript

import "testing"

// The recorder writes a digest and replay recomputes one. They mean nothing
// unless both hash the same bytes, which is why there is one function and not
// a copy each — the copies agreed only because core.ToolSchema happened to
// serialise like ToolSchemaView, and stopped the day it became ai.Schema.
func TestDigestToolsDoesNotDependOnOrder(t *testing.T) {
	a := []ToolSchemaView{{Name: "Read", Description: "read"}, {Name: "Write", Description: "write"}}
	b := []ToolSchemaView{{Name: "Write", Description: "write"}, {Name: "Read", Description: "read"}}
	if DigestTools(a) != DigestTools(b) {
		t.Error("the same tools in a different order digested differently")
	}
	if DigestTools(a) == DigestTools(nil) {
		t.Error("a toolset digested the same as no toolset")
	}
}

// Sorting must not reorder the caller's slice: the recorder digests the views
// it is about to write, and writing them in a different order than they were
// registered would be a second, quieter bug.
func TestDigestToolsLeavesTheCallersSliceAlone(t *testing.T) {
	views := []ToolSchemaView{{Name: "Write"}, {Name: "Read"}}
	DigestTools(views)
	if views[0].Name != "Write" {
		t.Errorf("the caller's slice was reordered: %v", views)
	}
}

func TestDigestSystemIsTheRenderedPrompt(t *testing.T) {
	if DigestSystem("a\n\nb") == DigestSystem("ab") {
		t.Error("two different prompts digested the same")
	}
}
