package selflearn

import (
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
)

func endTurn(toolUses int) core.Result {
	return core.Result{StopReason: core.StopEndTurn, ToolUses: toolUses}
}

type fireRec struct {
	kind       ReviewKind
	skillPerms SkillPermissions
}

// newTestReviewer wires a Reviewer whose review callback records the fired
// (kind, skill permissions) into a buffered channel.
func newTestReviewer(cfg Config) (*Reviewer, chan fireRec) {
	fired := make(chan fireRec, 8)
	r := New(cfg, func(k ReviewKind, p SkillPermissions, _ []core.Message) { fired <- fireRec{k, p} })
	return r, fired
}

// waitFire returns the next fired review, or fails after timeout.
func waitFire(t *testing.T, fired <-chan fireRec) fireRec {
	t.Helper()
	select {
	case f := <-fired:
		return f
	case <-time.After(time.Second):
		t.Fatal("expected a review to fire, none did")
		return fireRec{}
	}
}

// assertNoFire fails if a review fires within a short window.
func assertNoFire(t *testing.T, fired <-chan fireRec) {
	t.Helper()
	select {
	case f := <-fired:
		t.Fatalf("expected no review, but %v fired", f.kind)
	case <-time.After(80 * time.Millisecond):
	}
}

// mustStart waits for a review goroutine to signal start, failing fast instead
// of blocking forever (and tripping the 10-minute package timeout) if none does.
func mustStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected a review to start, none did")
	}
}

// waitInFlightClear blocks until no review is in flight. inFlight is cleared in
// the review goroutine's defer, *after* the callback returns — so synchronizing
// on a callback side effect (e.g. the fired channel) does not imply it has been
// cleared yet. Tests that fire a follow-up review must wait here first, or they
// race the reset and the follow-up is dropped as "still in flight".
func waitInFlightClear(t *testing.T, r *Reviewer) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		r.mu.Lock()
		inFlight := r.inFlight
		r.mu.Unlock()
		if !inFlight {
			return
		}
		select {
		case <-deadline:
			t.Fatal("review still in flight after 1s")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestReviewKindString covers the log-friendly labels surfaced to the
// wire-up's review-summary log entry.
func TestReviewKindString(t *testing.T) {
	cases := map[ReviewKind]string{
		0:                       "none",
		KindMemory:              "memory",
		KindSkills:              "skill",
		KindMemory | KindSkills: "memory+skill",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("kind %b: got %q, want %q", k, got, want)
		}
	}
}

// TestMemoryFiresOnRequest: memory fires only when the model requests a review
// (evolveRequested) and memory is enabled — there is no cadence fallback.
func TestMemoryFiresOnRequest(t *testing.T) {
	r, fired := newTestReviewer(Config{MemoryEnabled: true})

	r.Observe(endTurn(0), false, false) // no request
	r.Observe(endTurn(0), true, false)  // still no request
	assertNoFire(t, fired)
	r.Observe(endTurn(0), false, true) // requested
	if f := waitFire(t, fired); !f.kind.Has(KindMemory) || f.kind.Has(KindSkills) {
		t.Fatalf("want memory-only, got %v", f.kind)
	}
}

// TestSkillsScopedByUse: on a requested turn the skills pass is scoped by the
// objective fact of skill use — skill-free → create, skill-use → update+delete.
func TestSkillsScopedByUse(t *testing.T) {
	cfg := Config{Skills: AllowAllSkillActions()}

	t.Run("skill-free request scopes to create", func(t *testing.T) {
		r, fired := newTestReviewer(cfg)
		r.Observe(endTurn(1), false, true)
		if f := waitFire(t, fired); f.skillPerms != (SkillPermissions{AllowCreate: true}) {
			t.Fatalf("got %+v, want create-only", f.skillPerms)
		}
	})
	t.Run("skill-use request scopes to update+delete", func(t *testing.T) {
		r, fired := newTestReviewer(cfg)
		r.Observe(endTurn(1), true, true)
		if f := waitFire(t, fired); f.skillPerms != (SkillPermissions{AllowUpdate: true, AllowDelete: true}) {
			t.Fatalf("got %+v, want update+delete", f.skillPerms)
		}
	})
}

