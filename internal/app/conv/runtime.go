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

// Runtime defines callbacks that the conv event handlers need from the root
// model. Each method represents a coherent operation (not a fine-grained
// primitive), keeping the interface small and each implementation substantial.
type Runtime interface {
	CommitMessages() []tea.Cmd
	FlushStreamingBlocks() []tea.Cmd
	ContinueOutbox() tea.Cmd
	OnTokenUsage(resp *core.InferResponse)
	OnAgentMessage(msg core.Message) tea.Cmd
	OnToolResult(tr core.ToolResult) *core.ToolResult
	// TakeDecision consumes the auto-review decision stashed for a tool
	// call (nil if it was not auto-reviewed), to stamp onto its rendered result.
	TakeDecision(callID string) *core.ReviewDecision
	OnTurnEnd(result core.Result) tea.Cmd
	// DrainStepQueues releases pending work — parked notices, one queued user
	// message — to the agent at a step boundary (a continuing turn), so the next
	// step reads it.
	DrainStepQueues() tea.Cmd
	OnAgentStop(err error) tea.Cmd
	OnPermGateRequest(req *PermGateRequest) tea.Cmd
	OnCompactStart(count int) tea.Cmd
	OnCompacted(info core.CompactInfo) tea.Cmd
	OnCompactResult(msg CompactResultMsg) tea.Cmd
	OnTokenLimitResult(msg kit.TokenLimitResultMsg) tea.Cmd
	HasRunningTasks() bool
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
