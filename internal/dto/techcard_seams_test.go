package dto

import (
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func seamPbAnchor(piece, hint string) *pb_common.TechCardSeamAnchor {
	return &pb_common.TechCardSeamAnchor{
		PieceLineKey: piece,
		Samples: []*pb_common.TechCardSeamSample{
			{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4},
		},
		PerimShare: 0.12, LenMm: 142.5, Notches: 1, TurnDeg: -3,
		RangeFrom: 0.1, RangeTo: 0.9, EdgeHint: hint, ContourSig: "abcdef0123456789",
	}
}

func seamPb() *pb_common.TechCardSeam {
	return &pb_common.TechCardSeam{
		SeamKey:      "01jabcdefghjkmnpqrstvwxyz0",
		Status:       pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_CONFIRMED,
		Kind:         pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_PARTIAL,
		Direction:    pb_common.TechCardSeamDirection_TECH_CARD_SEAM_DIRECTION_SAME,
		Source:       pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_DOLL,
		SideA:        &pb_common.TechCardSeamSide{Parts: []*pb_common.TechCardSeamAnchor{seamPbAnchor("PIECE-A", "FP_L#2")}},
		SideB:        &pb_common.TechCardSeamSide{Parts: []*pb_common.TechCardSeamAnchor{seamPbAnchor("PIECE-B", "BP#3+4")}},
		AnchoredSize: "M",
		Note:         "eased at the yoke",
		// Output-only — must be ignored on write.
		Stale:     true,
		CreatedBy: "mallory",
	}
}

// Write → read round trip: every decision field and every anchor number survives, the key comes back
// canonical, output-only fields sent by a client are ignored.
func TestTechCardSeamsRoundTrip(t *testing.T) {
	in, err := TechCardSeamsWriteFromPb(7, []*pb_common.TechCardSeam{seamPb()}, "ann")
	require.NoError(t, err)
	require.Equal(t, "ann", in.By)
	require.Len(t, in.Seams, 1)

	stored := entity.TechCardSeam{
		TechCardSeamInput: in.Seams[0],
		TechCardId:        7,
		Stale:             false,
		CreatedBy:         "ann", CreatedAt: time.Unix(1700000000, 0),
		UpdatedBy: "bob", UpdatedAt: time.Unix(1700000600, 0),
	}
	out := TechCardSeamsToPb([]entity.TechCardSeam{stored})
	require.Len(t, out, 1)

	want := seamPb()
	want.SeamKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	want.Stale = false
	want.CreatedBy, want.UpdatedBy = "ann", "bob"
	want.CreatedAt, want.UpdatedAt = out[0].CreatedAt, out[0].UpdatedAt
	require.True(t, proto.Equal(want, out[0]), "round trip moved a field:\nwant %v\ngot  %v", want, out[0])
	require.Equal(t, int64(1700000600), out[0].GetUpdatedAt().GetSeconds())
}

func TestTechCardSeamsWriteFromPbRefusesUnknownDictionaries(t *testing.T) {
	cases := map[string]func(*pb_common.TechCardSeam){
		"seams[0].status": func(s *pb_common.TechCardSeam) { s.Status = pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_UNKNOWN },
		"seams[0].kind":   func(s *pb_common.TechCardSeam) { s.Kind = pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_UNKNOWN },
		"seams[0].source": func(s *pb_common.TechCardSeam) { s.Source = pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_UNKNOWN },
		"seams[0].direction": func(s *pb_common.TechCardSeam) {
			s.Direction = pb_common.TechCardSeamDirection(99)
		},
		"seams[0].side_b.parts": func(s *pb_common.TechCardSeam) { s.SideB = nil },
		"seams[0].side_a.parts[0].samples": func(s *pb_common.TechCardSeam) {
			s.SideA.Parts[0].Samples = make([]*pb_common.TechCardSeamSample, 100000)
		},
		"seams[0].side_a.parts[0].piece_line_key": func(s *pb_common.TechCardSeam) {
			s.SideA.Parts[0].PieceLineKey = strings.Repeat("K", 27)
		},
	}
	for field, edit := range cases {
		t.Run(field, func(t *testing.T) {
			s := seamPb()
			edit(s)
			_, err := TechCardSeamsWriteFromPb(7, []*pb_common.TechCardSeam{s}, "ann")
			var ve *entity.ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, field, ve.Field)
		})
	}
	// UNKNOWN direction is legal — the reader picks the pairing.
	s := seamPb()
	s.Direction = pb_common.TechCardSeamDirection_TECH_CARD_SEAM_DIRECTION_UNKNOWN
	in, err := TechCardSeamsWriteFromPb(7, []*pb_common.TechCardSeam{s}, "ann")
	require.NoError(t, err)
	require.Equal(t, entity.TechCardSeamDirectionUnknown, in.Seams[0].Direction)
}

