---
package: github.com/genai-io/san/internal/selflearn
layer: feature
---

# selflearn

Runs San's background self-learning pass: after a turn, a restricted fork
reviews the just-completed conversation and writes durable memory or
agent-created skills. Triggering is model-decided — the main agent calls the
`Evolve` tool when a turn is worth learning from.

## Purpose

`selflearn` owns the trigger logic, fork prompt, writable memory store, and
restricted skill-management surface for the L1 background review loop. The app
wires it per session after resolving settings; the package itself stays outside
the Bubble Tea model and exposes concrete types for the pieces the app needs.

This package deliberately writes only to San-managed durable state: project
memory under the encoded project configuration directory and skills marked with
agent-created provenance. User-created skills are read-only to the reviewer at
every setting.

## Triggers & flow

Triggering is entirely model-decided — there is no cadence. When self-learning
is active the main agent is given an `Evolve` tool; the per-arm settings are
permission / store config only (skill create/update/delete gates, memory
enable + cap + path, a shared strategy override).

End-to-end when the model calls `Evolve`:

1. The call is auto-permitted (`Evolve` is a safe tool) and returns an ack; the
   model keeps working. `model.OnToolResult` sets `evolveRequestedThisTurn`.
2. At `OnTurnEnd`, `Reviewer.Observe(result, skillUsed, evolveRequested)` fires a
   review over every enabled arm — memory if enabled, and skills scoped by skill
   use (skill-used → update/delete, skill-free → create) — bounded by the
   permission gates.
3. A background fork (`RunReview`) reflects on the turn snapshot with only
   `skill_manage` + `memory_write`, then writes memory and/or a skill, or does
   nothing. The status bar shows `evolving… → evolved · N changes`.

The `Evolve` tool's schema is built by `tool/evolve.Schema` from the enabled
capabilities (`evolve.Capabilities`) so the model is only invited to flag
learnings the review can act on — memory off never mentions memory — and is
injected through the toolset's generic `ExtraTools` hook. The writes affect
future sessions, not the current turn; at most one review runs at a time.
Capability changes are reconciled at the next turn start: `ensureAgentSession`
records what the toolset was built with (`agentEvolveCaps`) and rebuilds the
agent on drift — covering /evolve saves and external settings edits with one
mechanism. `SAN_DISABLE_SELF_LEARN=1` is a hard runtime switch: it suppresses
both the reviewer and the `Evolve` tool regardless of layered settings.

The skills JSON retains an explicit `enabled` compatibility marker. Legacy
empty `skills` objects came from `enabled:false` being omitted and migrate to
all three actions denied; current saves always emit the marker so permissive
permission sets round-trip without ambiguity.

## Contract

The package exposes concrete structs plus one callback type. There is no
producer-side service interface; callers construct the exact components they
need.

```go
package selflearn

type Config struct {
    MemoryEnabled  bool
    MemoryMaxChars int
    MemoryPath     string
    Skills         SkillPermissions
    Strategy       string // learning-strategy override (empty ⇒ built-in)
}

func (c Config) Enabled() bool
func ResolveSettings(s setting.SelfLearnSettings) (Config, error)

// SkillPermissions appears at three altitudes with one meaning: the configured
// gates (Config.Skills), the per-pass scope the trigger derives from them, and
// the SkillManager's hard floor at dispatch.
type SkillPermissions struct {
    AllowCreate, AllowUpdate, AllowDelete bool
}

func (p SkillPermissions) Any() bool
func AllowAllSkillActions() SkillPermissions

type ReviewKind uint8

const (
    KindMemory ReviewKind = 1 << iota
    KindSkills
)

func (k ReviewKind) Has(x ReviewKind) bool
func (k ReviewKind) String() string

type ReviewFunc func(kinds ReviewKind, skillPerms SkillPermissions, snapshot []core.Message)

type Reviewer struct { /* unexported */ }

func New(cfg Config, review ReviewFunc) *Reviewer
func (r *Reviewer) Observe(result core.Result, skillUsed, evolveRequested bool)

func DefaultStrategy() string // built-in guidance the /evolve Strategy editor seeds with

type ForkConfig struct {
    Client      func(msgs []core.Message) (*ai.Client, error)
    CallOptions func() []ai.Option
    System      *system.System
    CWD         string
    Memory      *MemoryStore
    Skills      *SkillManager // its Perms() also scope the review prompt
    Strategy    string
    OnEvent     func(core.Event)
}

func RunReview(ctx context.Context, fc ForkConfig, kinds ReviewKind, snapshot []core.Message) (string, error)

type MemoryStore struct { /* unexported */ }

func NewMemoryStore(cwd string, maxCharsPerFile int, dirOverride string) *MemoryStore
func (s *MemoryStore) MaxKB() int
func (s *MemoryStore) SetWriteObserver(fn MemoryWriteObserver)
func (s *MemoryStore) Dir() string
func (s *MemoryStore) Add(file, content, note string) (string, error)
func (s *MemoryStore) Replace(file, oldText, newContent, note string) (string, error)
func (s *MemoryStore) Remove(file, oldText, note string) (string, error)

type SkillManager struct { /* unexported */ }

func NewSkillManager(cwd string, perms SkillPermissions) *SkillManager
func (m *SkillManager) Perms() SkillPermissions
func (m *SkillManager) SetWriteObserver(fn SkillWriteObserver)
func (m *SkillManager) Inventory() []SkillInfo
func (m *SkillManager) Read(name string) (string, error)
func (m *SkillManager) Create(name, description, body, level, note string) (string, error)
func (m *SkillManager) Edit(name, body, note string) (string, error)
func (m *SkillManager) Patch(name, oldText, newText string, replaceAll bool, note string) (string, error)
func (m *SkillManager) WriteFile(name, file, content, note string) (string, error)
func (m *SkillManager) RemoveFile(name, file, note string) (string, error)
func (m *SkillManager) Delete(name, note string) (string, error)
```

