package techcard

import (
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestSavedAssemblyCompletePointClosureKeepsLegacyCanonAndBindsIntent(t *testing.T) {
	card, anchor, lines := completeSourceFixture()
	legacy, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	require.NotContains(t, string(legacy.CanonicalJSON), "pointClosure")

	source := pointClosureSources{
		patterns: make(map[string][]pointClosurePattern),
		blocks: map[string][]entity.PieceAreaBlockRef{
			"main":      {{BlockName: "PCK_L", PieceLineKey: completeSourceMainPiece}},
			"pocketing": {{BlockName: "PCK_L", PieceLineKey: completeSourceOtherPiece}},
		},
		sizeTokens: map[int]string{58: "M"},
		workByStep: map[int32][]string{130: {"buttonhole"}, 700: {"button_attach"}},
	}
	for _, r := range legacy.Rows {
		source.patterns[r.PatternLineKey] = []pointClosurePattern{{scope: r.ScopeKey, row: patternHistoryRow{
			LineKey: r.PatternLineKey, URL: r.URL, SavedRowRevision: r.SavedRowRevision, Version: r.PatternVersion,
			SizeBytes: sql.NullInt64{Int64: r.SizeBytes, Valid: true},
			SHA256:    sql.NullString{String: r.SHA256, Valid: true},
		}}}
	}
	build := func(rowKey, piece string, op uint32, role entity.TechCardSeamPointClosureRole) entity.TechCardSeamAnchor {
		r := source.patterns[rowKey][0].row
		const nativeBlock = "PCK_L_M"
		return entity.TechCardSeamAnchor{PieceLineKey: piece, PointClosure: &entity.TechCardSeamPointClosureAnchor{
			PatternLineKey: rowKey, SavedRowRevision: r.SavedRowRevision, DXFSHA256: r.SHA256.String,
			SizeID: 58, AliasBlockName: "PCK_L", NativeBlockName: nativeBlock,
			SourceInstancePath: []string{"b/" + hex.EncodeToString([]byte(nativeBlock)) + "/p/1/o/0/r/0/c/0"}, SourceEntityOp: &op,
			Role: role,
		}}
	}
	row := entity.TechCardSeam{TechCardId: card.Id, TechCardSeamInput: entity.TechCardSeamInput{
		SeamKey: completeSourceUnowned, Status: entity.TechCardSeamConfirmed,
		Kind: entity.TechCardSeamKindPointClosure, Direction: entity.TechCardSeamDirectionUnknown, Source: entity.TechCardSeamSourceManual,
		AnchoredSize: "M",
		SideA:        []entity.TechCardSeamAnchor{build(completeSourceMainRow, completeSourceMainPiece, 0, entity.TechCardSeamButtonholeMidpoint)},
		SideB:        []entity.TechCardSeamAnchor{build(completeSourcePocketRow, completeSourceOtherPiece, 14, entity.TechCardSeamButtonMarkCenter)},
	}}
	row.SideA[0].PointClosure.Intent = entity.TechCardSeamPointClosureOpen
	row.SideA[0].PointClosure.ButtonholeOperationNumber, row.SideA[0].PointClosure.ButtonOperationNumber = 130, 700
	require.NoError(t, source.check("upsert[0]", row.TechCardSeamInput, true))
	pattern := &source.patterns[completeSourceMainRow][0].row
	pattern.SizeId = 52
	require.NoError(t, source.check("upsert[0]", row.TechCardSeamInput, true), "the graded DXF filing slot is independent of the selected rank")
	wrongRank := row.TechCardSeamInput
	wrongRank.AnchoredSize = "L"
	require.ErrorContains(t, source.check("upsert[0]", wrongRank, true), "size_id")
	pattern.SavedRowRevision++
	require.ErrorIs(t, source.check("upsert[0]", row.TechCardSeamInput, true), entity.ErrTechCardSavedAssemblyMoved)
	require.NoError(t, source.check("read", row.TechCardSeamInput, false), "unchanged bytes and alias can survive an ordinary document revision")
	pattern.SHA256.String = "different"
	require.ErrorIs(t, source.check("read", row.TechCardSeamInput, false), entity.ErrTechCardSavedAssemblyMoved)

	card.Seams[0] = row
	card.Operations = append(card.Operations, entity.TechCardOperation{
		OperationNumber: sql.NullInt32{Int32: 700, Valid: true}, Work: sql.NullString{String: "button_attach", Valid: true},
		SeamKeys: []string{row.SeamKey},
	})
	active, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	require.Contains(t, string(active.CanonicalJSON), `"sourceEntityOp":0`)
	require.Contains(t, string(active.CanonicalJSON), "POINT_CLOSURE_INTENT_OPEN")
	row.SideA[0].PointClosure.Intent = entity.TechCardSeamPointClosureClosed
	closed, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	require.NotEqual(t, active.SourceSHA256, closed.SourceSHA256)
	_, err = selectSavedAssemblySeams(card, 700)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)

	card.Operations[len(card.Operations)-1].OperationNumber.Int32 = 130
	_, err = buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.ErrorContains(t, err, "button attach operation")
}

func TestPointClosureOwnerAndCompleteSourceOnlyAdmission(t *testing.T) {
	const key = completeSourceUnowned
	affected := map[string]int32{key: 700}
	valid := []entity.TechCardOperationSeamKeys{{OperationNumber: 700, SeamKeys: []string{key}}}
	require.NoError(t, checkPointClosureOwners(affected, valid, nil))
	require.NoError(t, checkPointClosureOwners(affected, nil, []string{key}))
	require.ErrorContains(t, checkPointClosureOwners(affected, nil, nil), "point_closure_owner_mismatch")
	require.Error(t, checkPointClosureOwners(affected, append(valid, entity.TechCardOperationSeamKeys{OperationNumber: 130, SeamKeys: []string{key}}), nil))

	number := sql.NullInt32{Int32: 700, Valid: true}
	links := map[string][]pointClosureOperationPosition{key: {{operation: number, position: sql.NullInt32{Int32: 1, Valid: true}}}}
	stored := entity.TechCardOperation{OperationNumber: number, SeamKeys: []string{completeSourceSeam, key}}
	require.NoError(t, comparePointClosureDocumentLinks(links, []entity.TechCardOperation{stored}))
	stored.SeamKeys = []string{key, completeSourceSeam}
	require.ErrorContains(t, comparePointClosureDocumentLinks(links, []entity.TechCardOperation{stored}), "point_closure_requires_change")

	_, anchor, _ := completeSourceFixture()
	in := entity.SavedAssemblySnapshotRequest{ExpectedRow: anchor, SelectedOperationNumber: 700, SourceOnly: true}
	require.ErrorContains(t, validateSavedAssemblySnapshotRequest(in), "source_only")
	in.Profile, in.SelectedOperationNumber = entity.SavedAssemblySnapshotCompleteSourceV1, 0
	require.NoError(t, validateSavedAssemblySnapshotRequest(in))
}
