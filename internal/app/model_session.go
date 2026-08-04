// Session persistence and per-session task storage.
// Save/load conversations + task snapshots to disk, wire the task tracker's
// storage directory, fork a fresh session from the current one.
package app

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool/fs"
)

func (m *model) InitTaskStorage() {
	m.initTaskStorage(m.services.Session.ID())
}

func (m *model) PersistSession() error {
	if err := m.services.Session.EnsureStore(m.env.CWD); err != nil {
		return err
	}
	sess := m.buildSessionSnapshot()
	if sess == nil {
		return nil
	}

	if err := m.services.Session.Save(sess); err != nil {
		return err
	}

	m.services.Session.SetID(sess.Metadata.ID)
	m.initTaskStorage(m.services.Session.ID())

	m.services.Hook.SetTranscriptPath(m.services.Session.GetStore().SessionPath(sess.Metadata.ID))
	m.ReconfigureAgentTool()

	return nil
}

type persistSessionDoneMsg struct{ err error }

// Only safe when the session ID is already established (i.e. not the first save).
func (m *model) persistSessionCmd() tea.Cmd {
	if err := m.services.Session.EnsureStore(m.env.CWD); err != nil {
		log.Logger().Warn("failed to ensure session store for async persist", zap.Error(err))
		return nil
	}
	sess := m.buildSessionSnapshot()
	if sess == nil {
		return nil
	}

	store := m.services.Session.GetStore()
	return func() tea.Msg {
		if store == nil {
			return persistSessionDoneMsg{err: fmt.Errorf("no session store")}
		}
		return persistSessionDoneMsg{err: store.Save(sess)}
	}
}

// persistAfterTurn saves the session at the end of a turn, choosing the
// strategy by session state: the first save (no ID yet) runs synchronously so
// the session ID, task storage, and transcript wiring are established before
// the next turn; later saves go through the async command. Returns the async
// command to batch, or nil when the synchronous path ran (it logs its own
// error, matching how async failures surface via persistSessionDoneMsg).
func (m *model) persistAfterTurn() tea.Cmd {
	if m.services.Session.ID() != "" {
		return m.persistSessionCmd()
	}
	if err := m.PersistSession(); err != nil {
		log.Logger().Warn("failed to persist session at turn end", zap.Error(err))
	}
	return nil
}

// buildSessionSnapshot assembles the current conversation + task state into a
// session.Snapshot ready to Save, or nil when there is nothing to persist (no
// messages). Shared by the synchronous PersistSession and the async
// persistSessionCmd so the snapshot stays identical across both paths.
func (m *model) buildSessionSnapshot() *session.Snapshot {
	if len(m.conv.Messages) == 0 {
		return nil
	}

	msgs := session.MessagesFromChat(m.conv.Messages)

	var providerName, modelID string
	if m.env.CurrentModel != nil {
		providerName = string(m.env.CurrentModel.Provider)
		modelID = m.env.CurrentModel.ModelID
	}

	sess := &session.Snapshot{
		Metadata: session.SessionMetadata{
			ID:         m.services.Session.ID(),
			Provider:   providerName,
			Model:      modelID,
			Cwd:        m.env.CWD,
			LastPrompt: session.ExtractLastUserText(msgs),
			Mode:       m.env.SessionMode(),
			AutoPilot:  marshalAutoPilot(m.env.AutoPilot),
		},
		Messages:          msgs,
		Tasks:             m.services.Tracker.Export(),
		OmitMessageWrites: m.services.Session.Recorder() != nil,
	}

	if sess.Metadata.Title == "" {
		if m.env.SessionName != "" {
			sess.Metadata.Title = m.env.SessionName
		} else {
			sess.Metadata.Title = session.GenerateTitle(sess.Messages)
		}
	}

	return sess
}

