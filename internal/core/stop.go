package core

import "github.com/genai-io/sdk-go/pkg/ai"

// StopReason says why a turn ended. It is San's vocabulary, not a provider's:
// the reasons a single inference can give are the SDK's business and are
// mapped on the way out (see stopReasonOf), so what reaches the interface
// describes the turn a person watched.
type StopReason string

const (
	StopEndTurn                    StopReason = "end_turn"
	StopMaxSteps                   StopReason = "max_steps"
	StopCancelled                  StopReason = "cancelled"
	StopHook                       StopReason = "stop_hook"
	StopMaxOutputRecoveryExhausted StopReason = "max_output_recovery_exhausted"
	// StopError marks a turn that died on an inference failure — the retry
	// budget ran out, or the error was not retryable. The turn still produced
	// messages and token usage, so it reports an outcome like any other stop
	// reason rather than vanishing. StopDetail carries the underlying error.
	StopError StopReason = "error"
)

// Usage is the SDK's: what one call spent, with the two cache halves named for
// what happened to the prefix rather than for the wire field it arrived in.
type Usage = ai.Usage
