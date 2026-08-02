package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/todo"
)

// runCmd executes cmd, flattening tea.Batch so the leaf commands (the ones that
// actually reach the agent) run too. Never hand it onMainNotice's result: that
// batch carries the listener re-arm, which blocks until the next notice.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, sub := range batch {
			runCmd(sub)
		}
	}
}

// noticeDeliveryModel returns a model whose agent session is live, plus the
// provider that records the message chain of its next inference — which is how
// these tests see what the model actually got to read.
func noticeDeliveryModel(t *testing.T) (*model, *restartStubProvider) {
	t.Helper()

	provider := &restartStubProvider{requests: make(chan []core.Message, 2)}
	sess := &agent.Session{}
	if err := sess.Start(agent.BuildParams{Provider: provider, ModelID: "m"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sess.Stop)

	return &model{
		services: services{
			Agent:    sess,
			Tracker:  todo.NewStore(),
			Subagent: subagent.NewRegistry(),
			Reminder: reminder.NewService(),
		},
		conv: conv.NewModel(80),
	}, provider
}

func taskNotice() mainNotice {
	return mainNotice{
		Display:   "Backend: research completed",
		Content:   `<task-notification task-id="t1" status="completed">done</task-notification>`,
		FromAgent: true,
	}
}

// assertNoticeShown fails unless the notice's display line is in the
// conversation.
func assertNoticeShown(t *testing.T, m *model) {
	t.Helper()
	for _, msg := range m.conv.Messages {
		if msg.Role == core.RoleNotice && strings.Contains(msg.Content, "research completed") {
			return
		}
	}
	t.Fatalf("notice line was not shown in the conversation: %+v", m.conv.Messages)
}

// assertAgentRead fails unless the agent's next inference carried the task
// notification.
func assertAgentRead(t *testing.T, provider *restartStubProvider) {
	t.Helper()
	select {
	case chain := <-provider.requests:
		for _, msg := range chain {
			if strings.Contains(msg.Content, "task-notification") {
				return
			}
		}
		t.Fatalf("agent inference carried no task notification: %+v", chain)
	case <-time.After(2 * time.Second):
		t.Fatal("notice never reached the agent")
	}
}

// A task finishing while a tool runs must not be parked: the conversation tail
// is a tool-result row then, so the notice can be appended and handed to the
// running turn on arrival.
func TestNoticeDuringToolExecutionIsNotParked(t *testing.T) {
	m, _ := noticeDeliveryModel(t)
	m.conv.Stream.Active = true
	m.conv.Append(core.ChatMessage{Role: core.RoleUser, ToolResult: &core.ToolResult{ToolCallID: "c1"}})

	m.onMainNotice(taskNotice()) // cmds unused: the routing decision is synchronous

	if len(m.pendingNotices) != 0 {
		t.Fatalf("notice was parked even though the tail was not streaming: %+v", m.pendingNotices)
	}
	assertNoticeShown(t, m)
}

// Delivery into a running turn goes through the agent's inbox, which it drains
// between steps — so the content is in the conversation its next inference
// reads, without starting a turn of its own.
func TestDeliveryToRunningTurnReachesTheNextInference(t *testing.T) {
	m, provider := noticeDeliveryModel(t)
	m.conv.Stream.Active = true

	runCmd(m.injectIntoRunningTurn(taskNotice()))

	assertAgentRead(t, provider)
}

// The stream finds its message by position, so a notice cannot be appended while
// it is writing — the notice parks, and the next completed tool batch releases it
// into the same turn rather than holding it until the turn ends.
func TestNoticeParkedWhileStreamingIsReleasedAtTheNextStep(t *testing.T) {
	m, provider := noticeDeliveryModel(t)
	m.conv.Stream.Active = true
	m.conv.Append(core.ChatMessage{Role: core.RoleAssistant, Content: "thinking out loud"})

	m.onMainNotice(taskNotice()) // cmds unused: the routing decision is synchronous
	if len(m.pendingNotices) != 1 {
		t.Fatalf("notice was appended while the stream was writing: %+v", m.conv.Messages)
	}

	// The tool batch completes: its result lands, leaving an appendable tail.
	m.conv.Append(core.ChatMessage{Role: core.RoleUser, ToolResult: &core.ToolResult{ToolCallID: "c1"}})
	runCmd(m.OnStepEnd())

	if len(m.pendingNotices) != 0 {
		t.Fatalf("notice still parked after a step boundary: %+v", m.pendingNotices)
	}
	assertNoticeShown(t, m)
	assertAgentRead(t, provider)
}

// With nothing parked and nothing queued, a step boundary must not disturb the
// running turn: no command at all.
func TestStepDrainWithNothingPendingIsInert(t *testing.T) {
	m, _ := noticeDeliveryModel(t)
	m.conv.Stream.Active = true

	if cmd := m.OnStepEnd(); cmd != nil {
		t.Fatal("step drain produced work with nothing to release")
	}
	if len(m.conv.Messages) != 0 {
		t.Fatalf("step drain added messages with nothing to release: %+v", m.conv.Messages)
	}
}
