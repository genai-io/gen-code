package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// NewMessageID returns a fresh short hex identifier for a Message.
// 8 bytes (16 hex chars) — collision space is large enough for the
// per-session message volume we ever see; brevity matters because the
// ID appears in every transcript record's id field.
func NewMessageID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Role identifies who produced a message in the conversation.
//
// A tool result is a RoleUser message carrying a non-nil ToolResult — that is
// the wire shape every provider expects (tool_result blocks ride inside a
// user turn), so it is also how we hold them in history. Distinguish a
// tool-result turn from a user-typed turn by ToolResult != nil, never by Role.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleNotice    Role = "notice"
)

// Signal represents control signals sent through channels.
type Signal string

const (
	SigStop Signal = "stop"
	// SigCompact asks the agent to compact in place using a precomputed
	// summary carried in the message Content. Handled at a phase boundary on
	// the agent's own goroutine, so it never races the conversation chain.
	SigCompact Signal = "compact"
)

// Message is the wire/agent-chain message: the unit the LLM provider and the
// agent run loop exchange, append to history, and persist. It holds no
// UI/display state — for the rendered TUI view-model, see ChatMessage.
type Message struct {
	ID             string  `json:"id,omitempty"`
	Role           Role    `json:"role"`
	Content        string  `json:"content,omitempty"`
	DisplayContent string  `json:"display_content,omitempty"`
	Images         []Image `json:"images,omitempty"`
	// Reasoning is carried in one of two provider shapes; they are mutually
	// exclusive on a given message.
	//
	// Thinking is the human-readable reasoning text shown in the UI. Nearly
	// every reasoning provider populates it (Anthropic; DeepSeek/Alibaba/
	// Moonshot/BigModel/Ollama via OpenAI-compat; OpenAI's reasoning summary).
	//
	// The other two carry provider-specific state for replaying reasoning on
	// the next turn:
	//   - ThinkingSignature: Anthropic's opaque token, paired with Thinking to
	//     replay that one thinking block verbatim.
	//   - Reasoning: OpenAI ChatGPT subscription (stateless store=false) —
	//     ordered {id, encrypted_content} items echoed back before each
	//     function_call. Here Thinking is display-only, not replayed.
	Thinking          string          `json:"thinking,omitempty"`
	ThinkingSignature string          `json:"thinking_signature,omitempty"`
	Reasoning         []ReasoningItem `json:"reasoning,omitempty"`
	ToolCalls         []ToolCall      `json:"tool_calls,omitempty"`
	ToolResult        *ToolResult     `json:"tool_result,omitempty"`
	Signal            Signal          `json:"-"`
}

// ReviewDecision is the auto-review judge's display-only outcome for one
// gray-zone tool call: whether it was auto-approved (vs. escalated to the user)
// and the judge's one-sentence reason. Rendered inline under the tool call it
// annotates; never persisted or sent to the provider.
type ReviewDecision struct {
	Approved bool
	Reason   string
}

// ChatMessage is the TUI view-model for one conversation entry: the same
// content as Message plus transient display state (the expand/collapse
// toggles). The app layer renders ChatMessages and converts them back to
// Message before sending to the provider — see
// conv.ConversationModel.ConvertToProvider.
//
// The tool's name lives on ToolResult.ToolName (the single source of truth),
// not on the ChatMessage itself.
type ChatMessage struct {
	// ID is a stable per-message identifier assigned once at construction.
	// The session.Save path uses it to dedupe appends, so it must not change
	// across saves of the same message — empty IDs would trigger re-appends
	// of the entire conversation on every persist.
	ID                string
	Role              Role
	Content           string
	DisplayContent    string
	Thinking          string
	ThinkingSignature string
	Images            []Image
	ToolCalls         []ToolCall
	ToolResult        *ToolResult
	ToolCallsExpanded bool // UI: the assistant's tool-call block is expanded
	Expanded          bool // UI: the tool-result block is expanded

	// Decision is the auto-review judge's decision for the tool call this message
	// carries the result of — set only on a RoleUser/ToolResult message whose
	// call was judged, so the renderer can draw the decision inline under the
	// tool call. Display-only: dropped by ToMessage, never persisted.
	Decision *ReviewDecision

	// AutopilotNote, when set, marks a user message the copilot produced — a
	// continuation ("2/5") or a rewrite ("refined") — and the renderer hangs a
	// green "⎿ autopilot · <note>" annotation under its "❭" line to show the
	// copilot typed it. Display-only: dropped by ToMessage, never persisted.
	AutopilotNote string

	// AgentNotice marks a RoleNotice that carries a message from a background
	// agent (a subagent completion or an interim report) rather than a plain
	// system notice, so the renderer styles it distinctly. Display-only.
	AgentNotice bool

	// Streaming-commit progress. While an assistant message streams, completed
	// markdown blocks are flushed to native scrollback (tea.Println) as they
	// finish, so the live view and the turn-end commit render only the
	// not-yet-committed remainder. These track how much is already in
	// scrollback. Non-zero only on the in-flight trailing message — reset to 0
	// once it is fully committed, so a later full rebuild (resize reflow,
	// compact reprint) renders it whole. Transient UI state, never persisted.
	ContentCommittedLen  int  // bytes of Content already flushed to scrollback
	ThinkingCommittedLen int  // bytes of Thinking already flushed to scrollback
	BulletEmitted        bool // the "● " content marker has already been emitted
	ThinkingEmitted      bool // the "✦ " thinking marker has already been emitted
}

