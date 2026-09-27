package product

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// T45 round 2 — the store's decisions about a colourway's SKU token, its relink and its name, each
// run against a scripted database (colorway_t45_scripted_db_test.go). What these prove is WHICH
// statements run and what the answer is; the container probe
// (internal/store/colourway_t45_integration_test.go) proves the same against MySQL.

func t45Insert(family, name string) *entity.ColorwayInsert {
	return &entity.ColorwayInsert{ProductBodyInsert: entity.ColorwayBodyInsert{ColorCode: family, Color: name}}
}

func t45RequireField(t *testing.T, err error, field, reason string) {
	t.Helper()
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a field violation, got %v", err)
	}
	if ve.Field != field || ve.Reason != reason {
		t.Fatalf("violation %s/%s, want %s/%s: %s", ve.Field, ve.Reason, field, reason, ve.Message)
	}
}

// The archive press restores the token it carried — verbatim when free, refused when the style
// holds it, and never re-minted: a re-mint would create a second colourway of the same colour.
// MUTATION: mint instead of refusing on a held restore — the second case gets a token, not an error.
func TestDecideNewColorwaySkuTokenRestoresOrRefuses(t *testing.T) {
	ctx := context.Background()

	db, script := t45DB(t, t45Tokens("BLK"))
	prd := t45Insert("WHT", "white")
	prd.RestoreSkuColorToken = " bkw "
	token, err := decideNewColorwaySkuToken(ctx, db, 12, prd, nil)
	if err != nil || token != "BKW" {
		t.Fatalf("a free restore is taken verbatim (normalised): %q, %v", token, err)
	}
	script.done() // and the dictionary was never read: nothing was minted

	db, script = t45DB(t, t45Tokens("BLK", "BKW"))
	prd = t45Insert("BLK", "black")
	prd.RestoreSkuColorToken = "BKW"
	_, err = decideNewColorwaySkuToken(ctx, db, 12, prd, nil)
	if !errors.Is(err, entity.ErrColorwaySkuTokenTaken) || !errors.Is(err, entity.ErrColorwayColorExists) {
		t.Fatalf("a held restore is the token «exists», got %v", err)
	}
	if !strings.Contains(err.Error(), "BKW") || !strings.Contains(err.Error(), "style 12") {
		t.Fatalf("the refusal names the token and the style: %v", err)
	}
	script.done()

	db, _ = t45DB(t, t45Tokens())
	prd = t45Insert("BLK", "black")
	prd.RestoreSkuColorToken = "BK"
	_, err = decideNewColorwaySkuToken(ctx, db, 12, prd, nil)
	t45RequireField(t, err, "merchandising.sku_color_token", "invalid_token")
}

// Without a restore: no palette keeps the family while it is free and mints from the name when it
// is not; a palette always mints from the name. MUTATION: return the family even when it is taken —
// the second case answers BLK twice in one style.
func TestDecideNewColorwaySkuTokenMintsWhenItMust(t *testing.T) {
	ctx := context.Background()

	db, script := t45DB(t, t45Tokens("WHT"))
	token, err := decideNewColorwaySkuToken(ctx, db, 12, t45Insert("BLK", "black"), nil)
	if err != nil || token != "BLK" {
		t.Fatalf("a free family is the token: %q, %v", token, err)
	}
	script.done()

	db, script = t45DB(t, t45Tokens("BLK"), t45Dictionary())
	token, err = decideNewColorwaySkuToken(ctx, db, 12, t45Insert("BLK", "black"), nil)
	if err != nil || token != "BLC" {
		t.Fatalf("a taken family mints from the name: %q, %v", token, err)
	}
	script.done()

	name := "Black and White"
	db, script = t45DB(t, t45Tokens(), t45Dictionary())
	token, err = decideNewColorwaySkuToken(ctx, db, 12, t45Insert("BLK", "black"),
		&entity.ColorwayDevelopmentPatch{Name: &name, Colours: []entity.ColorwayColour{{Label: "black"}, {Label: "white"}}})
	if err != nil || token != "BKW" {
		t.Fatalf("a palette mints from its name even when the family is free: %q, %v", token, err)
	}
	script.done()
}