// A row a NEWER binary wrote (a kind this one does not know) reads as UNKNOWN instead of failing the
// whole card read.
func TestTechCardSeamsToPbUnknownStoredValueDegrades(t *testing.T) {
	out := TechCardSeamsToPb([]entity.TechCardSeam{{TechCardSeamInput: entity.TechCardSeamInput{
		SeamKey: "01JABCDEFGHJKMNPQRSTVWXYZ0", Kind: "hem", Status: "confirmed", Source: "graph", Direction: "reversed",
	}}})
	require.Equal(t, pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_UNKNOWN, out[0].GetKind())
	require.Equal(t, pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_CONFIRMED, out[0].GetStatus())
	require.Nil(t, TechCardSeamsToPb(nil), "no seams = field absent on the wire")
}

// SEAMS ENTER NO SECTION DIGEST: the same card with and without seam decisions publishes byte-equal
// section digests and lock_version. (By construction — the digests run over TechCardInsert and seams
// are not on it — and this test keeps it that way when someone is tempted to move them.)
func TestTechCardSeamsDoNotMoveSectionDigests(t *testing.T) {
	base := &entity.TechCard{}
	base.Id = 7
	base.LockVersion = 3
	withSeams := *base
	in, err := TechCardSeamsWriteFromPb(7, []*pb_common.TechCardSeam{seamPb()}, "ann")
	require.NoError(t, err)
	withSeams.Seams = []entity.TechCardSeam{{TechCardSeamInput: in.Seams[0], TechCardId: 7, Stale: true}}

	a := ConvertEntityTechCardToPb(base, CostingFx{})
	b := ConvertEntityTechCardToPb(&withSeams, CostingFx{})
	require.Empty(t, a.GetSeams())
	require.Len(t, b.GetSeams(), 1)
	require.True(t, b.GetSeams()[0].GetStale())
	require.Equal(t, a.GetLockVersion(), b.GetLockVersion())
	ja, err := protojson.Marshal(&pb_common.TechCard{SectionDigests: a.GetSectionDigests()})
	require.NoError(t, err)
	jb, err := protojson.Marshal(&pb_common.TechCard{SectionDigests: b.GetSectionDigests()})
	require.NoError(t, err)
	require.NotEmpty(t, a.GetSectionDigests(), "positive control: digests are published at all")
	require.JSONEq(t, string(ja), string(jb))
}

func TestTechCardSeamsWriteFromPbBoundsBeforeCopy(t *testing.T) {
	many := make([]*pb_common.TechCardSeam, entity.TechCardSeamMaxRowsPerCard+1)
	_, err := TechCardSeamsWriteFromPb(7, many, "ann")
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "seams", ve.Field)

	s := seamPb()
	for i := 0; i < 9; i++ {
		s.SideB.Parts = append(s.SideB.Parts, seamPbAnchor("P", "h"))
	}
	_, err = TechCardSeamsWriteFromPb(7, []*pb_common.TechCardSeam{s}, "ann")
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "seams[0].side_b.parts", ve.Field)
}
