package cron

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultLoopInterval = "10m"

var (
	loopLeadingIntervalRe = regexp.MustCompile(`^(\d+)([smhd])$`)
	loopTrailingEveryRe   = regexp.MustCompile(`(?i)^(.*?)(?:\s+every\s+)(\d+)\s*(s|sec|secs|second|seconds|m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)\s*$`)
	loopTrailingInRe      = regexp.MustCompile(`(?i)^(.*?)(?:\s+in\s+)(\d+)\s*(s|sec|secs|second|seconds|m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)\s*$`)
)

type LoopCommand struct {
	Prompt string
	Cron   string
	Human  string
	Note   string
}

type loopCadence struct {
	cronExpr string
	label    string
	note     string
}

type loopIntervalSpec struct {
	Value     int
	Unit      string
	Requested string
}

func ParseLoopCommand(args string, now time.Time) (LoopCommand, error) {
	input := strings.TrimSpace(args)
	if input == "" {
		return LoopCommand{}, fmt.Errorf("empty loop command")
	}

	if matches := loopTrailingEveryRe.FindStringSubmatch(input); len(matches) == 4 {
		interval, err := normalizeLoopInterval(matches[2], matches[3])
		if err != nil {
			return LoopCommand{}, err
		}
		return buildLoopCommand(strings.TrimSpace(matches[1]), interval, now)
	}

	parts := strings.Fields(input)
	if len(parts) == 0 {
		return LoopCommand{}, fmt.Errorf("empty loop command")
	}
	if loopLeadingIntervalRe.MatchString(parts[0]) {
		return buildLoopCommand(strings.TrimSpace(strings.Join(parts[1:], " ")), parts[0], now)
	}

	return buildLoopCommand(input, DefaultLoopInterval, now)
}

func buildLoopCommand(prompt, interval string, now time.Time) (LoopCommand, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return LoopCommand{}, fmt.Errorf("empty prompt")
	}
	cadence, err := loopIntervalToCadence(interval, now)
	if err != nil {
		return LoopCommand{}, err
	}
	return LoopCommand{
		Prompt: prompt,
		Cron:   cadence.cronExpr,
		Human:  cadence.label,
		Note:   cadence.note,
	}, nil
}

func ParseLoopOnceCommand(args string, now time.Time) (LoopCommand, error) {
	input := strings.TrimSpace(args)
	if input == "" {
		return LoopCommand{}, fmt.Errorf("empty once command")
	}

	if matches := loopTrailingInRe.FindStringSubmatch(input); len(matches) == 4 {
		interval, err := normalizeLoopInterval(matches[2], matches[3])
		if err != nil {
			return LoopCommand{}, err
		}
		return buildLoopOnceCommand(strings.TrimSpace(matches[1]), interval, now)
	}

	parts := strings.Fields(input)
	if len(parts) == 0 {
		return LoopCommand{}, fmt.Errorf("empty once command")
	}
	if loopLeadingIntervalRe.MatchString(parts[0]) {
		return buildLoopOnceCommand(strings.TrimSpace(strings.Join(parts[1:], " ")), parts[0], now)
	}

	return LoopCommand{}, fmt.Errorf("invalid once command")
}

func buildLoopOnceCommand(prompt, interval string, now time.Time) (LoopCommand, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return LoopCommand{}, fmt.Errorf("empty prompt")
	}

	spec, err := parseLoopIntervalSpec(interval)
	if err != nil {
		return LoopCommand{}, err
	}
	minutes := intervalSpecToMinutes(spec)
	target := now.Add(time.Duration(minutes) * time.Minute).Truncate(time.Minute)

	note := ""
	if spec.Unit == "s" {
		note = fmt.Sprintf(" Rounded `%s` to `%s`.", spec.Requested, humanizeMinutes(minutes))
	}

	return LoopCommand{
		Prompt: prompt,
		Cron:   fmt.Sprintf("%d %d %d %d *", target.Minute(), target.Hour(), target.Day(), int(target.Month())),
		Human:  fmt.Sprintf("once at %s", target.Format("2006-01-02 15:04")),
		Note:   note,
	}, nil
}

func normalizeLoopInterval(value, unit string) (string, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("invalid interval")
	}
	switch strings.ToLower(unit) {
	case "s", "sec", "secs", "second", "seconds":
		return fmt.Sprintf("%ds", n), nil
	case "m", "min", "mins", "minute", "minutes":
		return fmt.Sprintf("%dm", n), nil
	case "h", "hr", "hrs", "hour", "hours":
		return fmt.Sprintf("%dh", n), nil
	case "d", "day", "days":
		return fmt.Sprintf("%dd", n), nil
	default:
		return "", fmt.Errorf("invalid interval unit")
	}
}

