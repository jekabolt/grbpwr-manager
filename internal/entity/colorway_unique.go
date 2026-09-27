package entity

import (
	"regexp"
	"strings"
)

// ─── T45: which per-style uniqueness refused a colourway write ───
//
// A style's colourways are kept apart by two unique indexes on product, and between the two T45
// pushes (D-69) BOTH are live:
//
//   - uniq_product_style_sku_color_token (0376) — the SKU colour token, for good;
//   - uniq_product_style_color (0151) — the dictionary family, until 0377 drops it. While it
//     stands, a second colourway of one family in a style is refused by MySQL (1062).
//
// Either refusal is the operator's to resolve, never an Internal: both are ErrColorwayColorExists
// under errors.Is (the answer every caller already gives «this colour is already on the style»),
// and each is its own sentinel so a caller that must say WHICH — the relink, the archive press —
// can.

// Index names of the per-style colourway uniqueness on product. The migrations own them; the
// migrationlint guards pin them to the files.
const (
	ColorwayFamilyUniqueIndex   = "uniq_product_style_color"
	ColorwaySkuTokenUniqueIndex = "uniq_product_style_sku_color_token"
)

// colorwayExistsError is a refusal that IS ErrColorwayColorExists for errors.Is while keeping its
// own identity and words.
type colorwayExistsError struct{ msg string }

func (e *colorwayExistsError) Error() string { return e.msg }

// Is makes every colorwayExistsError answer errors.Is(err, ErrColorwayColorExists).
func (e *colorwayExistsError) Is(target error) bool { return target == ErrColorwayColorExists }

var (
	// ErrColorwaySkuTokenTaken — the style already holds a colourway with this SKU colour token
	// (uniq_product_style_sku_color_token, or the store's own look before it). Tokens are
	// immutable, so the way out is on the OTHER colourway: a relink needs a target style without
	// that token, an archive restore finds the colour already there.
	ErrColorwaySkuTokenTaken error = &colorwayExistsError{"a colourway with this SKU colour token already exists for the style"}
	// ErrColorwayFamilyTaken — the style already holds a colourway of this dictionary family and
	// this base still keeps one colourway per family in a style: uniq_product_style_color stands
	// until migration 0377 drops it (the second T45 push). Worded as the pre-T45 «exists» on
	// purpose — that is exactly what it is on such a base.
	ErrColorwayFamilyTaken error = &colorwayExistsError{"a colourway with this colour already exists for the style"}
)

// duplicateKeyRe lifts the index name out of a MySQL duplicate-key message (1062 ER_DUP_ENTRY):
// «Duplicate entry '12-BKW' for key 'product.uniq_product_style_sku_color_token'». MySQL 8
// qualifies the name with the table, 5.7 does not.
var duplicateKeyRe = regexp.MustCompile(`(?s)Duplicate entry '.*' for key '([^']+)'`)

// ColorwayDuplicateKey classifies a duplicate-key error on one of product's per-style colourway
// indexes: ErrColorwaySkuTokenTaken for the token, ErrColorwayFamilyTaken for the family, nil for
// anything else — including another index's duplicate, which is not this function's to name.
// The match is on the EXACT index name: a guess from a substring («color») would have named the
// family for the token and sent the operator after the wrong colourway.
func ColorwayDuplicateKey(err error) error {
	if err == nil {
		return nil
	}
	m := duplicateKeyRe.FindStringSubmatch(err.Error())
	if len(m) != 2 {
		return nil
	}
	name := m[1]
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case ColorwaySkuTokenUniqueIndex:
		return ErrColorwaySkuTokenTaken
	case ColorwayFamilyUniqueIndex:
		return ErrColorwayFamilyTaken
	}
	return nil
}
