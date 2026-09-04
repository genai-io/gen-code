package core

import (
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// StopReason is the SDK's — the same values the loop reports, with San's names
// on the three it says differently. Mapping them onto an enum of San's own
// meant a default branch, and a default branch gets a new reason wrong: a
// refusal landed on StopError, which Run reads as the agent dying.
type StopReason = sdkagent.StopReason

const (
	StopEndTurn StopReason = sdkagent.StopEndTurn
	// StopTruncated: cut off by the output cap, after WithContinuation already
	// asked the model to carry on as often as it was allowed to.
	StopTruncated StopReason = sdkagent.StopMaxTokens
	// StopRefused: the model declined, or a content filter fired.
	StopRefused  StopReason = sdkagent.StopRefusal
	StopSequence StopReason = sdkagent.StopSequence
	StopMaxSteps StopReason = sdkagent.StopMaxSteps
	StopCanceled StopReason = sdkagent.StopCanceled
	// StopHook: a tool voted to end the turn, which in San is only ever a hook.
	StopHook StopReason = sdkagent.StopTerminated
	// StopError: the inference failed and the retry budget is gone. The turn
	// still produced messages and usage, so it reports an outcome rather than
	// vanishing; StopDetail carries the error.
	StopError StopReason = sdkagent.StopError
)

// Usage is the SDK's: what one call spent, with the cache halves named for what
// happened to the prefix rather than for the wire field it arrived in.
type Usage = ai.Usage
