// Main-loop notifications: the small events that wake the TUI's Update loop —
// a finished background subagent, an interim message from a running one, a
// self-learn review tick. They arrive on m.mainNotices, and this is where their
// delivery timing is defined; the mechanics live in model_turn_queue.go.
//
// A notice shows a line in the conversation, so it can only be delivered where
// appending is safe — anywhere except the last slot while the stream is still
// writing into it (conv.LastMessageIsStreaming). onMainNotice takes the earliest of
// three:
//
//   - no turn running: injected now, starting a fresh turn;
//   - turn running, tail free (a tool is executing): injected into that turn,
//     whose agent reads it at its next step;
//   - stream owns the tail: parked in pendingNotices, released at the next
//     completed tool batch (OnStepEnd) or, failing that, at OnTurnEnd.
//
// m.mainNotices is a TUI staging channel, not the main agent's inbox: unlike a
// subagent, whose broker delivery goes straight into its core.Agent inbox, the
// main conversation is UI-attached and has this display half to place. That is
// why the channel carries a mainNotice rather than a raw message.
package app

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/broker"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
)

// mainNotice is one message routed to the main conversation on Source 2 — a
// subagent completion or an interim message. It carries data only, no control
// flags: Display is the one-line notice shown to the user (may be empty);
// Content, if non-empty, is submitted to the main agent as a fresh turn (empty
// Content = display-only). Pure UI signals (e.g. the self-learn spinner start)
// do not ride here — they have their own channels.
type mainNotice struct {
	Display string
	Content string
	// FromAgent marks a notice relayed in from a background agent (a subagent
	// completion or interim report), so the UI renders its Display line as an
	// inbound agent message rather than a plain system notice.
	FromAgent bool
}

// fromBrokerMessage converts a message the broker routed to "main" (a subagent
// completion or an interim message) into a main-loop notice.
func fromBrokerMessage(m broker.Message) mainNotice {
	return mainNotice{Display: m.Subject, Content: m.Content, FromAgent: true}
}

// mergeNotices collapses notices drained together into one displayed line + one
// injected content. Display-only notices contribute their line but no content.
func mergeNotices(notices []mainNotice) mainNotice {
	if len(notices) == 1 {
		return notices[0]
	}
	lines := make([]string, 0, len(notices))
	contents := make([]string, 0, len(notices))
	for _, n := range notices {
		if n.Display != "" {
			lines = append(lines, n.Display)
		}
		if c := strings.TrimSpace(n.Content); c != "" {
			contents = append(contents, c)
		}
	}
	merged := mainNotice{Display: strings.Join(lines, "; ")}
	for _, n := range notices {
		if n.FromAgent {
			merged.FromAgent = true
			break
		}
	}
	if len(contents) == 1 {
		merged.Content = contents[0]
	} else if len(contents) > 1 {
		merged.Content = "<agent-messages>\n" + strings.Join(contents, "\n") + "\n</agent-messages>"
	}
	return merged
}

// maxTaskOutputInNotification is the largest task output, in bytes, inlined
// whole into the completion notification. At or below it, the full result rides
// in the notification so the reader needs no follow-up read; above it, the body
// is omitted and only the output-file pointer is given, so a large report is
// fetched in one deliberate read rather than a truncated preview plus a second
// read for the rest. Byte-sized, so a CJK report (3 bytes/char) inlines up to
// roughly a third as many characters — ~6.6K.
const maxTaskOutputInNotification = 20000

// taskCompletionMessage builds the broker message a finished task sends to the
// "main" address, or (_, false) if the task is not in a terminal state.
func taskCompletionMessage(info task.TaskInfo) (broker.Message, bool) {
	status := formatStatus(info.Status)
	if status == "" {
		return broker.Message{}, false
	}

	description := taskSubject(info)
	if description == "" {
		description = "Background task"
	}

	result := strings.TrimSpace(info.Output)

	var b strings.Builder
	fmt.Fprintf(&b, "<task-notification task-id=%q status=%q", info.ID, status)
	if info.AgentSessionID != "" {
		fmt.Fprintf(&b, " agent-id=%q", info.AgentSessionID)
	}
	if description != "" {
		fmt.Fprintf(&b, " description=%q", description)
	}
	if info.OutputFile != "" {
		fmt.Fprintf(&b, " output-file=%q", info.OutputFile)
	}
	b.WriteString(">\n")
	switch {
	case info.Error != "":
		b.WriteString(tool.EscapeXMLText(info.Error))
	case len(result) > maxTaskOutputInNotification && info.OutputFile != "":
		// Too large to inline without bloating the main context. Point at the
		// full result instead of dumping a partial preview.
		fmt.Fprintf(&b, "Output is %d bytes — too large to inline. Read the full result from the output file above.", len(result))
	case result != "":
		b.WriteString(tool.EscapeXMLText(result))
	}
	b.WriteString("\n</task-notification>")

	return broker.Message{
		From:    info.ID,
		To:      broker.Main,
		Subject: fmt.Sprintf("%s %s", description, status),
		Content: b.String(),
	}, true
}

// taskSubject generates a human-readable subject line from task info.
func taskSubject(info task.TaskInfo) string {
	switch info.Type {
	case task.TaskTypeAgent:
		if s := joinNameDesc(info.AgentName, info.Description); s != "" {
			return s
		}
	case task.TaskTypeBash:
		if info.Command != "" {
			return info.Command
		}
	}
	return info.Description
}

// (XML-body escaping now lives in tool.EscapeXMLText — newline-preserving and
// shared with the SendMessage envelope builder.)

func formatStatus(status task.TaskStatus) string {
	switch status {
	case task.StatusCompleted:
		return "completed"
	case task.StatusFailed:
		return "failed"
	case task.StatusKilled:
		return "killed"
	case task.StatusStopped:
		return "stopped"
	default:
		return ""
	}
}

func joinNameDesc(name, desc string) string {
	name = strings.TrimSpace(name)
	desc = strings.TrimSpace(desc)
	switch {
	case name != "" && desc != "" && !strings.EqualFold(name, desc):
		return name + ": " + desc
	case desc != "":
		return desc
	case name != "":
		return name
	default:
		return ""
	}
}
