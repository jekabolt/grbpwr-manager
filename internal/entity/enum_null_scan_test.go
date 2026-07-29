package entity

import "testing"

// A NULL style column must read as "unset", not fail the row.
//
// Regression: tech_card.target_gender is `VARCHAR(16) NULL` and season_code is `CHAR(2) NULL` — both
// legally unset. Scanned into a bare string type they produced
//
//	sql: Scan error on column index 17, name "target_gender": converting NULL to string is unsupported
//
// which failed the ENTIRE multi-row read: the admin catalogue answered 500 for a whole page because
// one style had no target gender. The empty value is already the documented "unknown" input to
// dto.ConvertEntityGenderToPbGenderEnum, so scanning NULL to "" loses nothing.
func TestGenderEnumScanNull(t *testing.T) {
	cases := []struct {
		name string
		src  any
		want GenderEnum
	}{
		{"null", nil, ""},
		{"string", "male", Male},
		{"bytes", []byte("female"), Female},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got GenderEnum
			if err := got.Scan(c.src); err != nil {
				t.Fatalf("Scan(%v): %v", c.src, err)
			}
			if got != c.want {
				t.Errorf("Scan(%v) = %q, want %q", c.src, got, c.want)
			}
		})
	}

	var g GenderEnum
	if err := g.Scan(42); err == nil {
		t.Error("Scan(int) should fail — a non-textual gender column is a schema bug, not an unset value")
	}
}

func TestSeasonEnumScanNull(t *testing.T) {
	cases := []struct {
		name string
		src  any
		want SeasonEnum
	}{
		{"null", nil, ""},
		{"string", "SS", SeasonSS},
		{"bytes", []byte("FW"), SeasonFW},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got SeasonEnum
			if err := got.Scan(c.src); err != nil {
				t.Fatalf("Scan(%v): %v", c.src, err)
			}
			if got != c.want {
				t.Errorf("Scan(%v) = %q, want %q", c.src, got, c.want)
			}
		})
	}

	var s SeasonEnum
	if err := s.Scan(42); err == nil {
		t.Error("Scan(int) should fail")
	}
}
