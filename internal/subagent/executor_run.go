package subagent

import (
	"context"
	"fmt"
	"time"

	"github.com/genai-io/san/internal/broker"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/ai"
	"go.uber.org/zap"
)

type preparedRun struct {
	req       tool.AgentExecRequest
	cfg       *runConfig
	cwd       string
	startedAt time.Time
	hookID    string
	// activity is the tool-call trail included in the parent-visible result.
	// Status lines (model, mode, token usage, spinner text) go only to
	// OnActivity for the UI — they would be noise in the parent's context.
	activity     []string
	inputTokens  int
	outputTokens int
}

// recordActivity adds a tool-call line to the parent-visible result trail and
// also streams it to the UI.
func (r *preparedRun) recordActivity(msg string) {
	r.activity = append(r.activity, msg)
	r.streamActivity(msg)
}

// streamActivity streams a line to the UI's OnActivity callback only — model,
// mode, and usage status that would be noise in the parent's result.
func (r *preparedRun) streamActivity(msg string) {
	if r.req.OnActivity != nil {
		r.req.OnActivity(msg)
	}
}

func (r *preparedRun) recordUsage(resp *ai.Response) {
	if r.req.OnActivity == nil || resp == nil {
		return
	}
	r.inputTokens += resp.Usage.Input
	r.outputTokens += resp.Usage.Output
	if r.inputTokens > 0 || r.outputTokens > 0 {
		r.streamActivity(formatUsageActivity(r.inputTokens, r.outputTokens))
	}
}

func (e *Executor) prepareRun(ctx context.Context, req tool.AgentExecRequest) (*preparedRun, error) {
	if err := e.validateRequest(req); err != nil {
		return nil, err
	}

	cfg, err := e.prepareRunConfig(ctx, req)
	if err != nil {
		return nil, err
	}

	return &preparedRun{
		req:       req,
		cfg:       cfg,
		cwd:       e.cwd,
		startedAt: time.Now(),
		hookID:    "a" + generateShortID(),
		activity:  make([]string, 0, 16),
	}, nil
}

func (e *Executor) attachRunContext(ctx context.Context, displayName string) context.Context {
	tracker := log.NewAgentTurnTracker(displayName, nil)
	return log.WithAgentTracker(ctx, tracker)
}

func (e *Executor) logRunStart(run *preparedRun) {
	log.Logger().Info("Starting agent execution",
		zap.String("agent", run.cfg.displayName),
		zap.String("description", run.req.Description),
		zap.Int("maxSteps", run.cfg.maxSteps),
	)
}

func (e *Executor) executePreparedRun(ctx context.Context, run *preparedRun) (*core.Result, error) {
	var onToolExec func(string, map[string]any)
	if run.req.OnActivity != nil {
		run.streamActivity(fmt.Sprintf("Model: %s", run.cfg.modelID))
		run.streamActivity(fmt.Sprintf("Mode: %s · max %d steps", displayPermissionMode(run.cfg.permMode), run.cfg.maxSteps))
		onToolExec = func(name string, params map[string]any) {
			run.recordActivity(formatToolActivity(name, params))
		}
	}
	ag, cleanupAgent, err := e.buildAgent(ctx, run, onToolExec, func(ev core.Event) {
		if resp, ok := ev.Response(); ok && ev.Type == core.PostInfer {
			run.recordUsage(resp)
		}
	})
	if err != nil {
		return nil, err
	}
	defer cleanupAgent()

	if err := e.loadConversation(ag, ctx, run.cfg, run.req); err != nil {
		return nil, err
	}
	if run.req.OnActivity != nil {
		run.streamActivity("Thinking...")
	}

	// Stamp the running worker's own broker address onto the context so its
	// SendMessage calls are attributed to it, not defaulted to "main". A
	// background run uses its task id (and registers it below so main can
	// message it mid-flight); a foreground run has no task id, so it borrows
	// its hook id purely as a sender identity — nothing routes back to a
	// foreground worker, which blocks its spawning turn.
	agentAddr := run.req.TaskID
	if agentAddr == "" {
		agentAddr = run.hookID
	}
	ctx = tool.WithAgentID(ctx, agentAddr)

	// A background subagent registers its task id with the broker for the
	// length of the run. A routed message lands in the subagent's inbox and is
	// read at its next step boundary; a full inbox reports the drop back to the
	// sender rather than silently swallowing it.
	if run.req.TaskID != "" {
		broker.Register(run.req.TaskID, func(m broker.Message) bool {
			select {
			case ag.Inbox() <- core.Inbound{Msg: core.UserMessage(m.Content, nil)}:
				return true
			default:
				log.Logger().Warn("subagent inbox full; dropped message",
					zap.String("from", m.From))
				return false
			}
		})
		defer broker.Unregister(run.req.TaskID)
	}

	result, err := ag.ThinkAct(ctx)
	if err != nil {
		if result != nil {
			return result, err
		}
		return nil, err
	}

	return result, nil
}

