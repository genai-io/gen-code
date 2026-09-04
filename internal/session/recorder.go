package session

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/session/transcript"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
)

// Recorder turns core.Agent lifecycle events into transcript records in
// causal order — every message.appended lands before the inference.requested
// that consumes it. One Recorder is bound to one (sessionID, agentID) pair;
// OnAgentEvent is the core.Config.OnEvent callback.
type Recorder struct {
	fs          *transcript.FileStore
	sessionID   string
	agentID     string
	isSidechain bool

	turn atomic.Int64

	mu            sync.Mutex
	lastRequest   *requestState
	lastMessageID string // for parentId on message.appended
}

type requestState struct {
	turn       int
	startedAt  time.Time
	messageIDs []string
}

// RecorderOptions configures a Recorder. Provider/Model/MaxTokens/AgentID
// land on session.started; mid-session changes need a model.changed event,
// not per-record restamping.
type RecorderOptions struct {
	FileStore *transcript.FileStore
	SessionID string
	AgentID   string
	Provider  string
	Model     string
	MaxTokens int
	Cwd       string
	ProjectID string
	// Sidechain marks each message.appended that flows through this
	// recorder as a sidechain entry — used for forked or subagent runs
	// (e.g. the L1 self-learning review) so the inspector can filter
	// them out of the main thread but still surface them on demand.
	Sidechain bool
}

// NewRecorder writes session.started before returning so observer-driven
// replay (system sections, tools) lands on a file that already carries
// session metadata. Start is idempotent so Store.Save's own Start stays a
// no-op.
func NewRecorder(opts RecorderOptions) *Recorder {
	if opts.FileStore != nil && opts.SessionID != "" {
		_ = opts.FileStore.Start(context.Background(), transcript.StartCommand{
			SessionID: opts.SessionID,
			ProjectID: opts.ProjectID,
			Cwd:       opts.Cwd,
			Provider:  opts.Provider,
			Model:     opts.Model,
			MaxTokens: opts.MaxTokens,
			AgentID:   opts.AgentID,
			Time:      time.Now(),
		})
	}
	return &Recorder{
		fs:          opts.FileStore,
		sessionID:   opts.SessionID,
		agentID:     opts.AgentID,
		isSidechain: opts.Sidechain,
	}
}

// SessionID returns the session ID the Recorder writes to — useful
// for surfacing the L1 fork's session in the post-review recap so a
// user can "san --resume <id>" to replay the review in isolation.
func (r *Recorder) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// seedLastMessageID primes the parent pointer for the next message.appended
// from a known leaf. Use after Continue/Resume so the first new turn chains
// off the loaded history instead of starting a fresh root and orphaning
// everything before it.
func (r *Recorder) seedLastMessageID(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	r.lastMessageID = id
	r.mu.Unlock()
}

// audit runs write under r's nil-guard, time-stamps it, and logs but does
// not propagate failures — audit telemetry must never block the recorder's
// caller (hook engine, permission decider, skill registry).
func (r *Recorder) audit(name string, write func(time.Time) error) {
	if r == nil || r.fs == nil || r.sessionID == "" {
		return
	}
	if err := write(time.Now()); err != nil {
		log.Logger().Warn("recorder: append "+name+" failed", zap.Error(err))
	}
}

// RecordHook writes one hook.fired record.
func (r *Recorder) RecordHook(rec transcript.HookRecord) {
	r.audit("hook", func(t time.Time) error {
		return r.fs.AppendHook(context.Background(), transcript.AppendHookCommand{
			SessionID: r.sessionID, Time: t, Record: rec,
		})
	})
}

// RecordSkillState writes one skill.state.changed record.
func (r *Recorder) RecordSkillState(rec transcript.SkillRecord) {
	r.audit("skill state", func(t time.Time) error {
		return r.fs.AppendSkillState(context.Background(), transcript.AppendSkillStateCommand{
			SessionID: r.sessionID, Time: t, Record: rec,
		})
	})
}

// RecordPermissionRequired emits permission.required for an ask escalation.
func (r *Recorder) RecordPermissionRequired(rec transcript.PermissionRecord) {
	r.recordPermission(transcript.PermissionRequired, rec)
}

