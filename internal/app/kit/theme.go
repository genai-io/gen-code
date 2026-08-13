package kit

import (
	"fmt"
	"os"

	"charm.land/lipgloss/v2"
)

// AdaptiveColor picks a Light or Dark color based on the detected terminal
// background. It implements color.Color (resolving at render time against the
// global background mode), so it drops into lipgloss styles directly — it
// replaces the lipgloss.AdaptiveColor that v1 provided and v2 removed.
type AdaptiveColor struct {
	Light, Dark string
}

// RGBA implements color.Color by resolving to the Light or Dark variant.
func (a AdaptiveColor) RGBA() (r, g, b, alpha uint32) {
	hex := a.Light
	if IsDarkBackground() {
		hex = a.Dark
	}
	return lipgloss.Color(hex).RGBA()
}

type Theme struct {
	Muted     AdaptiveColor
	Accent    AdaptiveColor
	Primary   AdaptiveColor
	AI        AdaptiveColor
	Separator AdaptiveColor

	// Focus is the single vivid brand accent (teal). It is reserved for the
	// "you are here" affordances — the selected-row bar, the input caret, the
	// active tab — so focus reads consistently and the brand mark from the
	// splash carries into the live UI. Everything else stays in the cool-gray
	// ramp; Focus is the only saturated hue, used sparingly.
	Focus AdaptiveColor

	Text         AdaptiveColor
	TextDim      AdaptiveColor
	TextBright   AdaptiveColor
	TextDisabled AdaptiveColor

	Success   AdaptiveColor
	Error     AdaptiveColor
	Warning   AdaptiveColor
	SuccessBg AdaptiveColor
	ErrorBg   AdaptiveColor

	// The *BgStrong pair marks the changed span inside a diff row — a step
	// more saturated than SuccessBg/ErrorBg so the eye lands on the words
	// that actually changed within an added/removed line.
	SuccessBgStrong AdaptiveColor
	ErrorBgStrong   AdaptiveColor

	// Yolo tints the unrestricted-mode indicator. It is deliberately not
	// Error: YOLO mode is a choice the user made, not something that went
	// wrong, and violet keeps red meaning "failure".
	Yolo AdaptiveColor

	Border     AdaptiveColor
	Background AdaptiveColor
}

var CurrentTheme = Theme{
	Muted:     AdaptiveColor{Dark: "#7B8696", Light: "#6B7280"},
	Accent:    AdaptiveColor{Dark: "#9DB5D4", Light: "#64748B"},
	Primary:   AdaptiveColor{Dark: "#D0DFEF", Light: "#475569"},
	AI:        AdaptiveColor{Dark: "#B0C4E0", Light: "#64748B"},
	Separator: AdaptiveColor{Dark: "#4E6580", Light: "#CBD5E1"},

	Focus: AdaptiveColor{Dark: "#46E8C0", Light: "#0D9488"},

	Text:         AdaptiveColor{Dark: "#DDDEE2", Light: "#18181B"},
	TextDim:      AdaptiveColor{Dark: "#A8AEBB", Light: "#71717A"},
	TextBright:   AdaptiveColor{Dark: "#FAFAFA", Light: "#09090B"},
	TextDisabled: AdaptiveColor{Dark: "#52525B", Light: "#A1A1AA"},

	Success:   AdaptiveColor{Dark: "#86EFAC", Light: "#15803D"},
	Error:     AdaptiveColor{Dark: "#FCA5A5", Light: "#B91C1C"},
	Warning:   AdaptiveColor{Dark: "#FCD34D", Light: "#B45309"},
	SuccessBg: AdaptiveColor{Dark: "#16281d", Light: "#DCFCE7"},
	ErrorBg:   AdaptiveColor{Dark: "#2b1818", Light: "#FEE2E2"},

	SuccessBgStrong: AdaptiveColor{Dark: "#2F5D3E", Light: "#A7F3D0"},
	ErrorBgStrong:   AdaptiveColor{Dark: "#5E2A2A", Light: "#FCB5B5"},

	Yolo: AdaptiveColor{Dark: "#C4B5FD", Light: "#6D28D9"},

	Border:     AdaptiveColor{Dark: "#52525B", Light: "#D4D4D8"},
	Background: AdaptiveColor{Dark: "#18181B", Light: "#FAFAFA"},
}

// darkModeVal caches the resolved background mode. It defaults to dark and is
// updated by InitTheme. IsDarkBackground stays cheap (it is consulted for every
// styled color), so it never performs a live terminal query — that happens once
// in InitTheme for the "auto" theme.
var darkModeVal = true

func InitTheme(t string) {
	switch t {
	case "light":
		darkModeVal = false
	case "dark":
		darkModeVal = true
	case "auto":
		darkModeVal = detectDarkBackground()
	default:
		return
	}
}

// detectDarkBackground queries the terminal for its background color. lipgloss
// v2 dropped the global SetHasDarkBackground/HasDarkBackground accessors in
// favor of an explicit query; default to dark if the terminal doesn't answer.
func detectDarkBackground() bool {
	return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
}

// ResolveTheme ensures a theme is configured.
// If configuredTheme is empty, it opens the interactive selector.
// Returns true if the user quit the selector without choosing.
func ResolveTheme(configuredTheme string, saveTheme func(string) error) (userQuit bool, err error) {
	if configuredTheme == "" {
		chosen, err := RunThemeSelector()
		if err != nil {
			return false, fmt.Errorf("theme selection failed: %w", err)
		}
		if chosen == "" {
			return true, nil
		}
		configuredTheme = chosen
		_ = saveTheme(configuredTheme)
	}
	InitTheme(configuredTheme)
	return false, nil
}

func IsDarkBackground() bool {
	return darkModeVal
}
