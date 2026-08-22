package kit

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/llm"
)

func FormatMoney(m llm.Money) string {
	switch m.Currency {
	case llm.CurrencyCNY:
		return formatCurrencyAmount("¥", m.Amount)
	case llm.CurrencyUSD:
		return formatCurrencyAmount("$", m.Amount)
	default:
		if m.Amount == 0 {
			return "0"
		}
		return fmt.Sprintf("%.3f %s", m.Amount, m.Currency)
	}
}

func formatCurrencyAmount(symbol string, amount float64) string {
	switch {
	case amount <= 0:
		return symbol + "0"
	case amount < 0.0001:
		return fmt.Sprintf("%s%.6f", symbol, amount)
	case amount < 0.01:
		return fmt.Sprintf("%s%.4f", symbol, amount)
	default:
		return fmt.Sprintf("%s%.3f", symbol, amount)
	}
}

// FormatCostTotal renders a session total that may span currencies. Providers
// do not agree on one and there is no rate to convert with, so each currency is
// shown on its own rather than folded into a single misleading figure.
func FormatCostTotal(total llm.CostTotal) string {
	amounts := total.Amounts()
	if len(amounts) == 0 {
		return "0"
	}
	parts := make([]string, 0, len(amounts))
	for _, m := range amounts {
		parts = append(parts, FormatMoney(m))
	}
	return strings.Join(parts, " + ")
}
