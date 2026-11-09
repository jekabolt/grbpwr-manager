package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

const (
	topDTOBom   = "01JABCDEFGHJKMNPQRSTVWXYD1"
	topDTOPiece = "01JABCDEFGHJKMNPQRSTVWXYD2"
	topDTOLine  = "01JABCDEFGHJKMNPQRSTVWXYD3"
)

func topDTOAnchor() entity.TechCardSeamAnchor {
	return entity.TechCardSeamAnchor{
		PieceLineKey: topDTOPiece,
		Samples: []entity.TechCardSeamSample{
			{U: 0, V: 0}, {U: .25, V: .1}, {U: .5, V: .2}, {U: .75, V: .3}, {U: 1, V: .4},
		},
		PerimShare: .2, LenMm: 250, EdgeHint: "FR#1", ContourSig: "abcdef",
	}
}

func topDTOEntityLine() entity.TechCardTopstitchLine {
	return entity.TechCardTopstitchLine{
		LineKey: topDTOLine, BomLineKey: topDTOBom, Parts: []entity.TechCardSeamAnchor{topDTOAnchor()},
		AnchoredSize: "m", Rows: 2, Stale: true,
		Sizes: []entity.TechCardTopstitchLineSize{
			{SizeId: 2, LineMm: nd("220.25")},
			{SizeId: 1, LineMm: nd("200")},
		},
	}
}

func topDTOInsert() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		SizeIds: []int{1, 2},
		Pieces:  []entity.TechCardPiece{{LineKey: topDTOPiece, Name: "front", PiecesPerGarment: 1}},
		BomItems: []entity.TechCardBomItem{{
			LineKey: topDTOBom, Section: entity.BomSectionThread,
			Unit: sql.NullString{String: "m", Valid: true}, ThreadPerMm: nd("2.5"),
		}},
		Operations:          []entity.TechCardOperation{{BomLineKeys: []string{topDTOBom}}},
		TopstitchLinesAware: true,
	}
}

func TestTopstitchLinesDTORoundTrip(t *testing.T) {
	wire := topstitchLinesToPb([]entity.TechCardTopstitchLine{topDTOEntityLine()})
	require.Len(t, wire, 1)
	require.True(t, wire[0].GetStale(), "stale is output")
	require.False(t, wire[0].GetMeasured(), "measured is write-only")
	wire[0].Measured = true
	wire[0].Sizes = append(wire[0].Sizes, &pb_common.TechCardTopstitchLineSize{
		SizeId: 2, LineMm: &pb_decimal.Decimal{},
	})

	pb := &pb_common.TechCardInsert{
		TopstitchLinesAware: true,
		Operations: []*pb_common.TechCardOperation{{
			BomLineKeys: []string{topDTOBom}, TopstitchLines: wire,
		}},
	}
	insert := topDTOInsert()
	require.NoError(t, parseTechCardTopstitchLines(pb, insert))
	require.Len(t, insert.Operations[0].TopstitchLines, 1)
	got := insert.Operations[0].TopstitchLines[0]
	require.Equal(t, topDTOLine, got.LineKey)
	require.Equal(t, topDTOBom, got.BomLineKey)
	require.Equal(t, "m", got.AnchoredSize)
	require.Equal(t, 2, got.Rows)
	require.True(t, got.Measured)
	require.False(t, got.Stale, "the output-only verdict is not accepted as state")
	require.Equal(t, []int{1, 2}, []int{got.Sizes[0].SizeId, got.Sizes[1].SizeId})
	require.Equal(t, "200", got.Sizes[0].LineMm.Decimal.String())
	require.Equal(t, "220.25", got.Sizes[1].LineMm.Decimal.String())
	require.Equal(t, topDTOAnchor(), got.Parts[0])
}

func TestTopstitchLinesDTORequiresAwareness(t *testing.T) {
	pb := &pb_common.TechCardInsert{Operations: []*pb_common.TechCardOperation{{
		TopstitchLines: topstitchLinesToPb([]entity.TechCardTopstitchLine{topDTOEntityLine()}),
	}}}
	err := parseTechCardTopstitchLines(pb, topDTOInsert())
	var violation *entity.ValidationError
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "operations[0].topstitch_lines", violation.Field)
}

func TestTopstitchLinesDTOMintsEmptyKey(t *testing.T) {
	wire := topstitchLinesToPb([]entity.TechCardTopstitchLine{topDTOEntityLine()})
	wire[0].LineKey = ""
	pb := &pb_common.TechCardInsert{TopstitchLinesAware: true, Operations: []*pb_common.TechCardOperation{{
		BomLineKeys: []string{topDTOBom}, TopstitchLines: wire,
	}}}
	insert := topDTOInsert()
	require.NoError(t, parseTechCardTopstitchLines(pb, insert))
	require.Len(t, insert.Operations[0].TopstitchLines[0].LineKey, 26)
}

