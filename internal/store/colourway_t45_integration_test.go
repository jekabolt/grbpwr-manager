package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// T45 (27.09) — CONTAINER PROBE. Runs in CI (CI=1, a throwaway MySQL) against the migrated schema;
// NEVER locally: a local run of this package points at a real database and drops tables.
//
// What only a database can show: 0375/0376 applied (the token column, the unique swap, the two
// tables), the SKU token minted inside CreateColorway's transaction against the style's tokens,
// immutability on UpdateColorway, the palette written by position and mirrored into
// pantone/pantone_system/dev_hex, product.color following the palette colourway's name, the
// development-only write, the per-language names, the legacy-row pin, and the «apply to slots»
// door against a real recipe.
// ─────────────────────────────────────────────────────────────────────────────

func t45Store(ctx context.Context, t *testing.T) *MYSQLStore {
	t.Helper()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// t45ReadableStyle is insertSeasonedTestStyle plus the four style facts getProductDetails scans into
// non-nullable fields (the same completion TestCreateColorwayPublishPreconditionsAndUpdateVersionGuard
// does), so the admin detail read works on its colourways.
func t45ReadableStyle(ctx context.Context, t *testing.T, tag string) int {
	t.Helper()
	styleID := insertSeasonedTestStyle(ctx, t, tag, "SS", "SS26", 2026)
	_, err := testDB.ExecContext(ctx, "UPDATE tech_card SET brand = 'ACME', top_category_id = 1, target_gender = 'unisex', collection = 'core' WHERE id = ?", styleID)
	require.NoError(t, err)
	return styleID
}

func t45DeleteProduct(t *testing.T, id int) {
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", id) })
}

type t45Row struct {
	ColorCode, Token, Color       string
	Pantone, PantoneSystem, DevHx sql.NullString
}

func t45ReadRow(ctx context.Context, t *testing.T, id int) t45Row {
	t.Helper()
	var r t45Row
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT color_code, COALESCE(sku_color_token, ''), color,
		pantone, pantone_system, dev_hex FROM product WHERE id = ?`, id).
		Scan(&r.ColorCode, &r.Token, &r.Color, &r.Pantone, &r.PantoneSystem, &r.DevHx))
	return r
}

func t45Palette(ctx context.Context, t *testing.T, id int) []entity.ColorwayColour {
	t.Helper()
	rows, err := testDB.QueryContext(ctx, `SELECT COALESCE(label, ''), COALESCE(hex, ''), COALESCE(pantone, ''),
		COALESCE(pantone_system, '') FROM product_colour WHERE product_id = ? ORDER BY position`, id)
	require.NoError(t, err)
	defer rows.Close()
	var out []entity.ColorwayColour
	for rows.Next() {
		var c entity.ColorwayColour
		require.NoError(t, rows.Scan(&c.Label, &c.Hex, &c.Pantone, &c.PantoneSystem))
		out = append(out, c)
	}
	require.NoError(t, rows.Err())
	return out
}

func t45LockVersion(ctx context.Context, t *testing.T, styleID int) int {
	t.Helper()
	var v int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&v))
	return v
}

func t45RequireViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "want a field violation, got %v", err)
	require.Equal(t, field, ve.Field, ve.Message)
	require.Equal(t, reason, ve.Reason, ve.Message)
}

func TestColourwayT45SchemaIsApplied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = t45Store(ctx, t)

	count := func(q string, args ...any) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, q, args...).Scan(&n))
		return n
	}
	require.Equal(t, 1, count(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku_color_token' AND IS_NULLABLE = 'YES'`),
		"the token column is NULLABLE — an older binary's insert must not be refused")
	require.Equal(t, 2, count(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_sku_color_token' AND NON_UNIQUE = 0`))
	require.Zero(t, count(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color'`),
		"two colourways of one style may share a family")
	for _, table := range []string{"product_colour", "product_colour_name_i18n"} {
		require.Equal(t, 1, count(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = ?`, table), table)
	}
	// style_id keeps an index for its foreign key after the swap.
	require.Positive(t, count(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'product' AND COLUMN_NAME = 'style_id' AND SEQ_IN_INDEX = 1`))
}

// Owner's decisions 1, 2, 4: the token is minted in the store, a family may repeat, a palette
// colourway prints its own name, and the main colour is mirrored for the pre-T45 readers.
func TestColourwayT45CreateMintsTheTokenAndWritesThePalette(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := t45ReadableStyle(ctx, t, "T45C")

	// A colourway created the pre-T45 way keeps its dictionary code as its token.
	legacyID, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("BLK", "black", "T45C-L", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	t45DeleteProduct(t, legacyID)
	legacy := t45ReadRow(ctx, t, legacyID)
	require.Equal(t, "BLK", legacy.Token)
	require.Equal(t, "black", legacy.Color, "a legacy colourway still prints its family's dictionary name")
	require.Empty(t, t45Palette(ctx, t, legacyID))

	// A palette colourway of the SAME family: the token is read from its name.
	name := "Black and White"
	palID, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("BLK", "black", "T45C-P", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{
			Name: &name,
			Colours: []entity.ColorwayColour{
				{Label: "black", Pantone: "19-4005", PantoneSystem: "TCX", Hex: "#2B2C30"},
				{Label: "white", Hex: "#F4F5F0"},
			},
		})
	require.NoError(t, err)
	t45DeleteProduct(t, palID)
	pal := t45ReadRow(ctx, t, palID)
	require.Equal(t, "BLK", pal.ColorCode, "two colourways of one style share the family")
	require.Equal(t, "BKW", pal.Token)
	require.Equal(t, "Black and White", pal.Color, "product.color follows a palette colourway's own name")
	require.Equal(t, "19-4005", pal.Pantone.String, "colours[0] is mirrored for the pre-T45 readers")
	require.Equal(t, "TCX", pal.PantoneSystem.String)
	require.Equal(t, "#2B2C30", pal.DevHx.String)
	require.Equal(t, []entity.ColorwayColour{
		{Label: "black", Pantone: "19-4005", PantoneSystem: "TCX", Hex: "#2B2C30"},
		{Label: "white", Hex: "#F4F5F0"},
	}, t45Palette(ctx, t, palID), "written by position, the main colour first")

	// A name that reads as ANOTHER family's code never mints that code («Grey» in the black family).
	grey := "Grey"
	greyID, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("BLK", "black", "T45C-G", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &grey, Colours: []entity.ColorwayColour{{Label: "grey"}}})
	require.NoError(t, err)
	t45DeleteProduct(t, greyID)
	require.Equal(t, "GRE", t45ReadRow(ctx, t, greyID).Token, "GRY is the grey family's code")

	// A caller that asks for the pre-T45 rule is refused on a taken token.
	strict := newColorwayInsert("BLK", "black", "T45C-S", mediaID, langID, prices)
	strict.RefuseTakenColourToken = true
	_, err = s.Products().CreateColorway(ctx, styleID, strict, []int{mediaID}, nil, prices, nil)
	require.ErrorIs(t, err, entity.ErrColorwayColorExists)

	// The admin detail read carries it all (the card read is checked in the apply probe, whose card
	// is a full AddTechCard one).
	pf, err := s.Products().GetProductByIdShowHidden(ctx, palID, true)
	require.NoError(t, err)
	body := pf.Product.ProductDisplay.ProductBody.ProductBodyInsert
	require.Equal(t, "BKW", body.SkuColorToken)
	require.Len(t, body.Colours, 2)
}

// Owner's decision 1: immutable. An echo of the stored token passes, anything else is refused, and
// a family change moves neither the token nor the SKU's colour segment.
func TestColourwayT45TokenIsImmutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "T45I", "SS", "SS26", 2026)

	id, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("BLK", "black", "T45I", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	t45DeleteProduct(t, id)

	v := t45LockVersion(ctx, t, styleID)
	echo := newColorwayInsert("BLK", "black", "T45I", mediaID, langID, prices)
	echo.ProductBodyInsert.SkuColorToken = "BLK"
	v, err = s.Products().UpdateColorway(ctx, id, v, echo, []int{mediaID}, nil, prices, nil)
	require.NoError(t, err)

	changed := newColorwayInsert("BLK", "black", "T45I", mediaID, langID, prices)
	changed.ProductBodyInsert.SkuColorToken = "ZZZ"
	_, err = s.Products().UpdateColorway(ctx, id, v, changed, []int{mediaID}, nil, prices, nil)
	t45RequireViolation(t, err, "merchandising.sku_color_token", "immutable")
	require.Equal(t, v, t45LockVersion(ctx, t, styleID), "a refused write bumps nothing")

	white := newColorwayInsert("WHT", "white", "T45I", mediaID, langID, prices)
	_, err = s.Products().UpdateColorway(ctx, id, v, white, []int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	row := t45ReadRow(ctx, t, id)
	require.Equal(t, "WHT", row.ColorCode)
	require.Equal(t, "BLK", row.Token)
	var sku string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product WHERE id = ?`, id).Scan(&sku))
	require.Regexp(t, `^SS26-[0-9]{5}-BLK$`, sku, "the SKU is built from the token, not the family")
}

// A row an older binary inserted (token NULL) reads its color_code, and is pinned to it before a
// write can move its family — otherwise COALESCE would silently follow the family change.
func TestColourwayT45PinsALegacyRowBeforeAFamilyChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := t45ReadableStyle(ctx, t, "T45R")

	id, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("BLK", "black", "T45R", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	t45DeleteProduct(t, id)
	_, err = testDB.ExecContext(ctx, `UPDATE product SET sku_color_token = NULL WHERE id = ?`, id)
	require.NoError(t, err)

	pf, err := s.Products().GetProductByIdShowHidden(ctx, id, true)
	require.NoError(t, err)
	require.Equal(t, "BLK", pf.Product.ProductDisplay.ProductBody.ProductBodyInsert.SkuColorToken, "the read falls back to color_code")

	_, err = s.Products().UpdateColorway(ctx, id, t45LockVersion(ctx, t, styleID),
		newColorwayInsert("WHT", "white", "T45R", mediaID, langID, prices), []int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	var token sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku_color_token FROM product WHERE id = ?`, id).Scan(&token))
	require.True(t, token.Valid)
	require.Equal(t, "BLK", token.String, "pinned to what it read, not to the new family")
}

// The palette editor's and the lab-dip panel's write: development only, merchandising untouched.
func TestColourwayT45DevelopmentOnlyWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "T45D", "SS", "SS26", 2026)

	name := "Ink"
	id, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("NAV", "navy", "T45D", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &name,
			Colours: []entity.ColorwayColour{{Label: "ink", Hex: "#1F2A44"}}})
	require.NoError(t, err)
	t45DeleteProduct(t, id)
	require.Equal(t, "INK", t45ReadRow(ctx, t, id).Token)

	// prd == nil: the palette and a translation are written, the merch row is not read.
	v := t45LockVersion(ctx, t, styleID)
	v2, err := s.Products().UpdateColorway(ctx, id, v, nil, []int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{
		Colours:  []entity.ColorwayColour{{Pantone: "19-4052", PantoneSystem: "TCX", Hex: "#0F4C81"}},
		NameI18n: map[int]string{langID: "Encre"},
	})
	require.NoError(t, err)
	require.Equal(t, v+1, v2, "a development write is a mutation of the style aggregate")
	row := t45ReadRow(ctx, t, id)
	require.Equal(t, "NAV", row.ColorCode, "the family is merchandising's, and merchandising was not written")
	require.Equal(t, "INK", row.Token)
	require.Equal(t, "Ink", row.Color)
	require.Equal(t, "19-4052", row.Pantone.String)
	require.Equal(t, "TCX", row.PantoneSystem.String)
	require.Equal(t, "#0F4C81", row.DevHx.String)
	require.Len(t, t45Palette(ctx, t, id), 1, "replace-all by position")
	var translated string
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT name FROM product_colour_name_i18n WHERE product_id = ? AND language_id = ?`, id, langID).Scan(&translated))
	require.Equal(t, "Encre", translated)

	// A pantone edit without the palette is refused on a palette colourway: it is colours[0]'s mirror.
	p := "11-0601"
	_, err = s.Products().UpdateColorway(ctx, id, v2, nil, nil, nil, nil, &entity.ColorwayDevelopmentPatch{Pantone: &p})
	t45RequireViolation(t, err, "development.pantone", "derived_from_palette")

	// A rename moves product.color with it.
	renamed := "Night Ink"
	v3, err := s.Products().UpdateColorway(ctx, id, v2, nil, nil, nil, nil, &entity.ColorwayDevelopmentPatch{Name: &renamed})
	require.NoError(t, err)
	require.Equal(t, "Night Ink", t45ReadRow(ctx, t, id).Color)
	require.Equal(t, "INK", t45ReadRow(ctx, t, id).Token, "a rename never re-mints the token")

	// "" deletes a translation; an unknown language is refused with its key.
	v4, err := s.Products().UpdateColorway(ctx, id, v3, nil, nil, nil, nil, &entity.ColorwayDevelopmentPatch{NameI18n: map[int]string{langID: ""}})
	require.NoError(t, err)
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_colour_name_i18n WHERE product_id = ?`, id).Scan(&n))
	require.Zero(t, n)
	_, err = s.Products().UpdateColorway(ctx, id, v4, nil, nil, nil, nil, &entity.ColorwayDevelopmentPatch{NameI18n: map[int]string{987654321: "x"}})
	t45RequireViolation(t, err, "development.name_i18n[987654321]", "unknown_language")
}

