package skill

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/genai-io/san/internal/skill"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/tool/toolresult"
)

const (
	IconSkill = "⚡"
)

// SkillTool allows the LLM to invoke skills programmatically
// It implements PermissionAwareTool to require user confirmation before loading skills
type SkillTool struct{}

func (t *SkillTool) Name() string { return "Skill" }

func (t *SkillTool) Description() string {
	return "Execute a skill within the main conversation"
}

func (t *SkillTool) Icon() string { return IconSkill }

// RequiresPermission returns true - skills require user confirmation
func (t *SkillTool) RequiresPermission() bool {
	return true
}

// PreparePermission prepares a permission request with skill metadata
func (t *SkillTool) PreparePermission(ctx context.Context, params map[string]any, cwd string) (*perm.PermissionRequest, error) {
	skillName, err := tool.RequireString(params, "skill")
	if err != nil {
		return nil, fmt.Errorf("skill parameter is required")
	}

	args := tool.GetString(params, "args")

	// Find skill in registry
	if skill.DefaultIfInit() == nil {
		return nil, fmt.Errorf("skill registry not initialized")
	}

	sk, ok := skill.Default().Get(skillName)
	if !ok {
		// Try to find by partial match
		sk = skill.Default().FindByPartialName(skillName)
		if sk == nil {
			return nil, fmt.Errorf("skill not found: %s", skillName)
		}
	}

	// Check if skill is enabled
	if !sk.IsEnabled() {
		return nil, fmt.Errorf("skill is disabled: %s", sk.FullName())
	}

	// Build description
	desc := fmt.Sprintf("Load skill: %s", sk.FullName())
	if args != "" {
		desc = fmt.Sprintf("Load skill: %s with args: %s", sk.FullName(), args)
	}

	return &perm.PermissionRequest{
		ID:          tool.GenerateRequestID(),
		ToolName:    t.Name(),
		Description: desc,
		SkillMeta: &perm.SkillMetadata{
			SkillName:   sk.FullName(),
			Description: sk.Description,
			Args:        args,
			ScriptCount: len(sk.Scripts),
			RefCount:    len(sk.References),
			Scripts:     sk.Scripts,
			References:  sk.References,
		},
	}, nil
}

// ExecuteApproved executes the skill after user approval
func (t *SkillTool) ExecuteApproved(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.execute(ctx, params, cwd)
}

// Execute runs the tool (for direct execution when permission is pre-approved)
func (t *SkillTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.execute(ctx, params, cwd)
}

// execute is the internal implementation
func (t *SkillTool) execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()

	skillName := tool.GetString(params, "skill")
	if skillName == "" {
		return toolresult.NewErrorResult(t.Name(), "skill parameter is required")
	}

	args := tool.GetString(params, "args")

	// Find skill in registry
	if skill.DefaultIfInit() == nil {
		return toolresult.NewErrorResult(t.Name(), "skill registry not initialized")
	}

	sk, ok := skill.Default().Get(skillName)
	if !ok {
		// Try to find by partial match
		sk = skill.Default().FindByPartialName(skillName)
		if sk == nil {
			return toolresult.NewErrorResult(t.Name(), fmt.Sprintf("skill not found: %s", skillName))
		}
	}

	// Check if skill is enabled
	if !sk.IsEnabled() {
		return toolresult.NewErrorResult(t.Name(), fmt.Sprintf("skill is disabled: %s", sk.FullName()))
	}

	if alreadyInlined(ctx, sk.FullName()) {
		duration := time.Since(start)
		return toolresult.ToolResult{
			Success: true,
			Output: fmt.Sprintf("<skill-already-loaded name=%q>\nThis skill's body is already inlined in the current user message under <skill-invocation name=%q>. Follow those instructions directly.\n</skill-already-loaded>",
				sk.FullName(), sk.FullName()),
			Metadata: toolresult.ResultMetadata{
				Title:    t.Name(),
				Icon:     t.Icon(),
				Subtitle: sk.FullName() + " (already inlined)",
				Duration: duration,
			},
			SkillInfo: &toolresult.SkillResultInfo{SkillName: sk.FullName()},
		}
	}

	// Load full instructions
	instructions := sk.GetInstructions()
	if instructions == "" {
		return toolresult.NewErrorResult(t.Name(), fmt.Sprintf("skill has no instructions: %s", sk.FullName()))
	}

	// Build skill invocation context
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<skill-invocation name=\"%s\">\n", sk.FullName()))

	// Include arguments if provided
	if args != "" {
		sb.WriteString(fmt.Sprintf("User arguments: %s\n\n", args))
	}

	// Include skill directory info for scripts/references/assets
	if sk.SkillDir != "" {
		if len(sk.Scripts) > 0 {
			sb.WriteString("Available scripts (use Bash to execute):\n")
			for _, script := range sk.Scripts {
				sb.WriteString(fmt.Sprintf("  - %s/scripts/%s\n", sk.SkillDir, script))
			}
			sb.WriteString("\n")
		}
		if len(sk.References) > 0 {
			sb.WriteString("Reference files (use Read when needed):\n")
			for _, ref := range sk.References {
				sb.WriteString(fmt.Sprintf("  - %s/references/%s\n", sk.SkillDir, ref))
			}
			sb.WriteString("\n")
		}
	}

	// Add instructions
	sb.WriteString(instructions)
	sb.WriteString("\n</skill-invocation>")

	duration := time.Since(start)

	// Count resources for display
	scriptCount := len(sk.Scripts)
	refCount := len(sk.References)

	return toolresult.ToolResult{
		Success: true,
		Output:  sb.String(),
		Metadata: toolresult.ResultMetadata{
			Title:     t.Name(),
			Icon:      t.Icon(),
			Subtitle:  sk.FullName(),
			Duration:  duration,
			ItemCount: scriptCount + refCount,
		},
		// Store skill-specific info for custom rendering
		SkillInfo: &toolresult.SkillResultInfo{
			SkillName:   sk.FullName(),
			ScriptCount: scriptCount,
			RefCount:    refCount,
		},
	}
}

// alreadyInlined reports whether the most recent user message starts with
// the <command-name> tag for this skill — meaning the slash-command path
// already inlined the body and the tool call is redundant. Anchored at the
// start so a quoted/pasted occurrence in later prose can't false-trigger.
func alreadyInlined(ctx context.Context, fullName string) bool {
	getter := tool.GetMessagesGetter(ctx)
	if getter == nil {
		return false
	}
	msgs := getter()
	tag := "<command-name>" + fullName + "</command-name>"
	for _, m := range slices.Backward(msgs) {

		// Want the most recent user-typed turn; a tool result is also RoleUser
		// (it carries a ToolResult), so skip those to avoid stopping on one.
		if m.Role != ai.RoleUser || m.ToolResult != nil {
			continue
		}
		return strings.HasPrefix(strings.TrimSpace(m.Content), tag)
	}
	return false
}

func init() {
	tool.Register(&SkillTool{})
}
