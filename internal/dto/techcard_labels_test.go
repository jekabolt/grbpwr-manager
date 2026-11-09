package dto

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
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
	perSize := true
	source := "make"
	foldLines, stitchLines := "4,11", "0,9"
	in := &pb_common.TechCardInsert{
		CareLabel: &pb_common.TechCardCareLabel{
			LogoMediaId: 44, CareProseLines: []string{"MACHINE WASH COLD"}, QrPreset: "fixed",
			QrTemplate: "https://grbpwr.com/x", BackCaptionLines: []string{"SCAN", "ME"}, AddressLines: []string{"A", "B", "C"},
			Colorways: []*pb_common.TechCardCareLabelColorway{{ColorwayId: 7, ColourName: "BLACK",
				Fibers: []*pb_common.TechCardCareLabelFiber{{Part: pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_SHELL, FiberCode: "COT", Pct: 100}}}},
			Orientation: "horizontal",
		},
		GarmentLabels: []*pb_common.TechCardGarmentLabel{{Key: "brand", Placement: "neck", Attachment: "sewn", Folding: "end fold",
			Size: "15x60", QtyPerGarment: 1, BomItemId: 3, Note: "n", MediaIds: []int32{8, 2},
			BomLineKey: "k1", CalloutNumber: 4, ArtworkPerSize: &perSize, ArtworkPerColorway: true,
			Source: &source, Kind: "brand", MadeBy: "printed", PrintMethod: "screen", LengthCm: &pb_decimal.Decimal{Value: "6.5"},
			LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ1", WidthMm: &pb_decimal.Decimal{Value: "60.25"},
			HeightMm: &pb_decimal.Decimal{Value: "15"}, Fold: "ends", FoldLinesMm: &foldLines, StitchLinesMm: &stitchLines,
			Files: []*pb_common.TechCardGarmentLabelFile{{ColorwayId: 7, SizeId: 2, LibraryFileId: 9, SourceHash: "h"}}}},
		PackagingItems: []*pb_common.TechCardPackagingItem{{Key: "polybag", Usage: "each", Packing: "folded", Size: "30x40",
			QtyPerGarment: 2, BomItemId: 4, Note: "m", MediaIds: []int32{6}, BomLineKey: "k2"}},
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

// --- sewn-in labels (0447) --------------------------------------------------------------------------

func artworkLabel() entity.TechCardGarmentLabel {
	return entity.TechCardGarmentLabel{Key: "size", QtyPerGarment: 1}
}

// A label with no pin, no modes and no files hashes exactly as before 0447: no old signature goes stale.
func TestLabelsDigestFrozenWithoutArtwork(t *testing.T) {
	legacy := digestOf([]any{[]any{"size", "", "", "", "", 1, int32(0), "", digestList([]int(nil))}})
	require.Equal(t, legacy, labelsDigest(&entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{artworkLabel()}}))
}

func TestLabelsDigestSeesArtworkChanges(t *testing.T) {
	base := func(mut func(*entity.TechCardGarmentLabel)) string {
		l := artworkLabel()
		mut(&l)
		return labelsDigest(&entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{l}})
	}
	plain := base(func(*entity.TechCardGarmentLabel) {})
	seen := map[string]string{"plain": plain}
	for name, mut := range map[string]func(*entity.TechCardGarmentLabel){
		"per size":   func(l *entity.TechCardGarmentLabel) { l.ArtworkPerSize = true },
		"per colour": func(l *entity.TechCardGarmentLabel) { l.ArtworkPerColorway = true },
		"pin":        func(l *entity.TechCardGarmentLabel) { l.CalloutNumber = 4 },
		"file": func(l *entity.TechCardGarmentLabel) {
			l.Files = []entity.TechCardGarmentLabelFile{{LibraryFileId: 1}}
		},
		"other file": func(l *entity.TechCardGarmentLabel) {
			l.Files = []entity.TechCardGarmentLabelFile{{LibraryFileId: 2}}
		},
		"file of a colour": func(l *entity.TechCardGarmentLabel) {
			l.Files = []entity.TechCardGarmentLabelFile{{ColorwayId: 9, LibraryFileId: 1}}
		},
		"rebuilt from other inputs": func(l *entity.TechCardGarmentLabel) {
			l.Files = []entity.TechCardGarmentLabelFile{{LibraryFileId: 1, SourceHash: "x"}}
		},
	} {
		d := base(mut)
		for other, od := range seen {
			require.NotEqualf(t, od, d, "%s must not hash as %s", name, other)
		}
		seen[name] = d
	}
}

