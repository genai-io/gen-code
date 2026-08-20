package log

import (
	"context"
	"fmt"
	"strings"
)

// LogResponseCtx logs an LLM response with context (supports agent tracking).
func LogResponseCtx(ctx context.Context, providerName string, resp any) {
	tracker := getAgentTracker(ctx)
	var turn int
	var prefix string

	if tracker != nil {
		turn = tracker.CurrentTurn()
		prefix = tracker.GetTurnPrefix(turn)
	} else {
		turn = currentTurn()
		prefix = getTurnPrefix(turn)
	}

	writeDevResponse(tracker, providerName, resp, turn)
	logResponse(prefix, providerName, resp)
}

// responseLoggable is satisfied by any LLM response type, avoiding a direct
// dependency on the core package so log stays at the foundation layer.
type responseLoggable interface {
	LogStopReason() string
	LogContent() string
	LogInputTokens() int
	LogOutputTokens() int
	LogThinking() string
	LogRawToolCalls() any
	LogRawUsage() any
	LogToolCallSummary(escaper func(string) string) string
}

// logResponse formats and logs an LLM response.
func logResponse(prefix, providerName string, resp any) {
	if !enabled {
		return
	}

	rl, ok := resp.(responseLoggable)
	if !ok {
		logger.Info(fmt.Sprintf("<<< [%s] %s (unstructured response)", prefix, providerName))
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<<< [%s] %s stop=%s | in=%d out=%d\n", prefix, providerName, rl.LogStopReason(), rl.LogInputTokens(), rl.LogOutputTokens())

	if content := rl.LogContent(); content != "" {
		sb.WriteString("    Content:\n")
		for line := range strings.SplitSeq(content, "\n") {
			fmt.Fprintf(&sb, "        %s\n", line)
		}
	}

	if summary := rl.LogToolCallSummary(EscapeForLog); summary != "" {
		sb.WriteString(summary)
	}

	logger.Info(sb.String())
}

// LogError logs an error in human-readable format
func LogError(context string, err error) {
	if !enabled {
		return
	}
	logger.Error(fmt.Sprintf("!!! ERROR [%s] %v", context, err))
}