// RecordPermissionDecided emits permission.decided for a terminal allow/reject.
func (r *Recorder) RecordPermissionDecided(rec transcript.PermissionRecord) {
	r.recordPermission(transcript.PermissionDecided, rec)
}

func (r *Recorder) recordPermission(typ string, rec transcript.PermissionRecord) {
	r.audit("permission", func(t time.Time) error {
		return r.fs.AppendPermission(context.Background(), transcript.AppendPermissionCommand{
			SessionID: r.sessionID, Time: t, Type: typ, Record: rec,
		})
	})
}

// OnAgentEvent is the core.Config.OnEvent callback. It dispatches by event
// type and writes the corresponding transcript record. Errors are logged
// rather than propagated — failing to record telemetry must not break the
// running session.
func (r *Recorder) OnAgentEvent(ev core.Event) {
	if r == nil || r.fs == nil || r.sessionID == "" {
		return
	}
	switch e := ev.(type) {
	case sdkagent.MessageStart:
		r.onInferenceRequested(e)
	case sdkagent.MessageEnd:
		r.onInferenceResponded(e)
	case core.SystemChange:
		r.onSystemChange(e)
	case core.ToolsChange:
		r.onToolsChange(e)
	case sdkagent.MessageAdded:
		r.onAppend(e.Message)
	case core.Compacted:
		r.onCompact(e)
	}
}

// onCompact persists the compaction boundary. The summary message itself is
// recorded via the preceding message.appended (compaction emits OnAppend for
// it first); this record marks that summary's ID as the point where replay
// stops walking parents, so the summarized-away history is not resurrected.
func (r *Recorder) onCompact(info core.Compacted) {
	if info.SummaryMessageID == "" {
		return
	}
	err := r.fs.Compact(context.Background(), transcript.CompactCommand{
		SessionID:        r.sessionID,
		Time:             time.Now(),
		SummaryMessageID: info.SummaryMessageID,
	})
	if err != nil {
		log.Logger().Warn("recorder: compact boundary failed", zap.Error(err))
	}
}

// onAppend persists message.appended at the moment the message enters the
// chain. This is what guarantees "causes before consumers": any subsequent
// inference.requested lands after the messages it references.
func (r *Recorder) onAppend(msg core.Message) {
	if msg.ID == "" {
		return
	}

	// Route through the same MessageToBlocks converter Store.Save uses, so the
	// dedupe key (message ID) maps to byte-identical content from either writer.
	// A tool result is a RoleUser message with a non-nil ToolResult; it
	// serializes as a "user" turn carrying tool_result blocks.
	// The agent's message is the conversation; the transcript is the view of
	// it. ToChat is that view with nothing drawn on it yet, which is exactly
	// what an agent-side message is.
	chat := core.ChatOf(msg)
	content := MessageToBlocks(chat)
	if len(content) == 0 {
		return // control signals etc. aren't model-visible
	}
	role := transcriptRole(chat.Role)

	r.mu.Lock()
	parent := r.lastMessageID
	r.lastMessageID = msg.ID
	r.mu.Unlock()

	err := r.fs.AppendMessage(context.Background(), transcript.AppendMessageCommand{
		SessionID:   r.sessionID,
		MessageID:   msg.ID,
		ParentID:    parent,
		Time:        time.Now(),
		AgentID:     r.agentID,
		IsSidechain: r.isSidechain,
		Role:        role,
		Content:     content,
	})
	if err != nil {
		log.Logger().Warn("recorder: append message failed", zap.Error(err))
	}
}

func (r *Recorder) onToolsChange(c core.ToolsChange) {
	typ := transcript.ToolAdded
	payload := transcript.ToolRecord{Caller: c.Caller}
	if c.Removed {
		typ = transcript.ToolRemoved
		payload.Name = c.Name
	} else {
		payload.Schema = toolSchemaView(c.Schema)
	}
	err := r.fs.AppendTool(context.Background(), transcript.AppendToolCommand{
		SessionID: r.sessionID,
		Time:      time.Now(),
		Type:      typ,
		Record:    payload,
	})
	if err != nil {
		log.Logger().Warn("recorder: append tools change failed", zap.Error(err))
	}
}