// Owner's decision 7: the palette reaches the recipe only through this door, and only the named
// slots; per-slot consumption is untouched; a slot without a garment-level row gets a colour-only one.
func TestColourwayT45ApplyPaletteToSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	T := s.TechCards()
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "T45 apply", Stage: entity.TechCardStageProto, StyleNumber: ns(fmt.Sprintf("T45-APPLY-%d", time.Now().UnixNano())),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "T45-SHELL", Section: entity.TechCardBomSection("fabric"), Name: "Shell"},
			{LineKey: "T45-THREAD", Section: entity.TechCardBomSection("thread"), Name: "Thread"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	name := "Black / Bone"
	cw, err := s.Products().CreateColorway(ctx, tcID, newColorwayInsert("BLK", "black", "T45A", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &name, Colours: []entity.ColorwayColour{
			{Label: "black", Pantone: "19-4005", PantoneSystem: "TCX"}, {Label: "bone"},
		}})
	require.NoError(t, err)
	t45DeleteProduct(t, cw)
	plain, err := s.Products().CreateColorway(ctx, tcID, newColorwayInsert("WHT", "white", "T45A-W", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	t45DeleteProduct(t, plain)

	// The card read carries the token, the palette and the (absent) names.
	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	var ref *entity.TechCardColorway
	for i := range card.Colorways {
		if card.Colorways[i].Id == cw {
			ref = &card.Colorways[i]
		}
	}
	require.NotNil(t, ref)
	require.Equal(t, "BKB", ref.SkuColorToken, "«Black / Bone» reads as BKB")
	require.Equal(t, "BLK", ref.ColorCode)
	require.Len(t, ref.Colours, 2)
	require.Empty(t, ref.NameI18n)

	v, err := T.UpdateColorwayRecipe(ctx, cw, t45LockVersion(ctx, t, tcID), []entity.TechCardColorwayUsage{
		{BomLineKey: "T45-SHELL", Color: ns("old"), Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.5"))},
	})
	require.NoError(t, err)

	assign := []entity.ColorwayPaletteSlotAssignment{{BomLineKey: "T45-SHELL", ColourPosition: 0}, {BomLineKey: "T45-THREAD", ColourPosition: 1}}
	res, err := T.ApplyColorwayPaletteToSlots(ctx, cw, v, assign)
	require.NoError(t, err)
	require.Equal(t, entity.ColorwayPaletteApplyResult{LockVersion: v + 1, RowsUpdated: 1, RowsCreated: 1}, res)
	require.Equal(t, v+1, t45LockVersion(ctx, t, tcID))

	recipe, err := T.GetColorwayRecipe(ctx, cw)
	require.NoError(t, err)
	require.Len(t, recipe, 2)
	shell, thread := recipe[0], recipe[1]
	require.Equal(t, "black", shell.Color.String)
	require.Equal(t, "19-4005 TCX", shell.Pantone.String, "the book is appended to the code")
	require.True(t, shell.Consumption.Valid && shell.Consumption.Decimal.Equal(decimal.RequireFromString("1.5")),
		"the norm is not the palette's to touch")
	require.Equal(t, "bone", thread.Color.String)
	require.False(t, thread.Pantone.Valid, "a label-only colour has no Pantone")
	require.False(t, thread.Consumption.Valid)
	require.Equal(t, entity.ConsumptionSourceManual, thread.ConsumptionSource.String)

	// Pressed again: both rows exist now and already wear the colour — they count, nothing is added.
	res, err = T.ApplyColorwayPaletteToSlots(ctx, cw, v+1, assign)
	require.NoError(t, err)
	require.Equal(t, entity.ColorwayPaletteApplyResult{LockVersion: v + 2, RowsUpdated: 2, RowsCreated: 0}, res)
	recipe, err = T.GetColorwayRecipe(ctx, cw)
	require.NoError(t, err)
	require.Len(t, recipe, 2, "no second colour-only row")

	// Refusals, each before any write.
	_, err = T.ApplyColorwayPaletteToSlots(ctx, cw, v, assign)
	require.ErrorIs(t, err, entity.ErrTechCardConflict)
	_, err = T.ApplyColorwayPaletteToSlots(ctx, cw, v+2, []entity.ColorwayPaletteSlotAssignment{{BomLineKey: "NOPE"}})
	t45RequireViolation(t, err, "assignments[0].bom_line_key", "unknown_slot")
	_, err = T.ApplyColorwayPaletteToSlots(ctx, cw, v+2, []entity.ColorwayPaletteSlotAssignment{{BomLineKey: "T45-SHELL", ColourPosition: 2}})
	t45RequireViolation(t, err, "assignments[0].colour_position", "out_of_palette")
	_, err = T.ApplyColorwayPaletteToSlots(ctx, plain, v+2, assign)
	require.ErrorIs(t, err, entity.ErrColorwayNoPalette)
	require.Equal(t, v+2, t45LockVersion(ctx, t, tcID), "refusals bump nothing")
}
