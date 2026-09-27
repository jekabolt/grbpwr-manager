//go:build integration

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/probegate"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// T45 (27.09) — CONTAINER PROBE, and it cannot run by accident.
//
// This package's TestMain migrates whatever database its configuration names and then DROPS EVERY
// TABLE of it; without CI set, that configuration is ../../config/config.toml — a real base. So this
// file is built only with `-tags integration`, and its init() (which runs before TestMain opens a
// connection) refuses the whole test binary unless the environment names a disposable container
// database: CI set, GRBPWR_DISPOSABLE_DB equal to MYSQL_DATABASE, a name that says it is disposable
// and is not a real base's, a local MYSQL_HOST (internal/store/probegate). The run that is meant:
//
//	CI=1 MYSQL_HOST=127.0.0.1 MYSQL_PORT=3306 MYSQL_USER=… MYSQL_PASSWORD=… \
//	  MYSQL_DATABASE=grbpwr_test GRBPWR_DISPOSABLE_DB=grbpwr_test \
//	  go test -tags integration -run ColourwayT45 ./internal/store/
//
// What only a database can show: 0375/0376 applied (the token column and its unique, the family
// unique still standing until 0377, the two tables), the SKU token minted inside CreateColorway's
// transaction against the style's tokens and a restored one refused when held, immutability on
// UpdateColorway, the palette written by position and mirrored into pantone/pantone_system/dev_hex,
// product.color following the palette colourway's name — which a palette requires — the
// development-only write, the per-language names, the legacy-row pin on both update paths, the
// relink refusing a held token before it writes, and the «apply to slots» door against a real recipe.
// ─────────────────────────────────────────────────────────────────────────────

