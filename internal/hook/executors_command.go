package hook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/setting"
)

func (e *Engine) executeCommand(ctx context.Context, hookCmd setting.HookCmd, input HookInput) HookOutcome {
	outcome := HookOutcome{ShouldContinue: true}
	if hookCmd.Command == "" {
		return outcome
	}

	timeout := defaultTimeout
	if hookCmd.Timeout > 0 {
		timeout = hookCmd.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		outcome.Error = fmt.Errorf("failed to marshal input: %w", err)
		return outcome
	}

	cwd := e.getCwd()
	cmd := buildShellCommand(ctx, hookCmd, cwd)
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Env = e.buildEnv(ctx, input)
	proc.SetProcessGroup(cmd)
	cmd.Cancel = func() error {
		_ = proc.TerminateGroup(cmd, syscall.SIGKILL)
		return nil
	}
	// Backstop: if a grandchild keeps the stdout/stderr pipe open after the
	// shell is killed (common on Windows where we can't group-kill), give Wait
	// a bounded time to drain before exec force-closes the pipes.
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := getExitCode(runErr)
	if exitCode < 0 {
		outcome.Error = runErr
		return outcome
	}
	if exitCode == 2 {
		return handleBlockingExit(&stderr)
	}
	if exitCode != 0 {
		// Report it. Leaving Error nil recorded the run as "ran" and dropped
		// stderr on the floor, so a hook that never worked — a typo, a missing
		// interpreter, exit 127 — looked identical to one that succeeded. The
		// user got no log line, no transcript record and no notice.
		//
		// ShouldContinue stays true: a non-zero exit that is not 2 is a failed
		// hook, not a blocking one, so the turn carries on.
		outcome.Error = commandFailure(exitCode, &stderr)
		return outcome
	}
	return e.parseOutput(strings.TrimSpace(stdout.String()), outcome)
}

// commandFailure describes a hook that exited non-zero, carrying the first
// line of its stderr — which is where the reason lives ("jq: command not
// found") and was previously discarded.
func commandFailure(exitCode int, stderr *bytes.Buffer) error {
	reason := strings.TrimSpace(stderr.String())
	if i := strings.IndexByte(reason, '\n'); i >= 0 {
		reason = reason[:i]
	}
	if reason == "" {
		return fmt.Errorf("hook exited %d", exitCode)
	}
	return fmt.Errorf("hook exited %d: %s", exitCode, reason)
}

func (e *Engine) executeCommandBidirectional(ctx context.Context, hookCmd setting.HookCmd, input HookInput) HookOutcome {
	outcome := HookOutcome{ShouldContinue: true}
	if hookCmd.Command == "" {
		return outcome
	}

	timeout := defaultTimeout
	if hookCmd.Timeout > 0 {
		timeout = hookCmd.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	detached := false
	defer func() {
		if !detached {
			cancel()
		}
	}()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		outcome.Error = fmt.Errorf("failed to marshal input: %w", err)
		return outcome
	}

	cwd := e.getCwd()
	cmd := buildShellCommand(ctx, hookCmd, cwd)
	cmd.Env = e.buildEnv(ctx, input)
	proc.SetProcessGroup(cmd)
	cmd.Cancel = func() error {
		_ = proc.TerminateGroup(cmd, syscall.SIGKILL)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		outcome.Error = fmt.Errorf("failed to create stdin pipe: %w", err)
		return outcome
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		outcome.Error = fmt.Errorf("failed to create stdout pipe: %w", err)
		return outcome
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		outcome.Error = fmt.Errorf("failed to start hook: %w", err)
		return outcome
	}
	if _, err := io.WriteString(stdinPipe, string(inputJSON)+"\n"); err != nil {
		outcome.Error = fmt.Errorf("failed to write to stdin: %w", err)
		_ = cmd.Wait()
		return outcome
	}

	// Auto-close stdin if the hook doesn't produce output quickly.
	// Hooks using `cat` (reads until EOF) will deadlock without this.
	// Interactive hooks (prompt-response) produce output before needing
	// more stdin, so the timer is cancelled in time.
	stdinTimer := time.AfterFunc(e.stdinIdleOrDefault(), func() {
		stdinPipe.Close()
	})
	defer stdinTimer.Stop()

	scanner := bufio.NewScanner(stdoutPipe)
	var finalOutput string
	firstLine := true
	promptCallback := e.getPromptCallback()

	for scanner.Scan() {
		stdinTimer.Stop()
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if firstLine {
			firstLine = false
			var async asyncFirstLine
			if json.Unmarshal([]byte(line), &async) == nil && async.Async {
				detached = true
				go func() {
					defer cancel()
					_ = cmd.Wait()
				}()
				return outcome
			}
		}

		var promptReq PromptRequest
		if err := json.Unmarshal([]byte(line), &promptReq); err == nil && promptReq.Prompt != "" && promptReq.Message != "" {
			if promptCallback == nil {
				continue
			}
			resp, cancelled := promptCallback(promptReq)
			if cancelled {
				_ = stdinPipe.Close()
				_ = cmd.Wait()
				return outcome
			}
			respJSON, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			if _, err := io.WriteString(stdinPipe, string(respJSON)+"\n"); err != nil {
				break
			}
			continue
		}
		finalOutput = line
	}
	exitCode := getExitCode(cmd.Wait())
	if exitCode == 2 {
		return handleBlockingExit(&stderr)
	}
	if exitCode != 0 && exitCode >= 0 {
		return outcome
	}
	return e.parseOutput(finalOutput, outcome)
}

func buildShellCommand(ctx context.Context, hookCmd setting.HookCmd, cwd string) *exec.Cmd {
	switch strings.ToLower(strings.TrimSpace(hookCmd.Shell)) {
	case "powershell", "pwsh":
		cmd := exec.CommandContext(ctx, "pwsh", "-NoProfile", "-Command", hookCmd.Command)
		cmd.Dir = cwd
		return cmd
	default:
		cmd := exec.CommandContext(ctx, "sh", "-c", hookCmd.Command)
		cmd.Dir = cwd
		return cmd
	}
}

func handleBlockingExit(stderr *bytes.Buffer) HookOutcome {
	reason := strings.TrimSpace(stderr.String())
	if reason == "" {
		reason = "Hook blocked execution"
	}
	return HookOutcome{
		ShouldContinue: false,
		ShouldBlock:    true,
		BlockReason:    reason,
	}
}
