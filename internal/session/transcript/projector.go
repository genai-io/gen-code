package transcript

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/genai-io/san/internal/todo"
)

func Project(records []Record) (*Transcript, error) {
	t := &Transcript{}
	messageMap := make(map[string]Node, len(records))
	order := make([]string, 0, len(records))
	var compactBoundary string

	for _, r := range records {
		switch r.Type {
		case SessionStarted:
			applyStarted(t, r)
		case SessionForked:
			applyForked(t, r)
		case MessageAppended:
			n, err := buildNode(r)
			if err != nil {
				return nil, err
			}
			messageMap[n.ID] = n
			order = append(order, n.ID)
			if t.ID == "" {
				t.ID = r.SessionID
			}
			if t.Cwd == "" {
				t.Cwd = r.Cwd
			}
			if t.CreatedAt.IsZero() {
				t.CreatedAt = r.Time
			}
			if t.UpdatedAt.Before(r.Time) {
				t.UpdatedAt = r.Time
			}
		case SessionStatePatched:
			if err := applyStatePatch(&t.State, r.State); err != nil {
				return nil, err
			}
			if t.ID == "" {
				t.ID = r.SessionID
			}
			if t.UpdatedAt.Before(r.Time) {
				t.UpdatedAt = r.Time
			}
		case SessionCompacted:
			if r.Session != nil {
				compactBoundary = r.Session.SummaryMessageID
			}
			if t.ID == "" {
				t.ID = r.SessionID
			}
			if t.UpdatedAt.Before(r.Time) {
				t.UpdatedAt = r.Time
			}
		}
	}

	t.Messages = materializeActiveChain(messageMap, order, compactBoundary)
	return t, nil
}

func applyStarted(t *Transcript, r Record) {
	if t.ID == "" {
		t.ID = r.SessionID
	}
	if t.Cwd == "" {
		t.Cwd = r.Cwd
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = r.Time
	}
	if t.UpdatedAt.Before(r.Time) {
		t.UpdatedAt = r.Time
	}
	if r.Session != nil {
		t.Provider = r.Session.Provider
		t.Model = r.Session.Model
		t.ParentID = r.Session.ParentID
	}
}

func applyForked(t *Transcript, r Record) {
	if t.ID == "" {
		t.ID = r.SessionID
	}
	if t.UpdatedAt.Before(r.Time) {
		t.UpdatedAt = r.Time
	}
	if r.Session != nil && r.Session.ParentID != "" {
		t.ParentID = r.Session.ParentID
	}
}

func buildNode(r Record) (Node, error) {
	if r.Message == nil {
		return Node{}, fmt.Errorf("message.appended missing payload")
	}
	return Node{
		ID:          r.Message.MessageID,
		ParentID:    r.ParentID,
		Role:        r.Message.Role,
		Time:        r.Time,
		Cwd:         r.Cwd,
		GitBranch:   r.GitBranch,
		AgentID:     r.AgentID,
		IsSidechain: r.IsSidechain,
		Content:     r.Message.Content,
	}, nil
}

func applyStatePatch(state *State, patch *StateRecord) error {
	if patch == nil {
		return nil
	}
	for _, op := range patch.Ops {
		switch op.Path {
		case PatchPathTitle:
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				return fmt.Errorf("patch %s: %w", op.Path, err)
			}
			state.Title = v
		case PatchPathLastPrompt:
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				return fmt.Errorf("patch %s: %w", op.Path, err)
			}
			state.LastPrompt = v
		case PatchPathTag:
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				return fmt.Errorf("patch %s: %w", op.Path, err)
			}
			state.Tag = v
		case PatchPathMode:
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				return fmt.Errorf("patch %s: %w", op.Path, err)
			}
			state.Mode = v
		case PatchPathAutoPilot:
			var v string
			if err := json.Unmarshal(op.Value, &v); err != nil {
				return fmt.Errorf("patch %s: %w", op.Path, err)
			}
			state.AutoPilot = v
		case PatchPathTasks:
			var tasks []todo.Item
			if err := json.Unmarshal(op.Value, &tasks); err != nil {
				return fmt.Errorf("patch %s: %w", op.Path, err)
			}
			state.Tasks = TrackerItemViewsFromItems(tasks)
		case PatchPathWorktree:
			if string(op.Value) == "null" {
				state.Worktree = nil
				continue
			}
			var wt WorktreeState
			if err := json.Unmarshal(op.Value, &wt); err != nil {
				return fmt.Errorf("patch %s: %w", op.Path, err)
			}
			state.Worktree = &wt
		default:
			// Unknown patch paths are ignored so older readers tolerate
			// records produced by newer schemas. New paths must remain
			// additive; never repurpose an existing path's meaning.
			continue
		}
	}
	return nil
}

func materializeActiveChain(messageMap map[string]Node, order []string, boundary string) []Node {
	if len(order) == 0 {
		return nil
	}

	hasChild := make(map[string]bool, len(order))
	for _, id := range order {
		n := messageMap[id]
		if n.ParentID != "" {
			hasChild[n.ParentID] = true
		}
	}

	var leafID string
	for _, id := range slices.Backward(order) {

		if !hasChild[id] {
			leafID = id
			break
		}
	}
	if leafID == "" {
		// All messages have children — likely a corrupted transcript with cycles.
		// Fall back to the last message in insertion order to avoid silent data loss.
		leafID = order[len(order)-1]
	}

	reversed := make([]Node, 0, len(order))
	cur := leafID
	seen := make(map[string]bool, len(order))
	for cur != "" {
		if seen[cur] {
			break
		}
		seen[cur] = true

		n, ok := messageMap[cur]
		if !ok {
			break
		}
		reversed = append(reversed, n)

		if boundary != "" && cur == boundary {
			break
		}
		cur = n.ParentID
	}

	out := make([]Node, 0, len(reversed))
	for _, r := range slices.Backward(reversed) {
		out = append(out, r)
	}
	return out
}