// Read and write forms of one label hash the same: the read adds the link, the key and the sort order,
// none of which is content. A signature taken on a write is not stale on the next read.
func TestLabelsDigestReadEqualsWrite(t *testing.T) {
	write := artworkLabel()
	write.ArtworkOmitted = false
	write.BomItemId = sql.NullInt32{Int32: 5, Valid: true}
	write.Files = []entity.TechCardGarmentLabelFile{
		{ColorwayId: 9, SizeId: 2, LibraryFileId: 2},
		{ColorwayId: 1, SizeId: 1, LibraryFileId: 1},
	}
	read := write
	read.BomLineKey = "k1"
	read.Files = []entity.TechCardGarmentLabelFile{
		{ColorwayId: 1, SizeId: 1, LibraryFileId: 1, LinkEpoch: 3, PublicURL: "https://x/api/f/a"},
		{ColorwayId: 9, SizeId: 2, LibraryFileId: 2},
	}
	require.Equal(t,
		labelsDigest(&entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{write}}),
		labelsDigest(&entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{read}}))
}

func TestParseGarmentLabelArtwork(t *testing.T) {
	perSize := true
	labels, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: "size", ArtworkPerSize: &perSize, ArtworkPerColorway: true, CalloutNumber: 2, BomLineKey: "  k1 ",
			Files: []*pb_common.TechCardGarmentLabelFile{
				{ColorwayId: 9, SizeId: 1, LibraryFileId: 3, PublicUrl: "https://ignored"},
				{ColorwayId: 0, SizeId: 1, LibraryFileId: 4},
			}},
		{Key: "brand"},
	})
	require.NoError(t, err)
	require.Equal(t, "k1", labels[0].BomLineKey)
	require.True(t, labels[0].ArtworkPerSize)
	require.True(t, labels[0].ArtworkPerColorway)
	require.False(t, labels[0].ArtworkOmitted)
	require.Equal(t, 0, labels[0].Files[0].ColorwayId, "sorted by colourway, size")
	require.Empty(t, labels[0].Files[1].PublicURL, "the link is output only")
	require.True(t, labels[1].ArtworkOmitted, "no artwork_per_size = a client that does not know the block")

	for name, f := range map[string][]*pb_common.TechCardGarmentLabelFile{
		"duplicate slot": {{LibraryFileId: 1}, {LibraryFileId: 2}},
		"no file":        {{ColorwayId: 1}},
		"bad colourway":  {{ColorwayId: -1, LibraryFileId: 1}},
		"bad size":       {{SizeId: -1, LibraryFileId: 1}},
	} {
		_, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{{Key: "size", Files: f}})
		require.Errorf(t, err, name)
	}
	_, err = parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{{Key: "size", CalloutNumber: -1}})
	require.Error(t, err)
}

