package conv

import (
	"github.com/genai-io/san/internal/core"
)

type StreamState struct {
	Active       bool
	BuildingTool string
}

func (s *StreamState) Stop() {
	s.Active = false
	s.BuildingTool = ""
}

type ConversationModel struct {
	Messages       []core.ChatMessage
	CommittedCount int
	Stream         StreamState
	Compact        CompactState
	Modal          ModalState
	Tool           ToolExecState
}

func NewConversation() ConversationModel {
	return ConversationModel{
		Messages: []core.ChatMessage{},
		Modal:    NewModalState(),
	}
}

func (m *ConversationModel) Append(msg core.ChatMessage) {
	// Stamp an ID once at append time so subsequent transcript saves can
	// dedupe by it. Without this, every save assigns a fresh UUID and the
	// append-only persistence path re-writes the entire history each turn.
	if msg.ID == "" {
		msg.ID = core.NewMessageID()
	}
	m.Messages = append(m.Messages, msg)
}

func (m *ConversationModel) Clear() {
	m.Messages = []core.ChatMessage{}
	m.CommittedCount = 0
}

func (m *ConversationModel) AddNotice(content string) {
	m.Messages = append(m.Messages, core.ChatMessage{Role: core.RoleNotice, Content: content})
}

// AddAgentNotice adds a notice that came from a background agent (a subagent
// completion or interim report), rendered distinctly from a plain system notice.
func (m *ConversationModel) AddAgentNotice(content string) {
	m.Messages = append(m.Messages, core.ChatMessage{Role: core.RoleNotice, Content: content, AgentNotice: true})
}

// LastMessageIsStreaming reports whether the stream is still writing into the
// last message. Everything that feeds a live message finds it by position rather
// than by ID — AppendToLast, SetLastToolCalls and SetLastThinkingSignature all
// reach for the last message and bail on anything else, and renderAndCommit
// holds a streaming last message back from scrollback. So while this holds, an
// append takes the slot the stream is using: the next chunk lands on the wrong
// message, and the half-streamed one freezes into scrollback.
//
// Tool execution does not hold it. A completed call appends its result, so the
// last message is a tool-result row that anything may append after.
func (m *ConversationModel) LastMessageIsStreaming() bool {
	if !m.Stream.Active || len(m.Messages) == 0 {
		return false
	}
	return m.Messages[len(m.Messages)-1].Role == core.RoleAssistant
}

func (m *ConversationModel) AppendToLast(text, thinking string) {
	if len(m.Messages) == 0 {
		return
	}
	idx := len(m.Messages) - 1
	if m.Messages[idx].Role != core.RoleAssistant {
		return
	}
	if thinking != "" {
		m.Messages[idx].Thinking += thinking
	}
	if text != "" {
		m.Messages[idx].Content += text
	}
}

func (m *ConversationModel) SetLastToolCalls(calls []core.ToolCall) {
	if len(m.Messages) == 0 {
		return
	}
	last := &m.Messages[len(m.Messages)-1]
	// Defensive: tool_calls only belong on an assistant message. Without
	// this check, a late PostInfer event landing after the cancel handler
	// has appended a trailing user marker would corrupt that marker.
	if last.Role != core.RoleAssistant {
		return
	}
	last.ToolCalls = calls
}

func (m *ConversationModel) SetLastThinkingSignature(sig string) {
	if sig == "" || len(m.Messages) == 0 {
		return
	}
	last := &m.Messages[len(m.Messages)-1]
	if last.Role != core.RoleAssistant {
		return
	}
	last.ThinkingSignature = sig
}

func (m *ConversationModel) AppendErrorToLast(err error) {
	if len(m.Messages) > 0 {
		idx := len(m.Messages) - 1
		m.Messages[idx].Content += "\n[Error: " + err.Error() + "]"
	}
}

func (m *ConversationModel) AppendCancelledToolResults(calls []core.ToolCall, contentFn func(core.ToolCall) string, decisionFn func(callID string) *core.ReviewDecision) {
	for _, tc := range calls {
		m.Append(core.ChatMessage{
			Role: core.RoleUser,
			ToolResult: &core.ToolResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Content:    contentFn(tc),
				IsError:    true,
			},
			// Consume the stashed auto-review decision so an interrupted judged
			// call still shows its annotation and its handoff entry is released
			// (this synthetic result never reaches the applyPostTool path).
			Decision: decisionFn(tc.ID),
		})
	}
}

