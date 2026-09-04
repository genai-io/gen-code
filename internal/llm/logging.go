package llm

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/log"
)

// The methods below satisfy the log package's requestLoggable and
// requestDevData interfaces through implicit duck typing.

// Compile-time assertions: ensure CompletionOptions satisfies the
// duck-typed logging interfaces. If a method is renamed or removed,
// these lines break at compile time rather than silently losing logging.
var _ interface{ LogMaxTokens() int } = CompletionOptions{}
var _ interface{ LogTemperature() float64 } = CompletionOptions{}
var _ interface{ LogSystemPrompt() string } = CompletionOptions{}
var _ interface{ LogToolNames() []string } = CompletionOptions{}
var _ interface{ LogMessageCount() int } = CompletionOptions{}
var _ interface {
	LogFormatMessages(sb *strings.Builder)
} = CompletionOptions{}
var _ interface{ LogRawTools() any } = CompletionOptions{}
var _ interface{ LogRawMessages() any } = CompletionOptions{}

// LogMaxTokens returns the max tokens setting for log output.
func (o CompletionOptions) LogMaxTokens() int { return o.MaxTokens }

// LogTemperature returns the temperature setting for log output.
func (o CompletionOptions) LogTemperature() float64 { return o.Temperature }

// LogSystemPrompt returns the system prompt for log output.
func (o CompletionOptions) LogSystemPrompt() string { return o.SystemPrompt }

// LogToolNames returns the tool names for log output.
func (o CompletionOptions) LogToolNames() []string {
	names := make([]string, len(o.Tools))
	for i, t := range o.Tools {
		names[i] = t.Name
	}
	return names
}

// LogMessageCount returns the number of messages for log output.
func (o CompletionOptions) LogMessageCount() int { return len(o.Messages) }

// LogFormatMessages writes a formatted summary of each message into sb.
func (o CompletionOptions) LogFormatMessages(sb *strings.Builder) {
	for i, msg := range o.Messages {
		switch msg.Role {
		case "user":
			if text := msg.Text(); text != "" {
				fmt.Fprintf(sb, "      [%d] User: %s\n", i, log.EscapeForLog(text))
			}
			for _, tr := range msg.ToolResults() {
				label := "ToolResult"
				if tr.IsError {
					label = "ToolResult ERROR"
				}
				fmt.Fprintf(sb, "      [%d] %s[%s]: %s\n", i, label, tr.ToolCallID, log.EscapeForLog(tr.Content.Text()))
			}
		case "assistant":
			if text := msg.Text(); text != "" {
				fmt.Fprintf(sb, "      [%d] Assistant: %s\n", i, log.EscapeForLog(text))
			}
			for _, tc := range msg.ToolCalls() {
				fmt.Fprintf(sb, "      [%d] ToolCall: %s(%s)\n", i, tc.Name, log.EscapeForLog(tc.Input))
			}
		}
	}
}

// LogRawTools returns the tools slice as any for JSON dev output.
func (o CompletionOptions) LogRawTools() any { return o.Tools }

// LogRawMessages returns the messages slice as any for JSON dev output.
func (o CompletionOptions) LogRawMessages() any { return o.Messages }
