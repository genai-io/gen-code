package session

import (
	"github.com/genai-io/san/internal/core"
)

// MessagesFromChat turns the conversation view-model into wire messages for
// persistence. Notices are display-only and dropped; the rest keep the stable
// IDs stamped at conv.Append time so the append-only save path can dedupe by
// them.
func MessagesFromChat(messages []core.ChatMessage) []core.Message {
	msgs := make([]core.Message, 0, len(messages))
	for _, msg := range messages {
		// A notice is San talking to the person, not a turn — ToMessage is
		// what knows that, so nothing out here has to remember it.
		if m, ok := msg.ToMessage(); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

// MessagesToChat turns loaded wire messages back into the conversation
// view-model.
func MessagesToChat(msgs []core.Message) []core.ChatMessage {
	messages := make([]core.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		messages = append(messages, m.ToChat())
	}
	return messages
}
