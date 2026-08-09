package llm

import (
	"crypto/rand"
	"fmt"
)

// NewRequestID returns a random UUIDv4 for the per-request and per-session
// identifiers provider backends expect in headers (OpenAI's `session_id`,
// Copilot's `X-Request-Id`).
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
