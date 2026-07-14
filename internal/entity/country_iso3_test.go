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
