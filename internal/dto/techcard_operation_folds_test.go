package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	opFoldK1 = "01JABCDEFGHJKMNPQRSTVWXYA1"
	opFoldK2 = "01JABCDEFGHJKMNPQRSTVWXYB2"
)

func TestOperationFoldKeysFromPbKeepsOrderAndNormalises(t *testing.T) {
	got, err := operationFoldKeysFromPb(0, []string{strings.ToLower(opFoldK2), " " + opFoldK1})
	require.NoError(t, err)
	require.Equal(t, []string{opFoldK2, opFoldK1}, got)

	got, err = operationFoldKeysFromPb(0, nil)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestOperationFoldKeysFromPbRefusals(t *testing.T) {
	many := make([]string, entity.TechCardOperationMaxFoldKeys+1)
	cases := []struct {
		name  string
		keys  []string
		field string
	}{
		{"not a ulid", []string{opFoldK1, "short"}, "operations[3].fold_keys[1]"},
		{"letter I", []string{"01JABCDEFGHJKMNPQRSTVWXYZI"}, "operations[3].fold_keys[0]"},
		{"duplicate after normalising", []string{opFoldK1, strings.ToLower(opFoldK1)}, "operations[3].fold_keys[1]"},
		{"too many", many, "operations[3].fold_keys"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := operationFoldKeysFromPb(3, c.keys)
			var ve *entity.ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, c.field, ve.Field)
		})
	}
}

func digestsOf(t *testing.T, card *pb_common.TechCard) string {
	t.Helper()
	raw, err := protojson.Marshal(&pb_common.TechCard{SectionDigests: card.GetSectionDigests()})
	require.NoError(t, err)
	return string(raw)
}

// The whole reason a card with no fold steps stays the card it was: the keys are in no digest, and an
// empty list is not on the wire at all.
func TestOperationFoldKeysReachThePbAndStayOutOfDigests(t *testing.T) {
	out := techCardOperationsToPb([]entity.TechCardOperation{{FoldKeys: []string{opFoldK2, opFoldK1}}})
	require.Equal(t, []string{opFoldK2, opFoldK1}, out[0].GetFoldKeys())

	base := &entity.TechCard{}
	base.Id = 7
	base.Operations = []entity.TechCardOperation{{}}
	marked := &entity.TechCard{}
	marked.Id = 7
	marked.Operations = []entity.TechCardOperation{{FoldKeys: []string{opFoldK1}}}
	a := ConvertEntityTechCardToPb(base, CostingFx{})
	b := ConvertEntityTechCardToPb(marked, CostingFx{})
	require.Equal(t, []string{opFoldK1}, b.GetTechCard().GetOperations()[0].GetFoldKeys())
	require.NotEmpty(t, a.GetSectionDigests())
	require.JSONEq(t, digestsOf(t, a), digestsOf(t, b), "the folds on a step are not part of any signed section")

	ja, err := protojson.Marshal(a)
	require.NoError(t, err)
	require.NotContains(t, string(ja), "foldKeys", "an empty list is not serialized")
	jb, err := protojson.Marshal(b)
	require.NoError(t, err)
	require.Contains(t, string(jb), "foldKeys")
}

// A write that names fold keys reaches the entity through the one converter every path shares.
func TestConvertInsertCarriesFoldKeys(t *testing.T) {
	stored := &entity.TechCard{}
	stored.Id = 7
	stored.Name = "n"
	stored.Stage = entity.TechCardStageProto
	stored.StyleNumber = sql.NullString{String: "OPF-1", Valid: true}
	stored.MeasurementUnit = entity.TechCardUnitMm
	stored.ApprovalState = entity.TechCardApprovalDraft
	stored.Operations = []entity.TechCardOperation{{OperationType: entity.OpTypeHandwork, Zone: entity.ZoneOuter,
		FoldKeys: []string{opFoldK1, opFoldK2}}}
	pb := ConvertEntityTechCardToPb(stored, CostingFx{})
	pb.TechCard.OperationFoldsAware = true
	in, err := ConvertPbTechCardInsertToEntity(pb.GetTechCard())
	require.NoError(t, err)
	require.Equal(t, []string{opFoldK1, opFoldK2}, in.Operations[0].FoldKeys)
}
