package task

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// newRunningBashTask builds a started BashTask over a throwaway child process,
// mirroring newRunningAgentTask for the sibling type. Tests that need the run's
// own context — to assert on it, or to give it a deadline — build their own.
func newRunningBashTask(t *testing.T, id, command, description string) *BashTask {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, "echo", "test")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return NewBashTask(id, command, description, cmd, cancel)
}

func TestBashTask_Complete(t *testing.T) {
	task := newRunningBashTask(t, "test-id", "echo test", "Test task")

	// Complete the task
	task.Complete(0, nil)

	info := task.GetStatus()
	if info.Status != StatusCompleted {
		t.Errorf("expected status 'completed', got '%s'", info.Status)
	}
	if info.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", info.ExitCode)
	}
}

func TestBashTask_Failed(t *testing.T) {
	task := newRunningBashTask(t, "fail-id", "exit 1", "Failing task")

	// Complete with non-zero exit code
	task.Complete(1, nil)

	info := task.GetStatus()
	if info.Status != StatusFailed {
		t.Errorf("expected status 'failed', got '%s'", info.Status)
	}
	if info.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", info.ExitCode)
	}
}

func TestBashTask_MarkKilled(t *testing.T) {
	task := newRunningBashTask(t, "kill-id", "sleep 100", "Long task")

	task.markKilled()

	info := task.GetStatus()
	if info.Status != StatusKilled {
		t.Errorf("expected status 'killed', got '%s'", info.Status)
	}
}

func TestBashTask_AppendAndGetOutput(t *testing.T) {
	task := newRunningBashTask(t, "output-id", "echo test", "Output task")

	task.AppendOutput([]byte("line 1\n"))
	task.AppendOutput([]byte("line 2\n"))

	output := task.GetOutput()
	expected := "line 1\nline 2\n"
	if output != expected {
		t.Errorf("expected output '%s', got '%s'", expected, output)
	}
}

func TestBashTask_IsRunning(t *testing.T) {
	task := newRunningBashTask(t, "running-id", "echo test", "Running task")

	if !task.IsRunning() {
		t.Error("task should be running initially")
	}

	task.Complete(0, nil)

	if task.IsRunning() {
		t.Error("task should not be running after completion")
	}
}

func TestBashTask_WaitForCompletion(t *testing.T) {
	task := newRunningBashTask(t, "wait-id", "echo test", "Wait task")

	// Complete in background after short delay
	go func() {
		time.Sleep(200 * time.Millisecond)
		task.Complete(0, nil)
	}()

	completed := task.WaitForCompletion(time.Second)
	if !completed {
		t.Error("expected task to complete within timeout")
	}
}

func TestBashTask_WaitForCompletionTimeout(t *testing.T) {
	task := newRunningBashTask(t, "timeout-id", "sleep 100", "Timeout task")

	// Don't complete the task, let it timeout
	completed := task.WaitForCompletion(200 * time.Millisecond)
	if completed {
		t.Error("expected timeout, but task completed")
	}
}

func TestBashTask_GetStatus(t *testing.T) {
	task := newRunningBashTask(t, "status-id", "echo test", "Status task")
	task.AppendOutput([]byte("output\n"))

	info := task.GetStatus()

	if info.ID != "status-id" {
		t.Errorf("expected ID 'status-id', got '%s'", info.ID)
	}
	if info.Type != TaskTypeBash {
		t.Errorf("expected type 'bash', got '%s'", info.Type)
	}
	if info.Command != "echo test" {
		t.Errorf("expected command 'echo test', got '%s'", info.Command)
	}
	if info.Status != StatusRunning {
		t.Errorf("expected status Running, got '%s'", info.Status)
	}
	if info.Output != "output\n" {
		t.Errorf("expected output 'output\\n', got '%s'", info.Output)
	}
}

func TestBashTask_ConcurrentAccess(t *testing.T) {
	task := newRunningBashTask(t, "concurrent-id", "echo test", "Concurrent task")

	var wg sync.WaitGroup

	// Multiple goroutines reading and writing
	for range 10 {
		wg.Add(3)

		go func() {
			defer wg.Done()
			task.AppendOutput([]byte("data\n"))
		}()

		go func() {
			defer wg.Done()
			_ = task.GetOutput()
		}()

		go func() {
			defer wg.Done()
			_ = task.IsRunning()
		}()
	}

	wg.Wait()

	// Complete should not panic with concurrent access
	task.Complete(0, nil)
}

func TestBashTask_StatusRunning(t *testing.T) {
	task := newRunningBashTask(t, "running-status-id", "echo test", "Running status task")

	// Newly created task should be in Running state
	info := task.GetStatus()
	if info.Status != StatusRunning {
		t.Errorf("expected initial status %q, got %q", StatusRunning, info.Status)
	}

	// IsRunning should also confirm
	if !task.IsRunning() {
		t.Error("IsRunning() should be true for new task")
	}
}

func TestBashTask_AllStateTransitions(t *testing.T) {
	// Running -> Completed
	t.Run("Running to Completed", func(t *testing.T) {
		task := newRunningBashTask(t, "t1", "echo test", "test")
		if info := task.GetStatus(); info.Status != StatusRunning {
			t.Errorf("want %s, got %s", StatusRunning, info.Status)
		}
		task.Complete(0, nil)
		if info := task.GetStatus(); info.Status != StatusCompleted {
			t.Errorf("want %s, got %s", StatusCompleted, info.Status)
		}
	})

	// Running -> Failed (non-zero exit)
	t.Run("Running to Failed", func(t *testing.T) {
		task := newRunningBashTask(t, "t2", "exit 1", "test")
		task.Complete(2, nil)
		if info := task.GetStatus(); info.Status != StatusFailed {
			t.Errorf("want %s, got %s", StatusFailed, info.Status)
		}
	})

	// Running -> Killed
	t.Run("Running to Killed", func(t *testing.T) {
		task := newRunningBashTask(t, "t3", "sleep 100", "test")
		task.markKilled()
		if info := task.GetStatus(); info.Status != StatusKilled {
			t.Errorf("want %s, got %s", StatusKilled, info.Status)
		}
	})
}

