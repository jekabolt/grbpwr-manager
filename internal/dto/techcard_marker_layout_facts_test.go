package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// The distillation is the only place the layout blob is READ on the way in, so what it misses the
// direction rule can never refuse. The 90° case is the one that must NOT register: cross-grain is
// allow_cross_grain's question, and folding it in here would make every stored marker with a
// sideways piece unsaveable on directional cloth.
func TestMarkerLayoutFactsFromPb(t *testing.T) {
	place := func(rot int32, flipped bool) *pb_common.TechCardMarkerPlacement {
		return &pb_common.TechCardMarkerPlacement{RotDeg: rot, Flipped: flipped}
	}
	cases := []struct {
		name   string
		layout *pb_common.TechCardMarkerLayout
		want   entity.MarkerLayoutFacts
	}{
		{
			name: "upright and cross-grain placements carry no facts",
			layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 2,
				Placements:    []*pb_common.TechCardMarkerPlacement{place(0, false), place(90, false), place(270, false)},
			},
			want: entity.MarkerLayoutFacts{SchemaVersion: 2},
		},
		{
			name: "a single half-turn among many is found",
			layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements:    []*pb_common.TechCardMarkerPlacement{place(0, false), place(90, false), place(180, false)},
			},
			want: entity.MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true},
		},
		{
			name: "a mirror is found independently of rotation",
			layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements:    []*pb_common.TechCardMarkerPlacement{place(0, false), place(0, true)},
			},
			want: entity.MarkerLayoutFacts{SchemaVersion: 3, HasFlip: true},
		},
		{
			name: "both, and the early exit does not lose either",
			layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements: []*pb_common.TechCardMarkerPlacement{
					place(180, false), place(0, true), place(0, false),
				},
			},
			want: entity.MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true, HasFlip: true},
		},
		{
			name:   "no placements, no facts",
			layout: &pb_common.TechCardMarkerLayout{SchemaVersion: 1},
			want:   entity.MarkerLayoutFacts{SchemaVersion: 1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MarkerLayoutFactsFromPb(c.layout); got != c.want {
				t.Errorf("facts = %+v, want %+v", got, c.want)
			}
		})
	}
}
