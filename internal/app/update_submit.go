// Submit dispatch — what happens when the user presses Enter, plus the
// single SubmitToAgent exit point that every input path funnels through.
// Lives entirely on *model so a reader can follow Enter → agent without
// jumping packages.
package app

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/image"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
)

// handleSubmit is the Enter handler. Reads the textarea; if a turn is
// already streaming, queues for later; otherwise runs the submission
// pipeline.
func (m *model) handleSubmit() tea.Cmd {
	m.userInput.PromptSuggestion.Clear()

	raw := strings.TrimSpace(m.userInput.FullValue())
	if raw == "" && len(m.userInput.Images.Pending) == 0 {
		return nil
	}

	if m.conv.Stream.Active {
		log.QueueLog("handleSubmit: stream active, enqueue %q", raw)
		return m.enqueueWhileStreaming(raw)
	}

	// A manual /compact computes its summary asynchronously, then applies it
	// in place. Accepting a message in that window would race the compaction
	// (the summary predates the new message, so an in-place replace could drop
	// it). Hold submission until compaction finishes; the textarea keeps the
	// text so the user just presses Enter again.
	if m.conv.Compact.Active {
		m.conv.AddNotice("Compaction in progress — please resubmit in a moment.")
		return nil
	}

	log.QueueLog("handleSubmit: stream idle, normal submit %q", raw)
	m.conv.Compact.ClearResult()
	return m.dispatchSubmission(raw)
}

// enqueueWhileStreaming parks the input in the queue and resets the
// textarea. The queued item is dequeued either by drainInputQueueWhileIdle
// (on stream cancel / edit exit) or by drainTurnQueues (at turn boundary).
func (m *model) enqueueWhileStreaming(raw string) tea.Cmd {
	images := m.userInput.PendingImages()
	if m.userInput.Queue.Enqueue(raw, images) < 0 {
		m.conv.AddNotice("Input queue is full. Please wait for the current turn to complete.")
		return nil
	}
	m.userInput.Reset()
	log.QueueLog("enqueueWhileStreaming: queued %q queueLen=%d", raw, m.userInput.Queue.Len())
	return nil
}

// dispatchSubmission runs the submission pipeline: exit shortcut →
// prompt-hook gate → history → slash command? → otherwise send to
// agent. Shared by the Enter handler and drainInputQueueWhileIdle.
func (m *model) dispatchSubmission(raw string) tea.Cmd {
	if input.IsExitRequest(raw) {
		cmd, _ := m.QuitWithCancel()
		return cmd
	}

	// A human turn resets the auto-continue budget; a copilot-driven continuation
	// (flagged) does not, so MaxContinuations bounds a run of consecutive
	// auto-turns rather than the whole session. Capture the copilot's note now,
	// before the flag resets, so the built message can wear its "⎿ autopilot ·
	// N/M" continuation annotation.
	autopilotNote := ""
	if m.autopilotContinuing {
		// A capped run counts toward its ceiling ("2/5"). An uncapped one has no
		// denominator, and a bare "2" hanging off the mark reads like a truncated
		// fraction — so it says what the number is instead.
		if m.env.AutoPilot.ContinuationsUnlimited() {
			autopilotNote = fmt.Sprintf("step %d", m.autopilotContinuations)
		} else {
			autopilotNote = fmt.Sprintf("%d/%d", m.autopilotContinuations, m.env.AutoPilot.ResolvedMaxContinuations())
		}
		m.autopilotContinuing = false
	} else {
		m.autopilotContinuations = 0
	}

	if blocked, reason := m.checkPromptHook(context.Background(), raw); blocked {
		m.conv.AddNotice("Prompt blocked: " + reason)
		m.userInput.Reset()
		return tea.Batch(m.CommitMessages()...)
	}

	m.userInput.RecordSubmission(m.env.CWD, raw)

	// A leading absolute path to an existing image (e.g. a drag-drop paste of
	// "upload an image first") starts with "/" and would otherwise parse as a
	// slash command. Bypass command matching so it flows through the normal
	// message path, where ProcessImageRefs attaches it as an image.
	if input.LeadingImagePath(m.env.CWD, raw) == "" {
		if cmd, handled := m.runSlashCommandIfMatched(raw); handled {
			return cmd
		}
	}

	msg, ok := m.buildUserMessage(raw)
	if !ok {
		return tea.Batch(m.CommitMessages()...)
	}
	// Text-only models can't receive image parts, so inline each image's path
	// into the content and let the model decide how to use it (e.g. call an
	// MCP tool to inspect the file) instead of refusing the turn. The appended
	// message keeps its images; only the provider send drops them.
	content, providerImages := m.adaptTurnForProvider(msg.Content, msg.Images)
	msg.Content = content
	msg.AutopilotNote = autopilotNote
	m.conv.Append(msg)
	m.userInput.Reset()
	return m.SubmitToAgent(msg.Content, providerImages)
}

// runSlashCommandIfMatched returns (cmd, true) if `raw` is a slash command
// the controller handled, or (nil, false) if it should fall through to a
// provider turn.
func (m *model) runSlashCommandIfMatched(raw string) (tea.Cmd, bool) {
	ctrl := input.NewSlashCommandController(m.slashCommandEnv())
	return ctrl.HandleSubmit(raw)
}

