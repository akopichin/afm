package accounting

type Coverage string

const (
	CoverageFull    Coverage = "full"
	CoveragePartial Coverage = "partial"
	CoverageNone    Coverage = "none"
)

func coverageOf(s Summary) Coverage {
	priced := s.Metered - s.Unpriced
	gaps := s.Unpriced + s.Unmetered
	switch {
	case priced >= 1 && gaps == 0:
		return CoverageFull
	case priced >= 1:
		return CoveragePartial
	default:
		return CoverageNone
	}
}

// DisplayCost is the one cost string every surface renders. Full/partial reuse
// FormatUSD's tiny-value handling for the priced sum; partial appends "+"; none
// is the em dash (dashUnknown). Never gates on the money value.
func DisplayCost(s Summary) string {
	switch coverageOf(s) {
	case CoverageFull:
		return FormatUSD(s.CostUSD, true)
	case CoveragePartial:
		return FormatUSD(s.CostUSD, true) + "+"
	default:
		return dashUnknown
	}
}
