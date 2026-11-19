package techcard

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestSavedAssemblyCompleteSourceCommitsOwnedCutStrip(t *testing.T) {
	card, anchor, lines := completeSourceFixture()
	before, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.NoError(t, err)

	const trimKey = "01JABCDEFGHJKMNPQRSTVWXYZB"
	const bomKey = "01JABCDEFGHJKMNPQRSTVWXYZC"
	card.SizeIds = []int{58}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 18, LineKey: bomKey, Section: entity.BomSectionHardware,
		Unit:       sql.NullString{String: "m", Valid: true},
		MaterialId: sql.NullInt64{Int64: 28, Valid: true},
	}}
	card.CutStrips = []entity.TechCardCutStrip{{
		LineKey: trimKey, TechCardId: card.Id, BomLineKey: bomKey, BomItemId: 18,
		LengthMode: entity.CutStripModePerSize,
		WidthMm:    decimal.NewFromInt(35), FitPct: decimal.NewFromInt(100), Qty: 1,
		Sizes: []entity.TechCardCutStripSize{{
			SizeId: 58, ManualCutMm: sql.NullInt32{Int32: 80, Valid: true},
		}},
	}}

	unused, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	require.Equal(t, before.CanonicalJSON, unused.CanonicalJSON)
	require.Equal(t, before.SourceSHA256, unused.SourceSHA256)

	card.Seams[2].SideB[0].PieceLineKey = trimKey
	card.Operations[1].InputKeys = append(card.Operations[1].InputKeys, trimKey)
	bound, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	require.Len(t, bound.CutStrips, 1)
	require.Equal(t, card.CutStrips[0], bound.CutStrips[0].Strip)
	require.Equal(t, "m", bound.CutStrips[0].BomUnit)
	var projection struct {
		CutStrips []struct {
			BomMaterialID string
			Strip         struct {
				LineKey string
				Sizes   []struct {
					SizeID int
					CutMm  int
					SewnMm struct{ Value string }
				}
			}
		}
	}
	require.NoError(t, json.Unmarshal(bound.CanonicalJSON, &projection))
	require.Len(t, projection.CutStrips, 1)
	require.Equal(t, "28", projection.CutStrips[0].BomMaterialID)
	require.Equal(t, trimKey, projection.CutStrips[0].Strip.LineKey)
	require.Len(t, projection.CutStrips[0].Strip.Sizes, 1)
	size := projection.CutStrips[0].Strip.Sizes[0]
	require.Equal(t, 58, size.SizeID)
	require.Equal(t, 80, size.CutMm)
	require.Equal(t, "80", size.SewnMm.Value)

	card.CutStrips[0].WidthMm = decimal.NewFromInt(34)
	_, err = buildCheckedSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock, bound.SourceSHA256)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)

	card.BomItems[0].Unit.String = "pcs"
	_, err = buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
}