func formatUsageActivity(inputTokens, outputTokens int) string {
	return fmt.Sprintf("Usage: input=%d output=%d", inputTokens, outputTokens)
}

func (e *Executor) logRunCompletion(run *preparedRun, result *core.Result, success bool) {
	logFields := []zap.Field{
		zap.String("agent", run.cfg.displayName),
		zap.String("stopReason", string(result.StopReason)),
		zap.Int("steps", result.Steps),
		zap.Int("inputTokens", result.Usage.Input),
		zap.Int("outputTokens", result.Usage.Output),
	}
	if success {
		log.Logger().Info("Agent completed", logFields...)
		return
	}
	log.Logger().Warn("Agent completed", logFields...)
}

func (e *Executor) buildAgentResult(run *preparedRun, result *core.Result) *AgentResult {
	success, errMsg := interpretStopReason(result, run.cfg.maxSteps)
	e.logRunCompletion(run, result, success)
	return e.finalizeResult(run, result, success, errMsg)
}

// buildUnfinishedAgentResult preserves the partial work of a run that ended
// without completing: the conversation is persisted, the stop hook fires, and
// the partial content travels back to the caller alongside the error. Covers
// both ways a turn can end early — the user cancelled it, or inference failed
// past the retry budget — since either way the steps already taken are worth
// keeping. A nil Result means there is nothing to preserve.
func (e *Executor) buildUnfinishedAgentResult(run *preparedRun, result *core.Result) *AgentResult {
	if result == nil {
		return nil
	}
	if result.StopReason != core.StopCanceled && result.StopReason != core.StopError {
		return nil
	}
	_, errMsg := interpretStopReason(result, run.cfg.maxSteps)
	return e.finalizeResult(run, result, false, errMsg)
}

// finalizeResult persists the session, fires the stop hook, and projects the
// run into an AgentResult. Both the success and cancelled paths share it —
// they differ only in Success/Error.
func (e *Executor) finalizeResult(run *preparedRun, result *core.Result, success bool, errMsg string) *AgentResult {
	agentSessionID, agentTranscriptPath := e.persistSubagentSession(
		run.cfg.displayName,
		run.cfg.modelID,
		run.req.Description,
		result.Messages,
	)
	e.fireSubagentStop(run.req, run.hookID, agentTranscriptPath, result.Content)

	return &AgentResult{
		AgentID:        agentSessionID,
		AgentName:      run.cfg.config.Name,
		TranscriptPath: agentTranscriptPath,
		Model:          run.cfg.modelID,
		Success:        success,
		Content:        result.Content,
		Messages:       result.Messages,
		StepCount:      result.Steps,
		ToolUses:       result.ToolUses,
		TokenUsage:     result.Usage,
		Duration:       time.Since(run.startedAt),
		Activity:       append([]string(nil), run.activity...),
		Error:          errMsg,
	}
}
