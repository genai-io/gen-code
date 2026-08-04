// conv.Runtime implementation: callbacks the agent's outbox event pump calls
// on the main bubbletea goroutine — turn start, token accounting, tool
// results, turn end, and stop. The actual side effects (committing
// scrollback, draining queues, firing hooks) live in adjacent model_*
// files; this file is the thin wire between agent events and those
// effects.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/tool"
)

func (m *model) OnInference(resp *core.InferResponse) {
	if resp == nil {
		return
	}
	// PostInfer starts a new step: re-arm the one-per-step queue release that
	// this step's PostTool events will use (see OnStepEnd).
	m.drainedThisStep = false

	if m.userInput.Provider.StatusMessage == "compacted" {
		m.userInput.Provider.StatusMessage = ""
	}

	// Bottom-right context usage reflects the latest infer call, not a
	// lifetime sum across the whole session. Use the full prompt size
	// (fresh + cache read + cache creation) so the ctx readout matches real
	// context-window occupancy rather than just the uncached delta.
	m.env.InputTokens = resp.TotalInputTokens()
	m.env.OutputTokens = resp.OutputTokens

	if m.env.CurrentModel != nil {
		usage := llm.Usage{
			InputTokens:              resp.InputTokens,
			OutputTokens:             resp.OutputTokens,
			CacheCreationInputTokens: resp.CacheCreationInputTokens,
			CacheReadInputTokens:     resp.CacheReadInputTokens,
		}
		if cost, ok := llm.EstimateCost(m.env.CurrentModel.Provider, m.env.CurrentModel.ModelID, usage); ok {
			m.env.ConversationCost = m.env.ConversationCost.Add(cost)
		}
	}
}

// HasRunningTasks gates the progress-hub tick's spinner batching. Like
// needsSpinner it reads live runtime state, not persisted tracker status.
func (m *model) HasRunningTasks() bool { return m.hasRunningBackgroundTask() }

// OnStepEnd releases pending work into a still-running turn at a step
// boundary (a PostTool, where the turn continues): notices the stream held
// back, plus one queued user message — the cap is what keeps the queue
// editable until release. Counterpart to drainTurnQueues, which runs once the
// turn has ended.
func (m *model) OnStepEnd() tea.Cmd {
	if !m.services.Agent.Active() {
		return nil
	}
	notices := m.releaseParkedNotices()
	if m.drainedThisStep {
		return notices
	}
	queued, released := m.releaseQueuedMessage()
	m.drainedThisStep = released
	return tea.Batch(notices, queued)
}

func (m *model) OnToolResult(tr core.ToolResult) *core.ToolResult {
	// Track skill usage for the self-learning trigger: any Skill tool call this
	// turn (even a failing one — a broken skill is a prime refine/retire
	// candidate) flips the turn onto the update/delete review path.
	if tr.ToolName == tool.ToolSkill {
		m.skillUsedThisTurn = true
	}
	// The Evolve tool is the model-decided self-learning trigger: its call
	// this turn queues a review at turn end.
	if tr.ToolName == tool.ToolEvolve && !tr.IsError {
		m.evolveRequestedThisTurn = true
	}

	sideEffect := m.services.Tool.PopSideEffect(tr.ToolCallID)
	details := m.services.Tool.PopResultDetails(tr.ToolCallID)
	if sideEffect != nil {
		m.applyToolSideEffects(tr.ToolName, sideEffect)
	}
	m.firePostToolHook(tr, sideEffect)

	result := &core.ToolResult{
		ToolCallID: tr.ToolCallID,
		ToolName:   tr.ToolName,
		Content:    tr.Content,
		IsError:    tr.IsError,
		Details:    details,
	}
	m.persistOverflow(result)
	return result
}

