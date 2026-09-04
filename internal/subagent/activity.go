package subagent

import (
	"context"

	"github.com/genai-io/san/internal/core"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
)

// activity reports each call as it is about to run, and refuses nothing.
//
// A gate rather than a wrapper because that is what the loop asks once per
// call: a decorator had to be applied on every path that hands a tool out, and
// one that was missed ran the tool unwatched.
func activity(onExec func(name string, params map[string]any)) core.Gate {
	if onExec == nil {
		return nil
	}
	return func(_ context.Context, c sdkagent.PreToolContext) (sdkagent.Decision, error) {
		params, _ := core.ParseToolInput(c.Call.Input)
		onExec(c.Call.Name, params)
		return sdkagent.Decision{}, nil
	}
}
