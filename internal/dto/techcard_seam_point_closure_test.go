package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestPointClosureWireKeepsZeroOccurrenceAndOmitsLegacyExtension(t *testing.T) {
	legacy := &pb_common.TechCardSeamAnchor{PieceLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ1"}
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(legacy)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "pointClosure")
	native := &pb_common.TechCardSeamPointClosureAnchor{
		PatternLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ0", SavedRowRevision: 2,
		DxfSha256: strings.Repeat("a", 64), SizeId: 58, AliasBlockName: "BH", NativeBlockName: "BH",
		SourceInstancePath: []string{"b/4248/p/1/o/0/r/0/c/0"}, SourceEntityOp: proto.Uint32(0),
		Role:                      pb_common.TechCardSeamPointClosureRole_TECH_CARD_SEAM_POINT_CLOSURE_ROLE_BUTTONHOLE_MIDPOINT,
		Intent:                    pb_common.TechCardSeamPointClosureIntent_TECH_CARD_SEAM_POINT_CLOSURE_INTENT_OPEN,
		ButtonholeOperationNumber: 130, ButtonOperationNumber: 700,
	}
	wire := &pb_common.TechCardSeamSide{Parts: []*pb_common.TechCardSeamAnchor{
		{PieceLineKey: legacy.PieceLineKey, NativeGeometry: &pb_common.TechCardSeamAnchor_PointClosure{PointClosure: native}},
	}}
	converted := seamSideFromPb(wire)
	require.Equal(t, entity.TechCardSeamButtonholeMidpoint, converted[0].PointClosure.Role)
	require.True(t, proto.Equal(wire, seamSideToPb(converted)))
	raw, err = (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(wire)
	require.NoError(t, err)
	require.Contains(t, strings.ReplaceAll(string(raw), " ", ""), `"sourceEntityOp":0`)
	require.Contains(t, strings.ReplaceAll(string(raw), " ", ""), `"savedRowRevision":2`)
}
