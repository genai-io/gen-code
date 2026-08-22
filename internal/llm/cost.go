// What a turn costs, and who can say.
//
// Two things live here because they are one subject: an amount of money and
// the per-vendor pricing that produces one. Currencies are kept apart rather
// than summed — there is no exchange rate here, and adding across them would
// invent a number.
package llm

import (
	"slices"
	"strings"
)

// CostEstimator computes the money cost of a single inference from its token
// usage, returning (_, false) when the model's pricing is unknown. The vendor
// table registers one per provider, so callers price a turn through
// EstimateCost without knowing which vendor served it.
type CostEstimator func(modelID string, usage Usage) (Money, bool)

// RegisterCostEstimator records a provider's pricing.
func RegisterCostEstimator(provider ProviderID, estimate CostEstimator) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.costs[provider] = estimate
}

// EstimateCost prices one turn, reporting ok=false when the provider has no
// registered pricing or the model has no published rate — which San renders as
// "--" rather than as free.
func EstimateCost(provider ProviderID, modelID string, usage Usage) (Money, bool) {
	globalRegistry.mu.RLock()
	estimate, ok := globalRegistry.costs[provider]
	globalRegistry.mu.RUnlock()

	if !ok {
		return Money{}, false
	}
	return estimate(modelID, usage)
}

type Currency string

const (
	CurrencyCNY Currency = "CNY"
	CurrencyUSD Currency = "USD"
)

// ModelPricing is a model's published rate per million tokens: the vendor's
// list card, not an invoice. A vendor that discounts by time of day or by
// region says so in its own note, and a turn's actual cost comes from
// EstimateCost.
//
// It is carried as numbers rather than as a rendered string because the picker
// aligns rates into a column, and because a string baked into the cached model
// listing would outlive the card it came from.
type ModelPricing struct {
	Currency Currency `json:"currency,omitempty"`
	Input    float64  `json:"input,omitempty"`
	Output   float64  `json:"output,omitempty"`
}

// Money is one amount in one currency — what a single provider charges for a
// single call. Amounts are not summed across currencies; see CostTotal.
type Money struct {
	Amount   float64
	Currency Currency
}

func (m Money) IsZero() bool {
	return m.Amount == 0 || m.Currency == ""
}

// CostTotal accumulates spend that may span currencies. A session can switch
// providers, and providers do not agree on one: MiniMax prices in CNY while
// DeepSeek, MiMo and Ollama price in USD. There is no exchange rate here, so
// adding across them would invent a number — each currency is kept on its own
// and rendered side by side.
//
// The zero value is an empty total. Add returns a new CostTotal rather than
// mutating, so the value can be copied freely; env is passed around by value.
type CostTotal struct {
	// entries holds at most one Money per currency, sorted by currency code so
	// the status bar renders them in a stable order rather than reshuffling as
	// the map iteration order changes.
	entries []Money
}

// NewCostTotal starts a total from a single amount.
func NewCostTotal(m Money) CostTotal {
	return CostTotal{}.Add(m)
}

// Add folds m into the total, keeping it on its own currency's running sum.
func (c CostTotal) Add(m Money) CostTotal {
	if m.IsZero() {
		return c
	}
	entries := slices.Clone(c.entries)
	if i := slices.IndexFunc(entries, func(e Money) bool { return e.Currency == m.Currency }); i >= 0 {
		entries[i].Amount += m.Amount
		return CostTotal{entries: entries}
	}
	entries = append(entries, m)
	slices.SortFunc(entries, func(a, b Money) int {
		return strings.Compare(string(a.Currency), string(b.Currency))
	})
	return CostTotal{entries: entries}
}

// IsZero reports whether nothing has been spent.
func (c CostTotal) IsZero() bool {
	return len(c.entries) == 0
}

// Amounts returns the per-currency totals, ordered by currency code.
func (c CostTotal) Amounts() []Money {
	return slices.Clone(c.entries)
}