func parseLoopIntervalSpec(interval string) (loopIntervalSpec, error) {
	matches := loopLeadingIntervalRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(interval)))
	if len(matches) != 3 {
		return loopIntervalSpec{}, fmt.Errorf("invalid interval")
	}

	value, err := strconv.Atoi(matches[1])
	if err != nil || value <= 0 {
		return loopIntervalSpec{}, fmt.Errorf("invalid interval")
	}

	return loopIntervalSpec{
		Value:     value,
		Unit:      matches[2],
		Requested: matches[0],
	}, nil
}

func loopIntervalToCadence(interval string, now time.Time) (loopCadence, error) {
	spec, err := parseLoopIntervalSpec(interval)
	if err != nil {
		return loopCadence{}, err
	}

	requestedMinutes := intervalSpecToMinutes(spec)
	effectiveMinutes := requestedMinutes
	switch {
	case requestedMinutes < 60:
		effectiveMinutes = nearestAllowed(requestedMinutes, []int{1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30, 60})
	case requestedMinutes < 24*60:
		effectiveMinutes = nearestAllowed(requestedMinutes, []int{60, 120, 180, 240, 360, 480, 720, 1440})
	default:
		var allowed []int
		for d := 1; d <= 31; d++ {
			allowed = append(allowed, d*24*60)
		}
		effectiveMinutes = nearestAllowed(requestedMinutes, allowed)
	}

	cadence := cadenceFromMinutes(effectiveMinutes, now)
	if requestedMinutes != effectiveMinutes {
		cadence.note = fmt.Sprintf(" Rounded `%s` to `%s`.", spec.Requested, humanizeMinutes(effectiveMinutes))
	}
	if spec.Unit == "s" && requestedMinutes == effectiveMinutes {
		cadence.note = fmt.Sprintf(" Rounded `%s` to `%s`.", spec.Requested, humanizeMinutes(effectiveMinutes))
	}
	return cadence, nil
}

func intervalSpecToMinutes(spec loopIntervalSpec) int {
	switch spec.Unit {
	case "s":
		minutes := max((spec.Value+59)/60, 1)
		return minutes
	case "m":
		return spec.Value
	case "h":
		return spec.Value * 60
	case "d":
		return spec.Value * 24 * 60
	default:
		return spec.Value
	}
}

func nearestAllowed(requested int, allowed []int) int {
	best := allowed[0]
	bestDelta := absInt(requested - best)
	for _, candidate := range allowed[1:] {
		delta := absInt(requested - candidate)
		if delta < bestDelta || (delta == bestDelta && candidate > best) {
			best = candidate
			bestDelta = delta
		}
	}
	return best
}

func cadenceFromMinutes(totalMinutes int, now time.Time) loopCadence {
	if totalMinutes < 60 {
		return loopCadence{
			cronExpr: fmt.Sprintf("*/%d * * * *", totalMinutes),
			label:    humanizeMinutes(totalMinutes),
		}
	}

	minute := chooseLoopScheduleMinute(now.Minute())
	if totalMinutes < 24*60 {
		hours := totalMinutes / 60
		hourField := "*"
		if hours > 1 {
			start := now.Hour() % hours
			if start == 0 {
				hourField = fmt.Sprintf("*/%d", hours)
			} else {
				hourField = fmt.Sprintf("%d-23/%d", start, hours)
			}
		}
		return loopCadence{
			cronExpr: fmt.Sprintf("%d %s * * *", minute, hourField),
			label:    humanizeMinutes(totalMinutes),
		}
	}

	days := totalMinutes / (24 * 60)
	hour := now.Hour()
	if days <= 1 {
		return loopCadence{
			cronExpr: fmt.Sprintf("%d %d * * *", minute, hour),
			label:    humanizeMinutes(totalMinutes),
		}
	}
	return loopCadence{
		cronExpr: fmt.Sprintf("%d %d */%d * *", minute, hour, days),
		label:    humanizeMinutes(totalMinutes),
	}
}

func chooseLoopScheduleMinute(minute int) int {
	switch minute {
	case 0:
		return 7
	case 30:
		return 37
	default:
		return minute
	}
}

func humanizeMinutes(totalMinutes int) string {
	switch {
	case totalMinutes < 60:
		return fmt.Sprintf("every %d minute(s)", totalMinutes)
	case totalMinutes%(24*60) == 0:
		return fmt.Sprintf("every %d day(s)", totalMinutes/(24*60))
	default:
		return fmt.Sprintf("every %d hour(s)", totalMinutes/60)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
