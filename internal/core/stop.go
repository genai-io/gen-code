package core

import (
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// StopReason says why a turn ended, and it is the SDK's — the same type and
// the same values the loop reports, not a copy of them.
//
// San used to keep its own enum and a stopReasonOf to map onto it. Nothing was
// gained: every value San had was one the SDK already reported, so the mapping
// was a transcription with a default branch, and a default branch is a thing
// that gets a new reason wrong. It did — a refusal landed on StopError, which
// Run reads as the agent dying, so a refused turn produced no turn boundary at
// all.
//
// The names below are San's words for the SDK's values, which is all a name
// should ever be. They cost nothing to be wrong about, because there is no
// third value they could take.
type StopReason = sdkagent.StopReason

const (
	StopEndTurn StopReason = sdkagent.StopEndTurn
	// StopTruncated is an answer the output cap cut off after WithContinuation
	// had already asked the model to carry on as often as it was allowed to.
	// The answer stops mid-sentence, which is what a person is looking at.
	StopTruncated StopReason = sdkagent.StopMaxTokens
	// StopRefused is the model declining, or a provider's content filter. The
	// turn ended; there is simply nothing to carry on from.
	StopRefused  StopReason = sdkagent.StopRefusal
	StopSequence StopReason = sdkagent.StopSequence
	StopMaxSteps StopReason = sdkagent.StopMaxSteps
	StopCanceled StopReason = sdkagent.StopCanceled
	// StopHook is a tool voting to end the turn, which in San is only ever a
	// hook doing it.
	StopHook StopReason = sdkagent.StopTerminated
	// StopError marks a turn that died on an inference failure — the retry
	// budget ran out, or the error was not retryable. The turn still produced
	// messages and token usage, so it reports an outcome like any other stop
	// reason rather than vanishing. StopDetail carries the underlying error.
	StopError StopReason = sdkagent.StopError
)

// Usage is the SDK's: what one call spent, with the two cache halves named for
// what happened to the prefix rather than for the wire field it arrived in.
type Usage = ai.Usage
