// Permission approval flow + gate response. The approval modal lives in
// the input package; here we handle the user's decision (once / for-session
// / persist-as-rule) and forward it through the PermissionGate that gates
// the agent's tool calls.
package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/session/transcript"
	"github.com/genai-io/san/internal/tool/perm"
)

type permissionDecision struct {
	Approved bool
	AllowAll bool // option 2: allow for the rest of the session
	Persist  bool // option 3: write a persistent rule
	Request  *perm.PermissionRequest
}

// Scope labels recorded for user-driven permission decisions. These names
// belong to the approval modal — the transcript schema treats them as opaque
// strings, so adding a new modal option (e.g. "this directory only") only
// requires a new label here, not a schema bump.
const (
	permScopeOnce       = "once"
	permScopeSession    = "session"
	permScopePersistent = "persistent"
)

// permDecisionFor maps the user's approve/reject bool to the transcript
// decision enum. Shared by the config-decided fast path (agent.go) and the
// user-decided ask path (this file).
func permDecisionFor(approved bool) string {
	if approved {
		return transcript.PermissionPermit
	}
	return transcript.PermissionReject
}

// permScope encodes which approval-modal option the user picked. Persist
// takes priority over AllowAll because the modal exposes them as
// mutually-exclusive radio-style choices.
func permScope(d permissionDecision) string {
	switch {
	case d.Persist:
		return permScopePersistent
	case d.AllowAll:
		return permScopeSession
	default:
		return permScopeOnce
	}
}

func (m *model) handlePermGateDecision(decision permissionDecision) tea.Cmd {
	if !m.services.Agent.Active() {
		return nil
	}
	req := m.services.Agent.PendingPermission()
	m.services.Agent.SetPendingPermission(nil)
	if req == nil {
		return nil
	}
	m.conv.Tool.ClearAwaitingApproval()
	reason := "user denied"
	if decision.Approved {
		reason = "user approved"
		// Restamp: the call has been sitting on its PreToolEvent stamp since
		// before the prompt went up, so the row's elapsed timer would otherwise
		// report how long the user deliberated as execution time.
		m.conv.Tool.MarkStarted(req.ToolCallID)
		if decision.AllowAll && m.env.SessionPermissions != nil && decision.Request != nil {
			m.env.SessionPermissions.AllowTool(decision.Request.ToolName)
		}
	}
	// Snapshot the request before releasing the permission gate. Agent tools
	// add runtime callbacks to their input map as soon as the response wakes
	// them; serializing that shared map afterward can otherwise race the
	// write and crash the process with concurrent map iteration/write.
	permRecord := permDecisionRecord(req, decision, reason, m.env.SessionMode())
	select {
	case req.Response <- conv.PermGateResponse{Allow: decision.Approved, Reason: reason}:
	default:
	}
	if rec := m.services.Session.Recorder(); rec != nil {
		rec.RecordPermissionDecided(permRecord)
	}
	return conv.PollPermGate(m.services.Agent.PermissionGate())
}

func permDecisionRecord(req *conv.PermGateRequest, decision permissionDecision, reason, mode string) transcript.PermissionRecord {
	return transcript.PermissionRecord{
		RequestID: req.RequestID,
		Tool:      req.ToolName,
		Input:     marshalPermInput(req.Input),
		Detail:    permDetail(decision.Request),
		Decision:  permDecisionFor(decision.Approved),
		Source:    transcript.PermissionSourceUser,
		Scope:     permScope(decision),
		Reason:    reason,
		Mode:      mode,
	}
}