func topDigestCard(line *entity.TechCardTopstitchLine) *entity.TechCardInsert {
	card := asmDigestCard(asmPlainOp("FR", "BK"))
	card.BomItems = []entity.TechCardBomItem{{
		LineKey: topDTOBom, Name: "thread", Section: entity.BomSectionThread,
		Unit: sql.NullString{String: "m", Valid: true}, ThreadPerMm: decimal.NewNullDecimal(decimal.RequireFromString("2.5")),
	}}
	if line != nil {
		copyLine := *line
		copyLine.Parts = append([]entity.TechCardSeamAnchor(nil), line.Parts...)
		copyLine.Sizes = append([]entity.TechCardTopstitchLineSize(nil), line.Sizes...)
		card.Operations[0].TopstitchLines = []entity.TechCardTopstitchLine{copyLine}
	}
	return card
}

func TestTopstitchLineDigestMovesMaterialsOnly(t *testing.T) {
	line := topDTOEntityLine()
	line.Stale = false
	base := TechCardSectionDigests(topDigestCard(nil))
	with := TechCardSectionDigests(topDigestCard(&line))
	require.NotEqual(t, base[entity.SignoffMaterials], with[entity.SignoffMaterials])
	require.Equal(t, base[entity.SignoffConstruction], with[entity.SignoffConstruction])

	for name, edit := range map[string]func(*entity.TechCardInsert){
		"line key": func(c *entity.TechCardInsert) {
			c.Operations[0].TopstitchLines[0].LineKey = "01JABCDEFGHJKMNPQRSTVWXYD4"
		},
		"rows":   func(c *entity.TechCardInsert) { c.Operations[0].TopstitchLines[0].Rows = 3 },
		"length": func(c *entity.TechCardInsert) { c.Operations[0].TopstitchLines[0].Sizes[0].LineMm = nd("221") },
		"size":   func(c *entity.TechCardInsert) { c.Operations[0].TopstitchLines[0].Sizes[0].SizeId = 3 },
		"factor": func(c *entity.TechCardInsert) { c.BomItems[0].ThreadPerMm = nd("3") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := topDigestCard(&line)
			edit(changed)
			got := TechCardSectionDigests(changed)
			require.NotEqual(t, with[entity.SignoffMaterials], got[entity.SignoffMaterials])
			require.Equal(t, with[entity.SignoffConstruction], got[entity.SignoffConstruction])
		})
	}

	anchors := topDigestCard(&line)
	anchors.Operations[0].TopstitchLines[0].Parts[0].EdgeHint = "other"
	anchors.Operations[0].TopstitchLines[0].AnchoredSize = "l"
	require.Equal(t, with, TechCardSectionDigests(anchors), "source geometry does not change consumption")
}

func TestThreadPerMmCarryUsesOmittedProtocol(t *testing.T) {
	stored := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{{LineKey: topDTOBom, ThreadPerMm: nd("2.5")}}}
	in := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{{LineKey: topDTOBom, ThreadPerMmOmitted: true}}}
	entity.CarryStoredThreadPerMm(in, stored)
	require.Equal(t, "2.5", in.BomItems[0].ThreadPerMm.Decimal.String())

	explicit := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{{LineKey: topDTOBom, ThreadPerMm: nd("3")}}}
	entity.CarryStoredThreadPerMm(explicit, stored)
	require.Equal(t, "3", explicit.BomItems[0].ThreadPerMm.Decimal.String())
}

func TestReleaseCostKeepsFrozenTopstitchCutLengthUsage(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{4, 6}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 1, LineKey: topDTOBom, Name: "thread", Section: entity.BomSectionThread,
		Unit: nstr("m"), UnitPrice: nd("10"), Currency: nstr("EUR"), ThreadPerMm: nd("2.5"),
	}}
	card.Operations = []entity.TechCardOperation{{TopstitchLines: []entity.TechCardTopstitchLine{{
		LineKey: topDTOLine, BomLineKey: topDTOBom, Rows: 2,
		Sizes: []entity.TechCardTopstitchLineSize{
			{SizeId: 4, LineMm: nd("100")},
			{SizeId: 6, LineMm: nd("200")},
		},
	}}}}
	card.Colorways = []entity.TechCardColorway{{
		Id: 55, ProductId: bidx(55),
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: nd("9"),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
		}},
	}}
	card.Costing = &entity.TechCardCosting{Currency: nstr("EUR")}
	entity.ApplyDerivedLineNorms(card.BomItems, nil, entity.TopstitchLines(card.Operations), card.SizeIds, card.Colorways)

	snap := releaseBlob(t, card)
	for _, op := range snap.GetTechCard().GetOperations() {
		for _, line := range op.GetTopstitchLines() {
			for _, size := range line.GetSizes() {
				size.LineMm = &pb_decimal.Decimal{Value: "999"}
			}
		}
	}
	frozen := releaseCostingCardFromSnapshot(snap)
	require.NotNil(t, frozen)
	require.True(t, frozen.BomItems[0].CutToLength)
	require.Len(t, frozen.Colorways, 1)
	require.Len(t, frozen.Colorways[0].Usages, 1)
	usage := frozen.Colorways[0].Usages[0]
	require.True(t, usage.IsCutLengthNorm())
	require.Equal(t, []string{"0.5", "1"}, []string{
		usage.SizeConsumptions[0].Consumption.String(), usage.SizeConsumptions[1].Consumption.String(),
	}, "the frozen usage wins over changed geometry")
}
