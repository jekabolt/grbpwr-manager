package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// T45 round 2 — the archive carries a colourway's identity (format 1.1, REVIEW-T45 finding 3).
//
// Before it, colorways.json carried only color_code, and the import took that one value as BOTH the
// family to create under and the token to restore: a colourway «Black and White» (family BLK, token
// BKW) came back as a BLK colourway with a BLK SKU, without its palette and without its names. Now
// the family, the token, the name with its translations and the palette travel separately, the
// press restores the token verbatim and validates the family on its own, and a 1.0 archive reads as
// it always did.
// ─────────────────────────────────────────────────────────────────────────────

// The source card: a palette colourway whose token is not its family, with a name, one translation
// the source's dictionary names and one it cannot, and a legacy colourway whose token is its family.
func tcac45SourceCard() *entity.TechCard {
	card := tcacCard()
	card.Colorways = []entity.TechCardColorway{
		{
			Id: 11, ColorCode: "BLK", SkuColorToken: "BKW", Name: " Black and White ",
			Colours: []entity.ColorwayColour{
				{Label: "black", Pantone: "19-4005", PantoneSystem: "TCX", Hex: "#2B2C30"},
				{Label: "white", Hex: "#F4F5F0"},
			},
			NameI18n: map[int]string{3: "Noir et blanc", 99: "???"},
		},
		{Id: 12, ColorCode: "OLV", SkuColorToken: "OLV"},
	}
	return card
}

// capture records what the press handed to CreateColorway, in order.
type tcac45Created struct {
	prd *entity.ColorwayInsert
	dev *entity.ColorwayDevelopmentPatch
}

func (r *tcacRig) expectCreates(t *testing.T, ids []int, out *[]tcac45Created, errs ...error) {
	t.Helper()
	for i, id := range ids {
		var err error
		if i < len(errs) {
			err = errs[i]
		}
		r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
			mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, _ int, prd *entity.ColorwayInsert, _ []int,
				_ []entity.ColorwayTagInsert, _ []entity.ColorwayPriceInsert, dev *entity.ColorwayDevelopmentPatch) {
				*out = append(*out, tcac45Created{prd: prd, dev: dev})
			}).Return(id, err).Once()
	}
}

// Export → colorways.json → the press, end to end. MUTATIONS: export ColorCode as the token (the
// restore reads BLK); drop the name_i18n remap by code (the translation lands under the SOURCE's
// id 3, which this base does not have); restore with no mask (the lab-dip half is written).
func TestArchiveCarriesAColourwaysIdentityRoundTrip(t *testing.T) {
	// ── export ──
	payloads, holes := collectArchiveColorways(tcac45SourceCard(), map[int]string{}, map[int]string{3: "fr"},
		&archiveSidecars{SizeNames: map[int]string{}})
	require.Len(t, payloads, 2)
	bw, olv := payloads[0], payloads[1]
	require.Equal(t, "BLK", bw.ColorCode, "the family")
	require.Equal(t, "BKW", bw.SkuColorToken, "the token travels on its own")
	require.Equal(t, "Black and White", bw.Name)
	require.Equal(t, map[string]string{"fr": "Noir et blanc"}, bw.NameI18n, "translations travel by language CODE")
	require.Equal(t, []techcardarchive.ColourLine{
		{Label: "black", Hex: "#2B2C30", Pantone: "19-4005", PantoneSystem: "TCX"},
		{Label: "white", Hex: "#F4F5F0"},
	}, bw.Colours, "the palette, main colour first")
	require.Equal(t, "OLV", olv.SkuColorToken)
	require.Empty(t, olv.Name)
	require.Nil(t, olv.Colours)
	require.Len(t, holes, 1, "the translation the source's dictionary cannot name is a hole, not a silent drop")
	require.Equal(t, techcardarchive.ReasonLanguageUnknown, holes[0].Reason)
	require.Equal(t, "color_code=BLK,sku_color_token=BKW language_id=99", holes[0].Ref)

	raw, err := json.Marshal(payloads)
	require.NoError(t, err)

	// ── import: a base whose «fr» is language 7, not 3 ──
	r := tcacServer(t)
	refs := []string{"BLK,sku_color_token=BKW", "OLV"}
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).Return(tcacImportRow(t, raw, refs...), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	var created []tcac45Created
	r.expectCreates(t, []int{901, 902}, &created)
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Equal(t, []int32{901, 902}, resp.GetCreatedColorwayIds())
	require.Len(t, created, 2)

	first := created[0]
	require.Equal(t, "BLK", first.prd.ProductBodyInsert.ColorCode, "created under its family")
	require.Equal(t, "BKW", first.prd.RestoreSkuColorToken, "with the token it had — never re-minted")
	require.NotNil(t, first.dev)
	require.Equal(t, "Black and White", *first.dev.Name)
	require.Equal(t, []entity.ColorwayColour{
		{Label: "black", Hex: "#2B2C30", Pantone: "19-4005", PantoneSystem: "TCX"},
		{Label: "white", Hex: "#F4F5F0"},
	}, first.dev.Colours)
	require.Equal(t, map[int]string{7: "Noir et blanc"}, first.dev.NameI18n, "remapped to THIS base's language id")
	require.False(t, first.dev.TouchesLabDip(), "lab dips do not travel")
	require.Nil(t, first.dev.Pantone, "the mirror is the store's to write from the palette")
	require.Nil(t, first.dev.DevCode)
	require.Nil(t, first.dev.Comment)

	second := created[1]
	require.Equal(t, "OLV", second.prd.ProductBodyInsert.ColorCode)
	require.Equal(t, "OLV", second.prd.RestoreSkuColorToken)
	require.Nil(t, second.dev, "a colourway with nothing but its colour creates as it always did")

	rep := resp.GetReport()
	for _, ref := range []string{"color_code=BLK,sku_color_token=BKW", "color_code=OLV"} {
		require.False(t, tcacHasLine(rep, ref, techcardarchive.ReasonColorwaysNotApplied),
			"the commit's line at %q is superseded by the press that created it:\n%s", ref, tcacDumpLines(rep))
	}
	cw := tcrepCounter(t, rep, techcardarchive.EntityColorway)
	require.Equal(t, int32(2), cw.GetImported())
	require.Zero(t, cw.GetSkipped())
	require.Zero(t, cw.GetDegraded())
}

