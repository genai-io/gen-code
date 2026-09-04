package session

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session/transcript"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// A user message that contains harness-injected <system-reminder> blocks must
// be persisted as multiple ContentBlocks: user-typed text with empty Source,
// reminder text with Source="reminder". Concatenating the text fields must
// reproduce the input byte-for-byte (round-trip safety for read path).
func Test_userContentToBlocks_splitsBySource(t *testing.T) {
	const reminder1 = "<system-reminder>\nskills directory\n</system-reminder>"
	const reminder2 = "<system-reminder>\nuser memory\n</system-reminder>"
	input := "hello\n\n" + reminder1 + "\n\n" + reminder2

	blocks := userContentToBlocks(input, "", nil)

	var sb strings.Builder
	var reminderCount, userCount int
	for _, b := range blocks {
		if b.Type != "text" {
			t.Fatalf("unexpected block type: %q", b.Type)
		}
		sb.WriteString(b.Text)
		switch b.Source {
		case SourceReminder:
			reminderCount++
			if !strings.HasPrefix(b.Text, "<system-reminder>") {
				t.Errorf("reminder block missing wrapper: %q", b.Text)
			}
		case "":
			userCount++
		default:
			t.Fatalf("unexpected Source: %q", b.Source)
		}
	}
	if sb.String() != input {
		t.Fatalf("round-trip mismatch:\n got: %q\nwant: %q", sb.String(), input)
	}
	if reminderCount != 2 {
		t.Fatalf("reminder block count = %d, want 2", reminderCount)
	}
	if userCount == 0 {
		t.Fatalf("expected at least one user block, got %d", userCount)
	}
}

// On resume, a reminder block must stay in Content (the model needs it) but be
// excluded from DisplayContent (the user must not see the harness scaffolding).
func Test_extractUserContent_stripsReminderFromDisplay(t *testing.T) {
	blocks := []ContentBlock{
		{Type: "text", Text: "hi\n\n"},
		{Type: "text", Text: "<system-reminder source=\"memory-auto\">\nmem\n</system-reminder>", Source: SourceReminder + ":memory-auto"},
	}
	var msg core.ChatMessage
	extractUserContent(blocks, &msg)

	if !strings.Contains(msg.Content, "system-reminder") {
		t.Fatalf("Content should keep the reminder for the model, got %q", msg.Content)
	}
	if strings.Contains(msg.DisplayContent, "system-reminder") {
		t.Fatalf("DisplayContent must not include the reminder, got %q", msg.DisplayContent)
	}
	if msg.DisplayContent != "hi" {
		t.Fatalf("DisplayContent = %q, want %q", msg.DisplayContent, "hi")
	}
}

// A slash-command invocation inlines the skill/custom-command body into the
// user message's Content (for the model) while DisplayContent stays the short
// command the user typed. On resume the body must round-trip as hidden context:
// preserved in Content, kept out of DisplayContent — otherwise the whole skill
// body re-renders as the user's turn.
func Test_commandEnvelope_hiddenFromDisplayOnResume(t *testing.T) {
	cases := map[string]struct{ content, display string }{
		"skill": {
			content: "<command-name>inkpost</command-name>\n\n<skill-invocation name=\"inkpost\">\nAvailable scripts (use Bash to execute):\n  - /path/shot.mjs\n\n# inkpost\n\nbody\n</skill-invocation>\n\nExecute the skill.",
			display: "Execute the skill.",
		},
		"skill with args": {
			content: "<command-name>inkpost</command-name>\n\n<skill-invocation name=\"inkpost\">\nbody\n</skill-invocation>\n\nmake a poster",
			display: "make a poster",
		},
		"custom command": {
			content: "<command-name>deploy</command-name>\n\n<custom-command name=\"deploy\">\nrun deploy\n</custom-command>\n\nship it",
			display: "ship it",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			blocks := userContentToBlocks(tc.content, tc.display, nil)

			// The envelope is tagged so it round-trips as hidden context.
			var joined strings.Builder
			var sawCommand bool
			for _, b := range blocks {
				joined.WriteString(b.Text)
				if b.Source == SourceCommand {
					sawCommand = true
				}
			}
			if !sawCommand {
				t.Fatalf("no block tagged Source=%q; envelope would leak into display", SourceCommand)
			}
			if joined.String() != tc.content {
				t.Fatalf("round-trip mismatch:\n got: %q\nwant: %q", joined.String(), tc.content)
			}

			var msg core.ChatMessage
			extractUserContent(blocks, &msg)
			if msg.Content != tc.content {
				t.Errorf("Content must keep the inlined body for the model:\n got %q\nwant %q", msg.Content, tc.content)
			}
			if msg.DisplayContent != tc.display {
				t.Errorf("DisplayContent = %q, want %q (skill body must not re-render on resume)", msg.DisplayContent, tc.display)
			}
		})
	}
}

// Plain user content with no reminders produces exactly one user-text block.
func Test_userContentToBlocks_plainTextOneBlock(t *testing.T) {
	blocks := userContentToBlocks("just a question", "", nil)
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Source != "" {
		t.Fatalf("expected 1 user-text block, got %+v", blocks)
	}
}