func TestCarryOmittedGarmentLabelArtwork(t *testing.T) {
	stored := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{
		Key: "size", CalloutNumber: 3, ArtworkPerSize: true, ArtworkPerColorway: true,
		Files: []entity.TechCardGarmentLabelFile{{SizeId: 1, LibraryFileId: 7}},
	}}}
	tc := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "size", ArtworkOmitted: true},
		{Key: "brand", ArtworkOmitted: true},
	}}
	CarryOmittedGarmentLabelArtwork(stored, tc)
	require.Equal(t, 3, tc.GarmentLabels[0].CalloutNumber)
	require.True(t, tc.GarmentLabels[0].ArtworkPerSize)
	require.True(t, tc.GarmentLabels[0].ArtworkPerColorway)
	require.Len(t, tc.GarmentLabels[0].Files, 1)
	require.Empty(t, tc.GarmentLabels[1].Files)

	// A client that speaks is taken at its word: cleared stays cleared.
	spoken := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "size"}}}
	CarryOmittedGarmentLabelArtwork(stored, spoken)
	require.Zero(t, spoken.GarmentLabels[0].CalloutNumber)
	require.Empty(t, spoken.GarmentLabels[0].Files)
}

func TestGarmentLabelLinksNeverBreakASave(t *testing.T) {
	stored := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{{Id: 11, LineKey: "kept"}, {Id: 12, LineKey: "gone"}}}
	tc := &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{{LineKey: "kept"}, {LineKey: "born"}},
		Callouts: []entity.TechCardCallout{{Number: 5}},
		GarmentLabels: []entity.TechCardGarmentLabel{
			{Key: "a", BomLineKey: "kept", CalloutNumber: 5},
			{Key: "b", BomLineKey: "gone", BomItemId: sql.NullInt32{Int32: 12, Valid: true}, CalloutNumber: 6},
			{Key: "c", BomLineKey: "born", BomItemId: sql.NullInt32{Int32: 99, Valid: true}},
			{Key: "d", BomItemId: sql.NullInt32{Int32: 11, Valid: true}},
		},
		PackagingItems: []entity.TechCardPackagingItem{{Key: "p", BomLineKey: "gone", BomItemId: sql.NullInt32{Int32: 12, Valid: true}}},
	}
	ClearDanglingGarmentLabelPins(tc)
	NormalizeLabelBomLinks(stored, tc)

	require.Equal(t, 5, tc.GarmentLabels[0].CalloutNumber)
	require.Zero(t, tc.GarmentLabels[1].CalloutNumber, "the callout is gone, the link is cleared")
	require.Equal(t, int32(11), tc.GarmentLabels[0].BomItemId.Int32, "the key gets the id the read will report")
	require.Empty(t, tc.GarmentLabels[1].BomLineKey, "a removed line clears the link")
	require.False(t, tc.GarmentLabels[1].BomItemId.Valid)
	require.Equal(t, "born", tc.GarmentLabels[2].BomLineKey)
	require.False(t, tc.GarmentLabels[2].BomItemId.Valid, "a line born now has no id yet")
	require.Equal(t, int32(11), tc.GarmentLabels[3].BomItemId.Int32, "a row without a key is left alone")
	require.False(t, tc.PackagingItems[0].BomItemId.Valid)
	require.Equal(t, []string{"c"}, LabelsLinkedToBornBomLines(tc))
}

func TestDropForeignGarmentLabelColorwayFiles(t *testing.T) {
	tc := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "size", Files: []entity.TechCardGarmentLabelFile{
		{ColorwayId: 0, LibraryFileId: 1}, {ColorwayId: 7, LibraryFileId: 2}, {ColorwayId: 8, LibraryFileId: 3},
	}}}}
	DropForeignGarmentLabelColorwayFiles([]entity.TechCardColorway{{Id: 7}}, tc)
	require.Len(t, tc.GarmentLabels[0].Files, 2)
	DropForeignGarmentLabelColorwayFiles(nil, tc)
	require.Len(t, tc.GarmentLabels[0].Files, 1)
	require.Equal(t, 1, tc.GarmentLabels[0].Files[0].LibraryFileId)
}

