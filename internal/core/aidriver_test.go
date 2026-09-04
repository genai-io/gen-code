package core

import (
	"iter"

	"github.com/genai-io/sdk-go/pkg/ai"
)

// Faking the model now means faking the protocol, which is the seam pkg/ai
// already has and the one five real drivers sit behind. A test says what the
// endpoint sends; nothing in between has to be stubbed.

// deltas renders one answer as the stream a driver would produce. The answer
// is already an ordered block sequence, so this walks it rather than
// reassembling one out of parallel fields.
func deltas(r ai.Response) []ai.Delta {
	var out []ai.Delta
	calls := 0
	for _, b := range r.Content {
		if b.Type == ai.BlockToolCall {
			calls++
			out = append(out, ai.Delta{Block: b})
			continue
		}
		out = append(out, ai.Delta{Block: b}, ai.Delta{EndBlock: true})
	}
	stop := r.StopReason
	switch {
	case calls > 0:
		stop = ai.StopToolUse
	case stop == "":
		stop = ai.StopEndTurn
	}
	usage := r.Usage
	return append(out, ai.Delta{StopReason: stop, Usage: &usage})
}

// yieldAll is the driver body a scripted double needs: send this, then stop.
func yieldAll(script []ai.Delta) iter.Seq2[ai.Delta, error] {
	return func(yield func(ai.Delta, error) bool) {
		for _, d := range script {
			if !yield(d, nil) {
				return
			}
		}
	}
}

// testClient wraps a driver as the per-turn client an agent is built on. The
// same client answers every turn: only a real endpoint varies its headers with
// what the turn sends.
func testClient(d ai.Driver) func([]Message) (*ai.Client, error) {
	client := ai.NewClientWithDriver(d, ai.Model{ID: "stub", API: "stub", ContextWindow: 200_000})
	return func([]Message) (*ai.Client, error) { return client, nil }
}
