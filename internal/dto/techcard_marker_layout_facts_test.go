package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// The distillation is the only place the layout blob is READ on the way in, so what it misses the
// direction rule can never refuse. The 90° case must NOT register: cross-grain is
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
			name: "both are collected even when they sit on different placements",
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
			got, err := MarkerLayoutFactsFromPb(c.layout)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("facts = %+v, want %+v", got, c.want)
			}
		})
	}
}

// rot_deg is constrained NOWHERE else — the proto comment is a comment and the blob has no CHECK
// behind it. A half-turn written as -180 or 540 is the same piece upside down, and a policy that
// compared the raw number would wave it through; an angle outside the set stores fine and then
// renders differently in the editor than in the plotter file.
func TestMarkerLayoutFactsNormalisesAndPolicesRotation(t *testing.T) {
	t.Run("equivalent half-turns count as half-turns and are canonicalised", func(t *testing.T) {
		for _, raw := range []int32{180, -180, 540, -540} {
			l := &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements:    []*pb_common.TechCardMarkerPlacement{{RotDeg: raw}},
			}
			got, err := MarkerLayoutFactsFromPb(l)
			if err != nil {
				t.Fatalf("rot_deg %d: unexpected error: %v", raw, err)
			}
			if !got.HasHalfTurn {
				t.Errorf("rot_deg %d must read as a half-turn", raw)
			}
			// The blob is marshalled from this same message: the bytes must agree with the facts,
			// or a consumer checking `rot === 180` disagrees with the server that stored it.
			if l.GetPlacements()[0].GetRotDeg() != 180 {
				t.Errorf("rot_deg %d was stored as %d, want the canonical 180", raw, l.GetPlacements()[0].GetRotDeg())
			}
		}
	})

	t.Run("the four quarter turns survive in every equivalent form", func(t *testing.T) {
		for raw, want := range map[int32]int32{0: 0, 360: 0, -360: 0, 90: 90, -270: 90, 450: 90, 270: 270, -90: 270} {
			l := &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements:    []*pb_common.TechCardMarkerPlacement{{RotDeg: raw}},
			}
			got, err := MarkerLayoutFactsFromPb(l)
			if err != nil {
				t.Fatalf("rot_deg %d: unexpected error: %v", raw, err)
			}
			if got.HasHalfTurn {
				t.Errorf("rot_deg %d must not read as a half-turn", raw)
			}
			if l.GetPlacements()[0].GetRotDeg() != want {
				t.Errorf("rot_deg %d normalised to %d, want %d", raw, l.GetPlacements()[0].GetRotDeg(), want)
			}
		}
	})

	t.Run("an uncuttable angle is refused with its index", func(t *testing.T) {
		l := &pb_common.TechCardMarkerLayout{
			SchemaVersion: 3,
			Placements: []*pb_common.TechCardMarkerPlacement{
				{RotDeg: 0}, {RotDeg: 37},
			},
		}
		_, err := MarkerLayoutFactsFromPb(l)
		if err == nil {
			t.Fatal("rot_deg 37 must be refused")
		}
		for _, want := range []string{"placements[1]", "37"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q must mention %q", err, want)
			}
		}
	})
}
