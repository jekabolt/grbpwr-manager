package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMarkerColorwayBinding covers 0264: a раскладка records WHICH COLOURWAY's article it was
// measured on.
//
// The reason the column exists is that fabric_width_cm alone is not attributable. Every colourway
// of a style cuts identical pieces, but each pins its own catalog article per slot, and articles
// differ in roll width and кромка — so one geometry laid on a 140 cm and on a 150 cm article is
// two markers with two measured lengths, and before this column there was nothing in the row to
// say which was which. Costing would then apply a length taken at the wrong width and produce a
// figure that looks entirely reasonable.
//
// What is pinned here: the round-trip, the two refusals that keep attribution honest (a colourway
// of ANOTHER style, and an archived one the card read does not even return), 0 meaning "not
// colourway-specific", and the ON DELETE SET NULL degradation — deliberate, and the one case where
// a marker silently widens from one colourway to all of them.
func TestMarkerColorwayBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	fabric := entity.TechCardBomItem{
		LineKey: "01MCWFABRIC0000000000000K1", Section: entity.BomSectionFabric, Name: "Основная",
	}
	newCard := func(style string) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "Marker CW " + style, Stage: entity.TechCardStageProto, StyleNumber: ns(style),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SizeIds:         []int{szA},
			BomItems:        []entity.TechCardBomItem{fabric},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id)
		})
		return id
	}
	// product.color_code is an FK into the `color` dictionary, and uniq_product_style_color means
	// each colourway of a card needs its own — which is the point: these are genuinely different
	// colourways, not copies. Take real codes from the dictionary rather than inventing them.
	var codes []string
	rows, err := testDB.QueryContext(ctx, "SELECT code FROM color ORDER BY code LIMIT 4")
	require.NoError(t, err)
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		codes = append(codes, c)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	require.Len(t, codes, 4, "the colour dictionary must carry at least four codes")

	// A colourway is a product under the style (post-R1 merge, 0151).
	newColorway := func(cardID int, code string, lifecycle int) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
			VALUES (?, ?, ?, '#000000', 'US', ?, ?, ?)`,
			fmt.Sprintf("MCW-%s-%d", code, cardID), code, code, mediaID, cardID, lifecycle)
		require.NoError(t, err)
		cw64, err := res.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cw64)
		})
		return int(cw64)
	}

	tcID := newCard("MCW-1")
	otherID := newCard("MCW-2")
	cwA := newColorway(tcID, codes[0], 1)
	cwB := newColorway(tcID, codes[1], 1)
	cwArchived := newColorway(tcID, codes[2], 4)
	cwForeign := newColorway(otherID, codes[3], 1)

	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	ins := func(name string, cw int, width string) entity.TechCardMarkerInsert {
		return entity.TechCardMarkerInsert{
			SizeId: szA, Name: name, Source: entity.MarkerSourceAuto,
			BomLineKey: fabric.LineKey, ColorwayId: cw,
			FabricWidthCm: d(width), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			Sets: 1, UsedLengthCm: d("120"),
			PlacedCount: 3, TotalCount: 3,
			Layout: `{"schemaVersion":1,"pieces":[],"placements":[]}`,
		}
	}
	markerByName := func(cardID int, name string) entity.TechCardMarkerSummary {
		c, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		for _, m := range c.Markers {
			if m.Name == name {
				return m
			}
		}
		t.Fatalf("no marker %q on card %d", name, cardID)
		return entity.TechCardMarkerSummary{}
	}

	// Two colourways of the same card, same size, same slot, different article widths — the exact
	// case that had nowhere to live before. Both save; each keeps its own attribution.
	idA, err := T.SaveMarker(ctx, tcID, 0, ins("M · A 140", cwA, "140"), "tester")
	require.NoError(t, err)
	idB, err := T.SaveMarker(ctx, tcID, 0, ins("M · B 150", cwB, "150"), "tester")
	require.NoError(t, err)
	require.NotEqual(t, idA, idB)

	gotA := markerByName(tcID, "M · A 140")
	require.True(t, gotA.ColorwayId.Valid)
	require.EqualValues(t, cwA, gotA.ColorwayId.Int64)
	gotB := markerByName(tcID, "M · B 150")
	require.EqualValues(t, cwB, gotB.ColorwayId.Int64)

	// The blob read carries it too — the modal reopens a stored layout from here.
	full, err := T.GetMarker(ctx, idA)
	require.NoError(t, err)
	require.EqualValues(t, cwA, full.ColorwayId.Int64)

	// 0 = not colourway-specific. Legacy markers read this way and stay offered to every colourway.
	idGeneral, err := T.SaveMarker(ctx, tcID, 0, ins("M · общий", 0, "140"), "tester")
	require.NoError(t, err)
	require.Positive(t, idGeneral)
	require.False(t, markerByName(tcID, "M · общий").ColorwayId.Valid)

	// A colourway of ANOTHER style is refused. The FK alone would have accepted it — it is a real
	// product row — and the layout would have surfaced in that style's recipe carrying a width
	// measured here.
	_, err = T.SaveMarker(ctx, tcID, 0, ins("M · чужой", cwForeign, "140"), "tester")
	require.Error(t, err, "a colourway of another tech card must not take a marker")

	// An ARCHIVED colourway is refused for the same reason the card read drops archived rows: the
	// operator cannot see it, so a marker attributed to it would be attributed to nothing visible.
	_, err = T.SaveMarker(ctx, tcID, 0, ins("M · архив", cwArchived, "140"), "tester")
	require.Error(t, err, "an archived colourway must not take a marker")

	// Re-binding on update, including back to «общий».
	upd := ins("M · A 140", cwB, "140")
	_, err = T.SaveMarker(ctx, tcID, idA, upd, "tester")
	require.NoError(t, err)
	require.EqualValues(t, cwB, markerByName(tcID, "M · A 140").ColorwayId.Int64)
	upd.ColorwayId = 0
	_, err = T.SaveMarker(ctx, tcID, idA, upd, "tester")
	require.NoError(t, err)
	require.False(t, markerByName(tcID, "M · A 140").ColorwayId.Valid,
		"clearing the colourway must actually clear it, not keep the stored one")

	// ON DELETE SET NULL: deleting a colourway leaves the MEASUREMENT (geometry and the width it
	// was taken at are still true) and drops only the attribution. Note what this means and why it
	// is the accepted trade: the marker widens from colourway B to all of them.
	_, err = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", cwB)
	require.NoError(t, err)
	survivor := markerByName(tcID, "M · B 150")
	require.False(t, survivor.ColorwayId.Valid)
	require.Equal(t, "150", survivor.FabricWidthCm.StringFixed(0),
		"the width it was measured at must survive — it is what makes the orphan still auditable")
}
