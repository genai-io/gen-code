package input

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/core"
)

const maxQueueSize = 50

// QueueItem is a user-typed message captured while the agent is mid-turn.
// Items live in the queue until the next turn boundary, when one is popped
// (FIFO), appended to the conversation, and handed to the agent. Until that
// moment the item is fully owned by the TUI — editable, cancelable,
// reorderable.
type QueueItem struct {
	ID      int
	Content string
	Images  []core.Attachment
}

type Queue struct {
	items     []QueueItem
	nextID    int
	SelectIdx int    // -1 = no selection, 0+ = selected queue item index
	Stashed   string // stashed textarea input while navigating into queue
}

func NewQueue() Queue {
	return Queue{SelectIdx: -1}
}

func (q *Queue) Enqueue(content string, images []core.Attachment) int {
	if len(q.items) >= maxQueueSize {
		return -1
	}
	q.nextID++
	q.items = append(q.items, QueueItem{ID: q.nextID, Content: content, Images: images})
	return q.nextID
}

func (q *Queue) Dequeue() (QueueItem, bool) {
	if len(q.items) == 0 {
		return QueueItem{}, false
	}
	item := q.items[0]
	q.items[0] = QueueItem{}
	q.items = q.items[1:]
	q.adjustSelectionAfterRemove(0)
	return item, true
}

func (q *Queue) At(idx int) (QueueItem, bool) {
	if idx < 0 || idx >= len(q.items) {
		return QueueItem{}, false
	}
	return q.items[idx], true
}

func (q *Queue) Items() []QueueItem {
	out := make([]QueueItem, len(q.items))
	copy(out, q.items)
	return out
}

func (q *Queue) Len() int { return len(q.items) }

func (q *Queue) LastIndex() int {
	if len(q.items) == 0 {
		return -1
	}
	return len(q.items) - 1
}

func (q *Queue) UpdateAt(idx int, content string, images []core.Attachment) bool {
	if idx < 0 || idx >= len(q.items) {
		return false
	}
	if content == "" && len(images) == 0 {
		q.items = append(q.items[:idx], q.items[idx+1:]...)
		q.adjustSelectionAfterRemove(idx)
		return true
	}
	q.items[idx].Content = content
	q.items[idx].Images = images
	return true
}

func (q *Queue) Clear() {
	q.items = nil
	q.ResetSelection()
}

func (q *Queue) ResetSelection() {
	q.SelectIdx = -1
	q.Stashed = ""
}

func (q *Queue) adjustSelectionAfterRemove(idx int) {
	if q.SelectIdx < 0 {
		return
	}
	switch {
	case len(q.items) == 0:
		q.ResetSelection()
	case q.SelectIdx > idx:
		q.SelectIdx--
	case q.SelectIdx >= len(q.items):
		q.SelectIdx = len(q.items) - 1
	}
}

// HandleQueueSelectKey handles keys when a queue item is selected.
// Only Up, Down, Enter, Escape, and Ctrl+C are intercepted; all other keys pass
// through to the textarea for normal in-place editing.
func (m *Model) HandleQueueSelectKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.Queue.SelectIdx < 0 {
		return nil, false
	}

	m.SaveCurrentQueueEdit()

	switch msg.String() {
	case "up":
		if m.Queue.SelectIdx > 0 {
			m.Queue.SelectIdx--
			m.LoadQueueItemIntoTextarea()
		} else {
			m.ExitQueueSelection()
			m.HistoryUp()
		}
		return nil, true

	case "down":
		qLen := m.Queue.Len()
		if m.Queue.SelectIdx >= qLen-1 {
			m.ExitQueueSelection()
			m.HistoryDown()
		} else {
			m.Queue.SelectIdx++
			m.LoadQueueItemIntoTextarea()
		}
		return nil, true

	case "enter", "esc":
		m.ExitQueueSelection()
		return nil, true

	case "ctrl+c":
		m.DeleteCurrentQueueItem()
		return nil, true
	}

	return nil, false
}

// DeleteCurrentQueueItem removes the selected queue item and navigates to the next.
func (m *Model) DeleteCurrentQueueItem() {
	if m.Queue.SelectIdx < 0 || m.Queue.SelectIdx >= m.Queue.Len() {
		return
	}
	m.Queue.UpdateAt(m.Queue.SelectIdx, "", nil)

	newLen := m.Queue.Len()
	if newLen == 0 {
		m.ExitQueueSelection()
		return
	}

	if m.Queue.SelectIdx >= newLen {
		m.Queue.SelectIdx = newLen - 1
	}
	m.LoadQueueItemIntoTextarea()
}

// EnterQueueSelection transitions into queue selection mode.
// Stashes current input and loads the last queue item into the textarea.
func (m *Model) EnterQueueSelection() {
	m.Queue.Stashed = m.Textarea.Value()
	m.Queue.SelectIdx = m.Queue.LastIndex()
	if m.Queue.SelectIdx < 0 {
		m.Queue.Stashed = ""
		return
	}
	m.LoadQueueItemIntoTextarea()
}

// ExitQueueSelection leaves queue selection mode and restores stashed input.
func (m *Model) ExitQueueSelection() {
	stashed := m.Queue.Stashed
	m.Queue.ResetSelection()
	m.Textarea.SetValue(stashed)
	m.Textarea.CursorEnd()
}

// SaveCurrentQueueEdit writes the current textarea content back to the
// selected queue item, preserving its position.
func (m *Model) SaveCurrentQueueEdit() {
	if m.Queue.SelectIdx < 0 || m.Queue.SelectIdx >= m.Queue.Len() {
		return
	}
	item, ok := m.Queue.At(m.Queue.SelectIdx)
	if !ok {
		return
	}
	content := strings.TrimSpace(m.Textarea.Value())
	m.Queue.UpdateAt(m.Queue.SelectIdx, content, item.Images)
}

// LoadQueueItemIntoTextarea loads the content of the selected queue item
// into the textarea for editing.
func (m *Model) LoadQueueItemIntoTextarea() {
	item, ok := m.Queue.At(m.Queue.SelectIdx)
	if !ok {
		return
	}
	m.Textarea.SetValue(item.Content)
	m.Textarea.CursorEnd()
}
