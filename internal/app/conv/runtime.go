package conv

import (
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
)

type AgentOutboxMsg struct {
	Event  core.Event
	Batch  []core.Event // set when multiple events were drained at once
	Closed bool
}

// Runtime defines what the conv event handlers need from the root model. Each
// method represents a coherent operation (not a fine-grained primitive),
// keeping the interface small and each implementation substantial.
//
// The prefix says where a member comes from:
//
//   - On*     — the agent did something. One per agent event, and they arrive
//     in the outbox's order, so they have a fixed place on the turn's timeline.
//   - Handle* — a system message arrived: a background command's result, a
//     timer, a side channel. No place on that timeline; it can land any time.
//   - a verb  — not a callback at all, but a service conv calls on the host.
type Runtime interface {
	// ── On*: agent lifecycle ────────────────────────────────────
	OnInference(resp *core.InferResponse) // PostInfer
	OnToolResult(tr core.ToolResult) *core.ToolResult
	// OnStepEnd fires when a tool batch completes and the turn continues, so
	// pending work (parked notices, one queued user message) can reach the
	// agent in time for its next step.
	OnStepEnd() tea.Cmd
	OnTurnEnd(result core.Result) tea.Cmd
	OnAgentStop(err error) tea.Cmd
	OnCompactStart(count int) tea.Cmd
	OnCompacted(info core.CompactInfo) tea.Cmd

	// ── Handle*: system messages ────────────────────────────────
	HandlePermGate(req *PermGateRequest) tea.Cmd
	HandleCompactResult(msg CompactResultMsg) tea.Cmd
	HandleTokenLimitResult(msg kit.TokenLimitResultMsg) tea.Cmd

	// ── Services conv calls on the host ─────────────────────────
	CommitMessages() []tea.Cmd
	FlushStreamingBlocks() []tea.Cmd
	// TakeReviewDecision consumes the auto-review decision stashed for a tool
	// call (nil if it was not auto-reviewed), to stamp onto its rendered result.
	TakeReviewDecision(callID string) *core.ReviewDecision
	HasRunningTasks() bool

	// ── Transport lifetime ──────────────────────────────────────
	// ContinueOutbox re-arms the one-shot outbox listener. Not a service but an
	// obligation, and a divided one: conv re-arms itself after every
	// non-terminal event, while a terminal event (OnTurn / OnStop / OnCompact)
	// hands the decision to the host — which re-arms in OnTurnEnd and
	// OnCompacted, and deliberately does not in OnAgentStop. Miss it on any one
	// path and the UI stops receiving agent events with no error at all: the
	// session simply looks frozen.
	ContinueOutbox() tea.Cmd
}

// DrainAgentOutbox blocks until at least one event is available, then greedily
// drains additional ready events to reduce Update+View cycles. Stops at
// terminal events (OnTurn/OnStop/OnCompact) so turn boundaries aren't crossed.
func DrainAgentOutbox(outbox <-chan core.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-outbox
		if !ok {
			return AgentOutboxMsg{Closed: true}
		}
		if isTerminalEvent(ev) {
			return AgentOutboxMsg{Event: ev}
		}
		batch := []core.Event{ev}
		for {
			select {
			case next, ok := <-outbox:
				if !ok {
					return AgentOutboxMsg{Batch: batch, Closed: true}
				}
				batch = append(batch, next)
				if isTerminalEvent(next) {
					return AgentOutboxMsg{Batch: batch}
				}
			default:
				if len(batch) == 1 {
					return AgentOutboxMsg{Event: batch[0]}
				}
				return AgentOutboxMsg{Batch: batch}
			}
		}
	}
}

func isTerminalEvent(ev core.Event) bool {
	return ev.Type == core.OnTurn || ev.Type == core.OnStop || ev.Type == core.OnCompact
}