## Internals

- `Reviewer` owns the at-most-one-in-flight gate. It
  observes completed `core.Result` values and launches the injected
  `ReviewFunc` on a background goroutine.
- `RunReview` builds a restricted review agent with only `memory_write` and
  `skill_manage`, trims trailing pending messages, and runs with a fixed step
  and wall-clock budget.
- `MemoryStore` writes delimited markdown entries under
  `system.AutoMemoryDir(cwd)`, with traversal checks, prompt-injection scans,
  and per-file size caps.
- `SkillManager` reads and writes San skill directories directly so the review
  sees its own mid-session writes without relying on the startup skill
  registry cache.
- Write observers are used by `internal/app` to update the live self-learning
  indicator and final recap.

## Lifecycle

- Construction: `internal/app` resolves `setting.SelfLearnSettings`, creates a
  session-scoped `Reviewer`, `MemoryStore`, and `SkillManager`, and injects the
  actual fork function.
- Runtime: `Reviewer.Observe` is called after cleanly completed turns only.
  Interrupted, cancelled, and max-step turns are ignored.
- Shutdown: app teardown cancels the review context and flips a liveness flag
  so late write notifications do not mutate stale UI state.
- Concurrency: `Reviewer`, `MemoryStore`, and `SkillManager` guard mutable
  state with mutexes. Cross-process disk writes are best-effort via atomic
  rename, not a distributed lock.

## Tests

```
internal/selflearn/reviewer_test.go       — model-request trigger, single-flight,
                                             scoping, and result filtering.
internal/selflearn/concurrency_test.go    — snapshot copying and race safety.
internal/selflearn/fork_test.go           — fork prompt/tool restrictions and
                                             inherited system state.
internal/selflearn/memory_test.go         — memory add/replace/remove, limits,
                                             traversal, and threat scanning.
internal/selflearn/skill_test.go          — skill create/update/delete,
                                             provenance, and permissions.
internal/selflearn/config_test.go         — settings resolution and defaults.
internal/selflearn/fixes_test.go          — regression coverage for security
                                             and patch edge cases.
internal/setting/selflearn_test.go        — permission schema migration.
internal/app/selflearn_review_fixes_test.go — runtime gates, layered overrides,
                                               and live-workspace stores.
```

### Testing the trigger manually

1. `/evolve` → keep at least one skill permission on (or enable memory), then
   **Save**. Saving rebuilds the agent so the `Evolve` tool is injected.
2. Confirm injection: ask the agent to list its tools — `Evolve` appears only
   when self-learning is active.
3. Do skill-worthy work, then have the agent call `Evolve` (e.g. reason
   "testing the trigger").
4. Watch: the status-bar indicator (`evolving… → evolved` / `nothing`), the
   `/evolve → Learned skills` inventory + RECENT recap, and
   `~/.san/debug.log` (`grep selflearn`).

Requires an active LLM provider (the fork makes a real call) and
`SAN_DISABLE_SELF_LEARN` unset.

## See Also

- Code: `internal/selflearn/`
- Related packages: [`setting`](setting.md), [`skill`](skill.md), [`reminder`](reminder.md)
- Concepts: [`concepts/harness-channels.md`](../../concepts/harness-channels.md)
- Layer: `feature` (see [`reference/dependency-rules.md`](../../reference/dependency-rules.md))
