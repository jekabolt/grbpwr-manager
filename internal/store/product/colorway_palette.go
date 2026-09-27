package product

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// T45 — the store half of «a colourway is a free name + an ordered palette + an immutable SKU
// token» (see entity/colorway_palette.go for the model and the owner's decisions it implements).
//
// Everything here runs inside the caller's transaction (CreateColorway / UpdateColorway, store.Tx:
// SERIALIZABLE with deadlock retry), so the token a create mints is decided against the style's
// tokens as they are at commit time: two concurrent creates in one style serialise on the rows they
// read, the loser retries and sees the winner's token. The unique index
// uniq_product_style_sku_color_token (0376) is the last word either way.

// styleSkuTokens returns every SKU colour token the style's colourways hold — ARCHIVED ones too:
// a frozen SKU keeps its token for ever and the unique index covers every row. A row an older
// binary inserted (token NULL) holds its color_code, which is what it minted with. excludeID leaves
// one colourway out (0 = none).
func styleSkuTokens(ctx context.Context, db dependency.DB, styleID, excludeID int) (map[string]bool, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Token string `db:"token"`
	}](ctx, db, styleSkuTokensQuery, map[string]any{"sid": styleID, "exclude": excludeID})
	if err != nil {
		return nil, fmt.Errorf("load sku colour tokens of style %d: %w", styleID, err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[strings.ToUpper(strings.TrimSpace(r.Token))] = true
	}
	return out, nil
}

// dictionaryColourCodes lists every dictionary code, archived ones included: a minted token equal
// to an archived code would still read as that colour in every SKU that carries it.
func dictionaryColourCodes(ctx context.Context, db dependency.DB) ([]string, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Code string `db:"code"`
	}](ctx, db, dictionaryColourCodesQuery, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("load colour dictionary codes: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Code)
	}
	return out, nil
}

// colorwayTokenName is the name a token is read from: the colourway's own name (development.name),
// else its main colour's label, else its family's dictionary name.
func colorwayTokenName(prd *entity.ColorwayInsert, dev *entity.ColorwayDevelopmentPatch) string {
	if dev != nil && dev.Name != nil && strings.TrimSpace(*dev.Name) != "" {
		return *dev.Name
	}
	if main := dev.MainColour(); main != nil && main.Label != "" {
		return main.Label
	}
	return prd.ProductBodyInsert.Color
}

// decideNewColorwaySkuToken is the minting rule for a colourway being CREATED (owner's decision 1):
//
//   - created WITHOUT a palette — the pre-T45 shape, every client that predates the palette — the
//     token is the family's dictionary code, exactly what its SKU segment used to be, whenever the
//     style does not hold it yet. When it does: a caller that set RefuseTakenColourToken gets
//     entity.ErrColorwayColorExists (the archive import keeps its colour-keyed idempotency), every
//     other caller gets a token minted from the name, so a second colourway of one family is no
//     longer refused;
//   - created WITH a palette — the token is minted from the name (entity.MintColorwaySkuToken): not
//     a token the style holds, not a dictionary code other than the colourway's own family.
func decideNewColorwaySkuToken(ctx context.Context, db dependency.DB, styleID int, prd *entity.ColorwayInsert, dev *entity.ColorwayDevelopmentPatch) (string, error) {
	family := prd.ProductBodyInsert.ColorCode
	taken, err := styleSkuTokens(ctx, db, styleID, 0)
	if err != nil {
		return "", err
	}
	if dev.MainColour() == nil {
		if !taken[family] && entity.IsValidSkuColorToken(family) {
			return family, nil
		}
		if prd.RefuseTakenColourToken {
			return "", entity.ErrColorwayColorExists
		}
	}
	codes, err := dictionaryColourCodes(ctx, db)
	if err != nil {
		return "", err
	}
	token := entity.MintColorwaySkuToken(colorwayTokenName(prd, dev), family, codes, taken)
	if !entity.IsValidSkuColorToken(token) {
		return "", fmt.Errorf("mint sku colour token for style %d: no free token", styleID)
	}
	return token, nil
}

