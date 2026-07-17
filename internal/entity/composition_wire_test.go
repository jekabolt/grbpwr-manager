package entity

import (
	"database/sql"
	"testing"
)

func TestUnquoteLegacyComposition(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"scalar", `"100% Cotton"`, "100% Cotton"},
		{"single-element array", `["100% COTTON"]`, "100% COTTON"},
		{"multi-element array", `["70% cotton", "30% polyester"]`, "70% cotton, 30% polyester"},
		{"plain text passthrough", "100% Cotton", "100% Cotton"},
		{"object passthrough", `{"fiber":"cotton"}`, `{"fiber":"cotton"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := UnquoteLegacyComposition(sql.NullString{String: c.in, Valid: true})
			if !got.Valid || got.String != c.want {
				t.Errorf("UnquoteLegacyComposition(%q) = %q, want %q", c.in, got.String, c.want)
			}
		})
	}
	if got := UnquoteLegacyComposition(sql.NullString{Valid: false}); got.Valid {
		t.Errorf("NULL must pass through as NULL")
	}
}