// ResetStreamCommit clears the streaming-commit progress so the message renders
// whole again — used once it is fully committed, or when a full rebuild reprints
// scrollback from scratch.
func (m *ChatMessage) ResetStreamCommit() {
	m.ContentCommittedLen = 0
	m.ThinkingCommittedLen = 0
	m.BulletEmitted = false
	m.ThinkingEmitted = false
}

// ToMessage returns the wire/agent Message underlying this view-model, dropping
// the transient display state. The ToolResult is deep-copied so a provider can
// consume the result without aliasing conv's copy. This is the single Chat →
// Message field mapping — every converter (provider, transcript) routes through
// it so a new field can never be forgotten in one path.
func (c ChatMessage) ToMessage() Message {
	msg := Message{
		ID:                c.ID,
		Role:              c.Role,
		Content:           c.Content,
		DisplayContent:    c.DisplayContent,
		Images:            c.Images,
		Thinking:          c.Thinking,
		ThinkingSignature: c.ThinkingSignature,
		ToolCalls:         c.ToolCalls,
	}
	if c.ToolResult != nil {
		tr := *c.ToolResult
		msg.ToolResult = &tr
	}
	return msg
}

// ToChat wraps a wire/agent Message as a fresh view-model with no display state
// set (expand toggles collapsed, streaming offsets zero). The single Message →
// Chat field mapping, mirroring ToMessage.
func (m Message) ToChat() ChatMessage {
	return ChatMessage{
		ID:                m.ID,
		Role:              m.Role,
		Content:           m.Content,
		DisplayContent:    m.DisplayContent,
		Images:            m.Images,
		Thinking:          m.Thinking,
		ThinkingSignature: m.ThinkingSignature,
		ToolCalls:         m.ToolCalls,
		ToolResult:        m.ToolResult,
	}
}

// Image represents an image attachment.
type Image struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	FileName  string `json:"file_name"`
	// Path is the absolute source path on disk, when the image came from a
	// file. Empty for images that have no backing file (e.g. clipboard pastes).
	Path string `json:"path,omitempty"`
	Size int    `json:"size"`
}

// ToolCall represents a tool call from the model.
type ToolCall struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Input            string `json:"input"`
	ThoughtSignature []byte `json:"thought_signature,omitempty"` // Google Gemini: opaque signature to echo back
}

