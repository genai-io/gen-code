package kit

import "fmt"

// Plural renders a count with its noun, so a line about one thing does not
// read "1 messages". Regular nouns only — an irregular one wants its own
// helper, as pluralKind has for memory entries.
func Plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