// A 1.0 archive — color_code and nothing else — restores that code as the token and creates with
// no development block, exactly as before T45. MUTATION: restore "" for a 1.0 payload — the store
// would mint instead of answering «exists» on a second press.
func TestApplyImportColorwaysReadsA10ArchiveAsBefore(t *testing.T) {
	raw := tcacPayloadOf(t, techcardarchive.ColorwayPayload{ColorCode: "OLV"})
	require.JSONEq(t, `[{"color_code":"OLV","recipe":null,"piece_materials":null}]`, string(raw),
		"a payload with no identity fields writes none of them: the 1.0 shape")

	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).Return(tcacImportRow(t, raw, "OLV"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	var created []tcac45Created
	r.expectCreates(t, []int{903}, &created)
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Len(t, created, 1)
	require.Equal(t, "OLV", created[0].prd.RestoreSkuColorToken, "a 1.0 archive's colour code was its token")
	require.Nil(t, created[0].dev)
	require.False(t, tcacHasLine(resp.GetReport(), "color_code=OLV", techcardarchive.ReasonColorwaysNotApplied))
}

// What cannot land is left out and said, once the colourway exists: a palette without a name (a
// palette colourway carries its own name), a palette that is not one, a translation into a language
// this base does not have. The colourway itself lands and counts as degraded. MUTATION: record the
// drops before the create — a refused create would leave lines about a colourway that is not there.
func TestApplyImportColorwaysDropsWhatCannotLand(t *testing.T) {
	nameless := techcardarchive.ColorwayPayload{
		ColorCode: "BLK", SkuColorToken: "BKW",
		Colours:  []techcardarchive.ColourLine{{Label: "black"}},
		NameI18n: map[string]string{"xx": "Schwarz", "fr": "Noir"},
	}
	nine := make([]techcardarchive.ColourLine, 9)
	for i := range nine {
		nine[i] = techcardarchive.ColourLine{Label: fmt.Sprintf("c%d", i)}
	}
	crowded := techcardarchive.ColorwayPayload{ColorCode: "OLV", SkuColorToken: "OL9", Name: "Nine", Colours: nine}
	raw := tcacPayloadOf(t, nameless, crowded)

	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, raw, "BLK,sku_color_token=BKW", "OLV,sku_color_token=OL9"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	var created []tcac45Created
	r.expectCreates(t, []int{904, 905}, &created)
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Len(t, created, 2)
	require.Nil(t, created[0].dev.Colours, "a nameless palette is not written")
	require.Nil(t, created[0].dev.Name)
	require.Equal(t, map[int]string{7: "Noir"}, created[0].dev.NameI18n, "the translation this base can hold lands")
	require.Nil(t, created[1].dev.Colours, "nine colours are not a palette")
	require.Equal(t, "Nine", *created[1].dev.Name, "the name still lands")

	rep := resp.GetReport()
	line := tcacLineFor(t, rep, "color_code=BLK,sku_color_token=BKW colours", techcardarchive.ReasonArchiveRowInvalid)
	require.Contains(t, line.GetDetail(), "no name")
	tcacLineFor(t, rep, "color_code=BLK,sku_color_token=BKW language=xx", techcardarchive.ReasonLanguageUnknown)
	line = tcacLineFor(t, rep, "color_code=OLV,sku_color_token=OL9 colours", techcardarchive.ReasonArchiveRowInvalid)
	require.Contains(t, line.GetDetail(), "palette_size")
	cw := tcrepCounter(t, rep, techcardarchive.EntityColorway)
	require.Equal(t, int32(2), cw.GetDegraded(), "both landed, both thinner than the archive")
	require.Zero(t, cw.GetImported())
}

// Between the two T45 pushes the family unique still stands (until 0377): the token is free, the
// family is not. That is NOT «exists» — the card's colourway of that family is a different
// colourway — so it is colorway_not_created, which pressing again (after 0377) cures; and nothing
// is said about the parts of a colourway that was not created. MUTATION: fold ErrColorwayFamilyTaken
// into the «exists» branch — the card is re-read (the strict mock refuses) and the line claims the
// colourway exists.
func TestApplyImportColorwaysBetweenThePushes(t *testing.T) {
	p := techcardarchive.ColorwayPayload{ColorCode: "BLK", SkuColorToken: "BKW", Name: "Black and White",
		NameI18n: map[string]string{"xx": "?"}}
	raw := tcacPayloadOf(t, p)
	card := tcacCard()
	card.Colorways = []entity.TechCardColorway{{Id: 777, ColorCode: "BLK", SkuColorToken: "MDN"}}

	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, raw, "BLK,sku_color_token=BKW"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	var created []tcac45Created
	r.expectCreates(t, []int{0}, &created, fmt.Errorf("%w: style %d already holds a colourway of the BLK family: %w",
		entity.ErrColorwayFamilyTaken, tcacCardID,
		fmt.Errorf("Error 1062 (23000): Duplicate entry '214-BLK' for key 'product.uniq_product_style_color'")))
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Empty(t, resp.GetCreatedColorwayIds())
	require.Equal(t, "BKW", created[0].prd.RestoreSkuColorToken)
	rep := resp.GetReport()
	line := tcacLineFor(t, rep, "color_code=BLK,sku_color_token=BKW", techcardarchive.ReasonColorwayNotCreated)
	require.Contains(t, line.GetDetail(), "BLK family")
	require.Contains(t, line.GetDetail(), "0377")
	require.False(t, tcacHasReason(rep, techcardarchive.ReasonLanguageUnknown),
		"a translation of a colourway that was not created is not news:\n%s", tcacDumpLines(rep))
	require.False(t, tcacHasReason(rep, techcardarchive.ReasonColorwayExists))
	require.Equal(t, int32(1), tcrepCounter(t, rep, techcardarchive.EntityColorway).GetSkipped())
}

// A 1.1 colourway already on the card — same TOKEN, whatever its family now — is standing, not
// created again. MUTATION: key the card's occupancy by family — the create is attempted.
func TestApplyImportColorwaysFindsA11ColourwayByItsToken(t *testing.T) {
	p := techcardarchive.ColorwayPayload{ColorCode: "BLK", SkuColorToken: "BKW", Name: "Black and White"}
	raw := tcacPayloadOf(t, p)
	card := tcacCard()
	card.Colorways = []entity.TechCardColorway{{Id: 778, ColorCode: "OLV", SkuColorToken: "BKW"}}

	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, raw, "BLK,sku_color_token=BKW"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Empty(t, resp.GetCreatedColorwayIds(), "no CreateColorway expectation: the strict mock proves it")
	line := tcacLineFor(t, resp.GetReport(), "color_code=BLK,sku_color_token=BKW", techcardarchive.ReasonColorwayExists)
	require.Contains(t, line.GetDetail(), "778")
}

// The commit's colorways_not_applied line is keyed by the SAME ref the press writes
// (techcardarchive.ColorwayRef): a 1.1 colourway whose token is not its family gets the comma ref,
// a 1.0 one keeps `color_code=…`. MUTATION: build the commit's ref from color_code alone — the
// press no longer finds (and supersedes) the line of the colourway it created.
func TestResolveImportNamesA11ColourwayByItsRef(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.with(techcardarchive.FileColorways, tcimpJSON(t, []techcardarchive.ColorwayPayload{
		{ColorCode: "BLK", SkuColorToken: "BKW", Name: "Black and White"},
		{ColorCode: "OLV"},
	}))
	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	refs := map[string]string{}
	for _, h := range tcimpHoles(res, techcardarchive.ReasonColorwaysNotApplied) {
		refs[h.Ref] = h.Detail
	}
	require.Contains(t, refs, "color_code=BLK,sku_color_token=BKW")
	require.Contains(t, refs, "color_code=OLV")
	require.Contains(t, refs["color_code=BLK,sku_color_token=BKW"], "Black and White (BKW, BLK family)")
	require.Contains(t, refs["color_code=OLV"], `"OLV"`, "a 1.0 colourway reads as it did")
}
