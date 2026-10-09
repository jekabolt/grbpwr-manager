package dto

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// Labels rework (0386): parse / convert / digest of the composition label record, the garment
// labels and the packaging items.

func requireLabelFieldViolation(t *testing.T, err error, field string) {
	t.Helper()
	require.Error(t, err)
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "want a field violation, got %v", err)
	require.Equal(t, field, ve.Field)
}

func TestParseGarmentLabelsRefusesBadInput(t *testing.T) {
	cases := []struct {
		name  string
		in    *pb_common.TechCardGarmentLabel
		field string
	}{
		{"empty key", &pb_common.TechCardGarmentLabel{Key: "  "}, "garment_labels[0].key"},
		{"key over 64", &pb_common.TechCardGarmentLabel{Key: strings.Repeat("я", 65)}, "garment_labels[0].key"},
		{"duplicate media", &pb_common.TechCardGarmentLabel{Key: "brand", MediaIds: []int32{5, 6, 5}}, "garment_labels[0].media_ids[2]"},
		{"non-positive media", &pb_common.TechCardGarmentLabel{Key: "brand", MediaIds: []int32{0}}, "garment_labels[0].media_ids[0]"},
		{"negative qty", &pb_common.TechCardGarmentLabel{Key: "brand", QtyPerGarment: -1}, "garment_labels[0].qty_per_garment"},
		{"negative bom", &pb_common.TechCardGarmentLabel{Key: "brand", BomItemId: -3}, "garment_labels[0].bom_item_id"},
		{"placement over 255", &pb_common.TechCardGarmentLabel{Key: "brand", Placement: strings.Repeat("x", 256)}, "garment_labels[0].placement"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{c.in})
			requireLabelFieldViolation(t, err, c.field)
		})
	}
}

func TestParseGarmentLabelsAcceptsAndDefaults(t *testing.T) {
	// 64 Cyrillic characters are 128 bytes: the limit is characters, like VARCHAR(64).
	key := strings.Repeat("я", 64)
	out, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: key, Placement: "neckline centre", MediaIds: []int32{9, 3}},
		{Key: "hangtag", QtyPerGarment: 2, BomItemId: 17},
	})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, 1, out[0].QtyPerGarment, "0 on the wire is one per garment")
	require.Equal(t, []int{9, 3}, out[0].MediaIds, "mockup order is display order")
	require.False(t, out[0].BomItemId.Valid)
	require.Equal(t, sql.NullInt32{Int32: 17, Valid: true}, out[1].BomItemId)
	require.Nil(t, out[1].MediaIds)
}

func TestParsePackagingItemsRefusesBadInput(t *testing.T) {
	_, err := parseTechCardPackagingItems([]*pb_common.TechCardPackagingItem{{Key: "polybag"}, {Key: ""}})
	requireLabelFieldViolation(t, err, "packaging_items[1].key")
	_, err = parseTechCardPackagingItems([]*pb_common.TechCardPackagingItem{{Key: "tissue", MediaIds: []int32{1, 1}}})
	requireLabelFieldViolation(t, err, "packaging_items[0].media_ids[1]")
	_, err = parseTechCardPackagingItems([]*pb_common.TechCardPackagingItem{{Key: "tissue", Usage: strings.Repeat("u", 256)}})
	requireLabelFieldViolation(t, err, "packaging_items[0].usage")
}

func TestParseCareLabelRefusesBadInput(t *testing.T) {
	fib := func(part pb_common.TechCardBomLabelPart, code string, pct int32) *pb_common.TechCardCareLabelFiber {
		return &pb_common.TechCardCareLabelFiber{Part: part, FiberCode: code, Pct: pct}
	}
	shell := pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_SHELL
	cases := []struct {
		name  string
		in    *pb_common.TechCardCareLabel
		field string
	}{
		{"pct 0", &pb_common.TechCardCareLabel{Colorways: []*pb_common.TechCardCareLabelColorway{
			{ColorwayId: 1, Fibers: []*pb_common.TechCardCareLabelFiber{fib(shell, "COT", 0)}}}},
			"care_label.colorways[0].fibers[0].pct"},
		{"pct 101", &pb_common.TechCardCareLabel{Colorways: []*pb_common.TechCardCareLabelColorway{
			{ColorwayId: 1, Fibers: []*pb_common.TechCardCareLabelFiber{fib(shell, "COT", 101)}}}},
			"care_label.colorways[0].fibers[0].pct"},
		{"part unspecified", &pb_common.TechCardCareLabel{Colorways: []*pb_common.TechCardCareLabelColorway{
			{ColorwayId: 1, Fibers: []*pb_common.TechCardCareLabelFiber{fib(0, "COT", 100)}}}},
			"care_label.colorways[0].fibers[0].part"},
		{"part not on label", &pb_common.TechCardCareLabel{Colorways: []*pb_common.TechCardCareLabelColorway{
			{ColorwayId: 1, Fibers: []*pb_common.TechCardCareLabelFiber{
				fib(pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_NOT_ON_LABEL, "COT", 100)}}}},
			"care_label.colorways[0].fibers[0].part"},
		{"duplicate fibre in a part", &pb_common.TechCardCareLabel{Colorways: []*pb_common.TechCardCareLabelColorway{
			{ColorwayId: 1, Fibers: []*pb_common.TechCardCareLabelFiber{fib(shell, "COT", 50), fib(shell, "COT", 50)}}}},
			"care_label.colorways[0].fibers[1].fiber_code"},
		{"duplicate colourway", &pb_common.TechCardCareLabel{Colorways: []*pb_common.TechCardCareLabelColorway{
			{ColorwayId: 4, ColourName: "BLACK"}, {ColorwayId: 4, ColourName: "COAL"}}},
			"care_label.colorways[1].colorway_id"},
		{"no colourway id", &pb_common.TechCardCareLabel{Colorways: []*pb_common.TechCardCareLabelColorway{{ColourName: "BLACK"}}},
			"care_label.colorways[0].colorway_id"},
		{"unknown qr preset", &pb_common.TechCardCareLabel{QrPreset: "dynamic"}, "care_label.qr_preset"},
		{"newline inside a line", &pb_common.TechCardCareLabel{AddressLines: []string{"GRBPWR\nLIMITED"}}, "care_label.address_lines[0]"},
		{"negative logo", &pb_common.TechCardCareLabel{LogoMediaId: -1}, "care_label.logo_media_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseTechCardCareLabel(c.in)
			requireLabelFieldViolation(t, err, c.field)
		})
	}
}

