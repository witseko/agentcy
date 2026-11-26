package shim

import "testing"

func TestOnlyTheKnownModernVersionIsModern(t *testing.T) {
	cases := map[string]bool{
		"2026-07-28": true,
		"2025-11-25": false,
		"2024-11-05": false,
		"":           false,
		"invalid":    false,
		"2027-01-01": false,
		"2026-07-29": false,
	}
	for version, want := range cases {
		if got := isModernEra(version); got != want {
			t.Errorf("isModernEra(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestHTTPRoutesOnlyHandshakeVersionsToTheStatefulHandler(t *testing.T) {
	cases := map[string]bool{
		"":           false,
		"2024-11-05": false,
		"2025-03-26": false,
		"2025-06-18": false,
		"2025-11-25": false,
		"2026-07-28": true,
		"invalid":    true,
		"2027-01-01": true,
		"2025-01-01": true,
	}
	for version, want := range cases {
		if got := routesToModernHandler(version); got != want {
			t.Errorf("routesToModernHandler(%q) = %v, want %v", version, got, want)
		}
	}
}
