package valueobject

import "strconv"

// FormatScaledTokens abbreviates token counts for budgets and UI (~200K/1M).
func FormatScaledTokens(n int) string {
	if n <= 0 {
		return "0"
	}
	const (
		thousand = 1_000
		million  = 1_000_000
	)
	if n >= million {
		if n%million == 0 {
			return strconv.Itoa(n/million) + "M"
		}
		whole := n / million
		frac := (n % million) * 10 / million
		if frac == 0 {
			return strconv.Itoa(whole) + "M"
		}
		return strconv.Itoa(whole) + "." + strconv.Itoa(frac) + "M"
	}
	if n >= thousand {
		if n%thousand == 0 {
			return strconv.Itoa(n/thousand) + "K"
		}
		return strconv.Itoa((n+thousand/2)/thousand) + "K"
	}
	return strconv.Itoa(n)
}
