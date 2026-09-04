package session

import (
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session/transcript"
)

// messagesToNodes projects the wire messages onto transcript nodes for the
// append-only save path. Node content comes from the shared MessageToBlocks
// converter — the same one the live Recorder writes through — so both writers
// produce byte-identical content for a given message ID. Only user/assistant
// messages become nodes (control signals are not model-visible); each node's
// timestamp is derived from createdAt so a re-save is deterministic.
func messagesToNodes(msgs []core.ChatMessage, defaultCwd string, createdAt time.Time, gitBranch string) []transcript.Node {
	nodes := make([]transcript.Node, 0, len(msgs))
	var prevID string

	for _, msg := range msgs {
		role := transcriptRole(msg.Role)
		if role == "" {
			continue
		}
		id := msg.ID
		if id == "" {
			id = core.NewMessageID()
		}
		nodes = append(nodes, transcript.Node{
			ID:        id,
			ParentID:  prevID,
			Role:      role,
			Time:      createdAt.Add(time.Duration(len(nodes)+1) * time.Millisecond),
			Cwd:       defaultCwd,
			GitBranch: gitBranch,
			Content:   MessageToBlocks(msg),
		})
		prevID = id
	}

	return nodes
}

// messagesFromNodes rebuilds the wire messages from transcript nodes on load.
// tool_result blocks carry only a tool_use id, so a first pass indexes tool
// names from assistant tool_use blocks to backfill ToolResult.ToolName.
func messagesFromNodes(nodes []transcript.Node) []core.ChatMessage {
	toolNameByID := make(map[string]string)
	for _, node := range nodes {
		if node.Role == "assistant" {
			for _, block := range node.Content {
				if block.Type == "tool_use" {
					toolNameByID[block.ID] = block.Name
				}
			}
		}
	}

	msgs := make([]core.ChatMessage, 0, len(nodes))
	for _, node := range nodes {
		if node.Role == "assistant" {
			msg := core.ChatMessage{Role: core.ChatAssistant, ID: node.ID}
			extractAssistantContent(node.Content, &msg)
			msgs = append(msgs, msg)
			continue
		}
		msg := core.ChatMessage{Role: core.ChatUser, ID: node.ID}
		extractUserContent(node.Content, &msg)
		if msg.ToolResult != nil && msg.ToolResult.ToolName == "" {
			if name, ok := toolNameByID[msg.ToolResult.ToolCallID]; ok {
				msg.ToolResult.ToolName = name
			}
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

// transcriptRole maps a wire role onto the transcript's role string. Only
// user and assistant turns are persisted; anything else returns "" so the
// caller skips it.
func transcriptRole(role core.ChatRole) string {
	switch role {
	case core.ChatUser:
		return "user"
	case core.ChatAssistant:
		return "assistant"
	default:
		return ""
	}
}
