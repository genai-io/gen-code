// Package cron provides cron expression parsing, next-fire calculation,
// and a scheduler for recurring/one-shot prompt injection.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// field represents a parsed cron field with the set of valid values.
type field struct {
	values map[int]bool // allowed values for this field
}

// expression represents a parsed 5-field cron expression:
// minute hour day-of-month month day-of-week
type expression struct {
	Raw    string
	Minute field
	Hour   field
	DOM    field // day of month
	Month  field
	DOW    field // day of week (0=Sunday)
}

// fieldSpec defines the range for a cron field.
type fieldSpec struct {
	min, max int
	names    map[string]int // optional name aliases (e.g., "jan"→1)
}

var specs = [5]fieldSpec{
	{0, 59, nil}, // minute
	{0, 23, nil}, // hour
	{1, 31, nil}, // day of month
	{1, 12, map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}}, // month
	{0, 6, map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}},                                                       // day of week
}

// parse parses a standard 5-field cron expression.
func parse(expr string) (*expression, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d", len(fields))
	}

	parsed := make([]field, 5)
	for i, f := range fields {
		p, err := parseField(f, specs[i])
		if err != nil {
			return nil, fmt.Errorf("cron field %d (%q): %w", i, f, err)
		}
		parsed[i] = p
	}

	return &expression{
		Raw:    expr,
		Minute: parsed[0],
		Hour:   parsed[1],
		DOM:    parsed[2],
		Month:  parsed[3],
		DOW:    parsed[4],
	}, nil
}

// parseField parses a single cron field (e.g., "*/5", "1,3,5", "1-10/2", "*").
func parseField(raw string, spec fieldSpec) (field, error) {
	f := field{values: make(map[int]bool)}

	for part := range strings.SplitSeq(raw, ",") {
		if err := parsePart(part, spec, f.values); err != nil {
			return f, err
		}
	}
	return f, nil
}

func parsePart(part string, spec fieldSpec, values map[int]bool) error {
	// Replace name aliases
	lower := strings.ToLower(part)
	for name, val := range spec.names {
		lower = strings.ReplaceAll(lower, name, strconv.Itoa(val))
	}
	part = lower

	// Handle step: */2, 1-10/3
	step := 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		s, err := strconv.Atoi(part[idx+1:])
		if err != nil || s <= 0 {
			return fmt.Errorf("invalid step: %q", part)
		}
		step = s
		part = part[:idx]
	}

	// Handle wildcard
	if part == "*" {
		for i := spec.min; i <= spec.max; i += step {
			values[i] = true
		}
		return nil
	}

	// Handle range: 1-5
	if idx := strings.Index(part, "-"); idx >= 0 {
		lo, err := strconv.Atoi(part[:idx])
		if err != nil {
			return fmt.Errorf("invalid range start: %q", part)
		}
		hi, err := strconv.Atoi(part[idx+1:])
		if err != nil {
			return fmt.Errorf("invalid range end: %q", part)
		}
		if lo < spec.min || hi > spec.max || lo > hi {
			return fmt.Errorf("range %d-%d out of bounds [%d, %d]", lo, hi, spec.min, spec.max)
		}
		for i := lo; i <= hi; i += step {
			values[i] = true
		}
		return nil
	}

	// Single value
	v, err := strconv.Atoi(part)
	if err != nil {
		return fmt.Errorf("invalid value: %q", part)
	}
	if v < spec.min || v > spec.max {
		return fmt.Errorf("value %d out of bounds [%d, %d]", v, spec.min, spec.max)
	}
	values[v] = true
	return nil
}

// nextAfter returns the next time the expression fires after the given time.
// It searches up to 4 years ahead to avoid infinite loops.
func (e *expression) nextAfter(after time.Time) time.Time {
	// Start from the next minute
	t := after.Truncate(time.Minute).Add(time.Minute)
	deadline := after.Add(4 * 365 * 24 * time.Hour)

	for t.Before(deadline) {
		if !e.Month.values[int(t.Month())] {
			// Advance to next valid month
			t = advanceMonth(t)
			continue
		}
		if !e.DOM.values[t.Day()] || !e.DOW.values[int(t.Weekday())] {
			t = t.Add(24 * time.Hour)
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			continue
		}
		if !e.Hour.values[t.Hour()] {
			t = t.Add(time.Hour)
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
			continue
		}
		if !e.Minute.values[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{} // no match found
}

// advanceMonth jumps to the 1st of the next month at 00:00.
func advanceMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	if m == time.December {
		return time.Date(y+1, time.January, 1, 0, 0, 0, 0, t.Location())
	}
	return time.Date(y, m+1, 1, 0, 0, 0, 0, t.Location())
}

// Describe returns a human-readable description of a cron expression.
func Describe(expr string) string {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return expr
	}
	min, hour, dom, mon, dow := fields[0], fields[1], fields[2], fields[3], fields[4]

	// Common patterns
	switch {
	case min == "*" && hour == "*" && dom == "*" && mon == "*" && dow == "*":
		return "every minute"
	case strings.HasPrefix(min, "*/") && hour == "*" && dom == "*" && mon == "*" && dow == "*":
		n := strings.TrimPrefix(min, "*/")
		if n == "1" {
			return "every minute"
		}
		return "every " + n + " minutes"
	case hour == "*" && dom == "*" && mon == "*" && dow == "*":
		return "at minute " + min + " of every hour"
	case strings.HasPrefix(hour, "*/") && dom == "*" && mon == "*" && dow == "*":
		n := strings.TrimPrefix(hour, "*/")
		if n == "1" {
			return "every hour at :" + min
		}
		return "every " + n + " hours at :" + min
	case dom == "*" && mon == "*" && dow == "*":
		return "daily at " + hour + ":" + min
	case strings.HasPrefix(dom, "*/") && mon == "*" && dow == "*":
		n := strings.TrimPrefix(dom, "*/")
		return "every " + n + " days at " + hour + ":" + min
	default:
		return expr
	}
}
