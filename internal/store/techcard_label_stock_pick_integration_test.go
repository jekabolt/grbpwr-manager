package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestLabelStockPickReachesRecipe drives a picked label (0451) through the real store: its BOM line gets
// the count on the slot and a row in every colourway's recipe, a recipe full replace cannot lose that
// row, an older label writes nothing, and the line leaves with the label without a RESTRICT refusal.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestLabelStockPickReachesRecipe(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()

	const (
		stockLine = "01LABELSTOCKPICKLINE000001"
		tapeLine  = "01LABELSTOCKPICKLINE000002"
		oldLine   = "01LABELSTOCKPICKLINE000003"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "LABEL PICK", Stage: entity.TechCardStageProto, StyleNumber: ns("LSP-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		LabelsAware: true,
		BomItems:    []entity.TechCardBomItem{{LineKey: oldLine, Section: entity.BomSectionLabel, Name: "old label"}},
		GarmentLabels: []entity.TechCardGarmentLabel{
			{Key: "brand", QtyPerGarment: 1, BomLineKey: oldLine},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	mediaID, err := st.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	colorway := func(sku, code string) int {
		t.Helper()
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, sku_locked_at)
			VALUES (?, 'black', ?, '#000000', 'IT', ?, ?, NOW())`, sku, code, mediaID, cardID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM product WHERE id = ?`, id) })
		return int(id)
	}
	black := colorway("LSP-1-BLK", "BLK")
	white := colorway("LSP-1-WHT", "WHT")

	rowsOf := func(line string, cw int) int {
		t.Helper()
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_colorway_usage u
			JOIN tech_card_bom_item b ON b.id = u.bom_item_id
			WHERE b.tech_card_id = ? AND b.line_key = ? AND u.colorway_id = ?`, cardID, line, cw).Scan(&n))
		return n
	}
	slot := func(card *entity.TechCard, line string) *entity.TechCardBomItem {
		t.Helper()
		for i := range card.BomItems {
			if card.BomItems[i].LineKey == line {
				return &card.BomItems[i]
			}
		}
		t.Fatalf("no BOM line %s", line)
		return nil
	}
	save := func(edit func(*entity.TechCardInsert)) *entity.TechCard {
		t.Helper()
		card, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		ins := card.TechCardInsert
		ins.LabelsAware = true
		edit(&ins)
		require.NoError(t, T.UpdateTechCard(ctx, cardID, &ins, card.LockVersion))
		card, err = T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		return card
	}

	// An older label holds a line and asks nothing of the recipe.
	require.Zero(t, rowsOf(oldLine, black))

	card := save(func(ins *entity.TechCardInsert) {
		ins.BomItems = append(ins.BomItems,
			entity.TechCardBomItem{LineKey: stockLine, Section: entity.BomSectionLabel, Name: "woven stock", CountableOmitted: true},
			entity.TechCardBomItem{LineKey: tapeLine, Section: entity.BomSectionLabel, Name: "tape", CountableOmitted: true})
		ins.GarmentLabels = append(ins.GarmentLabels,
			entity.TechCardGarmentLabel{Key: "woven stock", QtyPerGarment: 2, BomLineKey: stockLine,
				Source: entity.GarmentLabelSourceStock, Kind: "brand"},
			entity.TechCardGarmentLabel{Key: "tape", QtyPerGarment: 1, BomLineKey: tapeLine,
				Source: entity.GarmentLabelSourceMake, Kind: "loop", MadeBy: "printed", PrintMethod: "screen",
				LengthCm: decimal.RequireFromString("6.5")})
	})
	require.Zero(t, rowsOf(oldLine, black), "a save of a card with picks leaves an older line alone")
	require.Equal(t, 1, rowsOf(stockLine, black))
	require.Equal(t, 1, rowsOf(stockLine, white))
	require.Equal(t, 1, rowsOf(tapeLine, white))
	require.True(t, decimal.NewFromInt(2).Equal(slot(card, stockLine).QtyPerGarment.Decimal))
	require.True(t, decimal.RequireFromString("0.065").Equal(slot(card, tapeLine).QtyPerGarment.Decimal))
	require.False(t, slot(card, oldLine).QtyPerGarment.Valid)

	var made *entity.TechCardGarmentLabel
	for i := range card.GarmentLabels {
		if card.GarmentLabels[i].Key == "tape" {
			made = &card.GarmentLabels[i]
		}
	}
	require.NotNil(t, made)
	require.Equal(t, entity.GarmentLabelSourceMake, made.Source)
	require.Equal(t, "loop", made.Kind)
	require.Equal(t, "printed", made.MadeBy)
	require.Equal(t, "screen", made.PrintMethod)
	require.True(t, decimal.RequireFromString("6.5").Equal(made.LengthCm))

	// Saving again adds nothing, and a new count needs no recipe write.
	card = save(func(ins *entity.TechCardInsert) {
		for i := range ins.GarmentLabels {
			if ins.GarmentLabels[i].Key == "woven stock" {
				ins.GarmentLabels[i].QtyPerGarment = 3
			}
		}
	})
	require.Equal(t, 1, rowsOf(stockLine, black))
	require.True(t, decimal.NewFromInt(3).Equal(slot(card, stockLine).QtyPerGarment.Decimal))

	// A recipe form loaded before the pick replaces the whole recipe: the row comes back.
	_, err = T.UpdateColorwayRecipe(ctx, black, card.LockVersion, nil)
	require.NoError(t, err)
	require.Equal(t, 1, rowsOf(stockLine, black))
	require.Equal(t, 1, rowsOf(tapeLine, black))

	// A source without a counted line is refused, not saved as a label that costs nothing.
	stale, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	noLine := stale.TechCardInsert
	noLine.LabelsAware = true
	noLine.GarmentLabels = append(append([]entity.TechCardGarmentLabel{}, noLine.GarmentLabels...),
		entity.TechCardGarmentLabel{Key: "no line", QtyPerGarment: 1, Source: entity.GarmentLabelSourceStock})
	require.Error(t, T.UpdateTechCard(ctx, cardID, &noLine, stale.LockVersion))

	// Unpicked while the line stays: the server's rows and count go, a typed row and the line stay.
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card_colorway_usage u
		JOIN tech_card_bom_item b ON b.id = u.bom_item_id
		SET u.quantity = 4 WHERE b.tech_card_id = ? AND b.line_key = ? AND u.colorway_id = ?`, cardID, tapeLine, white)
	require.NoError(t, err)
	card = save(func(ins *entity.TechCardInsert) {
		for i := range ins.GarmentLabels {
			if ins.GarmentLabels[i].Key == "tape" {
				ins.GarmentLabels[i].Source = ""
			}
		}
		// The client never sends a countable line's number: the slot is the server's to keep or clear.
		for i := range ins.BomItems {
			ins.BomItems[i].CountableOmitted = true
		}
	})
	require.Zero(t, rowsOf(tapeLine, black))
	require.Equal(t, 1, rowsOf(tapeLine, white), "a typed row is a person's")
	require.False(t, slot(card, tapeLine).QtyPerGarment.Valid)
	card = save(func(ins *entity.TechCardInsert) {
		for i := range ins.GarmentLabels {
			if ins.GarmentLabels[i].Key == "tape" {
				ins.GarmentLabels[i].Source = entity.GarmentLabelSourceMake
			}
		}
	})
	require.Equal(t, 1, rowsOf(tapeLine, black))

	// The line leaves with its label: no RESTRICT refusal, rows gone, the other pick untouched.
	card = save(func(ins *entity.TechCardInsert) {
		var labels []entity.TechCardGarmentLabel
		for _, l := range ins.GarmentLabels {
			if l.Key != "woven stock" {
				labels = append(labels, l)
			}
		}
		ins.GarmentLabels = labels
		var bom []entity.TechCardBomItem
		for _, b := range ins.BomItems {
			if b.LineKey != stockLine {
				bom = append(bom, b)
			}
		}
		ins.BomItems = bom
	})
	require.Equal(t, 1, rowsOf(tapeLine, black))
	require.Equal(t, 1, rowsOf(tapeLine, white))
	for _, b := range card.BomItems {
		require.NotEqual(t, stockLine, b.LineKey)
	}
}

