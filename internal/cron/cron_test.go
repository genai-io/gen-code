package cron

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func Test_parse(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr bool
	}{
		{"*/5 * * * *", false},
		{"0 9 * * 1-5", false},
		{"30 14 28 2 *", false},
		{"0 */2 * * *", false},
		{"* * * * *", false},
		{"0 0 */3 * *", false},
		{"bad", true},
		{"1 2 3 4 5 6", true},
	}

	for _, tt := range tests {
		_, err := parse(tt.expr)
		if (err != nil) != tt.wantErr {
			t.Errorf("parse(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
		}
	}
}

func TestNextAfter(t *testing.T) { //nolint:revive // test name matches method
	// Every 5 minutes
	expr, _ := parse("*/5 * * * *")
	base := time.Date(2026, 3, 31, 10, 3, 0, 0, time.Local)
	next := expr.nextAfter(base)
	if next.Minute() != 5 {
		t.Errorf("expected minute 5, got %d (time: %v)", next.Minute(), next)
	}

	// Specific time: 9:30 weekdays
	expr2, _ := parse("30 9 * * 1-5")
	// Start on a Monday at 10:00
	monday := time.Date(2026, 3, 30, 10, 0, 0, 0, time.Local) // Monday
	next2 := expr2.nextAfter(monday)
	// Should be Tuesday 9:30
	if next2.Weekday() != time.Tuesday || next2.Hour() != 9 || next2.Minute() != 30 {
		t.Errorf("expected Tue 9:30, got %v", next2)
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"*/5 * * * *", "every 5 minutes"},
		{"*/1 * * * *", "every minute"},
		{"* * * * *", "every minute"},
		{"0 */2 * * *", "every 2 hours at :0"},
		{"0 0 */3 * *", "every 3 days at 0:0"},
	}

	for _, tt := range tests {
		got := Describe(tt.expr)
		if got != tt.want {
			t.Errorf("Describe(%q) = %q, want %q", tt.expr, got, tt.want)
		}
	}
}

func TestStoreCreateAndList(t *testing.T) {
	store := NewScheduler()
	job, err := store.Create("*/5 * * * *", "test prompt", true, false)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if job.ID == "" {
		t.Error("expected non-empty ID")
	}

	jobs := store.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Prompt != "test prompt" {
		t.Errorf("expected prompt 'test prompt', got %q", jobs[0].Prompt)
	}
}

