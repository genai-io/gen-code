// Command layercheck verifies that the repository's internal package imports
// obey the layer ordering documented in docs/reference/dependency-rules.md.
//
// Layers (top of stack → bottom):
//
//	cmd  →  app  →  feature  →  core  →  infrastructure
//
// A higher layer may import a lower layer; the reverse is forbidden. Same-layer
// imports are allowed.
//
// Run:
//
//	go run ./tools/layercheck
//
// Exits 0 on success, 1 on violations, 2 on tool error.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const repoModule = "github.com/genai-io/san"

// rank orders layers from top of stack (0) to bottom (4). A package may import
// other packages of equal or greater rank, never lower rank.
var rank = map[string]int{
	"cmd":            0,
	"app":            1,
	"feature":        2,
	"core":           3,
	"infrastructure": 4,
}

func main() {
	layerOf, err := loadLayerMap("docs/reference/package-map.md")
	if err != nil {
		fmt.Fprintln(os.Stderr, "layercheck:", err)
		os.Exit(2)
	}

	pkgs, err := loadPackages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "layercheck:", err)
		os.Exit(2)
	}

	type violation struct {
		from, fromLayer string
		to, toLayer     string
	}

	var violations []violation
	unknown := map[string]bool{}

	for _, p := range pkgs {
		fromRel, fromLayer, ok := lookupLayer(layerOf, p.ImportPath)
		if !ok {
			continue // not one of ours, or unmapped
		}
		for _, imp := range p.Imports {
			toRel, toLayer, ok := lookupLayer(layerOf, imp)
			if !ok {
				if after, ok0 := strings.CutPrefix(imp, repoModule+"/"); ok0 {
					unknown[after] = true
				}
				continue
			}
			if rank[fromLayer] > rank[toLayer] {
				violations = append(violations, violation{fromRel, fromLayer, toRel, toLayer})
			}
		}
	}

	if len(unknown) > 0 {
		ks := make([]string, 0, len(unknown))
		for k := range unknown {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Fprintln(os.Stderr, "layercheck: unmapped internal packages (add to docs/reference/package-map.md):")
		for _, k := range ks {
			fmt.Fprintln(os.Stderr, "  "+k)
		}
		fmt.Fprintln(os.Stderr)
	}

	if len(violations) == 0 {
		fmt.Println("layercheck: no layer violations")
		if len(unknown) > 0 {
			os.Exit(2)
		}
		return
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].from != violations[j].from {
			return violations[i].from < violations[j].from
		}
		return violations[i].to < violations[j].to
	})

	fmt.Printf("layercheck: %d violation(s)\n", len(violations))
	for _, v := range violations {
		fmt.Printf("  %s (%s) -> %s (%s)\n", v.from, v.fromLayer, v.to, v.toLayer)
	}
	os.Exit(1)
}

// loadLayerMap reads docs/reference/package-map.md and returns the package
// layer assignment. Subpackages inherit from the nearest mapped ancestor.
func loadLayerMap(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	layerOf, err := parseLayerMap(string(b))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return layerOf, nil
}

func parseLayerMap(markdown string) (map[string]string, error) {
	layerOf := map[string]string{}
	for line := range strings.SplitSeq(markdown, "\n") {
		cells := markdownTableCells(line)
		if len(cells) < 2 {
			continue
		}
		pkg, ok := codeCell(cells[0])
		if !ok || !(strings.HasPrefix(pkg, "cmd/") || strings.HasPrefix(pkg, "internal/")) {
			continue
		}
		layer, ok := codeCell(cells[1])
		if !ok {
			return nil, fmt.Errorf("package %s has non-code layer cell %q", pkg, cells[1])
		}
		if _, ok := rank[layer]; !ok {
			return nil, fmt.Errorf("package %s has unknown layer %q", pkg, layer)
		}
		if old, exists := layerOf[pkg]; exists && old != layer {
			return nil, fmt.Errorf("package %s assigned to both %q and %q", pkg, old, layer)
		}
		layerOf[pkg] = layer
	}
	if len(layerOf) == 0 {
		return nil, fmt.Errorf("no package rows found")
	}
	return layerOf, nil
}

func markdownTableCells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

func codeCell(cell string) (string, bool) {
	cell = strings.TrimSpace(cell)
	if len(cell) < 2 || cell[0] != '`' || cell[len(cell)-1] != '`' {
		return "", false
	}
	return strings.TrimSpace(cell[1 : len(cell)-1]), true
}

// pkgInfo is the subset of `go list -json` output that we need.
type pkgInfo struct {
	ImportPath string
	Imports    []string
}

// loadPackages calls `go list -json ./internal/... ./cmd/...` and decodes the
// concatenated JSON stream into a slice. Test packages are excluded via the
// default behavior of `go list`; we don't enumerate test imports.
func loadPackages() ([]pkgInfo, error) {
	cmd := exec.Command("go", "list", "-json", "./internal/...", "./cmd/...")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list: %w\n%s", err, ee.Stderr)
		}
		return nil, fmt.Errorf("go list: %w", err)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgs []pkgInfo
	for dec.More() {
		var p pkgInfo
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// lookupLayer returns the relative path and layer assignment for an absolute
// import path under repoModule. Subpackages inherit their nearest ancestor's
// layer. Returns ok=false for paths outside the repo.
func lookupLayer(layerOf map[string]string, absPath string) (rel, layer string, ok bool) {
	r, ok := strings.CutPrefix(absPath, repoModule+"/")
	if !ok && absPath != repoModule {
		return "", "", false
	}
	rel = r
	walk := rel
	for {
		if l, ok := layerOf[walk]; ok {
			return rel, l, true
		}
		idx := strings.LastIndex(walk, "/")
		if idx < 0 {
			return "", "", false
		}
		walk = walk[:idx]
	}
}
