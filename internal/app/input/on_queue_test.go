package input

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"testing"

	"github.com/genai-io/san/internal/core"
)

func TestQueueEnqueueDequeue(t *testing.T) {
	var q Queue
	id := q.Enqueue("hello", nil)
	if id != 1 {
		t.Fatalf("expected id 1, got %d", id)
	}
	item, ok := q.Dequeue()
	if !ok || item.Content != "hello" {
		t.Fatalf("unexpected: %v, %v", item, ok)
	}
	_, ok = q.Dequeue()
	if ok {
		t.Fatal("expected empty")
	}
}

func TestQueueDequeueIsFIFO(t *testing.T) {
	var q Queue
	q.Enqueue("first", nil)
	q.Enqueue("second", nil)
	q.Enqueue("third", nil)

	for _, want := range []string{"first", "second", "third"} {
		item, ok := q.Dequeue()
		if !ok {
			t.Fatalf("expected item %q, queue empty", want)
		}
		if item.Content != want {
			t.Fatalf("got %q, want %q", item.Content, want)
		}
	}
}

func TestQueueMaxSize(t *testing.T) {
	var q Queue
	for range maxQueueSize {
		q.Enqueue("item", nil)
	}
	if q.Enqueue("overflow", nil) != -1 {
		t.Fatal("expected -1")
	}
}

func TestQueueUpdateAtRemovesEmpty(t *testing.T) {
	var q Queue
	q.Enqueue("first", nil)
	q.Enqueue("second", nil)
	q.UpdateAt(0, "", nil)
	if q.Len() != 1 {
		t.Fatalf("expected 1, got %d", q.Len())
	}
	item, _ := q.At(0)
	if item.Content != "second" {
		t.Fatalf("expected 'second', got %q", item.Content)
	}
}

func TestQueueItems(t *testing.T) {
	var q Queue
	q.Enqueue("a", []core.Attachment{{Image: ai.Image{FileName: "test.png"}}})
	q.Enqueue("b", nil)
	items := q.Items()
	if len(items) != 2 {
		t.Fatalf("expected 2, got %d", len(items))
	}
}

// Removing a queue item should leave SelectIdx pointing at a still-valid
// neighbour so the user's cursor stays anchored. Covers Dequeue, UpdateAt's
// remove-on-empty branch, and the explicit DeleteCurrentQueueItem path.
func TestQueueAdjustSelectionAfterRemove(t *testing.T) {
	var q Queue
	q.Enqueue("a", nil)
	q.Enqueue("b", nil)
	q.Enqueue("c", nil)
	q.SelectIdx = 2 // pointing at "c"

	// Remove the head: SelectIdx should shift left to keep pointing at "c".
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("expected Dequeue to succeed")
	}
	if q.SelectIdx != 1 {
		t.Fatalf("SelectIdx = %d, want 1", q.SelectIdx)
	}
	cur, _ := q.At(q.SelectIdx)
	if cur.Content != "c" {
		t.Fatalf("selected content = %q, want %q", cur.Content, "c")
	}
}

func TestDeleteCurrentQueueItemRemovesSelected(t *testing.T) {
	m := New("", 10, nil, SelectorDeps{})
	m.Queue.Enqueue("first", nil)
	m.Queue.Enqueue("second", nil)
	m.Queue.SelectIdx = 0

	m.DeleteCurrentQueueItem()

	if m.Queue.Len() != 1 {
		t.Fatalf("queue len = %d, want 1", m.Queue.Len())
	}
	remaining, _ := m.Queue.At(0)
	if remaining.Content != "second" {
		t.Fatalf("remaining = %q, want %q", remaining.Content, "second")
	}
}

func TestSaveCurrentQueueEditWritesBack(t *testing.T) {
	m := New("", 10, nil, SelectorDeps{})
	m.Queue.Enqueue("original", nil)
	m.Queue.SelectIdx = 0
	m.Textarea.SetValue("edited")

	m.SaveCurrentQueueEdit()

	item, _ := m.Queue.At(0)
	if item.Content != "edited" {
		t.Fatalf("content = %q, want %q", item.Content, "edited")
	}
}