// pinLegacySkuToken gives a colourway whose token is NULL — a row an older binary inserted during
// a rollback window — the token it has been reading all along (its color_code) BEFORE anything in
// this write can move its family: with the token still NULL, COALESCE(sku_color_token, color_code)
// would silently follow a family change, and the token is immutable. Should another colourway of the
// style already hold that code as its token (only an older binary could have let that happen), a
// token is minted from the name instead. Returns the colourway's token.
func pinLegacySkuToken(ctx context.Context, db dependency.DB, colorwayID, styleID int, colorCode string, token sql.NullString, devName sql.NullString) (string, error) {
	if token.Valid && token.String != "" {
		return token.String, nil
	}
	taken, err := styleSkuTokens(ctx, db, styleID, colorwayID)
	if err != nil {
		return "", err
	}
	pin := colorCode
	if taken[pin] || !entity.IsValidSkuColorToken(pin) {
		codes, err := dictionaryColourCodes(ctx, db)
		if err != nil {
			return "", err
		}
		name := devName.String
		if strings.TrimSpace(name) == "" {
			name = colorCode
		}
		pin = entity.MintColorwaySkuToken(name, colorCode, codes, taken)
	}
	if err := storeutil.ExecNamed(ctx, db, pinLegacySkuTokenQuery,
		map[string]any{"token": pin, "id": colorwayID}); err != nil {
		return "", fmt.Errorf("pin sku colour token of colourway %d: %w", colorwayID, err)
	}
	return pin, nil
}

// colorwayHasPalette reports whether the colourway carries a palette (any product_colour row).
func colorwayHasPalette(ctx context.Context, db dependency.DB, colorwayID int) (bool, error) {
	row, err := storeutil.QueryNamedOne[struct {
		N int `db:"n"`
	}](ctx, db, colorwayHasPaletteQuery, map[string]any{"id": colorwayID})
	if err != nil {
		return false, fmt.Errorf("check palette of colourway %d: %w", colorwayID, err)
	}
	return row.N > 0, nil
}

// applyColorwayDevelopmentBlock applies a whole development patch in the order the invariants need:
// the scalar merge first (lab dip, name, pantone …), then the palette (replace + mirror of the main
// colour, which therefore wins over a scalar sent alongside it), then the name translations.
//
// On a colourway that HAS a palette, pantone / pantone_system / dev_hex are the palette's mirror; a
// patch that would change them without also writing the palette is refused (field violation) rather
// than left to diverge from colours[0] — or be silently overwritten by it.
func applyColorwayDevelopmentBlock(ctx context.Context, db dependency.DB, colorwayID int, patch *entity.ColorwayDevelopmentPatch) error {
	if patch.IsEmpty() {
		return nil
	}
	if patch.HasScalars() {
		guardMirror := false
		if patch.Colours == nil && (patch.Pantone != nil || patch.PantoneSystem != nil || patch.DevHex != nil) {
			has, err := colorwayHasPalette(ctx, db, colorwayID)
			if err != nil {
				return err
			}
			guardMirror = has
		}
		if err := applyColorwayDevelopment(ctx, db, colorwayID, patch, guardMirror); err != nil {
			return err
		}
	}
	if err := applyColorwayPalette(ctx, db, colorwayID, patch.Colours); err != nil {
		return err
	}
	return applyColorwayNameI18n(ctx, db, colorwayID, patch.NameI18n)
}

