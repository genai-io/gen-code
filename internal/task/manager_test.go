package task

import (
	"context"
	"os/exec"
	"testing"
)

func TestManager_CreateAndGet(t *testing.T) {
	m := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "echo", "test")
	cmd.Start()

	task := m.CreateBashTask(cmd, "echo test", "Test task", cancel)

	if task.ID == "" {
		t.Error("task ID should not be empty")
	}

	retrieved, ok := m.Get(task.ID)
	if !ok {
		t.Error("should find created task")
	}
	if retrieved.GetID() != task.ID {
		t.Error("retrieved task should match created task")
	}
}

func TestManager_GetNotFound(t *testing.T) {
	m := NewManager()

	_, ok := m.Get("nonexistent")
	if ok {
		t.Error("should not find nonexistent task")
	}
}

func TestManager_List(t *testing.T) {
	m := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create multiple tasks
	for range 3 {
		cmd := exec.CommandContext(ctx, "echo", "test")
		cmd.Start()
		m.CreateBashTask(cmd, "echo test", "Test task", cancel)
	}

	tasks := m.List()
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
}

func TestManager_ListRunning(t *testing.T) {
	m := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create 3 tasks
	var tasks []*BashTask
	for range 3 {
		cmd := exec.CommandContext(ctx, "echo", "test")
		cmd.Start()
		task := m.CreateBashTask(cmd, "echo test", "Test task", cancel)
		tasks = append(tasks, task)
	}

	// Complete one task
	tasks[0].Complete(0, nil)

	running := m.ListRunning()
	if len(running) != 2 {
		t.Errorf("expected 2 running tasks, got %d", len(running))
	}
}

func TestManager_Remove(t *testing.T) {
	m := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "echo", "test")
	cmd.Start()

	task := m.CreateBashTask(cmd, "echo test", "Test task", cancel)
	taskID := task.ID

	m.Remove(taskID)

	_, ok := m.Get(taskID)
	if ok {
		t.Error("task should be removed")
	}
}

func TestManager_GenerateUniqueIDs(t *testing.T) {
	m := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ids := make(map[string]bool)

	for range 100 {
		cmd := exec.CommandContext(ctx, "echo", "test")
		cmd.Start()
		task := m.CreateBashTask(cmd, "echo test", "Test task", cancel)

		if ids[task.ID] {
			t.Errorf("duplicate ID generated: %s", task.ID)
		}
		ids[task.ID] = true
	}
}

func TestManager_RegisterTask(t *testing.T) {
	m := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "echo", "test")
	cmd.Start()

	// Create task manually and register
	task := NewBashTask("manual-id", "echo test", "Manual task", cmd, cancel)
	m.RegisterTask(task)

	retrieved, ok := m.Get("manual-id")
	if !ok {
		t.Error("should find registered task")
	}
	if retrieved.GetID() != "manual-id" {
		t.Error("retrieved task should match registered task")
	}
}

func TestManager_GetBashTask(t *testing.T) {
	m := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "echo", "test")
	cmd.Start()

	created := m.CreateBashTask(cmd, "echo test", "Test task", cancel)

	task, ok := m.getBashTask(created.ID)
	if !ok {
		t.Error("should find bash task")
	}
	if task.ID != created.ID {
		t.Error("retrieved task should match created task")
	}
}