func TestGarmentLabelsWithoutMockupSeesFilesAndSkipsComposition(t *testing.T) {
	missing := GarmentLabelsWithoutMockup(&entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "composition"},
		{Key: "brand", Files: []entity.TechCardGarmentLabelFile{{LibraryFileId: 1}}},
		{Key: "flag", MediaIds: []int{1}},
		{Key: "size"},
	}})
	require.Equal(t, []string{"size (#4)"}, missing)
}

// Packaging picks (0448): shape on parse, the rest (exists, right kind, of this card) in the store.
func TestParsePackagingItemPicksRefusesBadShape(t *testing.T) {
	item := func(edit func(*pb_common.TechCardPackagingItem)) []*pb_common.TechCardPackagingItem {
		it := &pb_common.TechCardPackagingItem{Key: "polybag"}
		edit(it)
		return []*pb_common.TechCardPackagingItem{it}
	}
	for field, edit := range map[string]func(*pb_common.TechCardPackagingItem){
		"packaging_items[0].material_id":      func(it *pb_common.TechCardPackagingItem) { it.MaterialId = -1 },
		"packaging_items[0].aux_tech_card_id": func(it *pb_common.TechCardPackagingItem) { it.MaterialId, it.AuxTechCardId = 1, 2 },
		"packaging_items[0].colorway_ids[1]":  func(it *pb_common.TechCardPackagingItem) { it.ColorwayIds = []int32{5, -5} },
	} {
		_, err := parseTechCardPackagingItems(item(edit))
		requireLabelFieldViolation(t, err, field)
	}
	_, err := parseTechCardPackagingItems(item(func(it *pb_common.TechCardPackagingItem) { it.AuxTechCardId = -1 }))
	requireLabelFieldViolation(t, err, "packaging_items[0].aux_tech_card_id")
	_, err = parseTechCardPackagingItems(item(func(it *pb_common.TechCardPackagingItem) { it.ColorwayIds = []int32{5, 5} }))
	requireLabelFieldViolation(t, err, "packaging_items[0].colorway_ids[1]")
	_, err = parseTechCardPackaging(&pb_common.TechCardPackaging{PackedMediaId: -1})
	requireLabelFieldViolation(t, err, "packaging.packed_media_id")
}

// Wire → entity → wire keeps every field, the colourways ascending; output-only fields sent by a client
// are not believed, and a read fills them in.
func TestPackagingItemPicksRoundTrip(t *testing.T) {
	out, err := parseTechCardPackagingItems([]*pb_common.TechCardPackagingItem{
		{Key: "polybag", MaterialId: 7, PerOrder: true, ColorwayIds: []int32{9, 3}, PickedName: "forged"},
		{Key: "polybag", AuxTechCardId: 8},
		{Key: "tissue"},
	})
	require.NoError(t, err)
	require.Equal(t, sql.NullInt32{Int32: 7, Valid: true}, out[0].MaterialId)
	require.True(t, out[0].PerOrder)
	require.Equal(t, []int{3, 9}, out[0].ColorwayIds)
	require.Empty(t, out[0].PickedName, "picked_name is output-only")
	require.Equal(t, sql.NullInt32{Int32: 8, Valid: true}, out[1].AuxTechCardId)
	require.False(t, out[1].MaterialId.Valid)
	require.False(t, out[2].HasPick())
	require.Nil(t, out[2].ColorwayIds)

	out[0].PickedName = "Polybag"
	out[0].PickedThumbnail = &entity.MediaFull{Id: 4, MediaItem: entity.MediaItem{ThumbnailMediaURL: "https://x/t.jpg"}}
	back := techCardPackagingItemsToPb(out)
	require.EqualValues(t, 7, back[0].MaterialId)
	require.True(t, back[0].PerOrder)
	require.Equal(t, []int32{3, 9}, back[0].ColorwayIds)
	require.Equal(t, "Polybag", back[0].PickedName)
	require.EqualValues(t, 4, back[0].PickedThumbnail.GetId())
	require.Equal(t, "https://x/t.jpg", back[0].PickedThumbnail.GetMedia().GetThumbnail().GetMediaUrl())
	require.EqualValues(t, 8, back[1].AuxTechCardId)
	require.Nil(t, back[2].PickedThumbnail)
	require.Nil(t, back[2].ColorwayIds)

	sheet, err := parseTechCardPackaging(&pb_common.TechCardPackaging{PackedSize: "30x20", PackedMediaId: 6})
	require.NoError(t, err)
	require.Equal(t, "30x20", sheet.PackedSize.String)
	require.EqualValues(t, 6, sheet.PackedMediaId.Int32)
	sheetBack := techCardPackagingToPb(sheet)
	require.Equal(t, "30x20", sheetBack.PackedSize)
	require.EqualValues(t, 6, sheetBack.PackedMediaId)
}