func toolSchemaView(s core.ToolSchema) *transcript.ToolSchemaView {
	view := &transcript.ToolSchemaView{
		Name:        s.Name,
		Description: s.Description,
	}
	if s.Definition != nil {
		if data, err := json.Marshal(s.Definition); err == nil {
			view.Parameters = data
		}
	}
	return view
}

func (r *Recorder) onSystemChange(c core.SystemChange) {
	typ := transcript.SystemSectionAdded
	if c.Removed {
		typ = transcript.SystemSectionRemoved
	}
	err := r.fs.AppendSystemSection(context.Background(), transcript.AppendSystemSectionCommand{
		SessionID: r.sessionID,
		Time:      time.Now(),
		Type:      typ,
		Record: transcript.SystemSectionRecord{
			Name:    c.Name,
			Slot:    c.Slot,
			Content: c.Content,
			Caller:  c.Caller,
		},
	})
	if err != nil {
		log.Logger().Warn("recorder: append system section failed", zap.Error(err))
	}
}

// onInferenceRequested records the call as digests rather than content, so the
// transcript can reference what went out without copying it on every turn. The
// digesting lives here because this is the only thing that ever wanted it.
func (r *Recorder) onInferenceRequested(e sdkagent.MessageStart) {
	if e.Inference == nil {
		return
	}
	ic := inferenceDigest(e.Inference)

	turn := int(r.turn.Add(1))
	now := time.Now()

	r.mu.Lock()
	r.lastRequest = &requestState{
		turn:       turn,
		startedAt:  now,
		messageIDs: ic.MessageIDs,
	}
	r.mu.Unlock()

	err := r.fs.AppendInference(context.Background(), transcript.AppendInferenceCommand{
		SessionID: r.sessionID,
		Time:      now,
		Type:      transcript.InferenceRequested,
		Record: transcript.InferenceRecord{
			Turn:         turn,
			SystemDigest: ic.SystemDigest,
			ToolsDigest:  ic.ToolsDigest,
			MessageIDs:   ic.MessageIDs,
		},
	})
	if err != nil {
		log.Logger().Warn("recorder: append inference.requested failed", zap.Error(err))
	}
}

func (r *Recorder) onInferenceResponded(e sdkagent.MessageEnd) {
	resp := e.Response
	if resp == nil {
		return
	}

	r.mu.Lock()
	prev := r.lastRequest
	r.lastRequest = nil
	r.mu.Unlock()

	now := time.Now()
	var turn int
	var latencyMs int64
	if prev != nil {
		turn = prev.turn
		latencyMs = now.Sub(prev.startedAt).Milliseconds()
	}

	err := r.fs.AppendInference(context.Background(), transcript.AppendInferenceCommand{
		SessionID: r.sessionID,
		Time:      now,
		Type:      transcript.InferenceResponded,
		Record: transcript.InferenceRecord{
			Turn:       turn,
			StopReason: string(resp.StopReason),
			LatencyMs:  latencyMs,
			// The transcript's own names on the left: this is a disk format
			// and keeps the keys it was written with.
			Usage: &transcript.InferenceUsage{
				InputTokens:              resp.Usage.Input,
				OutputTokens:             resp.Usage.Output,
				CacheCreationInputTokens: resp.Usage.CacheWrite,
				CacheReadInputTokens:     resp.Usage.CacheRead,
			},
		},
	})
	if err != nil {
		log.Logger().Warn("recorder: append inference.responded failed", zap.Error(err))
	}
}

// inferenceDigest is what the transcript keeps of one call: content-addressed,
// so a record can reference the system prompt and the toolset without carrying
// either. It moved here from core when the loop's events stopped being
// translated — nothing else ever read it.
func inferenceDigest(inf *sdkagent.Inference) transcript.InferenceRecord {
	schemas := make([]core.ToolSchema, 0, len(inf.Tools))
	for _, t := range inf.Tools {
		schemas = append(schemas, t.Schema)
	}
	return transcript.InferenceRecord{
		SystemDigest: sha256Hex([]byte(inf.System)),
		ToolsDigest:  toolsDigest(schemas),
		MessageIDs:   messageIDs(inf.Messages),
	}
}
