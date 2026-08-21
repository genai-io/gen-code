package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/log"
)

const (
	// defaultExpiry is the auto-expiry duration for recurring jobs.
	defaultExpiry = 7 * 24 * time.Hour

	// maxJobs is the maximum number of concurrent cron jobs.
	maxJobs = 50

	// maxRecurringJitter bounds recurring schedule spread.
	maxRecurringJitter = 15 * time.Minute
)

// Job represents a scheduled cron job.
type Job struct {
	ID         string    `json:"id"`
	Cron       string    `json:"cron"`      // 5-field cron expression
	Prompt     string    `json:"prompt"`    // prompt to inject when fired
	Recurring  bool      `json:"recurring"` // true = repeats, false = one-shot
	Durable    bool      `json:"durable"`   // true = persists across sessions
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`  // auto-expiry time (zero = no expiry)
	NextFire   time.Time `json:"nextFire"`   // next scheduled fire time
	LastFired  time.Time `json:"lastFired"`  // last time this job fired
	FiredCount int       `json:"firedCount"` // total times fired

	expr *expression // parsed expression (not serialized)
}

// Scheduler manages cron jobs with thread-safe access.
// Session-only jobs are cleared when the process exits.
// Durable jobs persist to storagePath across sessions.
type Scheduler struct {
	mu          sync.RWMutex
	jobs        map[string]*Job
	storagePath string // file path for durable job persistence (empty = disabled)
	// knownDurable records every durable job id this process has held, so a
	// save can tell "I removed this" from "another instance created it". The
	// storage file is per-project, not per-process, and two san windows open on
	// one repo share it.
	knownDurable map[string]bool
}

// NewScheduler creates a new in-memory *Scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{
		jobs:         make(map[string]*Job),
		knownDurable: make(map[string]bool),
	}
}

// Create adds a new cron job and returns it.
func (s *Scheduler) Create(cronExpr, prompt string, recurring, durable bool) (*Job, error) {
	expr, err := parse(cronExpr)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.jobs) >= maxJobs {
		return nil, fmt.Errorf("cron: maximum number of jobs (%d) reached", maxJobs)
	}

	expiresAt := time.Time{}
	if recurring {
		expiresAt = now.Add(defaultExpiry)
	}

	job := &Job{
		ID:        generateID(),
		Cron:      cronExpr,
		Prompt:    prompt,
		Recurring: recurring,
		Durable:   durable,
		CreatedAt: now,
		ExpiresAt: expiresAt,
		expr:      expr,
	}
	job.NextFire = computeNextFire(expr, now, job.ID, recurring)
	if job.NextFire.IsZero() {
		return nil, fmt.Errorf("cron: no valid fire time found for %q", cronExpr)
	}

	s.jobs[job.ID] = job

	if durable {
		s.saveDurableLocked()
	}

	return job, nil
}

// Delete removes a job by ID.
func (s *Scheduler) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("cron: job %q not found", id)
	}
	wasDurable := job.Durable
	delete(s.jobs, id)

	if wasDurable {
		s.saveDurableLocked()
	}
	return nil
}

// List returns copies of all active jobs sorted by next fire time.
func (s *Scheduler) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		cp := *j
		jobs = append(jobs, &cp)
	}

	sort.Slice(jobs, func(i, k int) bool {
		return jobs[i].NextFire.Before(jobs[k].NextFire)
	})
	return jobs
}

// Empty returns true if the store has no jobs.
func (s *Scheduler) Empty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs) == 0
}

// Tick checks all jobs and returns prompts for any that should fire now.
// It advances fired jobs to their next fire time or removes one-shot/expired jobs.
func (s *Scheduler) Tick() []FiredJob {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var fired []FiredJob
	var toDelete []string
	changed := false

	for _, job := range s.jobs {
		// Check expiry
		if !job.ExpiresAt.IsZero() && now.After(job.ExpiresAt) {
			toDelete = append(toDelete, job.ID)
			if job.Durable {
				changed = true
			}
			continue
		}

		// Check if it should fire
		if now.Before(job.NextFire) {
			continue
		}

		fired = append(fired, FiredJob{
			ID:     job.ID,
			Prompt: job.Prompt,
		})

		job.LastFired = now
		job.FiredCount++
		if job.Durable {
			changed = true
		}

		if !job.Recurring {
			toDelete = append(toDelete, job.ID)
		} else {
			if job.expr == nil {
				if expr, err := parse(job.Cron); err == nil {
					job.expr = expr
				} else {
					// Can't compute next fire — delete to prevent infinite fire loop
					toDelete = append(toDelete, job.ID)
					continue
				}
			}
			if job.expr != nil {
				job.NextFire = computeNextFire(job.expr, now, job.ID, true)
			}
		}
	}

	for _, id := range toDelete {
		delete(s.jobs, id)
	}
	if changed {
		s.saveDurableLocked()
	}

	return fired
}

// FiredJob is returned by Tick when a job fires.
type FiredJob struct {
	ID     string
	Prompt string
}

// Add adds a pre-built job to the store, satisfying the Service interface.
// The job must have a valid cron expression in the Cron field.
func (s *Scheduler) Add(job Job) error {
	expr, err := parse(job.Cron)
	if err != nil {
		return err
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.jobs) >= maxJobs {
		return fmt.Errorf("cron: maximum number of jobs (%d) reached", maxJobs)
	}

	j := &Job{
		ID:        job.ID,
		Cron:      job.Cron,
		Prompt:    job.Prompt,
		Recurring: job.Recurring,
		Durable:   job.Durable,
		CreatedAt: now,
		ExpiresAt: job.ExpiresAt,
		expr:      expr,
	}
	if j.ID == "" {
		j.ID = generateID()
	}
	if j.Recurring && j.ExpiresAt.IsZero() {
		j.ExpiresAt = now.Add(defaultExpiry)
	}
	j.NextFire = computeNextFire(expr, now, j.ID, j.Recurring)
	if j.NextFire.IsZero() {
		return fmt.Errorf("cron: no valid fire time found for %q", job.Cron)
	}

	s.jobs[j.ID] = j

	if j.Durable {
		s.saveDurableLocked()
	}
	return nil
}

// Remove removes a job by ID, satisfying the Service interface.
// Returns true if the job was found and removed.
func (s *Scheduler) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return false
	}
	wasDurable := job.Durable
	delete(s.jobs, id)

	if wasDurable {
		s.saveDurableLocked()
	}
	return true
}

// Reset removes all jobs.
func (s *Scheduler) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = make(map[string]*Job)
	s.saveDurableLocked()
}

// SetStoragePath sets the file path for durable job persistence.
// Call LoadDurable() after this to restore previously saved jobs.
func (s *Scheduler) SetStoragePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storagePath = path
}

// foreignDurableLocked returns the durable jobs currently on disk that belong
// to another instance: ones this process has never held. Read failures yield
// nothing, which degrades to the previous behaviour rather than refusing the
// save.
func (s *Scheduler) foreignDurableLocked(mine []*Job) []*Job {
	data, err := os.ReadFile(s.storagePath)
	if err != nil {
		return nil
	}
	var onDisk []*Job
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return nil
	}

	held := make(map[string]bool, len(mine))
	for _, job := range mine {
		held[job.ID] = true
	}

	var foreign []*Job
	for _, job := range onDisk {
		if held[job.ID] || s.knownDurable[job.ID] {
			continue
		}
		foreign = append(foreign, job)
	}
	return foreign
}

// LoadDurable reads durable jobs from the storage file and merges them into the store.
func (s *Scheduler) LoadDurable() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.storagePath == "" {
		return nil
	}

	data, err := os.ReadFile(s.storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cron: failed to read durable jobs: %w", err)
	}

	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return fmt.Errorf("cron: failed to parse durable jobs: %w", err)
	}

	now := time.Now()
	for _, job := range jobs {
		// Skip expired jobs
		if !job.ExpiresAt.IsZero() && now.After(job.ExpiresAt) {
			continue
		}
		// Re-parse expression
		expr, err := parse(job.Cron)
		if err != nil {
			continue
		}
		job.expr = expr
		if job.Recurring {
			// Recalculate recurring jobs from "now" so long-lived schedules
			// continue cleanly after restart without replaying missed intervals.
			job.NextFire = computeNextFire(expr, now, job.ID, true)
			if job.NextFire.IsZero() {
				continue
			}
		} else {
			// One-shot durable jobs should catch up after restart instead of
			// being pushed to the cron expression's next future match.
			if job.NextFire.IsZero() {
				job.NextFire = now
			} else if !job.NextFire.After(now) {
				job.NextFire = now
			}
		}
		job.Durable = true
		s.jobs[job.ID] = job
		// Adopted at boot, so this process owns it: a later removal here is a
		// removal, not another instance's job to preserve.
		s.knownDurable[job.ID] = true
	}

	return nil
}

func computeNextFire(expr *expression, from time.Time, jobID string, recurring bool) time.Time {
	base := expr.nextAfter(from)
	if base.IsZero() {
		return time.Time{}
	}
	if !recurring {
		return base
	}

	jitter := computeRecurringJitter(expr, base, jobID)
	return base.Add(jitter)
}

func computeRecurringJitter(expr *expression, base time.Time, jobID string) time.Duration {
	period := estimateRecurringPeriod(expr, base)
	if period <= 0 {
		return 0
	}

	maxJitter := min(period/10, maxRecurringJitter)
	if maxJitter <= 0 {
		return 0
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(jobID))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(expr.Raw))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(base.Format("2006-01-02T15:04")))

	return time.Duration(h.Sum64() % uint64(maxJitter))
}

func estimateRecurringPeriod(expr *expression, base time.Time) time.Duration {
	// Ask for the next matching base time after the current base minute.
	next := expr.nextAfter(base)
	if next.IsZero() {
		return 0
	}
	return next.Sub(base)
}

// saveDurableLocked writes this process's durable jobs, preserving any the
// file holds that belong to another instance.
//
// The storage path is per-project, so two san windows open on one repo write
// the same file. Writing only the in-memory view erased whatever the other
// instance had added since this one booted — LoadDurable runs once, at
// startup, so a job created in the other window was simply not in this view.
// A durable job could vanish from disk without either user doing anything to
// it.
//
// Jobs this process never knew about are carried over verbatim: their
// schedule state belongs to the instance that owns them, and adopting them
// into s.jobs would put them through LoadDurable's boot-time NextFire
// recalculation, which is wrong mid-session. Jobs it did know about and no
// longer holds were deliberately removed, so they stay removed.
func (s *Scheduler) saveDurableLocked() {
	if s.storagePath == "" {
		return
	}

	var durable []*Job
	for _, job := range s.jobs {
		if job.Durable {
			durable = append(durable, job)
			s.knownDurable[job.ID] = true
		}
	}
	durable = append(durable, s.foreignDurableLocked(durable)...)

	if err := atomicfile.WriteJSON(s.storagePath, durable, 0o644); err != nil {
		log.Logger().Error("cron: persist durable jobs", zap.Error(err))
	}
}

func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