func Test_messagesToNodes_roundtrip(t *testing.T) {
	// Test that messagesToNodes -> messagesFromNodes roundtrips correctly.
	msgs := []core.ChatMessage{
		{Role: core.ChatUser, Content: "hello"},
		{Role: core.ChatAssistant, Content: "hi", Thinking: "let me think",
			ToolCalls: []core.ToolCall{{ID: "tc-1", Name: "Edit", Input: `{"path":"/tmp/test","edits":[{"oldText":"old","newText":"new"}]}`}}},
		{Role: core.ChatUser, ToolResult: &core.ToolResult{
			ToolCallID: "tc-1", ToolName: "Edit", Content: ai.TextContent("Edited /tmp/test (1 replacements, +1 -1)"),
		}, ToolDetails: toolresult.FileChangeDetails{Path: "/tmp/test", EditCount: 1, AddedLines: 1, RemovedLines: 1, UnifiedDiff: "@@ -1 +1 @@\n-old\n+new"}},
		{Role: core.ChatAssistant, ToolCalls: []core.ToolCall{{ID: "tc-2", Name: "Bash", Input: `{"command":"false"}`}}},
		{Role: core.ChatUser, ToolResult: &core.ToolResult{
			ToolCallID: "tc-2", ToolName: "Bash", Content: ai.TextContent("Error: exit code 1"), IsError: true,
		}, ToolDetails: toolresult.BashDetails{Error: "exit code 1", LineCount: 0}},
		{Role: core.ChatAssistant, Content: "I see the file."},
	}

	nodes := messagesToNodes(msgs, "/cwd", time.Time{}, "main")
	if len(nodes) != 6 {
		t.Fatalf("expected 6 nodes, got %d", len(nodes))
	}

	// Verify node roles
	if nodes[0].Role != "user" {
		t.Errorf("node[0] role: want user, got %s", nodes[0].Role)
	}
	if nodes[1].Role != "assistant" {
		t.Errorf("node[1] role: want assistant, got %s", nodes[1].Role)
	}
	if nodes[2].Role != "user" {
		t.Errorf("node[2] role: want user (tool_result), got %s", nodes[2].Role)
	}

	// Round-trip back to messages
	restored := messagesFromNodes(nodes)
	if len(restored) != 6 {
		t.Fatalf("expected 6 messages after roundtrip, got %d", len(restored))
	}
	if restored[0].Content != "hello" {
		t.Errorf("msg[0].Content: want 'hello', got %q", restored[0].Content)
	}
	if restored[1].Thinking != "let me think" {
		t.Errorf("msg[1].Thinking: want 'let me think', got %q", restored[1].Thinking)
	}
	if restored[2].ToolResult == nil {
		t.Fatal("msg[2].ToolResult should not be nil")
	}
	if restored[2].ToolResult.ToolCallID != "tc-1" {
		t.Errorf("msg[2].ToolResult.ToolCallID: want 'tc-1', got %q", restored[2].ToolResult.ToolCallID)
	}
	// Tool name should be resolved from the tool_use block
	if restored[2].ToolResult.ToolName != "Edit" {
		t.Errorf("msg[2].ToolResult.ToolName: want 'Edit', got %q", restored[2].ToolResult.ToolName)
	}
	details, ok := restored[2].ToolDetails.(toolresult.FileChangeDetails)
	if !ok || details.UnifiedDiff != "@@ -1 +1 @@\n-old\n+new" {
		t.Errorf("restored Edit details = %#v", restored[2].ToolDetails)
	}
	if restored[4].ToolResult == nil || restored[4].ToolResult.ToolName != "Bash" {
		t.Fatalf("restored Bash result = %#v", restored[4].ToolResult)
	}
	bashDetails, ok := restored[4].ToolDetails.(toolresult.BashDetails)
	if !ok || bashDetails.Error != "exit code 1" || bashDetails.LineCount != 0 {
		t.Errorf("restored Bash details = %#v", restored[4].ToolDetails)
	}
}

func Test_userContentToBlocks_preserveInlineImageOrder(t *testing.T) {
	blocks := userContentToBlocks(
		"这个图片说了什么 请说一下",
		"[Image #1] 这个图片说了什么 请说一下",
		[]core.Attachment{{Image: ai.Image{MediaType: "image/png", Data: "abc"}}},
	)

	if len(blocks) != 2 {
		t.Fatalf("expected image and text blocks, got %d", len(blocks))
	}
	if blocks[0].Type != "image" {
		t.Fatalf("expected first block to be image, got %q", blocks[0].Type)
	}
	if blocks[1].Type != "text" || blocks[1].Text != " 这个图片说了什么 请说一下" {
		t.Fatalf("unexpected second block: %#v", blocks[1])
	}
}

func Test_extractUserContent_restoresDisplayContent(t *testing.T) {
	msgs := messagesFromNodes([]transcript.Node{{
		Role: "user",
		Content: []ContentBlock{
			{Type: "text", Text: "前面 "},
			{Type: "image", ImageSource: &ImageSource{Type: "base64", MediaType: "image/png", Data: "abc"}},
			{Type: "text", Text: " 后面"},
		},
	}})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "前面  后面" {
		t.Fatalf("unexpected content: %q", msgs[0].Content)
	}
	if msgs[0].DisplayContent != "前面 [Image #1] 后面" {
		t.Fatalf("unexpected display content: %q", msgs[0].DisplayContent)
	}
}
