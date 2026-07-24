# Cost / Token Tracking

## Overview

Token usage is tracked per turn and accumulated across the session. Cost is calculated based on the active model's pricing.

- **Per turn:** input tokens, output tokens
- **Session total:** cumulative across all turns
- **Display:** status bar shows running totals
- **Pricing:** model-aware; updates when the model changes
- **Token limits:** `/tokenlimit <input> <output>` can persist a manual override

## UI Interactions

- **Status bar**: shows `in: N / out: N / $X.XX` after each turn.
- **`/tokenlimit`**: shows current usage and the model's context limit in a popup.
- **Auto-compact warning**: a notice appears when usage exceeds 80% of the limit.

## Automated Tests

```bash
go test ./internal/llm/... -v -run TestTokenUsage
go test ./internal/llm/... -v -run TestCostTracking
go test ./tests/integration/... -v -run TestLoop_TokenAccumulation
```

Covered:

```
# Token accumulation
TestLoop_TokenAccumulation              — token counts accumulate across turns in loop

# Max tokens resolution
TestResolveMaxTokens_CustomOverride     — custom max token override
TestResolveMaxTokens_FromProvider       — max tokens from provider config
TestResolveMaxTokens_Fallback           — fallback max tokens
TestOptsDefaultMaxTokens                — default max token opts
```

Cases to add:

```go
func TestCost_PerTurnAccumulation(t *testing.T) {
    // Token counts must accumulate correctly across multiple turns
}

func TestCost_SessionTotal_MatchesSumOfTurns(t *testing.T) {
    // Session total must equal the sum of all per-turn counts
}

func TestCost_AnthropicPricing_CalculatedCorrectly(t *testing.T) {
    // Cost in USD must reflect current Anthropic model pricing
}

func TestCost_ModelChange_UpdatesPricing(t *testing.T) {
    // Switching model must update pricing for subsequent turns
}

func TestCost_AutoCompactWarning_At80Percent(t *testing.T) {
    // Warning notice must appear when usage exceeds 80% of limit
}

func TestCost_StatusBarFormat(t *testing.T) {
    // Status bar must show "in: N / out: N / $X.XX" format
}

func TestCost_TokenLimitManualOverride_Persists(t *testing.T) {
    // Manual /tokenlimit overrides must be saved and used for future displays
}
```

## Interactive Tests (tmux)

```bash
tmux new-session -d -s t_cost -x 220 -y 60
tmux send-keys -t t_cost 'san' Enter
sleep 2

# Test 1: Send a message and inspect the status bar
tmux send-keys -t t_cost 'what is 2+2?' Enter
sleep 6
tmux capture-pane -t t_cost -p
# Expected: footer/status area updates with token usage after the turn

# Test 2: View token limit
tmux send-keys -t t_cost '/tokenlimit' Enter
sleep 2
tmux capture-pane -t t_cost -p
# Expected: current usage and context limit shown

# Test 3: Accumulate across turns
for i in {1..3}; do
  tmux send-keys -t t_cost "question $i: give me a short fact" Enter
  sleep 6
done
tmux capture-pane -t t_cost -p
# Expected: token usage in the footer/status area increases after each turn

# Test 4: Status bar format verification
tmux capture-pane -t t_cost -p | tail -3
# Expected: "in: N / out: N / $X.XX" visible with non-zero values

# Test 5: Cost display after model switch
tmux send-keys -t t_cost '/models' Enter
sleep 1
tmux send-keys -t t_cost Enter
sleep 1
tmux send-keys -t t_cost 'say hello' Enter
sleep 6
tmux capture-pane -t t_cost -p | tail -3
# Expected: cost updates reflect new model pricing

# Test 6: Manual token limit override
tmux send-keys -t t_cost '/tokenlimit 123456 4096' Enter
sleep 2
tmux capture-pane -t t_cost -p
# Expected: token limit display shows the custom override values

tmux kill-session -t t_cost
```
