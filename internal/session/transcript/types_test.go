package transcript

import (
	"reflect"
	"testing"
	"time"

	"github.com/genai-io/san/internal/todo"
)

func TestTranscriptTypesCarryProjectedState(t *testing.T) {
	now := time.Date(2026, 4, 6, 12, 30, 0, 0, time.UTC)
	tr := Transcript{
		ID:        "tx-1",
		ParentID:  "tx-0",
		Cwd:       "/tmp/project",
		CreatedAt: now,
		UpdatedAt: now,
		Provider:  "openai",
		Model:     "gpt-test",
		Messages: []Node{{
			ID:        "msg-1",
			ParentID:  "msg-0",
			Role:      "assistant",
			Time:      now,
			GitBranch: "main",
			Content:   []ContentBlock{{Type: "text", Text: "hello"}},
		}},
		State: State{
			Title:      "Fix persistence",
			LastPrompt: "continue",
			Tasks: []TrackerItemView{{
				ID:      "1",
				Subject: "Refactor store",
				Status:  "in_progress",
			}},
		},
	}

	if tr.Messages[0].Role != "assistant" {
		t.Fatalf("unexpected role: %q", tr.Messages[0].Role)
	}
	if tr.State.Tasks[0].Subject != "Refactor store" {
		t.Fatalf("unexpected task subject: %q", tr.State.Tasks[0].Subject)
	}
}

func TestProjectedTypesDoNotExposeJSONTags(t *testing.T) {
	fields := []reflect.StructField{
		reflect.TypeFor[Transcript]().Field(0),
		reflect.TypeFor[Node]().Field(0),
		reflect.TypeFor[State]().Field(0),
		reflect.TypeFor[ListItem]().Field(0),
	}

	for _, field := range fields {
		if tag := field.Tag.Get("json"); tag != "" {
			t.Fatalf("%s unexpectedly has json tag %q", field.Name, tag)
		}
	}
}

func TestMetadataAndTaskViewHelpers(t *testing.T) {
	now := time.Date(2026, 4, 6, 12, 30, 0, 0, time.UTC)
	meta := MetadataFromTranscript(&Transcript{
		ID:        "tx-1",
		ParentID:  "parent-1",
		Cwd:       "/tmp/project",
		CreatedAt: now,
		UpdatedAt: now.Add(time.Minute),
		Provider:  "openai",
		Model:     "gpt-test",
		Messages:  []Node{{ID: "m1"}},
		State: State{
			Title:      "Title",
			LastPrompt: "continue",
			Tag:        "tag",
			Mode:       "plan",
		},
	})
	if meta.ID != "tx-1" || meta.ParentSessionID != "parent-1" || meta.MessageCount != 1 {
		t.Fatalf("unexpected metadata projection: %+v", meta)
	}

	itemMeta := MetadataFromListItem(ListItem{
		SessionID:    "tx-2",
		Title:        "Resume work",
		LastPrompt:   "continue",
		CreatedAt:    now,
		UpdatedAt:    now.Add(time.Minute),
		MessageCount: 3,
	}, "/tmp/from-list")
	if itemMeta.ID != "tx-2" || itemMeta.Cwd != "/tmp/from-list" || itemMeta.MessageCount != 3 {
		t.Fatalf("unexpected list metadata projection: %+v", itemMeta)
	}

	taskTime := now.Add(2 * time.Minute)
	tasks := []todo.Item{{
		ID:              "1",
		Subject:         "Refactor",
		Description:     "Move projection helpers",
		ActiveForm:      "Refactoring",
		Status:          todo.StatusInProgress,
		Owner:           "main",
		Blocks:          []string{"2"},
		BlockedBy:       []string{"3"},
		CreatedAt:       taskTime,
		UpdatedAt:       taskTime,
		StatusChangedAt: taskTime,
	}}
	views := TrackerItemViewsFromItems(tasks)
	roundTrip := TrackerItemsFromView(views)
	if !reflect.DeepEqual(roundTrip, tasks) {
		t.Fatalf("task roundtrip mismatch:\n got: %+v\nwant: %+v", roundTrip, tasks)
	}
}
