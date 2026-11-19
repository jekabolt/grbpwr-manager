package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// The owner's under/over and the hand placement travel in seams.json both ways; unset stays unset.
func TestSeamArchiveKeepsLayerAndPlacement(t *testing.T) {
	side := []entity.TechCardSeamAnchor{
		{PieceLineKey: "BAG", Samples: []entity.TechCardSeamSample{{U: 0.5, V: 0.5}}, Layer: entity.TechCardSeamLayerOver,
			Placement: &entity.TechCardSeamPlacement{XMm: 12, YMm: 30, RotDeg: 90, Flip: true}},
		{PieceLineKey: "FRONT", Samples: []entity.TechCardSeamSample{{U: 1, V: 0}}},
	}
	back := seamSideFromArchive(seamSideToArchive(side))
	require.Equal(t, side, back)
}

func TestPointClosureArchiveKeepsExactOccurrenceAndLegacyShape(t *testing.T) {
	zero := uint32(0)
	side := []entity.TechCardSeamAnchor{{PieceLineKey: "POINT", Samples: []entity.TechCardSeamSample{},
		PointClosure: &entity.TechCardSeamPointClosureAnchor{
			PatternLineKey: "SHEET", SavedRowRevision: 2, SizeID: 58, NativeBlockName: "BTN_M",
			SourceEntityOp: &zero, SourceInstancePath: []string{"b/42544e5f4d/p/1/o/0/r/0/c/0"},
			Role: entity.TechCardSeamButtonholeMidpoint, Intent: entity.TechCardSeamPointClosureClosed,
			ButtonholeOperationNumber: 130, ButtonOperationNumber: 700,
		}},
		{PieceLineKey: "LEGACY", Samples: []entity.TechCardSeamSample{}},
	}
	archive := seamSideToArchive(side)
	require.Equal(t, side, seamSideFromArchive(archive))
	require.Nil(t, archive[1].PointClosure)
	raw, err := json.Marshal(archive[1])
	require.NoError(t, err)
	require.NotContains(t, string(raw), "point_closure")
	raw, err = json.Marshal(archive[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"source_entity_op":0`)
}

func TestPointClosureArchiveResolveRetainsUnverifiedSourceAsStale(t *testing.T) {
	const flap = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	const cuff = "01JABCDEFGHJKMNPQRSTVWXYZ2"
	no := uint32(0)
	a := &techcardarchive.SeamPointClosureAnchor{
		PatternLineKey: archFoldB, SavedRowRevision: 2, DXFSHA256: strings.Repeat("a", 64),
		SizeID: 4, AliasBlockName: "BH", NativeBlockName: "BH", SourceEntityOp: &no,
		SourceInstancePath: []string{"b/4248/p/1/o/0/r/0/c/0"},
		Role:               string(entity.TechCardSeamButtonholeMidpoint), Intent: string(entity.TechCardSeamPointClosureClosed),
		ButtonholeOperationNumber: 130, ButtonOperationNumber: 700,
	}
	b := *a
	b.AliasBlockName, b.NativeBlockName = "BTN", "BTN"
	b.SourceInstancePath = []string{"b/42544e/p/2/o/0/r/0/c/0"}
	b.Role, b.Intent = string(entity.TechCardSeamButtonMarkCenter), ""
	b.ButtonholeOperationNumber, b.ButtonOperationNumber = 0, 0
	row := techcardarchive.SeamPayload{
		SeamKey: archFoldA, Kind: string(entity.TechCardSeamKindPointClosure),
		Status: string(entity.TechCardSeamConfirmed), Source: string(entity.TechCardSeamSourceManual),
		Direction: string(entity.TechCardSeamDirectionUnknown),
		SideA:     []techcardarchive.SeamAnchor{{PieceLineKey: flap, PointClosure: a}},
		SideB:     []techcardarchive.SeamAnchor{{PieceLineKey: cuff, PointClosure: &b}},
	}
	arch := tcimpNewArchive().with(techcardarchive.FileSeams, tcimpJSON(t, []techcardarchive.SeamPayload{row}))
	arch.insert.Pieces = []*pb_common.TechCardPiece{{LineKey: flap}, {LineKey: cuff}}
	arch.insert.Operations = []*pb_common.TechCardOperation{{SeamKeys: []string{archFoldA}}}
	out := &resolvedTechCardImport{Insert: arch.insert, Counters: techcardarchive.NewCounters()}
	resolver := &tcimpResolver{a: arch.open(t), out: out, holeSeen: map[string]bool{}}

	require.NoError(t, resolver.resolveSeams())
	require.Len(t, out.SeamPlan, 1, "import report: %+v", out.Holes)
	require.Equal(t, []entity.TechCardSeamInput{seamFromArchive(row)}, out.SeamPlan)
	require.Equal(t, map[string]bool{archFoldA: true}, out.SeamStale)
	require.Equal(t, []string{archFoldA}, out.Insert.Operations[0].SeamKeys)
	require.Len(t, out.Holes, 1)
	require.Equal(t, techcardarchive.ReasonPointClosureSourceUnverified, out.Holes[0].Reason)
	require.Equal(t, techcardarchive.StatusDegraded, out.Holes[0].Status)
}
