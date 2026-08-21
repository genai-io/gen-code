package app

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/genai-io/san/internal/app/input"
)

// recentLearnCap bounds the rolling self-learning activity log surfaced in the
// /evolve RECENT zones. Older events fall off the front.
const recentLearnCap = 20

// RecentLearnEvent is one logged self-learning write plus the wall-clock time
// it landed, for the RECENT activity recap.
type RecentLearnEvent struct {
	ReviewAction
	At time.Time
}

// RecentLearnLog is a small mutex-guarded ring of recent self-learning writes.
// The per-session write observers append to it; the /evolve panels read a
// snapshot at open time. It outlives any one session (allocated once at
// services construction), so the recap survives /clear like the Indicator does.
type RecentLearnLog struct {
	mu     sync.Mutex
	events []RecentLearnEvent
}

// NewRecentLearnLog returns an empty log.
func NewRecentLearnLog() *RecentLearnLog { return &RecentLearnLog{} }

// Add appends one event, evicting the oldest past the cap. at is the moment it
// landed; the panel humanizes it to "2m ago" at read time.
func (l *RecentLearnLog) Add(act ReviewAction, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, RecentLearnEvent{ReviewAction: act, At: at})
	if len(l.events) > recentLearnCap {
		l.events = l.events[len(l.events)-recentLearnCap:]
	}
}

// recent returns the logged events newest first (the ring is already capped
// at recentLearnCap by Add).
func (l *RecentLearnLog) recent() []RecentLearnEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]RecentLearnEvent, 0, len(l.events))
	for _, v := range slices.Backward(l.events) {
		out = append(out, v)
	}
	return out
}

// newRecentLearnAccessor returns the /evolve RECENT-zone accessor: a snapshot
// of the log's events as input-layer DTOs with their age humanized against the
// call time.
func newRecentLearnAccessor(log *RecentLearnLog) func() []input.LearnEvent {
	return func() []input.LearnEvent {
		now := time.Now()
		evs := log.recent()
		out := make([]input.LearnEvent, 0, len(evs))
		for _, e := range evs {
			out = append(out, input.LearnEvent{
				Kind:   e.Kind,
				Verb:   e.Verb,
				Target: e.Target,
				Note:   e.Note,
				Ago:    humanizeAgo(now.Sub(e.At)),
			})
		}
		return out
	}
}

// humanizeAgo renders a duration as a compact "… ago" clause for the recap.
func humanizeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