// Every pick field moves the packaging digest; what a read adds and the order of a set do not; and a
// card with none of them hashes the bytes it always did, so no signature goes stale at deploy.
func TestPackagingDigestPicksTail(t *testing.T) {
	withItem := func(it entity.TechCardPackagingItem) string {
		tc := packagingGoldCard()
		tc.PackagingItems = []entity.TechCardPackagingItem{it}
		return packagingDigest(tc)
	}
	base := entity.TechCardPackagingItem{Key: "polybag", QtyPerGarment: 1}
	plain := withItem(base)
	seen := map[string]string{"plain": plain}
	for name, edit := range map[string]func(*entity.TechCardPackagingItem){
		"material":  func(it *entity.TechCardPackagingItem) { it.MaterialId = sql.NullInt32{Int32: 7, Valid: true} },
		"aux":       func(it *entity.TechCardPackagingItem) { it.AuxTechCardId = sql.NullInt32{Int32: 7, Valid: true} },
		"per order": func(it *entity.TechCardPackagingItem) { it.PerOrder = true },
		"colourway": func(it *entity.TechCardPackagingItem) { it.ColorwayIds = []int{3} },
	} {
		it := base
		edit(&it)
		hex := withItem(it)
		for other, otherHex := range seen {
			require.NotEqual(t, otherHex, hex, "%s hashes like %s", name, other)
		}
		seen[name] = hex
	}

	// A read adds these, a write form does not have them: the two forms must hash alike.
	read := base
	read.MaterialId = sql.NullInt32{Int32: 7, Valid: true}
	read.PickedName = "Polybag"
	read.PickedThumbnail = &entity.MediaFull{Id: 4}
	require.Equal(t, seen["material"], withItem(read))

	// False and empty add no pair.
	falsy := base
	falsy.PerOrder = false
	falsy.ColorwayIds = []int{}
	require.Equal(t, plain, withItem(falsy))

	// The colourways are a set, and hashing must not sort the caller's slice in place.
	a, b := base, base
	a.ColorwayIds = []int{9, 3}
	b.ColorwayIds = []int{3, 9}
	require.Equal(t, withItem(a), withItem(b))
	require.Equal(t, []int{9, 3}, a.ColorwayIds)

	gold := packagingGoldCard()
	require.Equal(t, packagingGoldDigestHex, packagingDigest(gold), "no picks: the frozen hex stands")

	sized := packagingGoldCard()
	sized.Packaging.PackedSize = sql.NullString{String: "30x20", Valid: true}
	photo := packagingGoldCard()
	photo.Packaging.PackedMediaId = sql.NullInt32{Int32: 6, Valid: true}
	require.NotEqual(t, packagingGoldDigestHex, packagingDigest(sized))
	require.NotEqual(t, packagingGoldDigestHex, packagingDigest(photo))
	require.NotEqual(t, packagingDigest(sized), packagingDigest(photo))

	// A sheet holding only the packed facts is not an empty sheet.
	require.NotEqual(t, packagingDigest(&entity.TechCardInsert{}),
		packagingDigest(&entity.TechCardInsert{Packaging: &entity.TechCardPackaging{PackedSize: sized.Packaging.PackedSize}}))
}
