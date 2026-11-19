package entity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func pointClosureFixture() TechCardSeamInput {
	opA, opB := uint32(0), uint32(44)
	a := TechCardSeamPointClosureAnchor{
		PatternLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ0", SavedRowRevision: 2,
		DXFSHA256: strings.Repeat("a", 64), SizeID: 58,
		AliasBlockName: "BH", NativeBlockName: "BH",
		SourceInstancePath: []string{"b/4248/p/1/o/0/r/0/c/0"}, SourceEntityOp: &opA,
		Role: TechCardSeamButtonholeMidpoint, Intent: TechCardSeamPointClosureOpen,
		ButtonholeOperationNumber: 130, ButtonOperationNumber: 700,
	}
	b := a
	b.AliasBlockName, b.NativeBlockName = "BTN", "BTN"
	b.SourceInstancePath, b.SourceEntityOp = []string{"b/42544e/p/2/o/0/r/0/c/0"}, &opB
	b.Role, b.Intent, b.ButtonholeOperationNumber, b.ButtonOperationNumber = TechCardSeamButtonMarkCenter, "", 0, 0
	return TechCardSeamInput{
		SeamKey: "01JABCDEFGHJKMNPQRSTVWXYZ9", Kind: TechCardSeamKindPointClosure,
		Status: TechCardSeamConfirmed, Direction: TechCardSeamDirectionUnknown, Source: TechCardSeamSourceManual,
		SideA: []TechCardSeamAnchor{{PieceLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ1", PointClosure: &a}},
		SideB: []TechCardSeamAnchor{{PieceLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ2", PointClosure: &b}},
	}
}

func TestTechCardSeamPointClosureExactShapeAndReuse(t *testing.T) {
	row := pointClosureFixture()
	require.NoError(t, ValidateTechCardSeamsWrite(&TechCardSeamsWrite{TechCardId: 2, Seams: []TechCardSeamInput{row}}))
	require.False(t, row.Kind.IsSeam())
	require.True(t, TechCardSeamKindClosure.IsSeam())
	sig := TechCardSeamPairRangeSig(row)
	row.SideA[0].PointClosure.Intent = TechCardSeamPointClosureClosed
	require.Equal(t, sig, TechCardSeamPairRangeSig(row))
	op := uint32(45)
	row.SideB[0].PointClosure.SourceEntityOp = &op
	require.NotEqual(t, sig, TechCardSeamPairRangeSig(row))

	for _, tc := range []struct {
		name string
		edit func(*TechCardSeamInput)
	}{
		{"missing source operation", func(s *TechCardSeamInput) { s.SideA[0].PointClosure.SourceEntityOp = nil }},
		{"role-only intent on B", func(s *TechCardSeamInput) { s.SideB[0].PointClosure.Intent = TechCardSeamPointClosureClosed }},
		{"run evidence", func(s *TechCardSeamInput) { s.SideA[0].LenMm = 1 }},
		{"native block mismatch", func(s *TechCardSeamInput) { s.SideA[0].PointClosure.NativeBlockName = "OTHER" }},
		{"missing role variant on B", func(s *TechCardSeamInput) { s.SideB[0].PointClosure = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := pointClosureFixture()
			tc.edit(&input)
			require.Error(t, ValidateTechCardSeamPointClosure("seam", input))
		})
	}
	staleLock := 5
	change := TechCardSeamsChange{TechCardId: 2, ExpectedLockVersion: &staleLock, Upsert: []TechCardSeamInput{pointClosureFixture()}}
	require.ErrorContains(t, ValidateTechCardSeamsChange(&change), "expected_server_assembly_revision")
	clock := "asm:v1:0123456789abcdef0123456789abcdef:6"
	change.ExpectedServerAssemblyRevision = &clock
	require.NoError(t, ValidateTechCardSeamsChange(&change))
}
