package reminder

import (
	"strings"
	"sync"
	"testing"
)

func TestWrapMemoryPreamble(t *testing.T) {
	user := WrapMemory("user", "Always use tabs.")
	if !strings.Contains(user, "user's saved memory") {
		t.Errorf("WrapMemory(user) missing user preamble: %q", user)
	}
	if !strings.HasSuffix(user, "<memory scope=\"user\">\nAlways use tabs.\n</memory>") {
		t.Errorf("WrapMemory(user) should end with the <memory> envelope: %q", user)
	}

	project := WrapMemory("project", "Run make lint.")
	if !strings.Contains(project, "saved project memory") {
		t.Errorf("WrapMemory(project) missing project preamble: %q", project)
	}

	if got := WrapMemory("user", "   "); got != "" {
		t.Errorf("WrapMemory with blank body = %q, want \"\"", got)
	}
}

func TestWrapEmpty(t *testing.T) {
	if got := Wrap(""); got != "" {
		t.Errorf("Wrap(\"\") = %q, want \"\"", got)
	}
	if got := Wrap("   \n\t "); got != "" {
		t.Errorf("Wrap whitespace-only = %q, want \"\"", got)
	}
}

func TestWrapNonEmpty(t *testing.T) {
	got := Wrap("hello world")
	want := "<system-reminder>\nhello world\n</system-reminder>"
	if got != want {
		t.Errorf("Wrap mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestServiceEnqueueAndDrain(t *testing.T) {
	s := NewService()
	if !s.Empty() {
		t.Fatal("new service should be empty")
	}

	s.Enqueue("first")
	s.Enqueue("second")
	s.Enqueue("") // dropped

	out := s.Drain()
	if len(out) != 2 {
		t.Fatalf("expected 2 reminders, got %d: %v", len(out), out)
	}
	if !strings.Contains(out[0], "first") {
		t.Errorf("first reminder missing body: %q", out[0])
	}
	if !strings.Contains(out[1], "second") {
		t.Errorf("second reminder missing body: %q", out[1])
	}
	if !s.Empty() {
		t.Error("service should be empty after Drain")
	}
}

func TestServiceProviderRegistration(t *testing.T) {
	s := NewService()

	rendered := 0
	s.Register(NewProvider("skills", func() string {
		rendered++
		return "- foo: do foo"
	}))

	s.RequeueSystemReminders()
	if rendered != 1 {
		t.Fatalf("provider Body should be called once, got %d", rendered)
	}

	out := s.Drain()
	if len(out) != 1 || !strings.Contains(out[0], "- foo: do foo") {
		t.Errorf("provider output not enqueued: %v", out)
	}
}

func TestServiceProviderReplaceByID(t *testing.T) {
	s := NewService()
	s.Register(NewProvider("skills", func() string { return "old" }))
	s.Register(NewProvider("skills", func() string { return "new" }))

	s.RequeueSystemReminders()
	out := s.Drain()
	if len(out) != 1 {
		t.Fatalf("registering same ID twice should produce one reminder, got %d: %v", len(out), out)
	}
	if !strings.Contains(out[0], "new") {
		t.Errorf("second registration should win, got %q", out[0])
	}
}

func TestServiceProviderEmptyOutput(t *testing.T) {
	s := NewService()
	s.Register(NewProvider("skills", func() string { return "" }))
	s.Register(NewProvider("memory", func() string { return "stuff" }))

	s.RequeueSystemReminders()
	out := s.Drain()
	if len(out) != 1 {
		t.Fatalf("empty provider output should be skipped, got %d reminders: %v", len(out), out)
	}
	if !strings.Contains(out[0], "stuff") {
		t.Errorf("non-empty provider should remain: %q", out[0])
	}
}

func TestServiceUnregister(t *testing.T) {
	s := NewService()
	s.Register(NewProvider("a", func() string { return "alpha" }))
	s.Register(NewProvider("b", func() string { return "beta" }))

	s.Unregister("a")
	s.RequeueSystemReminders()

	out := s.Drain()
	if len(out) != 1 || !strings.Contains(out[0], "beta") {
		t.Errorf("Unregister should drop the named provider, got %v", out)
	}
}

func TestAttachToContentNoReminders(t *testing.T) {
	got := AttachToContent("hello", nil)
	if got != "hello" {
		t.Errorf("nil reminders should leave content unchanged, got %q", got)
	}
}

func TestAttachToContentMultiple(t *testing.T) {
	reminders := []string{
		Wrap("alpha"),
		Wrap("beta"),
	}
	got := AttachToContent("hello", reminders)

	if !strings.HasPrefix(got, "hello") {
		t.Errorf("user content should come first, got: %s", got)
	}
	if strings.Index(got, "alpha") > strings.Index(got, "beta") {
		t.Errorf("reminders should preserve order: %s", got)
	}
	if !strings.Contains(got, "<system-reminder>") {
		t.Errorf("expected system-reminder tags in output: %s", got)
	}
}

// TestServiceFullSessionLifecycle simulates the harness flow end-to-end: a
// session starts, providers (skills + memory) get enqueued, the user types a
// message, attached reminders ride along; on PostCompact the same providers
// re-emit so the LLM doesn't lose context. This is what the model wires up
// across hooks.go (SessionStart), model.go (PostCompact), and agent.go
// (sendToAgent).
func TestServiceFullSessionLifecycle(t *testing.T) {
	s := NewService()

	skillsBody := "Use the Skill tool to invoke these capabilities:\n\n- git: Git workflow"
	memoryBody := "<memory scope=\"user\">\nAlways use tabs.\n</memory>"

	s.Register(NewProvider("skills-directory", func() string { return skillsBody }))
	s.Register(NewProvider("memory-user", func() string { return memoryBody }))

	// SessionStart: harness enqueues all providers.
	s.RequeueSystemReminders()

	// User submits "hello"; sendToAgent drains and attaches.
	firstUserMsg := AttachToContent("hello", s.Drain())
	if !strings.Contains(firstUserMsg, "hello") {
		t.Error("first user message should preserve the user's typed content")
	}
	if !strings.Contains(firstUserMsg, skillsBody) {
		t.Error("first user message should carry skills directory reminder")
	}
	if !strings.Contains(firstUserMsg, memoryBody) {
		t.Error("first user message should carry memory reminder")
	}
	if !strings.Contains(firstUserMsg, "<system-reminder") {
		t.Error("reminders must be wrapped in <system-reminder> (with or without source attr)")
	}

	// User submits "hello again"; queue empty after drain — no reminders.
	secondUserMsg := AttachToContent("hello again", s.Drain())
	if secondUserMsg != "hello again" {
		t.Errorf("second message should have no reminders attached, got %q", secondUserMsg)
	}

	// PostCompact: harness re-enqueues providers so the LLM can recover.
	s.RequeueSystemReminders()
	postCompactMsg := AttachToContent("after compact", s.Drain())
	if !strings.Contains(postCompactMsg, skillsBody) {
		t.Error("post-compact message should re-attach skills reminder")
	}
	if !strings.Contains(postCompactMsg, memoryBody) {
		t.Error("post-compact message should re-attach memory reminder")
	}
}

// TestServiceProviderReflectsLatestState verifies that providers are queried
// on every emission, so changes between SessionStart and PostCompact (e.g.
// the user toggled a skill in the middle) are picked up.
func TestServiceProviderReflectsLatestState(t *testing.T) {
	s := NewService()
	state := "v1"
	s.Register(NewProvider("skills", func() string { return state }))

	s.RequeueSystemReminders()
	if got := s.Drain(); !strings.Contains(got[0], "v1") {
		t.Fatalf("first emission should reflect v1, got %v", got)
	}

	state = "v2"

	s.RequeueSystemReminders()
	if got := s.Drain(); !strings.Contains(got[0], "v2") {
		t.Errorf("second emission should reflect mutated state v2, got %v", got)
	}
}

// TestServiceRequeueSystemRemindersIsIdempotent guards against the slow-growing
// duplicate-emission leak: SessionStart → PostCompact → /skills toggle in
// close succession should produce one emission per provider, not three.
// One-time notices (Enqueue) must survive a re-emission unmolested.
func TestServiceRequeueSystemRemindersIsIdempotent(t *testing.T) {
	s := NewService()
	s.Register(NewProvider("skills-directory", func() string { return "skills body" }))
	s.Register(NewProvider("memory-user", func() string { return "user mem" }))

	// Hook-context notice queued before the first emission.
	s.Enqueue("hook context A")

	s.RequeueSystemReminders() // first SessionStart
	s.RequeueSystemReminders() // /skills toggle
	s.RequeueSystemReminders() // PostCompact

	out := s.Drain()
	// 1 notice + 2 provider entries (one per provider) = 3 total.
	if len(out) != 3 {
		t.Fatalf("expected 3 reminders (1 notice + 2 providers), got %d: %v", len(out), out)
	}

	var skillsCount, memoryCount, hookCount int
	for _, r := range out {
		switch {
		case strings.Contains(r, "skills body"):
			skillsCount++
		case strings.Contains(r, "user mem"):
			memoryCount++
		case strings.Contains(r, "hook context A"):
			hookCount++
		}
	}
	if skillsCount != 1 || memoryCount != 1 || hookCount != 1 {
		t.Errorf("expected each reminder exactly once; got skills=%d memory=%d hook=%d",
			skillsCount, memoryCount, hookCount)
	}
}

func TestServiceConcurrentAccess(t *testing.T) {
	s := NewService()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Enqueue("item")
		}(i)
	}
	wg.Wait()

	out := s.Drain()
	if len(out) != 50 {
		t.Errorf("expected 50 enqueued items, got %d", len(out))
	}
}