// TestMaterialLabelTypeStored drives the stored label type (0451) through the real store: create
// writes it, an update that does not send it keeps it, another value replaces it, a move out of the
// label section clears it, and a type outside the section is refused.
func TestMaterialLabelTypeStored(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()

	base := func(section, labelType string) *entity.MaterialInsert {
		return &entity.MaterialInsert{
			Name: "label type probe", Section: section, LabelType: labelType,
			Unit: sql.NullString{String: "pcs", Valid: true},
		}
	}
	id, err := T.CreateMaterial(ctx, base("label", string(entity.MaterialLabelTypeHangtag)))
	require.NoError(t, err)
	read := func() (string, int) {
		m, err := T.GetMaterial(ctx, id)
		require.NoError(t, err)
		return m.LabelType, m.LockVersion
	}
	got, lock := read()
	require.Equal(t, "hangtag", got)

	require.NoError(t, T.UpdateMaterial(ctx, id, base("label", ""), lock))
	got, lock = read()
	require.Equal(t, "hangtag", got, "an update that does not send the type keeps it")

	require.NoError(t, T.UpdateMaterial(ctx, id, base("label", string(entity.MaterialLabelTypeBarcode)), lock))
	got, lock = read()
	require.Equal(t, "barcode", got)

	require.NoError(t, T.UpdateMaterial(ctx, id, base("trim", ""), lock))
	got, _ = read()
	require.Equal(t, "", got, "a move out of the label section clears the type")

	_, err = T.CreateMaterial(ctx, base("trim", string(entity.MaterialLabelTypeBrand)))
	require.Error(t, err, "a type outside the label section is refused")
}
