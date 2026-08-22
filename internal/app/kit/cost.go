package kit

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/genai-io/san/internal/llm"
)

func FormatMoney(m llm.Money) string {
	switch m.Currency {
	case llm.CurrencyCNY, llm.CurrencyUSD:
		return formatCurrencyAmount(CurrencySymbol(m.Currency), m.Amount)
	default:
		if m.Amount == 0 {
			return "0"
		}
		return fmt.Sprintf("%.3f %s", m.Amount, m.Currency)
	}
}

// CurrencySymbol returns the sign a currency is written with, falling back to
// the code itself for one San has no sign for.
func CurrencySymbol(currency llm.Currency) string {
	switch currency {
	case llm.CurrencyCNY:
		return "¥"
	case llm.CurrencyUSD, "":
		return "$"
	default:
		return string(currency) + " "
	}
}

// FormatModelRate renders a model's published input/output rate per million
// tokens, e.g. "$5/$25". Nil pricing renders empty: a model whose card San does
// not have is not a free one.
//
// Unlike FormatMoney, which renders spend and needs the small decimals, a rate
// is printed at the precision it actually carries — "$5", not "$5.000" — and to
// at least two places when it is fractional, because that is how money reads.
func FormatModelRate(p *llm.ModelPricing) string {
	if p == nil {
		return ""
	}
	symbol := CurrencySymbol(p.Currency)
	return symbol + formatRate(p.Input) + "/" + symbol + formatRate(p.Output)
}

func formatRate(rate float64) string {
	if rate == math.Trunc(rate) {
		return strconv.FormatFloat(rate, 'f', 0, 64)
	}
	exact := strconv.FormatFloat(rate, 'f', -1, 64)
	if decimals := len(exact) - strings.IndexByte(exact, '.') - 1; decimals < 2 {
		return strconv.FormatFloat(rate, 'f', 2, 64)
	}
	return exact
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