func TestStoreDelete(t *testing.T) {
	store := NewScheduler()
	job, _ := store.Create("*/5 * * * *", "to delete", true, false)
	if err := store.Delete(job.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if len(store.List()) != 0 {
		t.Error("expected 0 jobs after delete")
	}
}

func TestStoreTick(t *testing.T) {
	store := NewScheduler()
	// Create a one-shot job
	job, _ := store.Create("* * * * *", "fire me", false, false)

	// Force next fire to past
	store.mu.Lock()
	store.jobs[job.ID].NextFire = time.Now().Add(-time.Minute)
	store.mu.Unlock()

	fired := store.Tick()
	if len(fired) != 1 {
		t.Fatalf("expected 1 fired job, got %d", len(fired))
	}
	if fired[0].Prompt != "fire me" {
		t.Errorf("expected prompt 'fire me', got %q", fired[0].Prompt)
	}

	// One-shot should be deleted after firing
	if len(store.List()) != 0 {
		t.Error("expected 0 jobs after one-shot fired")
	}
}

func TestStore_maxJobs(t *testing.T) {
	store := NewScheduler()
	for i := range maxJobs {
		_, err := store.Create("*/5 * * * *", "job", true, false)
		if err != nil {
			t.Fatalf("Create %d failed: %v", i, err)
		}
	}
	_, err := store.Create("*/5 * * * *", "one too many", true, false)
	if err == nil {
		t.Error("expected error when exceeding MaxJobs")
	}
}

func TestCron_InvalidExpression_ReturnsError(t *testing.T) {
	invalidExpressions := []struct {
		expr string
		desc string
	}{
		{"bad", "single word"},
		{"1 2 3 4 5 6", "six fields"},
		{"60 * * * *", "minute out of range"},
		{"* 25 * * *", "hour out of range"},
		{"", "empty string"},
		{"* * * *", "four fields only"},
	}

	for _, tt := range invalidExpressions {
		t.Run(tt.desc, func(t *testing.T) {
			_, err := parse(tt.expr)
			if err == nil {
				t.Errorf("parse(%q) expected error for %s, got nil", tt.expr, tt.desc)
			}
		})
	}
}

func TestCron_Once_RemovedAfterFiring(t *testing.T) {
	store := NewScheduler()

	// Create a non-recurring (one-shot) job
	job, err := store.Create("* * * * *", "fire once", false, false)
	if err != nil {
		t.Fatalf("Create one-shot job failed: %v", err)
	}

	// Verify job was created
	if len(store.List()) != 1 {
		t.Fatalf("expected 1 job, got %d", len(store.List()))
	}

	// Force next fire time to past so Tick fires it
	store.mu.Lock()
	store.jobs[job.ID].NextFire = time.Now().Add(-time.Minute)
	store.mu.Unlock()

	// Tick should fire the job
	fired := store.Tick()
	if len(fired) != 1 {
		t.Fatalf("expected 1 fired job, got %d", len(fired))
	}
	if fired[0].Prompt != "fire once" {
		t.Errorf("expected prompt 'fire once', got %q", fired[0].Prompt)
	}

	// One-shot job should be removed after firing
	remaining := store.List()
	if len(remaining) != 0 {
		t.Errorf("expected 0 jobs after one-shot fires, got %d", len(remaining))
	}
}

func TestStoreDurable(t *testing.T) {
	tmpFile := t.TempDir() + "/scheduled_tasks.json"

	// Create a store with durable job
	store := NewScheduler()
	store.SetStoragePath(tmpFile)
	job, err := store.Create("*/10 * * * *", "durable prompt", true, true)
	if err != nil {
		t.Fatalf("Create durable failed: %v", err)
	}
	if !job.Durable {
		t.Error("expected Durable=true")
	}

	// Load into a fresh store
	store2 := NewScheduler()
	store2.SetStoragePath(tmpFile)
	if err := store2.LoadDurable(); err != nil {
		t.Fatalf("LoadDurable failed: %v", err)
	}

	jobs := store2.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 durable job, got %d", len(jobs))
	}
	if jobs[0].Prompt != "durable prompt" {
		t.Errorf("expected prompt 'durable prompt', got %q", jobs[0].Prompt)
	}
	if !jobs[0].Durable {
		t.Error("loaded job should be durable")
	}
}

func TestStoreTick_RecurringJobReschedulesAndTracksFireCount(t *testing.T) {
	store := NewScheduler()
	job, err := store.Create("* * * * *", "repeat me", true, false)
	if err != nil {
		t.Fatalf("Create recurring job failed: %v", err)
	}

	store.mu.Lock()
	store.jobs[job.ID].NextFire = time.Now().Add(-time.Minute)
	store.mu.Unlock()

	fired := store.Tick()
	if len(fired) != 1 {
		t.Fatalf("expected 1 fired job, got %d", len(fired))
	}

	jobs := store.List()
	if len(jobs) != 1 {
		t.Fatalf("expected recurring job to remain scheduled, got %d jobs", len(jobs))
	}
	if jobs[0].FiredCount != 1 {
		t.Fatalf("expected FiredCount=1, got %d", jobs[0].FiredCount)
	}
	if jobs[0].LastFired.IsZero() {
		t.Fatal("expected LastFired to be set")
	}
	if !jobs[0].NextFire.After(time.Now()) {
		t.Fatalf("expected NextFire to move forward, got %v", jobs[0].NextFire)
	}
	if !jobs[0].NextFire.After(jobs[0].LastFired) {
		t.Fatalf("expected NextFire to be after LastFired, last=%v next=%v", jobs[0].LastFired, jobs[0].NextFire)
	}
}

func TestStoreDelete_DurableRemovesPersistedJob(t *testing.T) {
	tmpFile := t.TempDir() + "/scheduled_tasks.json"

	store := NewScheduler()
	store.SetStoragePath(tmpFile)
	job, err := store.Create("*/10 * * * *", "durable prompt", true, true)
	if err != nil {
		t.Fatalf("Create durable failed: %v", err)
	}

	if err := store.Delete(job.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("ReadFile(durable store): %v", err)
	}

	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatalf("Unmarshal(durable store): %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected durable store file to be empty after delete, got %d jobs", len(jobs))
	}
}

