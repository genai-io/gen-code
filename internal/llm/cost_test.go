package llm

import (
	"slices"
	"testing"
)

func TestEstimateCostDispatchesToRegisteredProvider(t *testing.T) {
	const fake ProviderID = "test-cost-provider"
	want := Money{Amount: 1.5, Currency: CurrencyUSD}
	RegisterCostEstimator(fake, func(modelID string, usage Usage) (Money, bool) {
		if modelID != "m1" {
			t.Fatalf("modelID = %q, want m1", modelID)
		}
		if usage.InputTokens != 10 {
			t.Fatalf("usage.InputTokens = %d, want 10", usage.InputTokens)
		}
		return want, true
	})

	got, ok := EstimateCost(fake, "m1", Usage{InputTokens: 10})
	if !ok || got != want {
		t.Fatalf("EstimateCost = (%+v, %v), want (%+v, true)", got, ok, want)
	}
}

func TestEstimateCostUnknownProviderReturnsFalse(t *testing.T) {
	cost, ok := EstimateCost("totally-unregistered-provider", "m", Usage{})
	if ok || !cost.IsZero() {
		t.Fatalf("EstimateCost(unknown) = (%+v, %v), want (zero, false)", cost, ok)
	}
}

// A session can switch providers, and ConversationCost survives the switch —
// only /clear and /new reset it. MiniMax prices in CNY while DeepSeek, MiMo and
// Ollama price in USD, so the accumulator has to hold both. Summing them would
// invent a number; the previous Money.Add panicked instead, taking the TUI with
// it since nothing recovers on that path.
func TestCostTotalKeepsCurrenciesApart(t *testing.T) {
	total := NewCostTotal(Money{Amount: 1.20, Currency: CurrencyCNY}) // MiniMax
	total = total.Add(Money{Amount: 0.30, Currency: CurrencyUSD})     // switch to DeepSeek
	total = total.Add(Money{Amount: 0.80, Currency: CurrencyCNY})     // and back

	want := []Money{
		{Amount: 2.00, Currency: CurrencyCNY},
		{Amount: 0.30, Currency: CurrencyUSD},
	}
	got := total.Amounts()
	if len(got) != len(want) {
		t.Fatalf("Amounts() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Currency != want[i].Currency || !almostEqual(got[i].Amount, want[i].Amount) {
			t.Errorf("Amounts()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// Ordering is by currency code so the status bar does not reshuffle between
// renders as amounts arrive in different orders.
func TestCostTotalOrderIsStableRegardlessOfArrival(t *testing.T) {
	usdFirst := NewCostTotal(Money{Amount: 1, Currency: CurrencyUSD}).
		Add(Money{Amount: 1, Currency: CurrencyCNY})
	cnyFirst := NewCostTotal(Money{Amount: 1, Currency: CurrencyCNY}).
		Add(Money{Amount: 1, Currency: CurrencyUSD})

	if !slices.Equal(usdFirst.Amounts(), cnyFirst.Amounts()) {
		t.Errorf("order depends on arrival: %v vs %v", usdFirst.Amounts(), cnyFirst.Amounts())
	}
}

// Add returns a new value; a copy taken earlier must not move.
func TestCostTotalAddDoesNotMutateTheReceiver(t *testing.T) {
	base := NewCostTotal(Money{Amount: 1, Currency: CurrencyUSD})
	_ = base.Add(Money{Amount: 5, Currency: CurrencyUSD})

	if got := base.Amounts(); len(got) != 1 || !almostEqual(got[0].Amount, 1) {
		t.Errorf("receiver changed: %v", got)
	}
}

func TestCostTotalZeroValueAndZeroAmounts(t *testing.T) {
	var total CostTotal
	if !total.IsZero() {
		t.Error("the zero value should report IsZero")
	}
	// A provider with no registered pricing returns Money{}; it must not create
	// an empty-currency entry.
	if total = total.Add(Money{}); !total.IsZero() {
		t.Errorf("a zero Money should not register: %v", total.Amounts())
	}
}

func almostEqual(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}
