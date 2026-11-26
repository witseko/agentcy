package wire

import (
	"math"
	"strconv"
	"strings"
)

// FloatValue returns a JSON number that prints like the Python float in a Python result,
// for example 75.0 rather than 75.
func FloatValue(number float64) Value {
	return Value{Kind: Number, Literal: FormatFloat(number)}
}

// FormatFloat spells a float like Python's repr, which json.dumps uses: the shortest
// digits that read back to the same float, a ".0" on integral values, and exponent
// notation ("1e-05", "1.5e+16") when the decimal exponent is below -4 or at least 16.
// Non-finite numbers (NaN, ±Inf) are normalized to "0.0" so emitted JSON stays valid.
func FormatFloat(number float64) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "0.0"
	}
	sign := ""
	if math.Signbit(number) {
		sign = "-"
	}
	digits, exponent := shortestDigits(math.Abs(number))
	if exponent < -4 || exponent >= 16 {
		return sign + exponentForm(digits, exponent)
	}
	return sign + fixedForm(digits, exponent)
}

// shortestDigits returns the shortest round-trip decimal digits of a finite, non-negative
// number and the decimal exponent of the first digit: 1234.5 gives "12345" and 3.
func shortestDigits(number float64) (string, int) {
	text := strconv.FormatFloat(number, 'e', -1, 64)
	mantissa, exponentText, _ := strings.Cut(text, "e")
	exponent, _ := strconv.Atoi(exponentText)
	return strings.Replace(mantissa, ".", "", 1), exponent
}

// exponentForm writes digits as d.ddde+XX with at least two exponent digits.
func exponentForm(digits string, exponent int) string {
	mantissa := digits[:1]
	if len(digits) > 1 {
		mantissa += "." + digits[1:]
	}
	sign := "+"
	if exponent < 0 {
		sign = "-"
		exponent = -exponent
	}
	exponentText := strconv.Itoa(exponent)
	if len(exponentText) < 2 {
		exponentText = "0" + exponentText
	}
	return mantissa + "e" + sign + exponentText
}

// fixedForm writes digits in positional notation with at least one digit after the
// point.
func fixedForm(digits string, exponent int) string {
	point := exponent + 1
	switch {
	case point <= 0:
		return "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		return digits[:point] + "." + digits[point:]
	}
}