// TestNoRequestNeverFires: without evolveRequested nothing fires, however much
// work happened — the model's call is the only trigger.
func TestNoRequestNeverFires(t *testing.T) {
	r, fired := newTestReviewer(Config{
		MemoryEnabled: true,
		Skills:        AllowAllSkillActions(),
	})
	for range 10 {
		r.Observe(endTurn(5), false, false)
		r.Observe(endTurn(1), true, false)
	}
	assertNoFire(t, fired)
}

// TestRequestBoundedByPermissions: a skill-use request with update/delete denied
// yields nothing due; a skill-free request still creates.
func TestRequestBoundedByPermissions(t *testing.T) {
	r, fired := newTestReviewer(Config{Skills: SkillPermissions{AllowCreate: true}})
	r.Observe(endTurn(1), true, true) // skill used, update/delete denied → nothing to do
	assertNoFire(t, fired)
	r.Observe(endTurn(1), false, true) // skill-free → create allowed
	if f := waitFire(t, fired); f.skillPerms != (SkillPermissions{AllowCreate: true}) {
		t.Fatalf("got %+v, want create-only", f.skillPerms)
	}
}

// TestCombinedFiresBothArms: a requested turn with memory on and skills active
// fires both arms.
func TestCombinedFiresBothArms(t *testing.T) {
	r, fired := newTestReviewer(Config{
		MemoryEnabled: true,
		Skills:        SkillPermissions{AllowCreate: true, AllowUpdate: true},
	})
	r.Observe(endTurn(1), false, true)
	f := waitFire(t, fired)
	if !f.kind.Has(KindMemory) || !f.kind.Has(KindSkills) {
		t.Fatalf("want combined, got %v", f.kind)
	}
	if !f.skillPerms.AllowCreate {
		t.Fatalf("skill-free turn should be a create pass, got %+v", f.skillPerms)
	}
}

// TestSkipsNonEndTurn: cancelled / max-steps turns never fire, even when the
// model requested a review.
func TestSkipsNonEndTurn(t *testing.T) {
	r, fired := newTestReviewer(Config{MemoryEnabled: true})

	r.Observe(core.Result{StopReason: core.StopCanceled}, false, true)
	r.Observe(core.Result{StopReason: core.StopMaxSteps}, false, true)
	assertNoFire(t, fired)

	r.Observe(endTurn(0), false, true)
	if f := waitFire(t, fired); !f.kind.Has(KindMemory) {
		t.Fatalf("want memory, got %v", f.kind)
	}
}

// TestSingleFlightDropsConcurrent: a request arriving while a prior review runs
// is dropped (not queued). Once it clears, a fresh request fires normally.
func TestSingleFlightDropsConcurrent(t *testing.T) {
	fired := make(chan fireRec, 4)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	r := New(Config{MemoryEnabled: true}, func(k ReviewKind, p SkillPermissions, _ []core.Message) {
		started <- struct{}{}
		<-release // block until released → keeps the review in-flight
		fired <- fireRec{k, p}
	})

	r.Observe(endTurn(0), false, true) // fires review #1 (now in-flight, blocked)
	mustStart(t, started)

	r.Observe(endTurn(0), false, true) // arrives while #1 in-flight → dropped
	select {
	case <-started:
		t.Fatal("a second review started while one was in flight")
	case <-time.After(80 * time.Millisecond):
	}

	close(release) // let #1 finish
	if f := waitFire(t, fired); !f.kind.Has(KindMemory) {
		t.Fatalf("want memory, got %v", f.kind)
	}
	waitInFlightClear(t, r)

	// A fresh request after the prior clears fires normally.
	r.Observe(endTurn(0), false, true)
	mustStart(t, started)
	if f := waitFire(t, fired); !f.kind.Has(KindMemory) {
		t.Fatalf("retry: want memory, got %v", f.kind)
	}
}

func TestConfigEnabled(t *testing.T) {
	if (Config{}).Enabled() {
		t.Fatal("empty config should be disabled")
	}
	if !(Config{Skills: SkillPermissions{AllowCreate: true}}).Enabled() {
		t.Fatal("skills-on should be enabled")
	}
}
