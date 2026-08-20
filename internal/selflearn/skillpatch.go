package selflearn

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// applyPatch performs a targeted find-and-replace of oldText with newText in
// content, using a pragmatic fuzzy-match chain (Phase 1 subset of the chain
// described in notes/active/l1-background-review.md §5.3):
//
//  1. exact substring
//  2. line-trimmed (ignore leading/trailing whitespace per line)
//  3. whitespace-collapsed (collapse internal runs of whitespace per line)
//
// Block-anchor and context-similarity tiers are deferred. An escape-drift guard
// rejects matches where the model sent transport-added backslash escapes (\' \")
// that don't exist in the file, prompting a clean re-read. A match must be
// unique unless replaceAll.
func applyPatch(content, oldText, newText string, replaceAll bool) (string, error) {
	if oldText == "" {
		return "", fmt.Errorf("old_text cannot be empty")
	}
	if err := escapeDriftGuard(content, oldText); err != nil {
		return "", err
	}

	// Tier 1 — exact substring.
	if n := strings.Count(content, oldText); n > 0 {
		if n > 1 && !replaceAll {
			return "", fmt.Errorf("old_text matches %d places; add surrounding context or set replace_all", n)
		}
		if replaceAll {
			return strings.ReplaceAll(content, oldText, newText), nil
		}
		return strings.Replace(content, oldText, newText, 1), nil
	}

	// Tiers 2 & 3 — line-window matching with progressively looser comparison.
	for _, norm := range []func(string) string{strings.TrimSpace, collapseWS} {
		if out, ok, err := lineWindowReplace(content, oldText, newText, replaceAll, norm); err != nil {
			return "", err
		} else if ok {
			return out, nil
		}
	}

	return "", fmt.Errorf("old_text not found (after exact, line-trimmed, and whitespace-normalized matching); re-read the skill and retry with current text")
}

var wsRun = regexp.MustCompile(`\s+`)

func collapseWS(s string) string {
	return strings.TrimSpace(wsRun.ReplaceAllString(s, " "))
}

// lineWindowReplace slides a window the size of oldText's line count over
// content, comparing each line under norm. It replaces matched windows with
// newText's lines. Returns (out, matched, error); matched=false means no window
// matched under this normalizer (try the next tier).
func lineWindowReplace(content, oldText, newText string, replaceAll bool, norm func(string) string) (string, bool, error) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldText, "\n")
	w := len(oldLines)
	if w == 0 || w > len(contentLines) {
		return "", false, nil
	}

	normOld := make([]string, w)
	for i, l := range oldLines {
		normOld[i] = norm(l)
	}
	// Hoist norm() out of the inner window loop — a 500-line file with a
	// 5-line pattern would otherwise normalize each content line up to 5
	// times, and the cost is real when norm is collapseWS (regex pass per
	// line). One pass over contentLines keeps the comparison cheap.
	normContent := make([]string, len(contentLines))
	for i, l := range contentLines {
		normContent[i] = norm(l)
	}

	var starts []int
	for i := 0; i+w <= len(contentLines); {
		match := true
		for j := range w {
			if normContent[i+j] != normOld[j] {
				match = false
				break
			}
		}
		// Collect non-overlapping matches only (mirrors strings.Count in the
		// exact tier): advance past the whole window on a hit so a
		// self-overlapping pattern can't be counted — and then double-applied
		// by the backward replace below — twice. len(starts) > 1 with
		// !replaceAll is the ambiguity error rejected below.
		if match {
			starts = append(starts, i)
			i += w
		} else {
			i++
		}
	}
	if len(starts) == 0 {
		return "", false, nil
	}
	if len(starts) > 1 && !replaceAll {
		return "", false, fmt.Errorf("old_text matches %d places (fuzzy); add surrounding context or set replace_all", len(starts))
	}

	newLines := strings.Split(newText, "\n")
	// Replace from the last match backward so earlier indices stay valid.
	out := contentLines
	for _, s := range slices.Backward(starts) {

		out = append(out[:s], append(append([]string{}, newLines...), out[s+w:]...)...)
	}
	return strings.Join(out, "\n"), true, nil
}

// escapeDriftGuard rejects an oldText that carries backslash-escaped quotes
// (\' or \") that don't actually appear in the file — a sign the transport
// layer added escapes the source never had, which would make every tier miss.
func escapeDriftGuard(content, oldText string) error {
	for _, esc := range []string{`\'`, `\"`} {
		if strings.Contains(oldText, esc) && !strings.Contains(content, esc) {
			return fmt.Errorf("old_text contains escaped quotes (%s) not present in the file; re-read the skill and send the literal text", esc)
		}
	}
	return nil
}
