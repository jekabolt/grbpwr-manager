package techcard

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// sourcesOf builds seamSources the way loadSeamSources does, from already bucketed refs.
func sourcesOf(lines []entity.RollGoodsLine, sheets map[string][]entity.PatternSheetRef,
	blocks map[string][]entity.PieceAreaBlockRef) *seamSources {
	return &seamSources{
		sheets:       entity.WithoutInterliningScopes(sheets, lines),
		blocks:       entity.WithoutInterliningScopes(blocks, lines),
		memo:         map[string]string{},
		legacySheets: sheets, legacyBlocks: blocks, legacyMemo: map[string]string{},
		strips: map[string]bool{}, labels: map[string]bool{},
	}
}

// FUSING A PIECE DOES NOT STALE ITS FOLDS AND SEAMS (backlog: «склейка детали делает её сгибы
// устаревшими»). Fusing generates an interlining sheet whose blocks link back to the shell piece; the
// shell outline the fold sits on did not move. Card 110 BP: fused → its three folds read stale.
// No DB needed.
func TestFusingAPieceKeepsItsFoldsFresh(t *testing.T) {
	lines := []entity.RollGoodsLine{
		{LineKey: "MAIN-LINE", Purpose: "main", Section: "fabric"},
		{LineKey: "FUSE-LINE", Purpose: "interlining", Section: entity.BomSectionInterlining},
	}
	sheets := map[string][]entity.PatternSheetRef{
		"main": {{LineKey: "S-MAIN", URL: "u/main.dxf", Version: 1}},
	}
	blocks := map[string][]entity.PieceAreaBlockRef{
		"main": {{BlockName: "BP", PieceLineKey: "PIECE-BP"}},
	}
	bp := []string{"PIECE-BP"}
	stamped := sourcesOf(lines, sheets, blocks).fingerprint(bp)

	// The piece gets fused: the generated interlining sheet and its BP / BP_PART1 links appear, under
	// the line scope and the singleton-purpose legacy bucket alike.
	fuseSheet := []entity.PatternSheetRef{{LineKey: "S-FUSE", URL: "u/fuse.dxf", Version: 1}}
	fusedSheets := map[string][]entity.PatternSheetRef{
		"main": sheets["main"], "FUSE-LINE": fuseSheet, "interlining": fuseSheet,
	}
	fuseLinks := []entity.PieceAreaBlockRef{
		{BlockName: "BP", PieceLineKey: "PIECE-BP"}, {BlockName: "BP_PART1", PieceLineKey: "PIECE-BP"},
	}
	fusedBlocks := map[string][]entity.PieceAreaBlockRef{
		"main": blocks["main"], "FUSE-LINE": fuseLinks, "interlining": fuseLinks,
	}
	fused := sourcesOf(lines, fusedSheets, fusedBlocks)
	require.Equal(t, stamped, fused.fingerprint(bp), "fusing the piece does not move its fold fingerprint")
	require.True(t, fused.fresh(bp, stamped))

	// Reshaping the fuse zone regenerates the interlining sheet — still not the shell.
	fuseV2 := []entity.PatternSheetRef{{LineKey: "S-FUSE", URL: "u/fuse2.dxf", Version: 2}}
	reshaped := map[string][]entity.PatternSheetRef{
		"main": sheets["main"], "FUSE-LINE": fuseV2, "interlining": fuseV2,
	}
	require.True(t, sourcesOf(lines, reshaped, fusedBlocks).fresh(bp, stamped))

	// A row stamped while the piece was already fused (the old interlining-inclusive form) stays fresh
	// after the deploy; its next write re-stamps it in today's form.
	legacy := fused.legacyFingerprint(bp)
	require.NotEqual(t, stamped, legacy, "the old form did include the interlining scope")
	require.True(t, fused.fresh(bp, legacy))

	// The shell itself still counts: a MAIN re-upload stales the fold in either form.
	mainV2 := map[string][]entity.PatternSheetRef{
		"main":      {{LineKey: "S-MAIN", URL: "u/main2.dxf", Version: 2}},
		"FUSE-LINE": fuseSheet, "interlining": fuseSheet,
	}
	moved := sourcesOf(lines, mainV2, fusedBlocks)
	require.False(t, moved.fresh(bp, stamped))
	require.False(t, moved.fresh(bp, legacy))
}

// A REGENERATED FUSING SHEET DOES NOT STALE A LEGACY STAMP (prod 10.11, cards 5/6/11: the fusing DXF
// was regenerated and every seam of every fused piece read stale; the doll dropped them). The row was
// stamped in the old, interlining-inclusive form over fuse v1; the generator writes v2 and archives v1.
func TestRegeneratedFusingSheetKeepsLegacyStampFresh(t *testing.T) {
	lines := []entity.RollGoodsLine{
		{LineKey: "MAIN-LINE", Purpose: "main", Section: "fabric"},
		{LineKey: "FUSE-LINE", Purpose: "interlining", Section: entity.BomSectionInterlining},
	}
	mainV1 := []entity.PatternSheetRef{{LineKey: "S-MAIN", URL: "u/main.dxf", Version: 1}}
	fuseV1 := []entity.PatternSheetRef{{LineKey: "S-FUSE", URL: "u/fuse.dxf", Version: 1}}
	fuseV2 := []entity.PatternSheetRef{{LineKey: "S-FUSE", URL: "u/fuse2.dxf", Version: 2}}
	links := []entity.PieceAreaBlockRef{{BlockName: "WB", PieceLineKey: "PIECE-WB"}}
	fuseLinks := []entity.PieceAreaBlockRef{{BlockName: "WB_PART1", PieceLineKey: "PIECE-WB"}}
	blocks := map[string][]entity.PieceAreaBlockRef{"main": links, "FUSE-LINE": fuseLinks, "interlining": fuseLinks}
	sheetsWith := func(main, fuse []entity.PatternSheetRef) map[string][]entity.PatternSheetRef {
		return map[string][]entity.PatternSheetRef{"main": main, "FUSE-LINE": fuse, "interlining": fuse}
	}
	wb := []string{"PIECE-WB"}
	legacy := sourcesOf(lines, sheetsWith(mainV1, fuseV1), blocks).legacyFingerprint(wb)

	history := map[string][]entity.PatternSheetRef{"S-FUSE": fuseV1}
	regenerated := sourcesOf(lines, sheetsWith(mainV1, fuseV2), blocks)
	regenerated.interliningHistory = history
	require.True(t, regenerated.fresh(wb, legacy), "only the fusing sheet moved: the legacy stamp stays fresh")

	// The shell moved too: no history ref brings the stamp back.
	moved := sourcesOf(lines, sheetsWith([]entity.PatternSheetRef{{LineKey: "S-MAIN", URL: "u/main2.dxf", Version: 2}}, fuseV2), blocks)
	moved.interliningHistory = history
	require.False(t, moved.fresh(wb, legacy), "a shell re-upload still stales the row")

	// A shell link moved: still stale.
	relinked := sourcesOf(lines, sheetsWith(mainV1, fuseV2), map[string][]entity.PieceAreaBlockRef{
		"main": {{BlockName: "WB2", PieceLineKey: "PIECE-WB"}}, "FUSE-LINE": fuseLinks, "interlining": fuseLinks,
	})
	relinked.interliningHistory = history
	require.False(t, relinked.fresh(wb, legacy))

	// A restore's stale stamp never reads fresh.
	require.False(t, regenerated.fresh(wb, seamFingerprintStale))
}