func TestBashTask_ImplementsBackgroundTask(t *testing.T) {
	task := newRunningBashTask(t, "interface-id", "echo test", "Interface test")

	// Test that it implements BackgroundTask
	var bt BackgroundTask = task

	if bt.GetID() != "interface-id" {
		t.Errorf("GetID() = %s, want interface-id", bt.GetID())
	}
	if bt.GetType() != TaskTypeBash {
		t.Errorf("GetType() = %s, want bash", bt.GetType())
	}
	if bt.GetDescription() != "Interface test" {
		t.Errorf("GetDescription() = %s, want 'Interface test'", bt.GetDescription())
	}
}

// A stopped child dies of the signal Stop sent it, so cmd.Wait reports
// "signal: terminated". Recording that as failed would tell the main agent
// the work had broken rather than been called off, inviting an unwanted retry.
func TestBashTaskStopIsNotAFailure(t *testing.T) {
	task := newRunningBashTask(t, "stop-id", "sleep 100", "Long task")

	_ = task.Stop()
	task.Complete(signalExitCode(syscall.SIGTERM), errors.New("signal: terminated"))

	if info := task.GetStatus(); info.Status != StatusStopped {
		t.Errorf("expected status '%s', got '%s'", StatusStopped, info.Status)
	}
}

// Bash exposes the task's process-group ID, so the model can send this same
// SIGTERM directly from a later Bash invocation. The real *exec.ExitError
// from that death must classify as stopped, same as a Manager.Kill call.
func TestBashTaskDirectGracefulTerminationIsNotAFailure(t *testing.T) {
	task := newRunningBashTask(t, "direct-stop-id", "sleep 100", "Long task")

	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	err := cmd.Run()
	if err == nil {
		t.Fatal("helper was expected to die of SIGTERM")
	}

	task.Complete(signalExitCode(syscall.SIGTERM), err)

	if info := task.GetStatus(); info.Status != StatusStopped {
		t.Errorf("expected status '%s', got '%s'", StatusStopped, info.Status)
	}
}

// A run killed by its own timeout dies the same way, but nobody called it off,
// so it stays a failure. Only Stop exempts a task from that.
func TestBashTaskTimeoutRemainsAFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	cmd := exec.Command("echo", "test")
	cmd.Start()

	task := NewBashTask("timeout-id", "sleep 100", "Long task", cmd, cancel)

	<-ctx.Done()
	task.Complete(signalExitCode(syscall.SIGKILL), errors.New("signal: killed"))

	if info := task.GetStatus(); info.Status != StatusFailed {
		t.Errorf("expected status '%s', got '%s'", StatusFailed, info.Status)
	}
}

// Stop must not reach for cancellation — see BashTask.Stop for why that would
// defeat the graceful signal it just sent.
func TestBashTaskStopDoesNotCancelTheRun(t *testing.T) {
	cmd := exec.Command("echo", "test")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	cancelled := false
	task := NewBashTask("graceful-id", "sleep 100", "Long task", cmd, func() { cancelled = true })

	_ = task.Stop()

	if cancelled {
		t.Error("Stop cancelled the run; the SIGKILL that follows cancellation " +
			"would pre-empt the SIGTERM Stop just sent")
	}
}

// Kill delivers the group SIGKILL by cancelling the context: exec wires
// cmd.Cancel to signal the group, and os/exec runs it before reaping the child,
// so that path can never signal a PGID the kernel has reissued. The direct
// TerminateGroup is only a fallback for a task built without a cancel func.
func TestKillCancelsWhenACancelFuncIsPresent(t *testing.T) {
	cmd := exec.Command("echo", "test")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	cancelled := false
	task := NewBashTask("kill-id", "sleep 100", "Long task", cmd, func() { cancelled = true })

	_ = task.Kill()

	if !cancelled {
		t.Error("Kill did not cancel the run, which is what delivers the group SIGKILL")
	}
}

// Once cmd.Wait has reaped the child the raw PGID is no longer ours, so Stop
// refuses to signal it. This narrows the reuse window, it does not close it —
// see BashTask.MarkReaped.
func TestStopRefusedAfterReap(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	task := NewBashTask("reaped-id", "true", "done task", cmd, func() {})

	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	task.MarkReaped()

	if err := task.Stop(); err != nil {
		t.Errorf("Stop after the reap returned %v; it must be a no-op", err)
	}
}

// A background command that never stops talking used to have every byte it
// ever printed held in memory for the life of the process: BashTask skipped
// the cap AgentTask applied, and the manager never forgets a task.
func TestBashTaskOutputIsCapped(t *testing.T) {
	task := newRunningBashTask(t, "cap-id", "chatty", "Chatty task")

	for range 4 {
		task.AppendOutput(bytes.Repeat([]byte("x"), maxOutputBufferSize/2))
	}
	task.AppendOutput([]byte("TAIL"))

	output := task.GetOutput()
	if len(output) > maxOutputBufferSize {
		t.Fatalf("output buffer = %d bytes, want <= %d", len(output), maxOutputBufferSize)
	}
	// The tail is the part worth keeping — it is where a failure shows up.
	if !strings.HasSuffix(output, "TAIL") {
		t.Error("cap discarded the newest output instead of the oldest")
	}
}