// Blocks is the inverse of AttachToContent: whatever the harness attached to a
// user turn has to be recoverable from that turn's content, with the emitting
// provider intact, so /context can attribute those bytes to the right source.
func TestBlocksRecoversAttachedReminders(t *testing.T) {
	s := NewService()
	s.Register(NewProvider(ProviderSkillsDirectory, func() string { return "skills body" }))
	s.Register(NewProvider(ProviderMemoryProject, func() string { return "project memory body" }))
	s.RequeueSystemReminders()
	s.Enqueue("a one-time notice")

	content := AttachToContent("what the user typed", s.Drain())

	blocks := Blocks(content)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d from:\n%s", len(blocks), content)
	}
	wantSources := []string{ProviderSkillsDirectory, ProviderMemoryProject, ""}
	for i, want := range wantSources {
		if blocks[i].Source != want {
			t.Errorf("block %d: source = %q, want %q", i, blocks[i].Source, want)
		}
		if !strings.HasPrefix(blocks[i].Text, "<system-reminder") ||
			!strings.HasSuffix(blocks[i].Text, "</system-reminder>") {
			t.Errorf("block %d text is not a whole reminder: %q", i, blocks[i].Text)
		}
	}
	if strings.Contains(blocks[0].Text, "project memory body") {
		t.Error("blocks ran together; the skills block swallowed the memory block")
	}
}

func TestBlocksIgnoresMalformedTags(t *testing.T) {
	for name, content := range map[string]string{
		"no reminder at all": "plain user text",
		"unterminated":       "text <system-reminder>body with no close",
		"close only":         "text </system-reminder>",
	} {
		if got := Blocks(content); len(got) != 0 {
			t.Errorf("%s: expected no blocks, got %d", name, len(got))
		}
	}
}

// Pending has to leave the queue intact — /context inspects what the next turn
// will carry, it does not consume it.
func TestPendingDoesNotDrain(t *testing.T) {
	s := NewService()
	s.Enqueue("queued")

	if got := s.Pending(); len(got) != 1 {
		t.Fatalf("Pending returned %d entries, want 1", len(got))
	}
	if s.Empty() {
		t.Error("Pending consumed the queue")
	}
	if got := s.Drain(); len(got) != 1 {
		t.Errorf("Drain after Pending returned %d entries, want 1", len(got))
	}
}