// applyColorwayPalette replaces the colourway's palette by position and mirrors the main colour into
// product.pantone / pantone_system / dev_hex, the columns every pre-T45 reader takes a colourway's
// colour from. nil = leave the palette alone. The per-slot recipe is NOT touched (owner's decision
// 7): that is ApplyColorwayPaletteToSlots.
func applyColorwayPalette(ctx context.Context, db dependency.DB, colorwayID int, colours []entity.ColorwayColour) error {
	if colours == nil {
		return nil
	}
	if len(colours) == 0 || len(colours) > entity.ColorwayPaletteMaxColours {
		return fmt.Errorf("colourway %d palette of %d colours: the caller must validate 1…%d",
			colorwayID, len(colours), entity.ColorwayPaletteMaxColours)
	}
	if err := storeutil.ExecNamed(ctx, db, clearColorwayPaletteQuery,
		map[string]any{"id": colorwayID}); err != nil {
		return fmt.Errorf("clear palette of colourway %d: %w", colorwayID, err)
	}
	rows := make([][]any, 0, len(colours))
	for i, c := range colours {
		rows = append(rows, []any{colorwayID, i, nullableString(c.Label), nullableString(c.Hex),
			nullableString(c.Pantone), nullableString(c.PantoneSystem)})
	}
	if err := storeutil.BulkInsertRows(ctx, db, "product_colour",
		[]string{"product_id", "position", "label", "hex", "pantone", "pantone_system"}, rows); err != nil {
		return fmt.Errorf("write palette of colourway %d: %w", colorwayID, err)
	}
	main := colours[0]
	if err := storeutil.ExecNamed(ctx, db, mirrorMainColourQuery, map[string]any{
		"id":             colorwayID,
		"pantone":        nullableString(main.Pantone),
		"pantone_system": nullableString(main.PantoneSystem),
		"dev_hex":        nullableString(main.Hex),
	}); err != nil {
		return fmt.Errorf("mirror main colour of colourway %d: %w", colorwayID, err)
	}
	return nil
}

// applyColorwayNameI18n upserts the per-language colourway names: a non-empty name writes its
// language, "" deletes it (the reader falls back to the operator's name). An id the language table
// does not hold is refused with a field violation instead of a raw FK error.
func applyColorwayNameI18n(ctx context.Context, db dependency.DB, colorwayID int, names map[int]string) error {
	if len(names) == 0 {
		return nil
	}
	langs := make([]int, 0, len(names))
	for lang := range names {
		langs = append(langs, lang)
	}
	sort.Ints(langs)
	known, err := storeutil.QueryListNamed[struct {
		ID int `db:"id"`
	}](ctx, db, knownLanguagesQuery, map[string]any{"ids": langs})
	if err != nil {
		return fmt.Errorf("check languages of colourway %d names: %w", colorwayID, err)
	}
	isKnown := make(map[int]bool, len(known))
	for _, k := range known {
		isKnown[k.ID] = true
	}
	for _, lang := range langs {
		if !isKnown[lang] {
			return entity.NewFieldViolation(fmt.Sprintf("development.name_i18n[%d]", lang), "unknown_language", "",
				"key the translation by a language id from the dictionary")
		}
	}
	for _, lang := range langs {
		name := names[lang]
		params := map[string]any{"id": colorwayID, "lang": lang, "name": name}
		if name == "" {
			if err := storeutil.ExecNamed(ctx, db, deleteColorwayNameI18nQuery, params); err != nil {
				return fmt.Errorf("delete colourway %d name in language %d: %w", colorwayID, lang, err)
			}
			continue
		}
		if err := storeutil.ExecNamed(ctx, db, upsertColorwayNameI18nQuery, params); err != nil {
			return fmt.Errorf("write colourway %d name in language %d: %w", colorwayID, lang, err)
		}
	}
	return nil
}

// refreshColorwayDisplayName recomputes product.color, the name every legacy reader prints (orders,
// lays, the run pack, the storefront cart): the colourway's own name once it carries a palette, the
// family's dictionary name otherwise — so a legacy single-colour colourway reads exactly what it
// read before T45, whatever its dev name. Runs after the development block and the merchandising
// row of the same write.
func refreshColorwayDisplayName(ctx context.Context, db dependency.DB, colorwayID int) error {
	if err := storeutil.ExecNamed(ctx, db, refreshColorwayDisplayNameQuery, map[string]any{"id": colorwayID}); err != nil {
		return fmt.Errorf("refresh display name of colourway %d: %w", colorwayID, err)
	}
	return nil
}