// buildUserMessage resolves image references in raw text into a ChatMessage
// ready to append. Returns ok=false if image resolution failed (in which
// case a notice has already been appended to conv).
func (m *model) buildUserMessage(raw string) (core.ChatMessage, bool) {
	content, fileImages, err := input.ProcessImageRefs(m.env.CWD, raw)
	if err != nil {
		m.conv.AddNotice("Image error: " + err.Error())
		return core.ChatMessage{}, false
	}
	displayContent := content
	content, inlineImages := m.userInput.ExtractInlineImages(content)
	allImages := make([]core.Image, 0, len(inlineImages)+len(fileImages))
	allImages = append(allImages, inlineImages...)
	allImages = append(allImages, fileImages...)
	return core.ChatMessage{
		Role:           core.RoleUser,
		Content:        content,
		DisplayContent: displayContent,
		Images:         allImages,
	}, true
}

// adaptTurnForProvider fits a user turn to the active model's image capability,
// returning the content to send and the images the provider may receive. A
// vision-capable model takes both unchanged. A text-only one cannot receive
// image parts at all, so each image's path is inlined into the content and no
// image comes back — the model then decides how to use the path (e.g. call an
// MCP image-description tool) rather than the app refusing the turn.
//
// Callers keep the original slice on the message they append to conv, so the
// image stays visible in the transcript and a later switch to a vision-capable
// model can still use it; dropImagesTextOnlyModelRejects strips those on the
// way back out when a session is rebuilt. It does not cover the live turn:
// seedAgentMessages drops the pending message from the chain, and an already
// active session skips seeding altogether — which is why the images must not
// reach Send in the first place.
func (m *model) adaptTurnForProvider(content string, images []core.Image) (string, []core.Image) {
	if len(images) == 0 || llm.SupportsImages(m.env.LLMProvider, m.env.GetModelID()) {
		return content, images
	}
	var sb strings.Builder
	if content != "" {
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}
	sb.WriteString("[Attached image(s) — this model cannot view images directly. The files are on disk; use an available tool (e.g. an MCP image-description tool) to inspect them if you need their contents.]\n")
	for i, img := range images {
		path, temp, err := image.EnsureFilePath(img)
		if err != nil {
			// Nothing on disk to point at — the name is all the model gets.
			path = img.FileName
		}
		if temp {
			// The path below is inlined into content conv persists and replays, so
			// the file has to outlive this turn — see removeTempImageFiles.
			m.tempImageFiles = append(m.tempImageFiles, path)
		}
		fmt.Fprintf(&sb, "[Image #%d: %s]\n", i+1, path)
	}
	return strings.TrimSpace(sb.String()), nil
}

// drainInputQueueWhileIdle pops one queued item (if any) and runs it
// through the normal submission pipeline. Called wherever queued input may
// meet an idle agent: from handleStreamCancel, so input queued while a turn
// was streaming runs right after Ctrl+C/Esc; and from routeKeypress when
// leaving queue-edit mode, which releases a drain that drainTurnQueues held
// during the edit.
func (m *model) drainInputQueueWhileIdle() tea.Cmd {
	item, ok := m.userInput.Queue.Dequeue()
	if !ok {
		return nil
	}
	m.conv.Compact.ClearResult()

	// The textarea may hold an unrelated draft (in-progress typing on the
	// cancel path, the restored pre-edit stash on the edit-exit path) —
	// dispatchSubmission resets the textarea after a send, so park the draft
	// and put it back.
	draft := m.userInput.Textarea.Value()
	m.userInput.RestoreImages(item.Images)
	cmd := m.dispatchSubmission(item.Content)
	if draft != "" && m.userInput.Textarea.Value() == "" {
		m.userInput.Textarea.SetValue(draft)
		m.userInput.Textarea.CursorEnd()
	}
	return cmd
}

// SubmitToAgent is the single exit point for "send this content to the
// agent" — user Enter, slash command output, skill button, cron fire,
// hook continuation, hub notification. Ensures the agent session is up,
// pushes content+images onto its inbox, returns the outbox-poll cmd.
// On no-provider or ensureAgentSession failure, posts a notice and
// returns a commit cmd (the agent is not contacted).
func (m *model) SubmitToAgent(content string, images []core.Image) tea.Cmd {
	log.QueueLog("SubmitToAgent: %q", truncate(content, 60))
	if m.env.LLMProvider == nil {
		return m.notifyNoProvider()
	}

	startCmd, err := m.ensureAgentSession(content)
	if err != nil {
		m.conv.AddNotice("Failed to start agent: " + err.Error())
		return tea.Batch(m.CommitMessages()...)
	}

	m.env.DetectThinkingKeywords(content)

	sendCmd := m.sendToAgent(content, images)
	if startCmd != nil {
		return tea.Batch(startCmd, sendCmd)
	}
	return sendCmd
}

// notifyNoProvider posts the standard "no provider connected" notice
// and returns a commit cmd.
func (m *model) notifyNoProvider() tea.Cmd {
	m.conv.AddNotice(input.NoProviderMsg)
	return tea.Batch(m.CommitMessages()...)
}

// HandleSkillInvocation runs the agent against the pending skill
// invocation: consume → append to conv → SubmitToAgent. Plugin root
// (if the skill came from a plugin) is set on the agent so hooks/tools
// fired during the turn see PLUGIN_ROOT pointing at that plugin.
func (m *model) HandleSkillInvocation() tea.Cmd {
	displayMsg, fullMsg, pluginRoot := m.userInput.Skill.ConsumeInvocation()
	if m.env.LLMProvider == nil {
		return m.notifyNoProvider()
	}
	m.conv.Append(core.ChatMessage{Role: core.RoleUser, Content: fullMsg, DisplayContent: displayMsg})
	if pluginRoot != "" {
		m.services.Agent.SetPluginRoot(pluginRoot)
	}
	return m.SubmitToAgent(fullMsg, nil)
}
