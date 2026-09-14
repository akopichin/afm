package accounting

import "fmt"

// dashUnknown is the display string for an unknown/unpriced/absent value (never "$0.00").
const dashUnknown = "—"

// Rate is a list price in USD per 1,000,000 tokens.
type Rate float64

// costUSD returns the list-price cost of `tok` tokens at rate `r`.
func costUSD(tok uint64, r Rate) float64 {
	return float64(tok) / 1_000_000 * float64(r)
}

// FormatUSD renders an estimated cost for display. When !priced the value is
// unknown and rendered as an em dash (never $0.00). A tiny non-zero priced value
// renders as "<$0.0001" so it is not mistaken for free.
func FormatUSD(v float64, priced bool) string {
	if !priced {
		return dashUnknown
	}
	if v > 0 && v < 0.0001 {
		return "<$0.0001"
	}
	return fmt.Sprintf("$%.4f", v)
}
