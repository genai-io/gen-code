package session

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// A user message's Content can carry text the harness injected for the model
// but the user never typed. Two kinds, matched by the sub-patterns below:
//
//   - reminderPattern — a <system-reminder> block. reminder.AttachToContent
//     appends these (session/project context, hook additionalContext); capture
//     group 1 is the optional source="…" attribute that names the injecting
//     provider (skills-directory, memory-user, …).
//   - commandEnvelopePattern — the <command-name> + inlined skill/custom-command
//     body a slash-command invocation prepends (input.ConsumeInvocation,
//     command.WrapInvocation). Matched as one unit (name tag, body block, and the
//     trailing blank line) so the user's own text after it stays clean.
//
// Persisting these with a Source lets a resumed session keep them out of the
// visible message while preserving them in Content for the model — the same
// split RenderUserMessage draws live from DisplayContent vs Content.
// The reminder half comes from internal/reminder, which is also what writes
// the tag — spelling it out again here is how this splitter would silently
// stop recognizing reminders after a change to the wrapper.
const commandEnvelopePattern = `<command-name>.*?</command-name>\s*(?:<skill-invocation\b.*?</skill-invocation>|<custom-command\b.*?</custom-command>)\n*`

// harnessInjectedRe matches any harness-injected span. The command envelope only
// ever leads the message, so it's anchored; reminders can appear anywhere.
var harnessInjectedRe = regexp.MustCompile(`(?s)` + reminder.Pattern + `|^` + commandEnvelopePattern)

const (
	SourceReminder = "reminder"
	SourceCommand  = "command"
)

// isHiddenSource reports whether a content block's Source marks it as
// harness-injected text that must be kept out of the user-visible display on
// resume. Reminder sources carry an optional ":provider" suffix, so match by
// prefix.
func isHiddenSource(source string) bool {
	return source == SourceCommand || strings.HasPrefix(source, SourceReminder)
}

// splitTextBySource returns ContentBlocks that together reproduce the input
// byte-for-byte, tagging each harness-injected span (see harnessInjectedRe) with
// its Source so the read path (extractUserContent) can rebuild both the model's
// Content and the user's display text. Empty input returns nil; input with no
// injected spans returns a single untagged block.
func splitTextBySource(text string) []ContentBlock {
	if text == "" {
		return nil
	}
	matches := harnessInjectedRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []ContentBlock{{Type: "text", Text: text}}
	}

	blocks := make([]ContentBlock, 0, 2*len(matches)+1)
	cursor := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > cursor {
			blocks = append(blocks, ContentBlock{Type: "text", Text: text[cursor:start]})
		}
		blocks = append(blocks, ContentBlock{Type: "text", Text: text[start:end], Source: injectedSource(text, m)})
		cursor = end
	}
	if cursor < len(text) {
		blocks = append(blocks, ContentBlock{Type: "text", Text: text[cursor:]})
	}
	return blocks
}

// injectedSource classifies a harnessInjectedRe match. The command envelope is
// the alternative that starts with <command-name>; everything else is a
// reminder, which may carry a ":provider" attribution from capture group 1.
func injectedSource(text string, m []int) string {
	if strings.HasPrefix(text[m[0]:m[1]], "<command-name>") {
		return SourceCommand
	}
	if m[2] >= 0 && m[3] > m[2] {
		return SourceReminder + ":" + text[m[2]:m[3]]
	}
	return SourceReminder
}

// MessageToBlocks converts a wire message into the transcript content blocks that
// represent it on disk. It is the single source of truth for that mapping,
// shared by the append-only save path (messagesToNodes) and the live Recorder
// (onAppend), so a message serializes identically whichever writer runs. A
// control-signal / unknown-role message yields nil.
func MessageToBlocks(msg core.Message) []ContentBlock {
	switch msg.Role {
	case core.RoleUser:
		if msg.ToolResult != nil {
			return toolResultToBlocks(msg.ToolResult)
		}
		return userContentToBlocks(msg.Content, msg.DisplayContent, msg.Images)
	case core.RoleAssistant:
		return assistantContentToBlocks(msg.Content, msg.Thinking, msg.ThinkingSignature, msg.ToolCalls)
	default:
		return nil
	}
}

func userContentToBlocks(content, displayContent string, images []core.Attachment) []ContentBlock {
	if len(images) > 0 && displayContent != "" && core.InlineImageTokenRe.MatchString(displayContent) {
		return interleavedUserContentToBlocks(content, displayContent, images)
	}

	var blocks []ContentBlock
	for _, img := range images {
		blocks = append(blocks, ContentBlock{
			Type:        "image",
			ImageSource: &ImageSource{Type: "base64", MediaType: img.MediaType, Data: img.Data},
		})
	}
	blocks = append(blocks, splitTextBySource(content)...)
	return blocks
}