// ReasoningItem is an opaque reasoning block emitted by a model and echoed back
// on the next request. Used by OpenAI's stateless (store=false) ChatGPT
// subscription backend, where a reasoning model's function_call must be preceded
// by its reasoning item; EncryptedContent lets the model restore the reasoning
// without server-side state.
type ReasoningItem struct {
	ID               string `json:"id"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	Summary          string `json:"summary,omitempty"`
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	ToolCallID   string `json:"tool_call_id"`
	ToolName     string `json:"tool_name,omitempty"`
	Content      string `json:"content"`
	IsError      bool   `json:"is_error,omitempty"`
	HookResponse any    `json:"-"`
	Details      any    `json:"-"`
}

// --- Constructors ---

// UserMessage creates a user message with optional images.
func UserMessage(text string, images []Image) Message {
	return Message{
		Role:           RoleUser,
		Content:        text,
		DisplayContent: text,
		Images:         images,
	}
}

// AssistantMessage creates an assistant message.
func AssistantMessage(text, thinking string, calls []ToolCall) Message {
	return Message{
		Role:      RoleAssistant,
		Content:   text,
		Thinking:  thinking,
		ToolCalls: calls,
	}
}

// ErrorResult creates an error ToolResult for a tool call.
func ErrorResult(tc ToolCall, content string) *ToolResult {
	return &ToolResult{
		ToolCallID: tc.ID,
		ToolName:   tc.Name,
		Content:    content,
		IsError:    true,
	}
}

// ToolResultMessage creates a tool result message.
func ToolResultMessage(result ToolResult) Message {
	return Message{
		Role:       RoleUser,
		ToolResult: &result,
	}
}

// --- Utilities ---

// ParseToolInput deserializes JSON tool input into a params map.
func ParseToolInput(input string) (map[string]any, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return map[string]any{}, nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, err
	}
	return params, nil
}

const (
	systemReminderOpen  = "<system-reminder"
	systemReminderClose = "</system-reminder>"
)

// stripSystemReminders removes the trailing run of harness <system-reminder>
// blocks from user message content. AttachToContent always appends reminders
// after the user's own text, so we peel whole blocks off the end: while the
// right-trimmed content ends with a closing tag, cut back to the last opening
// tag.
//
// Anchoring on the last *opening* tag (not a regex over the merged text) is
// what makes this robust. A closing tag "</system-reminder>" never contains
// the opening-tag prefix "<system-reminder", so a reminder body that happens
// to include the literal "</system-reminder>" is still removed in full; and a
// <system-reminder> the user typed mid-message (followed by their own prose)
// is left untouched, because once the trailing block is peeled the remaining
// text no longer ends in a closing tag and the loop stops.
//
// Reminders (skills, memory, one-time notices) re-emit fresh after compaction,
// so the summary should capture only real conversation turns.
func stripSystemReminders(content string) string {
	for {
		trimmed := strings.TrimRight(content, " \t\r\n")
		if !strings.HasSuffix(trimmed, systemReminderClose) {
			break
		}
		open := strings.LastIndex(trimmed, systemReminderOpen)
		if open < 0 {
			break
		}
		content = trimmed[:open]
	}
	return strings.TrimSpace(content)
}

// BuildConversationText converts messages to text, keeping harness
// <system-reminder> content intact. Use it where the real prompt size matters
// (e.g. conversation-growth estimation for proactive compaction), since the
// reminders are part of what actually gets sent to the model.
func BuildConversationText(msgs []Message) string {
	return buildConversationText(msgs, false)
}

// BuildCompactionText is like BuildConversationText but strips the trailing
// <system-reminder> blocks from each user message and drops messages that were
// nothing but reminders. Use it for summarization: reminders re-emit fresh
// after compaction, so the summary should capture only real conversation turns.
func BuildCompactionText(msgs []Message) string {
	return buildConversationText(msgs, true)
}

func buildConversationText(msgs []Message, stripReminders bool) string {
	var sb strings.Builder
	writeConversationText(&sb, msgs, stripReminders)
	return sb.String()
}

// conversationTextLen returns the conversation text length without allocating it.
// It shares the formatter with BuildConversationText to keep both results aligned.
func conversationTextLen(msgs []Message) int {
	var c lenCounter
	writeConversationText(&c, msgs, false)
	return int(c)
}

// lenCounter counts bytes written without storing them.
type lenCounter int

func (c *lenCounter) Write(p []byte) (int, error) {
	*c += lenCounter(len(p))
	return len(p), nil
}

// writeConversationText renders plain text for conversation summarization.
// Both builders and counters use it to produce matching output.
func writeConversationText(w io.Writer, msgs []Message, stripReminders bool) {
	io.WriteString(w, "Please summarize this coding conversation:\n\n")

	for _, msg := range msgs {
		switch msg.Role {
		case RoleUser:
			if msg.ToolResult != nil {
				content := msg.ToolResult.Content
				if len(content) > 500 {
					content = content[:500] + "...[truncated]"
				}
				fmt.Fprintf(w, "[Tool Result: %s]\n%s\n\n", msg.ToolResult.ToolName, content)
			} else {
				content := msg.Content
				if stripReminders {
					content = stripSystemReminders(content)
					if content == "" {
						continue
					}
				}
				fmt.Fprintf(w, "User: %s\n\n", content)
			}

		case RoleAssistant:
			if msg.Content != "" {
				fmt.Fprintf(w, "Assistant: %s\n\n", msg.Content)
			}
			if len(msg.ToolCalls) > 0 {
				counts := make(map[string]int, len(msg.ToolCalls))
				order := make([]string, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					if counts[tc.Name] == 0 {
						order = append(order, tc.Name)
					}
					counts[tc.Name]++
				}
				parts := make([]string, 0, len(order))
				for _, name := range order {
					if counts[name] == 1 {
						parts = append(parts, name)
					} else {
						parts = append(parts, fmt.Sprintf("%s × %d", name, counts[name]))
					}
				}
				fmt.Fprintf(w, "[Tool Calls: %s]\n", strings.Join(parts, ", "))
				io.WriteString(w, "\n")
			}
		}
	}
}

// LastAssistantChatContent returns the most recent non-empty assistant content from chat messages.
func LastAssistantChatContent(msgs []ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleAssistant && msgs[i].Content != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// AutoCompactThresholdPercent is the share of the model's input limit at which
// the conversation is auto-compacted. The status bar's critical tier derives
// from this constant so the bar turns critical exactly when compaction is due —
// two separate literals would let the display and the trigger drift apart.
const AutoCompactThresholdPercent = 90

// NeedsCompaction reports whether the prompt has reached
// AutoCompactThresholdPercent of the model's input limit. promptTokens must be
// the FULL prompt size — including any cache-read/cache-creation portion, i.e.
// InferResponse.TotalInputTokens — not just the uncached delta.
func NeedsCompaction(promptTokens, inputLimit int) bool {
	if inputLimit == 0 || promptTokens == 0 {
		return false
	}
	return float64(promptTokens)/float64(inputLimit)*100 >= AutoCompactThresholdPercent
}

// --- Content Parts ---

// ContentPartType distinguishes text from image in interleaved content.
type ContentPartType string

const (
	ContentPartText  ContentPartType = "text"
	ContentPartImage ContentPartType = "image"
)

// ContentPart represents a text or image segment in interleaved content.
type ContentPart struct {
	Type  ContentPartType
	Text  string
	Image *Image
}

// InlineImageTokenRe matches the "[Image #N]" placeholder tokens that stand in
// for image attachments in a message's DisplayContent. It is the single
// definition of that wire token format, shared by the display and persistence
// layers so they parse it identically.
var InlineImageTokenRe = regexp.MustCompile(`\[Image #(\d+)\]`)

// InterleavedContentParts parses [Image #N] tokens from display content and returns
// interleaved text and image parts.
func InterleavedContentParts(msg Message) []ContentPart {
	if len(msg.Images) == 0 || msg.DisplayContent == "" || !InlineImageTokenRe.MatchString(msg.DisplayContent) {
		return nil
	}

	idToIdx := BuildImageIDMap(msg.DisplayContent, len(msg.Images))

	var parts []ContentPart
	last := 0
	matches := InlineImageTokenRe.FindAllStringSubmatchIndex(msg.DisplayContent, -1)
	if len(matches) > 0 {
		parts = make([]ContentPart, 0, len(matches)*2+1)
	}
	for _, match := range matches {
		start, end := match[0], match[1]
		idStart, idEnd := match[2], match[3]

		if text := msg.DisplayContent[last:start]; text != "" {
			parts = append(parts, ContentPart{Type: ContentPartText, Text: text})
		}

		id, err := strconv.Atoi(msg.DisplayContent[idStart:idEnd])
		if err == nil {
			if idx, ok := idToIdx[id]; ok && idx < len(msg.Images) {
				img := msg.Images[idx]
				parts = append(parts, ContentPart{Type: ContentPartImage, Image: &img})
			}
		}

		last = end
	}

	if tail := msg.DisplayContent[last:]; tail != "" {
		parts = append(parts, ContentPart{Type: ContentPartText, Text: tail})
	}

	if len(parts) == 0 {
		return nil
	}
	return parts
}

// BuildImageIDMap parses [Image #N] tokens from displayContent and returns a map
// from token ID to sequential index (0-based). imageCount caps the number of entries.
func BuildImageIDMap(displayContent string, imageCount int) map[int]int {
	m := make(map[int]int, imageCount)
	matches := InlineImageTokenRe.FindAllStringSubmatch(displayContent, -1)
	idx := 0
	for _, match := range matches {
		id, err := strconv.Atoi(match[1])
		if err == nil && idx < imageCount {
			m[id] = idx
			idx++
		}
	}
	return m
}
