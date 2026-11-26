package tokens

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/paths"
)

// TestFormatFloatMatchesRecordedPython compares with repr output recorded from Python 3.
func TestFormatFloatMatchesRecordedPython(t *testing.T) {
	cases := []struct {
		number float64
		want   string
	}{
		{75, "75.0"}, {0.3, "0.3"}, {99.7, "99.7"}, {0, "0.0"}, {math.Copysign(0, -1), "-0.0"},
		{100, "100.0"}, {-2.5, "-2.5"}, {0.30000000000000004, "0.30000000000000004"},
		{1e16, "1e+16"}, {1.5e16, "1.5e+16"}, {9999999999999998, "9999999999999998.0"},
		{1e15, "1000000000000000.0"}, {123456789012345680, "1.2345678901234568e+17"},
		{0.0001, "0.0001"}, {0.00001, "1e-05"}, {0.000123, "0.000123"}, {0.0000123, "1.23e-05"},
		{1e-100, "1e-100"}, {1.7976931348623157e308, "1.7976931348623157e+308"},
		{5e-324, "5e-324"}, {math.NaN(), "0.0"}, {math.Inf(1), "0.0"}, {math.Inf(-1), "0.0"},
	}
	for _, test := range cases {
		if got := FormatFloat(test.number); got != test.want {
			t.Errorf("FormatFloat(%v) = %q, want %q", test.number, got, test.want)
		}
	}
	if got := FloatValue(75).Compact(); got != "75.0" {
		t.Errorf("FloatValue(75) encodes as %s", got)
	}
}

// TestRoundingMatchesRecordedPython compares with round(x, 1) and f"{x:.1f}" recorded
// from Python 3, including halves that are not exact in binary.
func TestRoundingMatchesRecordedPython(t *testing.T) {
	cases := []struct {
		number  float64
		rounded string
		text    string
	}{
		{0.25, "0.2", "0.2"}, {0.35, "0.3", "0.3"}, {2.675, "2.7", "2.7"}, {0.05, "0.1", "0.1"},
		{0.15, "0.1", "0.1"}, {1.45, "1.4", "1.4"}, {33.333333333333336, "33.3", "33.3"},
		{66.66666666666667, "66.7", "66.7"}, {-0.04, "-0.0", "-0.0"}, {99.95, "100.0", "100.0"},
	}
	for _, test := range cases {
		if got := FormatFloat(roundOneDecimal(test.number)); got != test.rounded {
			t.Errorf("round(%v, 1) = %s, want %s", test.number, got, test.rounded)
		}
		if got := formatOneDecimal(test.number); got != test.text {
			t.Errorf("format(%v, '.1f') = %s, want %s", test.number, got, test.text)
		}
	}
}

// pythonFloats prints, for each float given as 16 hex digits of its bits, its repr,
// the repr of round(x, 1) and f"{x:.1f}".
const pythonFloats = `
import struct, sys
for line in sys.stdin:
    x = struct.unpack(">d", bytes.fromhex(line.strip()))[0]
    print(repr(x), repr(round(x, 1)), f"{x:.1f}")
`

// TestFloatsMatchPython compares random floats of every magnitude with Python through
// uv, and skips when uv is unavailable.
func TestFloatsMatchPython(t *testing.T) {
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := paths.FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	generator := rand.New(rand.NewPCG(3, 5))
	numbers := []float64{}
	for range 3000 {
		numbers = append(numbers, math.Float64frombits(generator.Uint64()))
		numbers = append(numbers, float64(generator.IntN(2000001))/10)
		numbers = append(numbers, 100*float64(generator.IntN(1000000))/float64(1+generator.IntN(1000000)))
	}
	var input bytes.Buffer
	kept := numbers[:0]
	for _, number := range numbers {
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Abs(number) > 1e300 {
			continue
		}
		kept = append(kept, number)
		fmt.Fprintf(&input, "%016x\n", math.Float64bits(number))
	}
	repository, _ := filepath.Abs("../..")
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-I", "-c", pythonFloats)
	command.Env = os.Environ()
	command.Stdin = &input
	output, err := command.Output()
	if err != nil {
		t.Skipf("Python unavailable: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != len(kept) {
		t.Fatalf("Python printed %d lines for %d floats", len(lines), len(kept))
	}
	for index, number := range kept {
		got := FormatFloat(number) + " " + FormatFloat(roundOneDecimal(number)) + " " + formatOneDecimal(number)
		if got != lines[index] {
			t.Errorf("%v: Go %q, Python %q", number, got, lines[index])
		}
	}
}
