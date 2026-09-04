package inspector

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/genai-io/san/internal/session/transcript"
)

type replayState struct {
	RecordID       string                      `json:"recordId,omitempty"`
	RecordType     string                      `json:"recordType,omitempty"`
	Provider       string                      `json:"provider,omitempty"`
	Model          string                      `json:"model,omitempty"`
	ComposedSystem string                      `json:"composedSystem,omitempty"`
	MaxTokens      int                         `json:"maxTokens,omitempty"`
	Cwd            string                      `json:"cwd,omitempty"`
	AgentID        string                      `json:"agentId,omitempty"`
	System         []replaySystemSection       `json:"system"`
	Tools          []transcript.ToolSchemaView `json:"tools"`
	Messages       []replayMessage             `json:"messages"`
	Digests        replayDigests               `json:"digests"`
	Integrity      []replayIntegrityCheck      `json:"integrity,omitempty"`
}

type replaySystemSection struct {
	Name    string `json:"name"`
	Slot    int    `json:"slot"`
	Content string `json:"content"`
	Caller  string `json:"caller,omitempty"`
}

type replayMessage struct {
	ID       string                    `json:"id"`
	ParentID string                    `json:"parentId,omitempty"`
	Role     string                    `json:"role"`
	Content  []transcript.ContentBlock `json:"content,omitempty"`
}

type replayDigests struct {
	System string `json:"system"`
	Tools  string `json:"tools"`
}

// replayIntegrityCheck reports per-field divergence between what an
// inference.requested record claims (Expected) and what the replayer
// reconstructs (Got). Both sides are emitted as json.RawMessage so the
// wire format is self-describing — string fields for digests, array
// fields for messageIds — and the JS side can dispatch on shape without
// re-parsing string-of-JSON.
type replayIntegrityCheck struct {
	Field    string          `json:"field"`
	Expected json.RawMessage `json:"expected"`
	Got      json.RawMessage `json:"got"`
	OK       bool            `json:"ok"`
}

type replaySectionEntry struct {
	replaySystemSection
	order int
}

func replayFile(path, targetID string) (replayState, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return replayState{}, false, err
	}
	defer f.Close()

	var st replayBuilder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var found bool
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec transcript.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return replayState{}, false, err
		}
		st.apply(rec)
		if targetID != "" && rec.ID == targetID {
			found = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return replayState{}, false, err
	}
	if targetID != "" && !found {
		return replayState{}, false, nil
	}
	return st.snapshot(), true, nil
}

type replayBuilder struct {
	recordID   string
	recordType string
	provider   string
	model      string
	maxTokens  int
	cwd        string
	agentID    string
	seq        int
	sections   map[string]replaySectionEntry        // section name -> latest section
	tools      map[string]transcript.ToolSchemaView // tool name -> latest schema
	messages   map[string]replayMessage             // message id -> message node
	order      []string                             // message append order
	boundaryID string
	integrity  []replayIntegrityCheck
}

func (b *replayBuilder) apply(rec transcript.Record) {
	b.recordID = rec.ID
	b.recordType = rec.Type
	switch rec.Type {
	case transcript.SessionStarted:
		b.cwd = rec.Cwd
		if rec.Session != nil {
			b.provider = rec.Session.Provider
			b.model = rec.Session.Model
			b.maxTokens = rec.Session.MaxTokens
			b.agentID = rec.Session.AgentID
		}
	case transcript.SessionCompacted:
		if rec.Session != nil {
			b.boundaryID = rec.Session.SummaryMessageID
		}
	case transcript.SystemSectionAdded:
		if rec.System == nil {
			return
		}
		if b.sections == nil {
			b.sections = make(map[string]replaySectionEntry)
		}
		entry, existed := b.sections[rec.System.Name]
		if !existed {
			b.seq++
			entry.order = b.seq
		}
		entry.replaySystemSection = replaySystemSection{
			Name:    rec.System.Name,
			Slot:    rec.System.Slot,
			Content: rec.System.Content,
			Caller:  rec.System.Caller,
		}
		b.sections[rec.System.Name] = entry
	case transcript.SystemSectionRemoved:
		if rec.System != nil && b.sections != nil {
			delete(b.sections, rec.System.Name)
		}
	case transcript.ToolAdded:
		if rec.Tool == nil || rec.Tool.Schema == nil {
			return
		}
		if b.tools == nil {
			b.tools = make(map[string]transcript.ToolSchemaView)
		}
		b.tools[rec.Tool.Schema.Name] = *rec.Tool.Schema
	case transcript.ToolRemoved:
		if rec.Tool != nil && b.tools != nil {
			delete(b.tools, rec.Tool.Name)
		}
	case transcript.MessageAppended:
		if rec.Message != nil {
			if b.messages == nil {
				b.messages = make(map[string]replayMessage)
			}
			msg := replayMessage{
				ID:       rec.Message.MessageID,
				ParentID: rec.ParentID,
				Role:     rec.Message.Role,
				Content:  append([]transcript.ContentBlock(nil), rec.Message.Content...),
			}
			b.messages[msg.ID] = msg
			b.order = append(b.order, msg.ID)
		}
	case transcript.InferenceRequested:
		if rec.Inference != nil {
			b.checkInference(rec.Inference)
		}
	}
}

