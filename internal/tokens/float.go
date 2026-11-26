package tokens

import (
	"strconv"

	"github.com/witseko/agentcy/internal/wire"
)

// FloatValue returns a JSON number formatted like Python's float repr.
func FloatValue(number float64) wire.Value {
	return wire.FloatValue(number)
}

// FormatFloat spells a float like Python's repr.
func FormatFloat(number float64) string {
	return wire.FormatFloat(number)
}

// roundOneDecimal rounds like Python's round(number, 1): to the nearest float of the
// correctly rounded one-decimal text, ties to even.
func roundOneDecimal(number float64) float64 {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(number, 'f', 1, 64), 64)
	return rounded
}

// formatOneDecimal spells a float like Python's f"{number:.1f}".
func formatOneDecimal(number float64) string {
	return strconv.FormatFloat(number, 'f', 1, 64)
}