func TestStoreTick_DurableRecurringPersistsUpdatedState(t *testing.T) {
	tmpFile := t.TempDir() + "/scheduled_tasks.json"

	store := NewScheduler()
	store.SetStoragePath(tmpFile)
	job, err := store.Create("* * * * *", "durable repeat", true, true)
	if err != nil {
		t.Fatalf("Create durable recurring failed: %v", err)
	}

	store.mu.Lock()
	store.jobs[job.ID].NextFire = time.Now().Add(-time.Minute)
	store.mu.Unlock()

	fired := store.Tick()
	if len(fired) != 1 {
		t.Fatalf("expected 1 fired job, got %d", len(fired))
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("ReadFile(durable store): %v", err)
	}

	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatalf("Unmarshal(durable store): %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 persisted durable job, got %d", len(jobs))
	}
	if jobs[0].FiredCount != 1 {
		t.Fatalf("expected persisted FiredCount=1, got %d", jobs[0].FiredCount)
	}
	if jobs[0].LastFired.IsZero() {
		t.Fatal("expected persisted LastFired to be set")
	}
}

func TestComputeNextFire_RecurringAddsDeterministicBoundedJitter(t *testing.T) {
	expr, err := parse("0 * * * *")
	if err != nil {
		t.Fatalf("parse() failed: %v", err)
	}

	from := time.Date(2026, 4, 6, 10, 7, 0, 0, time.Local)
	base := expr.nextAfter(from)
	if base.Minute() != 0 || base.Hour() != 11 {
		t.Fatalf("unexpected base time %v", base)
	}

	got1 := computeNextFire(expr, from, "job-123", true)
	got2 := computeNextFire(expr, from, "job-123", true)
	if !got1.Equal(got2) {
		t.Fatalf("expected deterministic jitter, got %v and %v", got1, got2)
	}
	if got1.Before(base) {
		t.Fatalf("expected jittered fire time >= base, got base=%v jittered=%v", base, got1)
	}
	if !got1.Before(base.Add(6 * time.Minute)) {
		t.Fatalf("expected hourly jitter to stay under 10%% period, got base=%v jittered=%v", base, got1)
	}
}

func TestComputeNextFire_OneShotDoesNotAddJitter(t *testing.T) {
	expr, err := parse("0 * * * *")
	if err != nil {
		t.Fatalf("parse() failed: %v", err)
	}

	from := time.Date(2026, 4, 6, 10, 7, 0, 0, time.Local)
	base := expr.nextAfter(from)
	got := computeNextFire(expr, from, "job-123", false)
	if !got.Equal(base) {
		t.Fatalf("expected one-shot next fire to equal base, got base=%v next=%v", base, got)
	}
}

func TestLoadDurable_OneShotPastDueFiresOnNextTick(t *testing.T) {
	tmpFile := t.TempDir() + "/scheduled_tasks.json"

	store := NewScheduler()
	store.SetStoragePath(tmpFile)
	job, err := store.Create("30 9 28 2 *", "missed once", false, true)
	if err != nil {
		t.Fatalf("Create durable one-shot failed: %v", err)
	}

	store.mu.Lock()
	store.jobs[job.ID].NextFire = time.Now().Add(-2 * time.Hour)
	store.saveDurableLocked()
	store.mu.Unlock()

	store2 := NewScheduler()
	store2.SetStoragePath(tmpFile)
	if err := store2.LoadDurable(); err != nil {
		t.Fatalf("LoadDurable failed: %v", err)
	}

	jobs := store2.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 loaded durable job, got %d", len(jobs))
	}
	if jobs[0].Recurring {
		t.Fatal("expected one-shot durable job")
	}
	if jobs[0].NextFire.After(time.Now().Add(2 * time.Second)) {
		t.Fatalf("expected one-shot catch-up to be immediate, got %v", jobs[0].NextFire)
	}

	fired := store2.Tick()
	if len(fired) != 1 {
		t.Fatalf("expected catch-up tick to fire 1 job, got %d", len(fired))
	}
	if fired[0].Prompt != "missed once" {
		t.Fatalf("unexpected fired prompt %q", fired[0].Prompt)
	}
}