func TestParseCareLabelNormalises(t *testing.T) {
	got, err := parseTechCardCareLabel(nil)
	require.NoError(t, err)
	require.Nil(t, got, "absent record = keep the stored one")

	got, err = parseTechCardCareLabel(&pb_common.TechCardCareLabel{
		Colorways: []*pb_common.TechCardCareLabelColorway{
			{ColorwayId: 9, ColourName: "BLACK"},
			{ColorwayId: 3}, // overrides nothing: dropped
			{ColorwayId: 5, Fibers: []*pb_common.TechCardCareLabelFiber{{
				Part: pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_SHELL, FiberCode: "COT", Pct: 100}}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, entity.CareLabelQRStorefront, got.QRPreset, `"" is the storefront preset`)
	require.Len(t, got.Colorways, 2)
	require.Equal(t, 5, got.Colorways[0].ColorwayId, "sorted by colourway id")
	require.Equal(t, 9, got.Colorways[1].ColorwayId)
	require.Equal(t, entity.BomLabelPartShell, got.Colorways[0].Fibers[0].Part)
}

// Round trip pb → entity → pb keeps every field of the three sections.
func TestLabelsReworkRoundTrip(t *testing.T) {
	in := &pb_common.TechCardInsert{
		CareLabel: &pb_common.TechCardCareLabel{
			LogoMediaId: 44, CareProseLines: []string{"MACHINE WASH COLD"}, QrPreset: "fixed",
			QrTemplate: "https://grbpwr.com/x", BackCaptionLines: []string{"SCAN", "ME"}, AddressLines: []string{"A", "B", "C"},
			Colorways: []*pb_common.TechCardCareLabelColorway{{ColorwayId: 7, ColourName: "BLACK",
				Fibers: []*pb_common.TechCardCareLabelFiber{{Part: pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_SHELL, FiberCode: "COT", Pct: 100}}}},
		},
		GarmentLabels: []*pb_common.TechCardGarmentLabel{{Key: "brand", Placement: "neck", Attachment: "sewn", Folding: "end fold",
			Size: "15x60", QtyPerGarment: 1, BomItemId: 3, Note: "n", MediaIds: []int32{8, 2}}},
		PackagingItems: []*pb_common.TechCardPackagingItem{{Key: "polybag", Usage: "each", Packing: "folded", Size: "30x40",
			QtyPerGarment: 2, BomItemId: 4, Note: "m", MediaIds: []int32{6}}},
	}
	care, err := parseTechCardCareLabel(in.CareLabel)
	require.NoError(t, err)
	gl, err := parseTechCardGarmentLabels(in.GarmentLabels)
	require.NoError(t, err)
	pi, err := parseTechCardPackagingItems(in.PackagingItems)
	require.NoError(t, err)

	require.Equal(t, in.CareLabel.String(), techCardCareLabelToPb(care).String())
	require.Equal(t, in.GarmentLabels[0].String(), techCardGarmentLabelsToPb(gl)[0].String())
	require.Equal(t, in.PackagingItems[0].String(), techCardPackagingItemsToPb(pi)[0].String())
}

// --- digest --------------------------------------------------------------------------------------

func labelsDigest(tc *entity.TechCardInsert) string {
	return TechCardSectionDigests(tc)[entity.SignoffLabels]
}

// A card with no labels hashed `[]` before the wave; a card with no new data must hash the same —
// whether the composition record is absent or present-but-all-default — and the legacy rows no
// longer count (they are neither written nor sent any more).
func TestLabelsDigestUnchangedForACardWithoutNewData(t *testing.T) {
	preWaveEmpty := digestOf([]any{})
	require.Equal(t, preWaveEmpty, labelsDigest(&entity.TechCardInsert{}))
	require.Equal(t, preWaveEmpty, labelsDigest(&entity.TechCardInsert{CareLabel: &entity.TechCardCareLabel{}}))
	require.Equal(t, preWaveEmpty, labelsDigest(&entity.TechCardInsert{
		CareLabel: &entity.TechCardCareLabel{QRPreset: entity.CareLabelQRStorefront}}))
	require.Equal(t, preWaveEmpty, labelsDigest(&entity.TechCardInsert{
		Labels: []entity.TechCardLabel{{LabelType: entity.LabelTypeMain, Content: ns("GRBPWR")}}}),
		"legacy tech_card_label rows left the projection (D-09)")
}

func TestLabelsDigestCoversGarmentLabelsAndCareLabel(t *testing.T) {
	base := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "brand", QtyPerGarment: 1}}}
	d0 := labelsDigest(base)
	require.NotEqual(t, digestOf([]any{}), d0)

	withMockup := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "brand", QtyPerGarment: 1, MediaIds: []int{5}}}}
	require.NotEqual(t, d0, labelsDigest(withMockup))

	// nil and empty mockup lists are one content (write sends [] where read gives nil).
	emptyMedia := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "brand", QtyPerGarment: 1, MediaIds: []int{}}}}
	require.Equal(t, d0, labelsDigest(emptyMedia))

	// BomLineKey is clone transport, not content.
	keyed := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "brand", QtyPerGarment: 1, BomLineKey: "X"}}}
	require.Equal(t, d0, labelsDigest(keyed))

	withCare := &entity.TechCardInsert{GarmentLabels: base.GarmentLabels,
		CareLabel: &entity.TechCardCareLabel{Colorways: []entity.TechCardCareLabelColorway{{ColorwayId: 3, ColourName: ns("BLACK")}}}}
	require.NotEqual(t, d0, labelsDigest(withCare))

	// Colourway order is not content.
	a := &entity.TechCardInsert{CareLabel: &entity.TechCardCareLabel{Colorways: []entity.TechCardCareLabelColorway{
		{ColorwayId: 3, ColourName: ns("A")}, {ColorwayId: 1, ColourName: ns("B")}}}}
	b := &entity.TechCardInsert{CareLabel: &entity.TechCardCareLabel{Colorways: []entity.TechCardCareLabelColorway{
		{ColorwayId: 1, ColourName: ns("B")}, {ColorwayId: 3, ColourName: ns("A")}}}}
	require.Equal(t, labelsDigest(a), labelsDigest(b))
}