func (m *model) loadSessionByID(id string) error {
	if err := m.services.Session.EnsureStore(m.env.CWD); err != nil {
		return err
	}

	sess, err := m.services.Session.Load(id)
	if err != nil {
		return err
	}

	// This is a different persisted conversation. Do not let a stopped agent's
	// restart snapshot override the selected transcript on the next turn, and
	// don't let the previous conversation's Reads satisfy Edit's
	// read-before-modify gate in this one.
	m.ResetAgentSession()
	fs.ResetFileViews()
	m.services.Tracker.SetStorageDir("")
	m.restoreSessionData(sess)

	if len(sess.Tasks) == 0 {
		m.services.Tracker.Reset()
	}

	m.env.InputTokens = 0
	m.env.OutputTokens = 0

	return nil
}

func (m *model) restoreSessionData(sess *session.Snapshot) {
	m.conv.Messages = session.MessagesToChat(sess.Messages)
	m.services.Session.SetID(sess.Metadata.ID)
	m.env.SessionName = sess.Metadata.Title

	m.initTaskStorage(m.services.Session.ID())

	if len(sess.Tasks) > 0 {
		m.services.Tracker.Import(sess.Tasks)
	}

	// Restore the session's autopilot config; an empty blob (e.g. an older
	// session) leaves the settings-seeded default in place. Rebuild the runtime
	// snapshot so a mid-session /resume (the agent may already be running) takes
	// the resumed steers/model/permission config immediately, not on the next
	// /autopilot Save.
	if ar := parseAutoPilot(sess.Metadata.AutoPilot); !ar.IsZero() {
		m.env.AutoPilot = ar
	}
	m.rebuildAutopilotReviewer()

	// Resume into the operation mode the session was saved in, so an autopilot
	// run picks up where it left off without re-cycling shift+tab. (Bypass never
	// round-trips — parseSessionMode maps it to Normal.) A session that saved no
	// mode keeps the launch's — see resumeOperationMode.
	if mode := resumeOperationMode(sess.Metadata.Mode, m.env.OperationMode); mode != m.env.OperationMode {
		m.env.OperationMode = mode
		m.applyOperationMode()
	}
}

func (m *model) initTaskStorage(sessionID string) {
	if m.services.Tracker.GetStorageDir() != "" {
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Logger().Warn("failed to get home directory for task storage", zap.Error(err))
		return
	}

	taskListID := setting.Getenv("TASK_LIST_ID")
	if taskListID != "" {
		dir := filepath.Join(confdir.Dir(homeDir), "tasks", taskListID)
		m.services.Tracker.SetStorageDir(dir)
		_ = m.services.Task.SetOutputDir(filepath.Join(dir, "outputs"))
		return
	}

	if sessionID == "" {
		return
	}
	dir := filepath.Join(confdir.Dir(homeDir), "tasks", sessionID)
	m.services.Tracker.SetStorageDir(dir)
	_ = m.services.Task.SetOutputDir(filepath.Join(dir, "outputs"))
}

func (m *model) forkSession() (string, error) {
	if m.services.Session.ID() == "" {
		return "", fmt.Errorf("no active session to fork")
	}
	forked, err := m.services.Session.Fork(m.services.Session.ID())
	if err != nil {
		return "", err
	}
	originalID := forked.Metadata.ParentSessionID
	m.services.Session.SetID(forked.Metadata.ID)
	// The live agent holds an onEvent closure over a Recorder bound to the
	// parent's id, and a Recorder's session is fixed at construction. Without
	// this stop it keeps writing the fork's messages, inferences, permissions
	// and hooks into the parent transcript — so resuming the original, which is
	// exactly what the UI tells the user to do, replays post-fork history and
	// the parent is no longer frozen at the fork point.
	//
	// Stop rather than Reset: a fork continues the conversation under a new id,
	// so the chain has to survive into the rebuilt agent. The next turn's
	// ensureAgentSession rebuilds with a Recorder bound to the new id, since
	// NewRecorder invalidates its cache on exactly that mismatch.
	m.StopAgentSession()
	m.services.Tracker.SetStorageDir("")
	return originalID, nil
}

// renameSession sets a custom name for the current session and persists it.
// The name is stored in env so subsequent saves use it instead of auto-generating
// a title from the conversation entries.
func (m *model) renameSession(name string) error {
	m.env.SessionName = name
	return m.PersistSession()
}