func (b *replayBuilder) checkInference(inf *transcript.InferenceRecord) {
	system := b.sortedSections()
	tools := b.sortedTools()
	messageIDs := replayMessageIDs(b.activeMessages())
	sysDigest := transcript.DigestSystem(renderSections(system))
	toolsDigest := transcript.DigestTools(tools)
	b.integrity = append(b.integrity,
		replayIntegrityCheck{
			Field: "systemDigest", Expected: jsonRaw(inf.SystemDigest), Got: jsonRaw(sysDigest),
			OK: inf.SystemDigest == "" || inf.SystemDigest == sysDigest,
		},
		replayIntegrityCheck{
			Field: "toolsDigest", Expected: jsonRaw(inf.ToolsDigest), Got: jsonRaw(toolsDigest),
			OK: inf.ToolsDigest == "" || inf.ToolsDigest == toolsDigest,
		},
		replayIntegrityCheck{
			Field: "messageIds", Expected: jsonRaw(inf.MessageIDs), Got: jsonRaw(messageIDs),
			OK: len(inf.MessageIDs) == 0 || reflect.DeepEqual(inf.MessageIDs, messageIDs),
		},
	)
}

func (b *replayBuilder) snapshot() replayState {
	system := b.sortedSections()
	tools := b.sortedTools()
	return replayState{
		RecordID:       b.recordID,
		RecordType:     b.recordType,
		Provider:       b.provider,
		Model:          b.model,
		MaxTokens:      b.maxTokens,
		Cwd:            b.cwd,
		AgentID:        b.agentID,
		ComposedSystem: composedSystem(system),
		System:         system,
		Tools:          tools,
		Messages:       b.activeMessages(),
		Digests:        replayDigests{System: transcript.DigestSystem(renderSections(system)), Tools: transcript.DigestTools(tools)},
		Integrity:      append([]replayIntegrityCheck(nil), b.integrity...),
	}
}

// composedSystem joins section contents with blank lines, mirroring
// core.System.Prompt() — this is the exact string the model receives.
// The same join rule produces digestSystem, so a matching composedSystem
// hash equals systemDigest in the inference record.
func composedSystem(sections []replaySystemSection) string {
	parts := make([]string, 0, len(sections))
	for _, sec := range sections {
		if sec.Content != "" {
			parts = append(parts, sec.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (b *replayBuilder) sortedSections() []replaySystemSection {
	if len(b.sections) == 0 {
		return nil
	}
	entries := make([]replaySectionEntry, 0, len(b.sections))
	for _, e := range b.sections {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Slot != entries[j].Slot {
			return entries[i].Slot < entries[j].Slot
		}
		return entries[i].order < entries[j].order
	})
	out := make([]replaySystemSection, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.replaySystemSection)
	}
	return out
}

func (b *replayBuilder) sortedTools() []transcript.ToolSchemaView {
	if len(b.tools) == 0 {
		return nil
	}
	out := make([]transcript.ToolSchemaView, 0, len(b.tools))
	for _, t := range b.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (b *replayBuilder) activeMessages() []replayMessage {
	if len(b.order) == 0 {
		return nil
	}

	hasChild := make(map[string]bool, len(b.order))
	for _, id := range b.order {
		n := b.messages[id]
		if n.ParentID != "" {
			hasChild[n.ParentID] = true
		}
	}

	var leafID string
	for _, id := range slices.Backward(b.order) {

		if !hasChild[id] {
			leafID = id
			break
		}
	}
	if leafID == "" {
		leafID = b.order[len(b.order)-1]
	}

	reversed := make([]replayMessage, 0, len(b.order))
	seen := make(map[string]bool, len(b.order))
	for cur := leafID; cur != ""; {
		if seen[cur] {
			break
		}
		seen[cur] = true

		n, ok := b.messages[cur]
		if !ok {
			break
		}
		reversed = append(reversed, n)
		if b.boundaryID != "" && cur == b.boundaryID {
			break
		}
		cur = n.ParentID
	}

	out := make([]replayMessage, 0, len(reversed))
	for _, r := range slices.Backward(reversed) {
		out = append(out, r)
	}
	return out
}

func replayMessageIDs(messages []replayMessage) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
}

// jsonRaw marshals v to a json.RawMessage. A marshal error returns "null" so
// the field still serializes legally; downstream consumers treat null as
// "no value" the same way they treat an empty input.
func jsonRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// renderSections rebuilds the prompt the agent sent from the section records
// it left behind, the way core/system builds it: in slot order, joined by a
// blank line. This is replay's own answer, which is why it is here and not in
// transcript — the digest is one number, but reconstructing what was hashed is
// what replay is for.
func renderSections(sections []replaySystemSection) string {
	parts := make([]string, 0, len(sections))
	for _, sec := range sections {
		if sec.Content != "" {
			parts = append(parts, sec.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}