// A card without packaging items keeps its frozen PACKAGING bytes; items change them, and an
// items-only card (no carton sheet) is distinct from a card with neither.
func TestPackagingDigestItemsTail(t *testing.T) {
	gold := packagingGoldCard()
	require.Equal(t, packagingGoldDigestHex, packagingDigest(gold), "no items: the frozen hex stands")

	withItems := packagingGoldCard()
	withItems.PackagingItems = []entity.TechCardPackagingItem{{Key: "polybag", QtyPerGarment: 1, MediaIds: []int{4}}}
	require.NotEqual(t, packagingGoldDigestHex, packagingDigest(withItems))

	otherItem := packagingGoldCard()
	otherItem.PackagingItems = []entity.TechCardPackagingItem{{Key: "tissue", QtyPerGarment: 1, MediaIds: []int{4}}}
	require.NotEqual(t, packagingDigest(withItems), packagingDigest(otherItem))

	none := packagingDigest(&entity.TechCardInsert{})
	itemsOnly := packagingDigest(&entity.TechCardInsert{PackagingItems: withItems.PackagingItems})
	require.NotEqual(t, none, itemsOnly)
	// Absent sheet and blank sheet are one content with items too.
	require.Equal(t, itemsOnly, packagingDigest(&entity.TechCardInsert{
		Packaging: &entity.TechCardPackaging{}, PackagingItems: withItems.PackagingItems}))
	// An empty items list is no items.
	emptyList := packagingGoldCard()
	emptyList.PackagingItems = []entity.TechCardPackagingItem{}
	require.Equal(t, packagingGoldDigestHex, packagingDigest(emptyList))
}

// labels_aware is transport, not content.
func TestLabelsAwareIsNotHashed(t *testing.T) {
	a := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "brand", QtyPerGarment: 1}}}
	b := &entity.TechCardInsert{GarmentLabels: a.GarmentLabels, LabelsAware: true}
	require.Equal(t, TechCardSectionDigests(a), TechCardSectionDigests(b))
}

func TestGarmentLabelsWithoutMockup(t *testing.T) {
	tc := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "brand", MediaIds: []int{1}}, {Key: "hangtag"}, {Key: "tax stamp"},
	}}
	require.Equal(t, []string{"hangtag (#2)", "tax stamp (#3)"}, GarmentLabelsWithoutMockup(tc))
	require.Nil(t, GarmentLabelsWithoutMockup(&entity.TechCardInsert{}))
}
