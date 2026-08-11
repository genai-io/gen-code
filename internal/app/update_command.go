// Slash-command execution: builds SlashCommandEnv and runs commands
// through input.NewSlashCommandController.
package app

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/input"
)

func (m *model) slashCommandEnv() input.SlashCommandEnv {
	return input.SlashCommandEnv{
		// UI state
		Input:        &m.userInput,
		Conversation: &m.conv.ConversationModel,
		Tool:         &m.conv.Tool,
		Width:        m.env.Width,
		Height:       m.env.Height,
		Cwd:          m.env.CWD,
		InputTokens:  m.env.InputTokens,

		// Services
		Setting: m.services.Setting,
		LLM:     m.services.LLM,
		Session: m.services.Session,
		Command: m.services.Command,
		Skill:   m.services.Skill,
		Plugin:  m.services.Plugin,
		MCP:     m.services.MCP,
		Tracker: m.services.Tracker,
		Cron:    m.services.Cron,
		ToolSvc: m.services.Tool,

		// Env callbacks
		GetThinkingEffort: func() string { return m.env.EffectiveThinkingEffort() },
		SetThinkingEffort: m.env.SetThinkingEffort,
		ResetTokens:       m.env.ResetTokens,
		GetGoal:           func() string { return strings.TrimSpace(m.env.AutoPilot.Mission) },
		ContextUsage:      m.contextUsage,

		// Model actions
		CommitMessages:          m.CommitMessages,
		SubmitToAgent:           m.SubmitToAgent,
		HandleSkillInvocation:   m.HandleSkillInvocation,
		StartExternalEditor:     m.StartExternalEditor,
		ReloadAfterPluginChange: m.ReloadAfterPluginChange,
		PersistSession:          m.PersistSession,
		InitTaskStorage:         m.InitTaskStorage,
		ReconfigureAgentTool:    m.ReconfigureAgentTool,
		StopAgentSession:        m.StopAgentSession,
		ResetAgentSession:       m.ResetAgentSession,
		FireSessionEnd:          m.FireSessionEnd,
		BuildCompactRequest:     m.BuildCompactRequest,
		SpinnerTickCmd:          m.SpinnerTickCmd,
		ResetCronQueue:          m.ResetCronQueue,
		ForkSession:             m.forkSession,
		RunSelfLearnDemo:        m.runSelfLearnDemo,
		SetActivePersona:        m.setActivePersona,
		RenameSession:           m.renameSession,
	}
}

func (m *model) executeCommand(ctx context.Context, inputText string) (string, tea.Cmd, bool) {
	return input.NewSlashCommandController(m.slashCommandEnv()).Execute(ctx, inputText)
}