func (m *model) OnTurnEnd(result core.Result) tea.Cmd {
	// Only a list the model closed out is safe to discard. An item left open —
	// pending, or in_progress with nothing executing it — is unfinished work, so
	// the list survives the turn and stays on screen.
	//
	// This is the sole automatic reset, so one item the model never closes keeps
	// the list alive and growing for the rest of the session. That is why the
	// tracker windows on its newest rows (see maxVisibleTasks) rather than
	// assuming it renders a single turn's worth.
	if m.services.Tracker.AllMarkedCompleted() {
		m.services.Tracker.Reset()
	}
	// The turn reached its end, so whatever failure the copilot was recovering
	// from is behind us — give a later failure its full recovery budget again.
	m.autopilotRecoveries = 0
	m.services.Agent.SetPluginRoot("")
	// Clean up any temp image files created for text-only models this turn.
	for _, p := range m.tempImageFiles {
		if err := os.Remove(p); err != nil {
			log.Logger().Warn("remove temp image file", zap.String("path", p), zap.Error(err))
		}
	}
	m.tempImageFiles = nil
	// Forward to L1 self-learning with whether this turn used a skill and
	// whether the model called Evolve (the model-decided trigger). No-op when
	// disabled; the reviewer gates on StopEndTurn internally so cancelled /
	// interrupted turns are skipped. Clear both flags for the next turn either
	// way.
	if s := m.services.SelfLearn.session; s != nil {
		s.reviewer.Observe(result, m.skillUsedThisTurn, m.evolveRequestedThisTurn)
	}
	m.skillUsedThisTurn = false
	m.evolveRequestedThisTurn = false
	log.QueueLog("OnTurnEnd: starting queueLen=%d", m.userInput.Queue.Len())
	commitCmds := m.CommitMessages()

	// User-initiated cancel surfaces here as a Result with StopCancelled now
	// that ThinkAct returns a phantom Result on context.Canceled. It precedes
	// the queue drain because draining submits a fresh turn — the input queue,
	// a cron prompt, an async hook or a parked agent notice — which is how Esc
	// stopped one inference and left the loop running. Each queue has its own
	// later drain, so holding them here loses nothing.
	//
	// Stop / idle-notification hooks would otherwise fire on every Esc —
	// confusing for the user and for hooks that template result.Content (which
	// is empty for a cancelled turn). We still persist so the [Interrupted]
	// marker and cancelled tool_result rows survive a crash/quit, and
	// re-arm prompt suggestions for the now-idle textarea.
	if result.StopReason == core.StopCancelled {
		log.QueueLog("OnTurnEnd: turn was cancelled, holding queues and skipping idle hooks")
		if cmd := m.persistAfterTurn(); cmd != nil {
			commitCmds = append(commitCmds, cmd)
		}
		if cmd := m.startPromptSuggestion(); cmd != nil {
			commitCmds = append(commitCmds, cmd)
		}
		commitCmds = append(commitCmds, m.ContinueOutbox())
		return tea.Batch(commitCmds...)
	}

	if cmd, found := m.drainTurnQueues(); found {
		log.QueueLog("OnTurnEnd: drained queued message, skipping hooks")
		if cmd != nil {
			commitCmds = append(commitCmds, cmd)
		}
		commitCmds = append(commitCmds, m.ContinueOutbox())
		return tea.Batch(commitCmds...)
	}

	// Autopilot TurnEnd steer (#5): let the copilot decide whether to keep the
	// session going toward the mission. When it wants to, the idle hooks are
	// deferred until handleAutopilotDecision (which fires them if it stops).
	if cmd := m.autopilotContinueCmd(result); cmd != nil {
		log.QueueLog("OnTurnEnd: autopilot deciding whether to continue")
		commitCmds = append(commitCmds, cmd, m.ContinueOutbox())
		return tea.Batch(commitCmds...)
	}

	log.QueueLog("OnTurnEnd: firing idle hooks async")
	commitCmds = append(commitCmds, m.fireIdleHooksCmd(result), m.ContinueOutbox())
	return tea.Batch(commitCmds...)
}

func (m *model) OnAgentStop(err error) tea.Cmd {
	// /clear and manual stop cancel the active agent context; that is expected
	// shutdown, not an agent failure the user needs to see.
	failed := err != nil && !errors.Is(err, context.Canceled)
	if failed {
		m.conv.AddNotice(fmt.Sprintf("Agent error: %v", err))
		m.fireStopFailureHook(core.LastAssistantChatContent(m.conv.Messages), err)
	}
	m.conv.AgentToUI.DrainPendingQuestions()
	m.conv.Modal.Question.Hide()
	commitCmds := m.CommitMessages()
	m.StopAgentSession()
	// An unattended run must survive a failed turn: schedule the copilot to look
	// at the failure and decide whether the mission can resume. No-op outside
	// AutoPilot, and it still hands back when the error needs a human.
	if failed {
		if cmd := m.autopilotRecoverCmd(err); cmd != nil {
			commitCmds = append(commitCmds, cmd)
		}
	}
	return tea.Batch(commitCmds...)
}
