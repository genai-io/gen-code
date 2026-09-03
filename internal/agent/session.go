package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/genai-io/san/internal/core"
)

type Session struct {
	mu                 sync.RWMutex
	agent              core.Agent
	permGate           *PermissionGate
	cancel             context.CancelFunc
	pendingPermRequest *PermGateRequest
	pluginRoot         string // see SetPluginRoot
}

func (s *Session) Start(params BuildParams, messages []core.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.agent != nil {
		return fmt.Errorf("agent session already active")
	}

	ag, pg, err := buildAgent(params)
	if err != nil {
		return err
	}
	s.agent = ag
	s.permGate = pg

	if len(messages) > 0 {
		s.agent.SetMessages(messages)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	// Capture this build's agent. Stop clears s.agent immediately, and a rapid
	// stop/restart can otherwise make this goroutine dereference nil or run the
	// replacement agent under the previous build's cancelled context.
	go func() { _ = ag.Run(ctx) }()

	return nil
}

func (s *Session) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Session) stopLocked() {
	if s.agent == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	select {
	case s.agent.Inbox() <- core.Message{Signal: core.SigStop}:
	default:
	}
	s.agent = nil
	s.permGate = nil
	s.pendingPermRequest = nil
	s.pluginRoot = ""
}

func (s *Session) Active() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agent != nil
}

// Messages returns a snapshot of the running agent's conversation chain, or nil
// when no agent is active. This is the authoritative context the agent is
// actually sending to the model — used to carry history into a rebuilt agent so
// a mid-conversation rebuild never drops the conversation.
func (s *Session) Messages() []core.Message {
	s.mu.RLock()
	ag := s.agent
	s.mu.RUnlock()
	if ag == nil {
		return nil
	}
	return ag.Messages()
}

func (s *Session) Send(content string, images []core.Attachment) {
	s.mu.RLock()
	ag := s.agent
	s.mu.RUnlock()
	if ag == nil {
		return
	}
	ag.Inbox() <- core.Message{Role: core.RoleUser, Content: content, Images: images}
}

// Compact asks the running agent to compact in place using the precomputed
// summary, replacing its conversation chain without tearing the agent down (so
// the system prompt and tools are not rebuilt). The agent records the summary
// and a compaction boundary and emits CompactEvent. Returns false when there is
// no active agent to compact. Safe because the agent applies it at a phase
// boundary on its own goroutine.
func (s *Session) Compact(summary string) bool {
	s.mu.RLock()
	ag := s.agent
	s.mu.RUnlock()
	if ag == nil {
		return false
	}
	ag.Inbox() <- core.Message{Signal: core.SigCompact, Content: summary}
	return true
}

// interruptDrainTimeout caps how long InterruptTurn waits for the agent
// goroutine to actually unwind its in-flight ThinkAct. Keeping this
// tight avoids UI stalls; if the agent is still in a slow tool the
// caller proceeds anyway — provider-side convert layers strip any
// orphaned tool_use blocks before the next inference fires.
const interruptDrainTimeout = 250 * time.Millisecond

// InterruptTurn cancels the agent's in-flight turn without ending its
// Run loop and waits briefly for the turn to actually unwind. The next
// Send goes through the same inbox channel and resumes the session in
// place — no rebuild, no Stop/Start event pair.
//
// Also clears pendingPermRequest: a permission prompt that was open at
// the moment of interrupt is dropped along with the turn, so the
// dangling *PermGateRequest must not survive into the next turn (a
// later SetPendingPermission would then race a stale request against a
// fresh one). The clear runs AFTER the agent quiesces so an in-flight
// PermissionFunc can't repopulate pendingPermRequest via PollPermGate
// → SetPendingPermission between the clear and the cancel.
func (s *Session) InterruptTurn() {
	s.mu.RLock()
	ag := s.agent
	s.mu.RUnlock()
	if ag == nil {
		return
	}
	done := ag.InterruptCurrentTurn()
	timer := time.NewTimer(interruptDrainTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	s.mu.Lock()
	s.pendingPermRequest = nil
	s.mu.Unlock()
}

func (s *Session) Outbox() <-chan core.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agent == nil {
		return nil
	}
	return s.agent.Outbox()
}

func (s *Session) PermissionGate() *PermissionGate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.permGate
}

func (s *Session) PendingPermission() *PermGateRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingPermRequest
}

func (s *Session) SetPendingPermission(req *PermGateRequest) {
	s.mu.Lock()
	s.pendingPermRequest = req
	s.mu.Unlock()
}

func (s *Session) System() core.System {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agent == nil {
		return nil
	}
	return s.agent.System()
}

// Tools returns the running agent's toolset, or nil when no agent is active.
// Like System(), this is the toolset actually being sent — built-ins after
// the disabled-tools filter, plus whatever MCP and conditional tools were
// wired in — not a reconstruction of what the params asked for.
func (s *Session) Tools() core.Tools {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agent == nil {
		return nil
	}
	return s.agent.Tools()
}

// SetPluginRoot scopes the next agent turn to a plugin. The slash command
// flow calls this when the user invokes a /plugin-skill so subprocesses
// spawned during the turn see PLUGIN_ROOT pointing at that plugin.
// Pass "" to clear (typically done at turn end).
func (s *Session) SetPluginRoot(path string) {
	s.mu.Lock()
	s.pluginRoot = path
	s.mu.Unlock()
}

// PluginRoot returns the plugin scope for the current turn, or "" if
// no plugin scope is active.
func (s *Session) PluginRoot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pluginRoot
}
