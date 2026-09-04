package core

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
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

// ChatRole says who put a row on the screen, which is a different question
// from who spoke in the conversation and has a different answer set.
//
// The conversation has two producers, the person and the model, and that is
// ai.Role. The interface has three: San writes rows of its own — a notice, a
// status line, the summary shown after a compaction — that the model must
// never see. Modelling that as a third ai.Role would put messages the model
// cannot be shown into the type the model is sent, and leave every consumer
// remembering that a role might not be one.
//
// A tool result is a ChatUser row carrying a non-nil ToolResult, which is the
// wire shape every provider expects. Tell it from a typed turn by
// ToolResult != nil, never by the role.
type ChatRole string

const (
	ChatUser      ChatRole = "user"
	ChatAssistant ChatRole = "assistant"
	// ChatNotice is San talking to the person. It never reaches a model.
	ChatNotice ChatRole = "notice"
)

// AIRole is the conversation role this row maps to. A notice has none: it is
// not a turn, which is why ToAI drops it rather than translating it.
func (r ChatRole) AIRole() (ai.Role, bool) {
	switch r {
	case ChatUser:
		return ai.RoleUser, true
	case ChatAssistant:
		return ai.RoleAssistant, true
	}
	return "", false
}

// Inbound is what arrives on an agent's inbox: a message to add, or a signal
// to act on. They ride one channel so that both land at a phase boundary on
// the agent's own goroutine, in the order they were sent.
//
// It exists because ai.Message is the conversation and a signal is not part of
// one. It goes away with the inbox itself, when the loop becomes pkg/agent's:
// there a stop is Interrupt and a compaction is SetMessages, both methods.
type Inbound struct {
	Msg    Message
	Signal Signal
	// Summary carries SigCompact's precomputed replacement.
	Summary string
}

// Signal represents control signals sent through channels.
type Signal string

const (
	SigStop Signal = "stop"
	// SigCompact asks the agent to compact in place using a precomputed
	// summary carried in the message Content. Handled at a phase boundary on
	// the agent's own goroutine, so it never races the conversation chain.
	SigCompact Signal = "compact"
)

// Message is the conversation, and it is the SDK's — one ordered sequence of
// blocks, in the order the model produced them, which is the order every
// protocol wants them replayed. San's own flat fields live on ChatMessage,
// where the interface reads them; the conversion between the two is ToMessage.
type Message = ai.Message

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
	Role              ChatRole
	Content           string
	DisplayContent    string
	Thinking          string
	ThinkingSignature string
	// Reasoning is the opaque state a Responses model must have echoed back.
	// It is unreadable and never drawn — it is here because a conversation
	// that goes through the view and back must come out whole, and before
	// this field it did not.
	Reasoning         []ReasoningItem
	Images            []Attachment
	ToolCalls         []ToolCall
	ToolResult        *ToolResult
	ToolCallsExpanded bool // UI: the assistant's tool-call block is expanded
	Expanded          bool // UI: the tool-result block is expanded

	// ToolDetails is the structured form of what a tool produced — the diff
	// behind an edit, the exit code behind a command — kept so the interface
	// can draw more than the text the model was given. The model never sees
	// it, which is why it is here and not on ToolResult: a field the
	// conversation carries is a field a provider is sent.
	ToolDetails any

	// Decision is the auto-review judge's decision for the tool call this message
	// carries the result of — set only on a ai.RoleUser/ToolResult message whose
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
//
// A notice is not a turn and has no conversation role, so it converts to
// nothing and says so. Callers that were filtering notices by role now ask
// here instead, which is the one place that knows.
func (c ChatMessage) ToMessage() (Message, bool) {
	role, ok := c.Role.AIRole()
	if !ok {
		return Message{}, false
	}
	if c.ToolResult != nil {
		msg := ToolResultMessage(*c.ToolResult)
		msg.ID = c.ID
		return msg, true
	}
	msg := Message{ID: c.ID, Role: role}
	if role == ai.RoleAssistant {
		msg.Content = c.assistantContent()
	} else {
		msg.Content = c.userContent()
	}
	return msg, true
}

// userContent keeps text and pictures in the order they were typed where the
// row records one, which the [Image #N] tokens in DisplayContent are.
func (c ChatMessage) userContent() ai.Content {
	parts := InterleavedContentParts(c)
	if parts == nil {
		content := ai.TextContent(c.Content)
		for _, img := range c.Images {
			content = append(content, ai.ImageBlock(img.Image))
		}
		return content
	}
	content := make(ai.Content, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case ContentPartText:
			if part.Text != "" {
				content = append(content, ai.TextBlock(part.Text))
			}
		case ContentPartImage:
			if part.Image != nil {
				content = append(content, ai.ImageBlock(part.Image.Image))
			}
		}
	}
	return content
}

// assistantContent lays a model turn down in replay order: reasoning first —
// Anthropic rejects a thinking block that does not lead, and a Responses call
// whose reasoning item does not precede it — then the answer, then the calls.
// Which of the reasoning the endpoint can take back is ai.Model's to decide.
func (c ChatMessage) assistantContent() ai.Content {
	content := make(ai.Content, 0, 2+len(c.Reasoning)+len(c.ToolCalls))
	if c.Thinking != "" {
		content = append(content, ai.ThinkingBlock(c.Thinking, c.ThinkingSignature))
	}
	for _, item := range c.Reasoning {
		content = append(content, ai.ReasoningBlock(ai.ReasoningItem(item)))
	}
	if c.Content != "" {
		content = append(content, ai.TextBlock(c.Content))
	}
	for _, call := range c.ToolCalls {
		content = append(content, ai.ToolCallBlock(call))
	}
	return content
}

