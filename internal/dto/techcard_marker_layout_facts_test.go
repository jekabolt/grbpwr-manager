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
			want: entity.MarkerLayoutFacts{SchemaVersion: 3, HalfTurnCount: 1},
		},
		{
			name: "a mirror is found independently of rotation",
			layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements:    []*pb_common.TechCardMarkerPlacement{place(0, false), place(0, true)},
			},
			want: entity.MarkerLayoutFacts{SchemaVersion: 3, FlipCount: 1},
		},
		{
			name: "both are collected even when they sit on different placements",
			layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements: []*pb_common.TechCardMarkerPlacement{
					place(180, false), place(0, true), place(0, false),
				},
			},
			want: entity.MarkerLayoutFacts{SchemaVersion: 3, HalfTurnCount: 1, FlipCount: 1},
		},
		{
			name: "every upside-down placement is counted, not just the first",
			layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 3,
				Placements: []*pb_common.TechCardMarkerPlacement{
					place(180, false), place(180, false), place(180, true), place(0, false),
				},
			},
			want: entity.MarkerLayoutFacts{SchemaVersion: 3, HalfTurnCount: 3, FlipCount: 1},
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
			if !got.HasHalfTurn() {
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
			if got.HasHalfTurn() {
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

// The STORED blob is history: it may predate every validation this server has, so reading it must
// judge nothing. That is the whole difference from MarkerLayoutFactsFromPb, which polices a payload —
// and confusing the two would turn «this row is old» into «this row cannot be saved».
func TestMarkerLayoutFactsFromBlob(t *testing.T) {
	t.Run("reads the two forbidden properties", func(t *testing.T) {
		cases := map[string]entity.MarkerLayoutFacts{
			`{"schemaVersion":1,"placements":[{"pieceId":1}]}`:                   {SchemaVersion: 1},
			`{"schemaVersion":1,"placements":[{"rotDeg":180}]}`:                  {SchemaVersion: 1, HalfTurnCount: 1},
			`{"schemaVersion":3,"placements":[{"flipped":true}]}`:                {SchemaVersion: 3, FlipCount: 1},
			`{"schemaVersion":3,"placements":[{"rotDeg":180},{"flipped":true}]}`: {SchemaVersion: 3, HalfTurnCount: 1, FlipCount: 1},
			// COUNTED, not flagged: the exemption compares how many, so a reader that stopped at the
			// first one would hand it «1» for a blob carrying forty.
			`{"schemaVersion":1,"placements":[{"rotDeg":180},{"rotDeg":180},{"rotDeg":180}]}`: {SchemaVersion: 1, HalfTurnCount: 3},
			`{"schemaVersion":3,"placements":[{"flipped":true},{"flipped":true}]}`:            {SchemaVersion: 3, FlipCount: 2},
			`{"schemaVersion":3,"placements":[{"rotDeg":180,"flipped":true}]}`:                {SchemaVersion: 3, HalfTurnCount: 1, FlipCount: 1},
			`{"schemaVersion":2,"placements":[]}`:                                             {SchemaVersion: 2},
		}
		for blob, want := range cases {
			got, err := MarkerLayoutFactsFromBlob(blob)
			if err != nil {
				t.Fatalf("%s: unexpected error %v", blob, err)
			}
			if got != want {
				t.Errorf("%s: facts = %+v, want %+v", blob, got, want)
			}
		}
	})

	t.Run("an equivalent half-turn on file still counts", func(t *testing.T) {
		// Written before rot_deg was policed at all, so -180 and 540 are both on file as half-turns.
		for _, raw := range []string{"-180", "540"} {
			got, err := MarkerLayoutFactsFromBlob(`{"schemaVersion":1,"placements":[{"rotDeg":` + raw + `}]}`)
			if err != nil {
				t.Fatalf("rot %s: unexpected error %v", raw, err)
			}
			if !got.HasHalfTurn() {
				t.Errorf("rot %s on file must read as a half-turn", raw)
			}
		}
	})

	t.Run("an uncuttable angle is read, not refused", func(t *testing.T) {
		// The payload validator refuses 37°; history containing one must still be READABLE, or a row
		// written before that rule could never be judged against its own geometry again.
		got, err := MarkerLayoutFactsFromBlob(`{"schemaVersion":1,"placements":[{"rotDeg":37}]}`)
		if err != nil {
			t.Fatalf("stored geometry must not be judged: %v", err)
		}
		if got.HasHalfTurn() {
			t.Error("37° is not a half-turn")
		}
	})

	t.Run("a blob that does not parse is an error, not empty facts", func(t *testing.T) {
		// The caller must be able to tell «nothing forbidden was on file» from «I could not look».
		if _, err := MarkerLayoutFactsFromBlob(`{"schemaVersion":"не число"}`); err == nil {
			t.Fatal("an unparsable blob must report an error")
		}
	})

	t.Run("a blob from a newer server still reads", func(t *testing.T) {
		// DiscardUnknown, like the read path: after a rollback the markers saved meanwhile must not
		// all become unexemptible.
		got, err := MarkerLayoutFactsFromBlob(`{"schemaVersion":4,"placements":[{"rotDeg":180,"someFutureField":7}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.HasHalfTurn() {
			t.Error("the half-turn must still be seen through an unknown field")
		}
	})
}
