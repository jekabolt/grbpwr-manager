package dto

import (
	"strings"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// TestPbFabricAttrsSelvedge locks the 0259 selvedge validation: non-negative, at most half the
// width when both are known (two selvedges cannot exceed the roll), zero when unset, and a clean
// round-trip through the entity.
func TestPbFabricAttrsSelvedge(t *testing.T) {
	mk := func(width, selvedge string) *pb_common.MaterialFabricAttrs {
		a := &pb_common.MaterialFabricAttrs{}
		if width != "" {
			a.WidthCm = &pb_decimal.Decimal{Value: width}
		}
		if selvedge != "" {
			a.SelvedgeCm = &pb_decimal.Decimal{Value: selvedge}
		}
		return a
	}

	for _, c := range []struct {
		name     string
		width    string
		selvedge string
		wantErr  string
		want     string // expected entity selvedge as string
	}{
		{"unset selvedge is zero", "150", "", "", "0"},
		{"plain selvedge", "150", "1.5", "", "1.5"},
		{"selvedge without width", "", "2", "", "2"},
		{"exactly half the width is allowed", "10", "5", "", "5"},
		{"negative rejected", "150", "-0.5", "must not be negative", ""},
		{"two selvedges over the width rejected", "10", "5.01", "exceed width_cm", ""},
		{"garbage rejected", "150", "wide", "selvedge_cm", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := pbFabricAttrs(mk(c.width, c.selvedge))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.SelvedgeCm.String() != c.want {
				t.Fatalf("selvedge = %s, want %s", got.SelvedgeCm, c.want)
			}
		})
	}
}