// DropStreamingAssistant removes the trailing assistant row that the in-flight
// (now failed) inference was streaming into, so a retry re-streams onto a clean
// tail instead of duplicating or interleaving partial output. Only uncommitted
// rows are eligible, so committed scrollback is never touched.
func (m *ConversationModel) DropStreamingAssistant() {
	n := len(m.Messages)
	if n == 0 || n <= m.CommittedCount {
		return
	}
	if m.Messages[n-1].Role == core.RoleAssistant {
		m.Messages = m.Messages[:n-1]
	}
}

func (m *ConversationModel) RemoveEmptyLastAssistant() {
	if len(m.Messages) > 0 {
		last := m.Messages[len(m.Messages)-1]
		if last.Role == core.RoleAssistant && last.Content == "" {
			m.Messages = m.Messages[:len(m.Messages)-1]
		}
	}
}

func (m *ConversationModel) MarkLastInterrupted() {
	for i := len(m.Messages) - 1; i >= 0; i-- {
		msg := &m.Messages[i]
		if msg.Role != core.RoleAssistant {
			continue
		}
		if len(msg.ToolCalls) == 0 {
			if msg.Content == "" {
				msg.Content = InterruptedMarker
			} else {
				msg.Content += " " + InterruptedMarker
			}
		}
		return
	}
}

// Toggling only touches the live (uncommitted) tail: committed messages are
// already in the terminal's native scrollback and can't be re-rendered in
// place, so they stay frozen as last drawn.

func (m *ConversationModel) ToggleMostRecentExpandable() {
	for i := len(m.Messages) - 1; i >= m.CommittedCount; i-- {
		msg := &m.Messages[i]
		switch {
		case msg.ToolResult != nil:
			msg.Expanded = !msg.Expanded
			return
		case len(msg.ToolCalls) > 0:
			msg.ToolCallsExpanded = !msg.ToolCallsExpanded
			return
		}
	}
}

func (m *ConversationModel) ToggleAllExpandable() {
	anyExpanded := false
	for i := m.CommittedCount; i < len(m.Messages); i++ {
		msg := m.Messages[i]
		if (msg.ToolResult != nil && msg.Expanded) ||
			(len(msg.ToolCalls) > 0 && msg.ToolCallsExpanded) {
			anyExpanded = true
			break
		}
	}
	for i := m.CommittedCount; i < len(m.Messages); i++ {
		if m.Messages[i].ToolResult != nil {
			m.Messages[i].Expanded = !anyExpanded
		}
		if len(m.Messages[i].ToolCalls) > 0 {
			m.Messages[i].ToolCallsExpanded = !anyExpanded
		}
	}
}

func (m *ConversationModel) HasAllToolResults(idx int) bool {
	if idx < 0 || idx >= len(m.Messages) {
		return true
	}
	toolCalls := m.Messages[idx].ToolCalls
	if len(toolCalls) == 0 {
		return true
	}

	expected := make(map[string]bool, len(toolCalls))
	for _, tc := range toolCalls {
		expected[tc.ID] = false
	}

	for j := idx + 1; j < len(m.Messages); j++ {
		msg := m.Messages[j]
		if msg.Role == core.RoleNotice {
			continue
		}
		if msg.ToolResult == nil {
			break
		}
		if _, ok := expected[msg.ToolResult.ToolCallID]; ok {
			expected[msg.ToolResult.ToolCallID] = true
		}
		allFound := true
		for _, found := range expected {
			if !found {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}

	return false
}

func (m ConversationModel) ConvertToProvider() []core.Message {
	return m.ConvertToProviderFrom(0)
}

func (m ConversationModel) ConvertToProviderFrom(startIdx int) []core.Message {
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx > len(m.Messages) {
		startIdx = len(m.Messages)
	}
	providerMsgs := make([]core.Message, 0, len(m.Messages)-startIdx)
	for i := startIdx; i < len(m.Messages); i++ {
		msg := m.Messages[i]
		if msg.Role == core.RoleNotice {
			continue
		}
		providerMsgs = append(providerMsgs, msg.ToMessage())
	}
	return providerMsgs
}