// ChatOf projects a conversation turn onto the flat fields the interface
// reads, with no display state set. The mirror of ToMessage, and the reason
// both live here: a block kind that gains a field has one place to gain it.
func ChatOf(m Message) ChatMessage {
	c := ChatMessage{ID: m.ID, Role: ChatRole(m.Role), Content: m.Text()}
	for _, block := range m.Content {
		switch block.Type {
		case ai.BlockThinking:
			c.Thinking += block.Text
			if block.Signature != "" {
				c.ThinkingSignature = block.Signature
			}
		case ai.BlockReasoning:
			if block.Reasoning != nil {
				c.Reasoning = append(c.Reasoning, ReasoningItem(*block.Reasoning))
			}
		case ai.BlockImage:
			if block.Image != nil {
				c.Images = append(c.Images, Attachment{Image: *block.Image})
			}
		case ai.BlockToolCall:
			if block.ToolCall != nil {
				c.ToolCalls = append(c.ToolCalls, *block.ToolCall)
			}
		case ai.BlockToolResult:
			if block.ToolResult != nil {
				tr := *block.ToolResult
				c.ToolResult = &tr
			}
		}
	}
	return c
}

// Attachment is a picture a person attached, and where it came from.
//
// The picture itself is ai.Image — bytes and a media type — and that is all the
// model is ever sent. Path is San's: the absolute source path when the file
// came from disk, empty for a clipboard paste. It is provenance, a fact about
// the act of attaching rather than about the image, which is why it lives out
// here and not on the conversation.
//
// It buys two things. A model that cannot see pictures is handed a path to
// open instead, and a pasted image is not written to a temp file twice.
type Attachment struct {
	ai.Image
	Path string `json:"path,omitempty"`
}

// ToolCall represents a tool call from the model.
type ToolCall = ai.ToolCall

// ReasoningItem is the SDK's: opaque provider reasoning state, replayed
// untouched or a reasoning model starts over.
type ReasoningItem = ai.ReasoningItem

// ToolResult is what a tool call came to say, as the model is told it. It is
// ai.ToolResult: the same four things, with Content an ordered sequence rather
// than a string, so a tool that looked at something can answer with it — a
// screenshot, a rendered chart — on the protocols that carry one.
type ToolResult = ai.ToolResult

// --- Constructors ---

// UserMessage creates a user turn: the text, then any pictures attached to it.
func UserMessage(text string, images []Attachment) Message {
	content := ai.TextContent(text)
	for _, img := range images {
		content = append(content, ai.ImageBlock(img.Image))
	}
	return Message{Role: ai.RoleUser, Content: content}
}

// AssistantMessage creates a model turn in replay order: what it thought,
// what it said, then what it asked to run.
func AssistantMessage(text, thinking string, calls []ToolCall) Message {
	content := make(ai.Content, 0, 2+len(calls))
	if thinking != "" {
		content = append(content, ai.ThinkingBlock(thinking, ""))
	}
	if text != "" {
		content = append(content, ai.TextBlock(text))
	}
	for _, call := range calls {
		content = append(content, ai.ToolCallBlock(call))
	}
	return Message{Role: ai.RoleAssistant, Content: content}
}

// ErrorResult creates an error ToolResult for a tool call.
func ErrorResult(tc ToolCall, content string) *ToolResult {
	return &ToolResult{
		ToolCallID: tc.ID,
		ToolName:   tc.Name,
		Content:    ai.TextContent(content),
		IsError:    true,
	}
}

// ToolResultMessage creates a tool result message.
func ToolResultMessage(result ToolResult) Message {
	return ai.ToolResultsMessage(result)
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

// BuildCompactionText renders the conversation for summarization, stripping
// the trailing <system-reminder> blocks from each user message and dropping
// messages that were nothing but reminders: reminders re-emit fresh after a
// compaction, so the summary should capture only real conversation turns.
func BuildCompactionText(msgs []Message) string {
	var sb strings.Builder
	writeConversationText(&sb, msgs)
	return sb.String()
}

// writeConversationText renders plain text for conversation summarization.
func writeConversationText(w io.Writer, msgs []Message) {
	io.WriteString(w, "Please summarize this coding conversation:\n\n")

	for _, msg := range msgs {
		switch msg.Role {
		case ai.RoleUser:
			if results := msg.ToolResults(); len(results) > 0 {
				for _, tr := range results {
					content := tr.Content.Text()
					if len(content) > 500 {
						content = content[:500] + "...[truncated]"
					}
					fmt.Fprintf(w, "[Tool Result: %s]\n%s\n\n", tr.ToolName, content)
				}
			} else {
				content := msg.Text()
				content = stripSystemReminders(content)
				if content == "" {
					continue
				}
				fmt.Fprintf(w, "User: %s\n\n", content)
			}

		case ai.RoleAssistant:
			if text := msg.Text(); text != "" {
				fmt.Fprintf(w, "Assistant: %s\n\n", text)
			}
			if calls := msg.ToolCalls(); len(calls) > 0 {
				counts := make(map[string]int, len(calls))
				order := make([]string, 0, len(calls))
				for _, tc := range calls {
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
	for _, msg := range slices.Backward(msgs) {
		if msg.Role == ChatAssistant && msg.Content != "" {
			return msg.Content
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
// ai.Usage.TotalInput — not just the uncached delta.
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
	Image *Attachment
}

// InlineImageTokenRe matches the "[Image #N]" placeholder tokens that stand in
// for image attachments in a message's DisplayContent. It is the single
// definition of that wire token format, shared by the display and persistence
// layers so they parse it identically.
var InlineImageTokenRe = regexp.MustCompile(`\[Image #(\d+)\]`)

// InterleavedContentParts parses [Image #N] tokens from display content and returns
// interleaved text and image parts.
func InterleavedContentParts(msg ChatMessage) []ContentPart {
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
