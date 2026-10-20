// Package money enforces the shared NUMERIC(12,2) storage range in cents.
package money

import "math"

const MaxCents int64 = 999_999_999_999

func Cents(price float64) (int64, bool) {
	cents := math.Round(price * 100)
	if price < 0 || math.IsNaN(cents) || math.IsInf(cents, 0) || cents > float64(MaxCents) {
		return 0, false
	}
	return int64(cents), true
}
