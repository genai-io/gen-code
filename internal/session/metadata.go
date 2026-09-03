package session

import (
	"time"

	"github.com/genai-io/san/internal/core"
)

func NormalizeMetadata(meta *SessionMetadata, msgs []core.ChatMessage, defaultCwd string, now time.Time) {
	if meta.ID == "" {
		meta.ID = generateSessionID()
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now
	meta.MessageCount = len(msgs)
	if meta.Cwd == "" {
		meta.Cwd = defaultCwd
	}
	if meta.LastPrompt == "" {
		meta.LastPrompt = ExtractLastUserText(msgs)
	}
	if meta.Title == "" {
		meta.Title = GenerateTitle(msgs)
	}
}
