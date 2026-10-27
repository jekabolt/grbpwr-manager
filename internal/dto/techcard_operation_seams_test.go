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

const (
	opSeamK1 = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	opSeamK2 = "01JABCDEFGHJKMNPQRSTVWXYZ2"
)

func TestOperationSeamKeysFromPbKeepsOrderAndNormalises(t *testing.T) {
	got, err := operationSeamKeysFromPb(0, []string{strings.ToLower(opSeamK2), " " + opSeamK1})
	require.NoError(t, err)
	require.Equal(t, []string{opSeamK2, opSeamK1}, got, "sewing order is the client's order")

	got, err = operationSeamKeysFromPb(0, nil)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestOperationSeamKeysFromPbRefusals(t *testing.T) {
	many := make([]string, entity.TechCardOperationMaxSeamKeys+1)
	cases := []struct {
		name  string
		keys  []string
		field string
	}{
		{"not a ulid", []string{opSeamK1, "short"}, "operations[3].seam_keys[1]"},
		{"letter I", []string{"01JABCDEFGHJKMNPQRSTVWXYZI"}, "operations[3].seam_keys[0]"},
		{"duplicate after normalising", []string{opSeamK1, strings.ToLower(opSeamK1)}, "operations[3].seam_keys[1]"},
		{"too many", many, "operations[3].seam_keys"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := operationSeamKeysFromPb(3, c.keys)
			var ve *entity.ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, c.field, ve.Field)
		})
	}
}

func TestOperationSeamKeysReachThePbAndStayOutOfDigests(t *testing.T) {
	out := techCardOperationsToPb([]entity.TechCardOperation{{SeamKeys: []string{opSeamK2, opSeamK1}}})
	require.Equal(t, []string{opSeamK2, opSeamK1}, out[0].GetSeamKeys())

	base := &entity.TechCard{}
	base.Id = 7
	base.Operations = []entity.TechCardOperation{{}}
	marked := &entity.TechCard{}
	marked.Id = 7
	marked.Operations = []entity.TechCardOperation{{SeamKeys: []string{opSeamK1}}}
	a := ConvertEntityTechCardToPb(base, CostingFx{})
	b := ConvertEntityTechCardToPb(marked, CostingFx{})
	require.Equal(t, []string{opSeamK1}, b.GetTechCard().GetOperations()[0].GetSeamKeys())
	ja, err := protojson.Marshal(&pb_common.TechCard{SectionDigests: a.GetSectionDigests()})
	require.NoError(t, err)
	jb, err := protojson.Marshal(&pb_common.TechCard{SectionDigests: b.GetSectionDigests()})
	require.NoError(t, err)
	require.NotEmpty(t, a.GetSectionDigests())
	require.JSONEq(t, string(ja), string(jb), "a step's seam picks are not part of any signed section")
}

// Placement and the inner-line edge hint ("i<k>") survive write → read unchanged; no placement stays nil.
func TestTechCardSeamPlacementAndInnerHintRoundTrip(t *testing.T) {
	seam := seamPb()
	seam.SideB.Parts[0].EdgeHint = "i3"
	seam.SideB.Parts[0].Placement = &pb_common.TechCardSeamPlacement{XMm: -12.5, YMm: 40, RotDeg: 270, Flip: true}
	in, err := TechCardSeamsWriteFromPb(7, []*pb_common.TechCardSeam{seam}, "ann")
	require.NoError(t, err)
	out := TechCardSeamsToPb([]entity.TechCardSeam{{TechCardSeamInput: in.Seams[0], TechCardId: 7}})
	require.Equal(t, "i3", out[0].GetSideB().GetParts()[0].GetEdgeHint())
	require.True(t, proto.Equal(seam.SideB.Parts[0].Placement, out[0].GetSideB().GetParts()[0].GetPlacement()))
	require.Nil(t, out[0].GetSideA().GetParts()[0].GetPlacement())
}