// A relink into a style that holds the token is refused by the look, and the refusal names the
// token, the target and the colourway in the way. MUTATION: drop the holder check — the case gets
// nil.
func TestRefuseRelinkOntoHeldToken(t *testing.T) {
	ctx := context.Background()
	holder := t45Step{match: "COALESCE(sku_color_token, color_code) = ?", cols: []string{"id"}, rows: [][]driver.Value{{int64(55)}}}
	db, script := t45DB(t, holder)
	err := refuseRelinkOntoHeldToken(ctx, db, 41, 9, "BKW")
	if !errors.Is(err, entity.ErrColorwaySkuTokenTaken) || !errors.Is(err, entity.ErrColorwayColorExists) {
		t.Fatalf("a held token refuses the relink, got %v", err)
	}
	for _, want := range []string{"BKW", "colourway 55", "style 9"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q: %v", want, err)
		}
	}
	script.done()

	db, script = t45DB(t, t45Step{match: "COALESCE(sku_color_token, color_code) = ?", cols: []string{"id"}})
	if err := refuseRelinkOntoHeldToken(ctx, db, 41, 9, "BKW"); err != nil {
		t.Fatalf("a free token passes: %v", err)
	}
	script.done()
}

// A palette colourway keeps a name: an update that brings a palette to a nameless colourway, or
// clears the name of one that has a palette, is refused — and the stored palette is read only when
// the answer depends on it. MUTATION: skip the stored-palette read — clearing the name of a palette
// colourway passes.
func TestCheckPaletteNameOnUpdate(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	palette := []entity.ColorwayColour{{Label: "bone"}}
	stored := func(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

	for _, tc := range []struct {
		name   string
		stored string
		dev    *entity.ColorwayDevelopmentPatch
		steps  []t45Step
		refuse bool
	}{
		{"no development", "", nil, nil, false},
		{"a lab-dip scalar", "", &entity.ColorwayDevelopmentPatch{Comment: str("x")}, nil, false},
		{"a first palette without a name", "", &entity.ColorwayDevelopmentPatch{Colours: palette}, nil, true},
		{"a first palette with a blank name", "", &entity.ColorwayDevelopmentPatch{Name: str("  "), Colours: palette}, nil, true},
		{"a palette, the stored name stands", "Bone", &entity.ColorwayDevelopmentPatch{Colours: palette}, nil, false},
		{"a palette with its name", "", &entity.ColorwayDevelopmentPatch{Name: str("Bone"), Colours: palette}, nil, false},
		{"a rename", "Bone", &entity.ColorwayDevelopmentPatch{Name: str("Ivory")}, nil, false},
		{"clearing the name of a palette colourway", "Bone", &entity.ColorwayDevelopmentPatch{Name: str("")}, []t45Step{t45Count(2)}, true},
		{"blanking the name of a palette colourway", "Bone", &entity.ColorwayDevelopmentPatch{Name: str(" ")}, []t45Step{t45Count(1)}, true},
		{"clearing the name of a legacy colourway", "Bone", &entity.ColorwayDevelopmentPatch{Name: str("")}, []t45Step{t45Count(0)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, script := t45DB(t, tc.steps...)
			err := checkPaletteNameOnUpdate(ctx, db, 41, stored(tc.stored), tc.dev)
			if tc.refuse {
				t45RequireField(t, err, "development.name", "name_required_with_palette")
			} else if err != nil {
				t.Fatalf("refused: %v", err)
			}
			script.done()
		})
	}
}

// The backstop names what MySQL refused: the token index is the token, the family index — until
// 0377 — the family, anything else stays the error it was. The raw error stays in the chain.
// MUTATION: classify by «contains color» — the family case is named as the token.
func TestColorwayDuplicateNamesTheIndex(t *testing.T) {
	raw := t45Dup(entity.ColorwaySkuTokenUniqueIndex)
	err := colorwayDuplicate(raw, 12, "BKW", "BLK")
	if !errors.Is(err, entity.ErrColorwaySkuTokenTaken) || errors.Is(err, entity.ErrColorwayFamilyTaken) || !errors.Is(err, raw) {
		t.Fatalf("the token index is the token: %v", err)
	}
	if !strings.Contains(err.Error(), "BKW") || !strings.Contains(err.Error(), "style 12") {
		t.Fatalf("names the token and the style: %v", err)
	}

	raw = t45Dup(entity.ColorwayFamilyUniqueIndex)
	err = colorwayDuplicate(raw, 12, "BKW", "BLK")
	if !errors.Is(err, entity.ErrColorwayFamilyTaken) || errors.Is(err, entity.ErrColorwaySkuTokenTaken) || !errors.Is(err, entity.ErrColorwayColorExists) {
		t.Fatalf("the family index is the family: %v", err)
	}
	if !strings.Contains(err.Error(), "BLK family") || !strings.Contains(err.Error(), "style 12") {
		t.Fatalf("names the family and the style: %v", err)
	}

	raw = t45Dup("sku")
	if err := colorwayDuplicate(raw, 12, "BKW", "BLK"); err != raw {
		t.Fatalf("another index is not ours to name: %v", err)
	}
}
