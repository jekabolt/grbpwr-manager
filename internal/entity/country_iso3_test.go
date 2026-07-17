package entity

import "testing"

func TestResolveCountryISO3(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"DE", "DEU", true},      // alpha-2
		{"de", "DEU", true},      // case-insensitive
		{"USA", "USA", true},     // already alpha-3
		{"gb", "GBR", true},      // alpha-2
		{"Germany", "DEU", true}, // name
		{"United States", "USA", true},
		{"  France  ", "FRA", true}, // trimmed name
		{"JP", "JPN", true},
		{"", "", false},
		{"Zzz", "", false}, // unresolvable
		{"XX", "", false},  // unknown alpha-2
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ResolveCountryISO3(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ResolveCountryISO3(%q) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestResolveSeededCountryISO2(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Germany", "DE", true},
		{"de", "DE", true},
		{"DEU", "DE", true}, // alpha-3 -> alpha-2
		{"  France  ", "FR", true},
		// Kosovo resolves to XK via the legacy name map, but XK has no ISO2->ISO3 counterpart and is
		// NOT in the country seed, so the seeded resolver must reject it (would else violate the FK).
		{"kosovo", "", false},
		{"XK", "", false},
		{"Zzz", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ResolveSeededCountryISO2(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ResolveSeededCountryISO2(%q) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
	// Sanity: plain ResolveCountryISO2 still returns the (unseeded) XK for kosovo — proving the seeded
	// wrapper is what filters it, not a change to the base resolver.
	if iso2, ok := ResolveCountryISO2("kosovo"); !ok || iso2 != "XK" {
		t.Errorf("ResolveCountryISO2(kosovo) = (%q,%v), want (XK,true)", iso2, ok)
	}
}