func init() {
	if err := probegate.Check(os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "internal/store: the integration probes refuse to start, nothing was opened: %v\n", err)
		os.Exit(2)
	}
}

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
	require.Equal(t, 2, count(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color' AND NON_UNIQUE = 0`),
		"0376 is additive (D-69): the family unique stands until 0377")
	for _, table := range []string{"product_colour", "product_colour_name_i18n"} {
		require.Equal(t, 1, count(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = ?`, table), table)
	}
	// style_id keeps an index for its foreign key after the swap.
	require.Positive(t, count(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'product' AND COLUMN_NAME = 'style_id' AND SEQ_IN_INDEX = 1`))
}

// Owner's decisions 1, 2, 4: the token is minted in the store, a palette colourway prints its own
// name, and the main colour is mirrored for the pre-T45 readers. Until 0377 the family is still
// unique per style, and a second colourway of one family is the ordinary «exists».
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

	// A second colourway of the SAME family in the SAME style: until 0377 the family unique refuses
	// it, and the refusal is the ordinary «exists», not a raw 1062.
	_, err = s.Products().CreateColorway(ctx, styleID, newColorwayInsert("BLK", "black", "T45C-2", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, nil)
	require.ErrorIs(t, err, entity.ErrColorwayFamilyTaken)
	require.ErrorIs(t, err, entity.ErrColorwayColorExists)

	// A trusted restore (the archive press) of a token the style holds is refused on the TOKEN…
	strict := newColorwayInsert("WHT", "white", "T45C-S", mediaID, langID, prices)
	strict.RestoreSkuColorToken = "BLK"
	_, err = s.Products().CreateColorway(ctx, styleID, strict, []int{mediaID}, nil, prices, nil)
	require.ErrorIs(t, err, entity.ErrColorwaySkuTokenTaken)
	require.ErrorIs(t, err, entity.ErrColorwayColorExists)
	// …and a free one is taken verbatim, whatever its family.
	restored := newColorwayInsert("WHT", "white", "T45C-R", mediaID, langID, prices)
	restored.RestoreSkuColorToken = "BKW"
	restoredID, err := s.Products().CreateColorway(ctx, styleID, restored, []int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	t45DeleteProduct(t, restoredID)
	require.Equal(t, "BKW", t45ReadRow(ctx, t, restoredID).Token)

	// A palette colourway: the token is read from its name.
	palStyle := t45ReadableStyle(ctx, t, "T45C-P")
	name := "Black and White"
	palID, err := s.Products().CreateColorway(ctx, palStyle, newColorwayInsert("BLK", "black", "T45C-P", mediaID, langID, prices),
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
	require.Equal(t, "BLK", pal.ColorCode)
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
	greyStyle := t45ReadableStyle(ctx, t, "T45C-G")
	grey := "Grey"
	greyID, err := s.Products().CreateColorway(ctx, greyStyle, newColorwayInsert("BLK", "black", "T45C-G", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &grey, Colours: []entity.ColorwayColour{{Label: "grey"}}})
	require.NoError(t, err)
	t45DeleteProduct(t, greyID)
	require.Equal(t, "GRE", t45ReadRow(ctx, t, greyID).Token, "GRY is the grey family's code")

	// The admin detail read carries it all (the card read is checked in the apply probe, whose card
	// is a full AddTechCard one).
	pf, err := s.Products().GetProductByIdShowHidden(ctx, palID, true)
	require.NoError(t, err)
	body := pf.Product.ProductDisplay.ProductBody.ProductBodyInsert
	require.Equal(t, "BKW", body.SkuColorToken)
	require.Len(t, body.Colours, 2)
}

// Owner's decision 3: a palette colourway carries its own name. Refused on create, when a nameless
// legacy colourway is given its first palette, and when the name of a palette colourway is cleared —
// each before anything is written.
func TestColourwayT45PaletteNeedsAName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "T45N", "SS", "SS26", 2026)
	palette := []entity.ColorwayColour{{Label: "bone", Hex: "#E8E2D0"}}

	count := func() int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM product WHERE style_id = ?`, styleID).Scan(&n))
		return n
	}
	before := count()
	blank := "  "
	_, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("WHT", "white", "T45N-C", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &blank, Colours: palette})
	t45RequireViolation(t, err, "development.name", "name_required_with_palette")
	require.Equal(t, before, count(), "a refused create writes no colourway")

	// A legacy colourway (no palette, no name) given its first palette without a name.
	id, err := s.Products().CreateColorway(ctx, styleID, newColorwayInsert("WHT", "white", "T45N-L", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	t45DeleteProduct(t, id)
	v := t45LockVersion(ctx, t, styleID)
	_, err = s.Products().UpdateColorway(ctx, id, v, nil, nil, nil, nil, &entity.ColorwayDevelopmentPatch{Colours: palette})
	t45RequireViolation(t, err, "development.name", "name_required_with_palette")
	require.Empty(t, t45Palette(ctx, t, id), "nothing of the refused write landed")
	require.Equal(t, v, t45LockVersion(ctx, t, styleID))

	// With the name in the same write it lands; clearing the name afterwards is refused.
	bone := "Bone"
	v, err = s.Products().UpdateColorway(ctx, id, v, nil, nil, nil, nil, &entity.ColorwayDevelopmentPatch{Name: &bone, Colours: palette})
	require.NoError(t, err)
	require.Equal(t, "Bone", t45ReadRow(ctx, t, id).Color)
	empty := ""
	_, err = s.Products().UpdateColorway(ctx, id, v, nil, nil, nil, nil, &entity.ColorwayDevelopmentPatch{Name: &empty})
	t45RequireViolation(t, err, "development.name", "name_required_with_palette")
	require.Equal(t, "Bone", t45ReadRow(ctx, t, id).Color, "the name stands")
	require.Equal(t, v, t45LockVersion(ctx, t, styleID))
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

// The relink carries the colourway's immutable SKU token into the target style, where it must be
// free: a target already holding it is refused BEFORE anything is written, naming the token, the
// target and the colourway in the way. Until 0377 a target already holding the FAMILY is refused by
// uniq_product_style_color — as the «exists» it is, not a raw 1062, and with nothing moved.
func TestColourwayT45RelinkRefusesAHeldToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	src := insertSeasonedTestStyle(ctx, t, "T45L-S", "SS", "SS26", 2026)
	tgt := insertSeasonedTestStyle(ctx, t, "T45L-T", "SS", "SS26", 2026)
	bw := "Black and White"
	palette := []entity.ColorwayColour{{Label: "black"}, {Label: "white"}}

	moving, err := s.Products().CreateColorway(ctx, src, newColorwayInsert("BLK", "black", "T45L-M", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &bw, Colours: palette})
	require.NoError(t, err)
	t45DeleteProduct(t, moving)
	// The target holds BKW under ANOTHER family — the token is the identity, not the family.
	holder, err := s.Products().CreateColorway(ctx, tgt, newColorwayInsert("WHT", "white", "T45L-H", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &bw, Colours: palette})
	require.NoError(t, err)
	t45DeleteProduct(t, holder)
	require.Equal(t, "BKW", t45ReadRow(ctx, t, moving).Token)
	require.Equal(t, "BKW", t45ReadRow(ctx, t, holder).Token)

	srcLV, tgtLV := t45LockVersion(ctx, t, src), t45LockVersion(ctx, t, tgt)
	err = s.Products().RelinkDraftColorway(ctx, moving, tgt, srcLV, tgtLV)
	require.ErrorIs(t, err, entity.ErrColorwaySkuTokenTaken)
	require.ErrorIs(t, err, entity.ErrColorwayColorExists)
	require.Contains(t, err.Error(), "BKW")
	require.Contains(t, err.Error(), "style "+strconv.Itoa(tgt))
	require.Contains(t, err.Error(), "colourway "+strconv.Itoa(holder))
	var styleNow int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT style_id FROM product WHERE id = ?`, moving).Scan(&styleNow))
	require.Equal(t, src, styleNow, "nothing moved")
	require.Equal(t, srcLV, t45LockVersion(ctx, t, src))
	require.Equal(t, tgtLV, t45LockVersion(ctx, t, tgt))

	// The family backstop: a legacy BLK colourway (token BLK) into a target whose BLK-family colourway
	// holds another token. The token is free there; until 0377 the family is not.
	legacySrc := insertSeasonedTestStyle(ctx, t, "T45L-LS", "SS", "SS26", 2026)
	legacy, err := s.Products().CreateColorway(ctx, legacySrc, newColorwayInsert("BLK", "black", "T45L-L", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, nil)
	require.NoError(t, err)
	t45DeleteProduct(t, legacy)
	famTgt := insertSeasonedTestStyle(ctx, t, "T45L-FT", "SS", "SS26", 2026)
	midnight := "Midnight"
	famHolder, err := s.Products().CreateColorway(ctx, famTgt, newColorwayInsert("BLK", "black", "T45L-F", mediaID, langID, prices),
		[]int{mediaID}, nil, prices, &entity.ColorwayDevelopmentPatch{Name: &midnight, Colours: []entity.ColorwayColour{{Label: "midnight"}}})
	require.NoError(t, err)
	t45DeleteProduct(t, famHolder)
	require.NotEqual(t, "BLK", t45ReadRow(ctx, t, famHolder).Token)

	lsLV, ftLV := t45LockVersion(ctx, t, legacySrc), t45LockVersion(ctx, t, famTgt)
	err = s.Products().RelinkDraftColorway(ctx, legacy, famTgt, lsLV, ftLV)
	require.ErrorIs(t, err, entity.ErrColorwayFamilyTaken)
	require.ErrorIs(t, err, entity.ErrColorwayColorExists)
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT style_id FROM product WHERE id = ?`, legacy).Scan(&styleNow))
	require.Equal(t, legacySrc, styleNow, "the refused move rolled back whole")
	require.Equal(t, lsLV, t45LockVersion(ctx, t, legacySrc))
	require.Equal(t, ftLV, t45LockVersion(ctx, t, famTgt))
}

// The legacy coupled UpdateProduct (a store fixture path) pins a NULL token before it moves the
// family, exactly like UpdateColorway: otherwise COALESCE(sku_color_token, color_code) would follow
// the family and the «immutable» token would change under an old-style save.
func TestColourwayT45LegacyUpdateProductPinsTheToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := t45Store(ctx, t)
	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
	payload := func(code, name string) *entity.ColorwayNew {
		prd := newColorwayInsert(code, name, "T45U", mediaID, langID, prices)
		return &entity.ColorwayNew{
			Product: prd,
			SizeMeasurements: []entity.SizeWithMeasurementInsert{
				{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(1)}},
			},
			MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
		}
	}
	id, err := s.Products().AddProduct(ctx, payload("BLK", "black"))
	require.NoError(t, err)
	t45DeleteProduct(t, id)
	_, err = testDB.ExecContext(ctx, `UPDATE product SET sku_color_token = NULL WHERE id = ?`, id)
	require.NoError(t, err)

	require.NoError(t, s.Products().UpdateProduct(ctx, payload("WHT", "white"), id))
	var token sql.NullString
	var family string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku_color_token, color_code FROM product WHERE id = ?`, id).Scan(&token, &family))
	require.Equal(t, "WHT", family)
	require.True(t, token.Valid, "pinned, not left NULL to follow the family")
	require.Equal(t, "BLK", token.String, "pinned to what the row read before the save")
}