// ColorwayPalettesByID loads the palettes of several colourways, position order.
func ColorwayPalettesByID(ctx context.Context, db dependency.DB, ids []int) (map[int][]entity.ColorwayColour, error) {
	out := make(map[int][]entity.ColorwayColour, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[colorwayColourRow](ctx, db, colorwayPaletteQuery, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("load colourway palettes: %w", err)
	}
	for _, r := range rows {
		out[r.ProductID] = append(out[r.ProductID], entity.ColorwayColour{
			Label: r.Label, Hex: r.Hex, Pantone: r.Pantone, PantoneSystem: r.PantoneSystem,
		})
	}
	return out, nil
}

// ColorwayNameI18nByID loads the per-language names of several colourways.
func ColorwayNameI18nByID(ctx context.Context, db dependency.DB, ids []int) (map[int]map[int]string, error) {
	out := make(map[int]map[int]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[colorwayNameI18nRow](ctx, db, colorwayNameI18nQuery, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("load colourway name translations: %w", err)
	}
	for _, r := range rows {
		if out[r.ProductID] == nil {
			out[r.ProductID] = map[int]string{}
		}
		out[r.ProductID][r.LanguageID] = r.Name
	}
	return out, nil
}

type colorwayColourRow struct {
	ProductID     int    `db:"product_id"`
	Position      int    `db:"position"`
	Label         string `db:"label"`
	Hex           string `db:"hex"`
	Pantone       string `db:"pantone"`
	PantoneSystem string `db:"pantone_system"`
}

type colorwayNameI18nRow struct {
	ProductID  int    `db:"product_id"`
	LanguageID int    `db:"language_id"`
	Name       string `db:"name"`
}

// Every statement of this file is a package constant so the DB-free binding test
// (colorway_palette_bind_test.go) can hold it: a stray colon in a named query fails at bind time,
// not at compile time, and nothing but a MySQL-backed run would otherwise see it.
const (
	styleSkuTokensQuery = `
		SELECT COALESCE(sku_color_token, color_code) AS token
		FROM product
		WHERE style_id = :sid AND id <> :exclude`
	dictionaryColourCodesQuery = `SELECT code FROM color ORDER BY code`
	pinLegacySkuTokenQuery     = `UPDATE product SET sku_color_token = :token WHERE id = :id AND sku_color_token IS NULL`
	colorwayHasPaletteQuery    = `SELECT COUNT(*) AS n FROM product_colour WHERE product_id = :id`
	clearColorwayPaletteQuery  = `DELETE FROM product_colour WHERE product_id = :id`
	mirrorMainColourQuery      = `
		UPDATE product SET pantone = :pantone, pantone_system = :pantone_system, dev_hex = :dev_hex
		WHERE id = :id`
	knownLanguagesQuery         = `SELECT id FROM language WHERE id IN (:ids)`
	deleteColorwayNameI18nQuery = `DELETE FROM product_colour_name_i18n WHERE product_id = :id AND language_id = :lang`
	upsertColorwayNameI18nQuery = `
		INSERT INTO product_colour_name_i18n (product_id, language_id, name)
		VALUES (:id, :lang, :name)
		ON DUPLICATE KEY UPDATE name = VALUES(name)`
	// product.color: the colourway's own name once it carries a palette, the family's dictionary
	// name otherwise (see refreshColorwayDisplayName). updated_at moves with it on purpose — the
	// display name did change.
	refreshColorwayDisplayNameQuery = `
		UPDATE product p
		JOIN color c ON c.code = p.color_code
		SET p.color = CASE
			WHEN EXISTS (SELECT 1 FROM product_colour pc WHERE pc.product_id = p.id)
			     AND NULLIF(TRIM(p.dev_name), '') IS NOT NULL
			THEN TRIM(p.dev_name)
			ELSE c.name
		END
		WHERE p.id = :id`

	colorwayPaletteQuery = `
		SELECT product_id, position, COALESCE(label, '') AS label, COALESCE(hex, '') AS hex,
		       COALESCE(pantone, '') AS pantone, COALESCE(pantone_system, '') AS pantone_system
		FROM product_colour
		WHERE product_id IN (:ids)
		ORDER BY product_id, position`
	colorwayNameI18nQuery = `
		SELECT product_id, language_id, name
		FROM product_colour_name_i18n
		WHERE product_id IN (:ids)
		ORDER BY product_id, language_id`
)