func interleavedUserContentToBlocks(content, displayContent string, images []core.Attachment) []ContentBlock {
	var blocks []ContentBlock
	last := 0

	idToIdx := core.BuildImageIDMap(displayContent, len(images))

	matches := core.InlineImageTokenRe.FindAllStringSubmatchIndex(displayContent, -1)
	for _, match := range matches {
		start, end := match[0], match[1]
		idStart, idEnd := match[2], match[3]

		if textPart := displayContent[last:start]; textPart != "" {
			blocks = append(blocks, splitTextBySource(textPart)...)
		}

		id, err := strconv.Atoi(displayContent[idStart:idEnd])
		if err == nil {
			if idx, ok := idToIdx[id]; ok && idx < len(images) {
				img := images[idx]
				blocks = append(blocks, ContentBlock{
					Type:        "image",
					ImageSource: &ImageSource{Type: "base64", MediaType: img.MediaType, Data: img.Data},
				})
			}
		}

		last = end
	}

	if tail := displayContent[last:]; tail != "" {
		blocks = append(blocks, splitTextBySource(tail)...)
	}

	if len(blocks) == 0 && content != "" {
		blocks = append(blocks, splitTextBySource(content)...)
	}

	return blocks
}

func assistantContentToBlocks(content, thinking, thinkingSignature string, toolCalls []core.ToolCall) []ContentBlock {
	var blocks []ContentBlock
	if thinking != "" {
		blocks = append(blocks, ContentBlock{Type: "thinking", Thinking: thinking, Signature: thinkingSignature})
	}
	if content != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: content})
	}
	for _, tc := range toolCalls {
		block := ContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name}
		if tc.Input != "" {
			block.Input = json.RawMessage(tc.Input)
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func toolResultToBlocks(tr *core.ToolResult) []ContentBlock {
	block := ContentBlock{Type: "tool_result", ToolUseID: tr.ToolCallID, IsError: tr.IsError}
	switch details := tr.Details.(type) {
	case toolresult.FileChangeDetails:
		block.EditDetails, _ = json.Marshal(details)
	case toolresult.BashDetails:
		block.BashDetails, _ = json.Marshal(details)
	}
	if tr.Content != "" {
		block.Content = []ContentBlock{{Type: "text", Text: tr.Content}}
	}
	return []ContentBlock{block}
}

func ExtractLastUserText(msgs []core.Message) string {
	for _, msg := range slices.Backward(msgs) {
		if text, ok := extractUserText(msg); ok {
			return text
		}
	}
	return ""
}

func extractUserContent(blocks []ContentBlock, msg *core.Message) {
	imageCount := 0
	var display strings.Builder
	var content strings.Builder

	for _, block := range blocks {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
			// Harness-injected spans (system-reminders, and the inlined
			// command/skill body) are context the model sees but the user must
			// not: keep them in Content, exclude them from the displayed text
			// (otherwise a resumed session re-renders the whole block to the user).
			if !isHiddenSource(block.Source) {
				display.WriteString(block.Text)
			}
		case "image":
			if block.ImageSource != nil {
				msg.Images = append(msg.Images, core.Attachment{Image: ai.Image{MediaType: block.ImageSource.MediaType, Data: block.ImageSource.Data}})
				imageCount++
				display.WriteString(fmt.Sprintf("[Image #%d]", imageCount))
			}
		case "tool_result":
			tr := &core.ToolResult{ToolCallID: block.ToolUseID, IsError: block.IsError}
			if len(block.EditDetails) > 0 {
				var details toolresult.FileChangeDetails
				if json.Unmarshal(block.EditDetails, &details) == nil {
					tr.Details = details
				}
			} else if len(block.BashDetails) > 0 {
				var details toolresult.BashDetails
				if json.Unmarshal(block.BashDetails, &details) == nil {
					tr.Details = details
				}
			}
			for _, sub := range block.Content {
				if sub.Type == "text" {
					tr.Content = sub.Text
				}
			}
			msg.ToolResult = tr
		}
	}

	if msg.ToolResult == nil {
		msg.Content = content.String()
		// Trim the trailing blank line left where a reminder was appended
		// (AttachToContent joins with "\n\n"), so the displayed text matches
		// what the user originally typed.
		msg.DisplayContent = strings.TrimRight(display.String(), "\n")
	}
}

func extractAssistantContent(blocks []ContentBlock, msg *core.Message) {
	var content strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			msg.Thinking = block.Thinking
			msg.ThinkingSignature = block.Signature
		case "tool_use":
			tc := core.ToolCall{ID: block.ID, Name: block.Name}
			if block.Input != nil {
				tc.Input = string(block.Input)
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
	}
	msg.Content = content.String()
}

func extractUserText(msg core.Message) (string, bool) {
	if msg.Role != core.RoleUser || msg.ToolResult != nil {
		return "", false
	}
	for _, block := range MessageToBlocks(msg) {
		if block.Type == "text" && block.Text != "" {
			return block.Text, true
		}
	}
	return "", false
}
