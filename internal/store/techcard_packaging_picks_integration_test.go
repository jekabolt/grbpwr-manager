package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	"github.com/stretchr/testify/require"
)

// packagingPickFixture is one card with two colourways, plus everything an item can pick.
type packagingPickFixture struct {
	st          *MYSQLStore
	card        int
	colorwayA   int
	colorwayB   int
	material    int
	wrongClass  int
	aux         int
	plain       int
	otherCard   int
	otherColor  int
	materialImg int
	auxMedia    int
	packedMedia int
}

var packagingPickSeq int

func newPackagingPickFixture(t *testing.T, ctx context.Context) *packagingPickFixture {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	T := st.TechCards()

	packagingPickSeq++
	seq := packagingPickSeq
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	media := func(name string) int {
		t.Helper()
		id, err := st.Media().AddMedia(ctx, &entity.MediaItem{
			FullSizeMediaURL: "https://x/" + name + "-f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
			ThumbnailMediaURL: "https://x/" + name + "-t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
			CompressedMediaURL: "https://x/" + name + "-c.jpg", CompressedWidth: 50, CompressedHeight: 50,
		})
		require.NoError(t, err)
		return id
	}
	card := func(name string, purpose entity.TechCardPurpose, subtype sql.NullString, in *entity.TechCardInsert) int {
		t.Helper()
		if in == nil {
			in = &entity.TechCardInsert{}
		}
		in.Name = name
		in.StyleNumber = ns(fmt.Sprintf("PICK-%d-%s", seq, name))
		in.Stage = entity.TechCardStageProto
		in.ApprovalState = entity.TechCardApprovalDraft
		in.MeasurementUnit = entity.TechCardUnitMm
		in.Purpose = purpose
		in.AuxSubtype = subtype
		// A card save re-mints its colourways' SKUs, and the mint needs a whole season.
		in.SeasonCode = ns("SS")
		in.SeasonYear = sql.NullInt32{Int32: 2026, Valid: true}
		id, err := T.AddTechCard(ctx, in)
		require.NoError(t, err)
		t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), id) })
		return id
	}

	f := &packagingPickFixture{st: st}
	f.materialImg = media("material")
	f.auxMedia = media("aux")
	f.packedMedia = media("packed")

	tokens := 0
	colorway := func(cardID int) int {
		t.Helper()
		tokens++
		token := fmt.Sprintf("C%02d", tokens)
		res, err := testDB.ExecContext(ctx, `
			INSERT INTO product (sku, color, color_code, sku_color_token, color_hex, country_of_origin, thumbnail_id, style_id)
			VALUES (CONCAT('SS26-00001-', LEFT(MD5(RAND()), 3)), 'c', 'BLK', ?, '#000000', 'US', ?, ?)`, token, f.materialImg, cardID)
		require.NoError(t, err)
		id64, err := res.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM product WHERE id = ?`, id64) })
		return int(id64)
	}
	material := func(name string, class entity.MaterialClass) int {
		t.Helper()
		id, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
			Name: fmt.Sprintf("%s %d", name, seq), Section: "packaging", MaterialClass: string(class),
			CreatedBy: "t", UpdatedBy: "t",
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM material WHERE id = ?`, id) })
		return id
	}

	f.card = card("HOST", entity.TechCardPurposeSellable, sql.NullString{}, nil)
	f.colorwayA = colorway(f.card)
	f.colorwayB = colorway(f.card)
	f.otherCard = card("OTHER", entity.TechCardPurposeSellable, sql.NullString{}, nil)
	f.otherColor = colorway(f.otherCard)

	f.material = material("Polybag", entity.MaterialClassPackaging)
	_, err = testDB.ExecContext(ctx, `UPDATE material SET image_id = ? WHERE id = ?`, f.materialImg, f.material)
	require.NoError(t, err)
	f.wrongClass = material("Shell", entity.MaterialClassFabric)

	f.aux = card("AUX", entity.TechCardPurposeAuxiliary, ns("care_label"), nil)
	f.plain = f.otherCard
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_media (tech_card_id, media_id, category, kind) VALUES (?, ?, ?, ?)`,
		f.aux, f.auxMedia, string(entity.TechCardMediaCategoryTechnical), string(entity.TechCardMediaPreview))
	require.NoError(t, err)
	return f
}

// save writes the items through a real card update, the way a labels-aware client does.
func (f *packagingPickFixture) save(t *testing.T, ctx context.Context, items []entity.TechCardPackagingItem, sheet *entity.TechCardPackaging) error {
	t.Helper()
	T := f.st.TechCards()
	card, err := T.GetTechCardById(ctx, f.card)
	require.NoError(t, err)
	edit := card.TechCardInsert
	edit.LabelsAware = true
	edit.PackagingItems = items
	edit.Packaging = sheet
	return T.UpdateTechCard(ctx, f.card, &edit, card.LockVersion)
}

func packagingPickInt(id int) sql.NullInt32 { return sql.NullInt32{Int32: int32(id), Valid: true} }

// The same picked thing twice with different colourways and quantities, an aux pick, an order-wide item
// and the packed facts all come back, and carrying the read card back changes nothing.
func TestTechCardPackagingPicksRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	f := newPackagingPickFixture(t, ctx)
	T := f.st.TechCards()

	require.NoError(t, f.save(t, ctx, []entity.TechCardPackagingItem{
		{Key: "polybag", QtyPerGarment: 1, MaterialId: packagingPickInt(f.material), ColorwayIds: []int{f.colorwayB, f.colorwayA}},
		{Key: "polybag", QtyPerGarment: 2, MaterialId: packagingPickInt(f.material), ColorwayIds: []int{f.colorwayB}},
		{Key: "swing tag", QtyPerGarment: 1, AuxTechCardId: packagingPickInt(f.aux), PerOrder: true},
		{Key: "free words", QtyPerGarment: 1},
	}, &entity.TechCardPackaging{
		PackedSize:    sql.NullString{String: "30x20x4", Valid: true},
		PackedMediaId: packagingPickInt(f.packedMedia),
	}))

	check := func() *entity.TechCard {
		t.Helper()
		card, err := T.GetTechCardById(ctx, f.card)
		require.NoError(t, err)
		require.Len(t, card.PackagingItems, 4)
		first, second, aux, plain := card.PackagingItems[0], card.PackagingItems[1], card.PackagingItems[2], card.PackagingItems[3]

		lowFirst, highFirst := f.colorwayA, f.colorwayB
		if lowFirst > highFirst {
			lowFirst, highFirst = highFirst, lowFirst
		}
		require.Equal(t, []int{lowFirst, highFirst}, first.ColorwayIds, "ascending, whatever order the payload used")
		require.Equal(t, packagingPickInt(f.material), first.MaterialId)
		require.False(t, first.AuxTechCardId.Valid)
		require.Contains(t, first.PickedName, "Polybag")
		require.NotNil(t, first.PickedThumbnail)
		require.Equal(t, f.materialImg, first.PickedThumbnail.Id)
		require.Equal(t, "https://x/material-t.jpg", first.PickedThumbnail.ThumbnailMediaURL)

		require.Equal(t, packagingPickInt(f.material), second.MaterialId, "no uniqueness on the pick")
		require.Equal(t, []int{f.colorwayB}, second.ColorwayIds)
		require.Equal(t, first.PickedName, second.PickedName)

		require.Equal(t, packagingPickInt(f.aux), aux.AuxTechCardId)
		require.True(t, aux.PerOrder)
		require.Empty(t, aux.ColorwayIds, "no rows means every colourway")
		require.Contains(t, aux.PickedName, "AUX")
		require.NotNil(t, aux.PickedThumbnail)
		require.Equal(t, f.auxMedia, aux.PickedThumbnail.Id)

		require.False(t, plain.HasPick())
		require.False(t, plain.PerOrder)
		require.Empty(t, plain.PickedName)
		require.Nil(t, plain.PickedThumbnail)

		require.NotNil(t, card.Packaging)
		require.Equal(t, "30x20x4", card.Packaging.PackedSize.String)
		require.Equal(t, packagingPickInt(f.packedMedia), card.Packaging.PackedMediaId)
		require.Contains(t, card.TechCardInsert.LabelMediaIds(), f.packedMedia)
		return card
	}

	card := check()
	edit := card.TechCardInsert
	edit.LabelsAware = true
	require.NoError(t, T.UpdateTechCard(ctx, f.card, &edit, card.LockVersion))
	check()
}

func TestTechCardPackagingPicksValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	f := newPackagingPickFixture(t, ctx)

	cases := []struct {
		name  string
		item  entity.TechCardPackagingItem
		field string
	}{
		{"material of another class", entity.TechCardPackagingItem{MaterialId: packagingPickInt(f.wrongClass)},
			"packaging_items[0].material_id"},
		{"material that does not exist", entity.TechCardPackagingItem{MaterialId: packagingPickInt(999999999)},
			"packaging_items[0].material_id"},
		{"card that is not auxiliary", entity.TechCardPackagingItem{AuxTechCardId: packagingPickInt(f.plain)},
			"packaging_items[0].aux_tech_card_id"},
		{"card that does not exist", entity.TechCardPackagingItem{AuxTechCardId: packagingPickInt(999999999)},
			"packaging_items[0].aux_tech_card_id"},
		{"both picks", entity.TechCardPackagingItem{MaterialId: packagingPickInt(f.material), AuxTechCardId: packagingPickInt(f.aux)},
			"packaging_items[0].aux_tech_card_id"},
		{"colourway of another card", entity.TechCardPackagingItem{ColorwayIds: []int{f.colorwayA, f.otherColor}},
			"packaging_items[0].colorway_ids[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.item.Key = "polybag"
			tc.item.QtyPerGarment = 1
			err := f.save(t, ctx, []entity.TechCardPackagingItem{tc.item}, nil)
			require.Error(t, err)
			var ve *entity.ValidationError
			require.True(t, errors.As(err, &ve), "a plain field violation, got: %v", err)
			require.Equal(t, tc.field, ve.Field)
		})
	}

	// A refused save wrote nothing: the transaction took the items with it.
	card, err := f.st.TechCards().GetTechCardById(ctx, f.card)
	require.NoError(t, err)
	require.Empty(t, card.PackagingItems)
}

// A material or card going away leaves the item as plain words; a colourway going away takes only its
// own row, and a replaced item takes its rows with it.
func TestTechCardPackagingPicksForeignKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	f := newPackagingPickFixture(t, ctx)
	T := f.st.TechCards()

	require.NoError(t, f.save(t, ctx, []entity.TechCardPackagingItem{
		{Key: "polybag", QtyPerGarment: 1, MaterialId: packagingPickInt(f.material), ColorwayIds: []int{f.colorwayA, f.colorwayB}},
		{Key: "swing tag", QtyPerGarment: 1, AuxTechCardId: packagingPickInt(f.aux)},
	}, nil))

	_, err := testDB.ExecContext(ctx, `DELETE FROM material WHERE id = ?`, f.material)
	require.NoError(t, err)
	require.NoError(t, T.DeleteTechCard(ctx, f.aux))

	card, err := T.GetTechCardById(ctx, f.card)
	require.NoError(t, err)
	require.Len(t, card.PackagingItems, 2)
	require.False(t, card.PackagingItems[0].MaterialId.Valid)
	require.Empty(t, card.PackagingItems[0].PickedName)
	require.Nil(t, card.PackagingItems[0].PickedThumbnail)
	require.Equal(t, "polybag", card.PackagingItems[0].Key)
	require.False(t, card.PackagingItems[1].AuxTechCardId.Valid)
	require.Equal(t, "swing tag", card.PackagingItems[1].Key)

	_, err = testDB.ExecContext(ctx, `DELETE FROM product WHERE id = ?`, f.colorwayA)
	require.NoError(t, err)
	card, err = T.GetTechCardById(ctx, f.card)
	require.NoError(t, err)
	require.Equal(t, []int{f.colorwayB}, card.PackagingItems[0].ColorwayIds)

	// The save rewrites the items, so the old rows must not outlive them.
	var oldItemID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM tech_card_packaging_item WHERE tech_card_id = ? AND item_key = ?`, f.card, "polybag").Scan(&oldItemID))
	require.NoError(t, f.save(t, ctx, []entity.TechCardPackagingItem{{Key: "tissue", QtyPerGarment: 1}}, nil))
	var left int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_packaging_item_colorway WHERE item_id = ?`, oldItemID).Scan(&left))
	require.Zero(t, left)
	card, err = T.GetTechCardById(ctx, f.card)
	require.NoError(t, err)
	require.Empty(t, card.PackagingItems[0].ColorwayIds)

	// The packed photo is a media carrier: the media row refuses to go while the sheet names it.
	require.NoError(t, f.save(t, ctx, nil, &entity.TechCardPackaging{PackedMediaId: packagingPickInt(f.packedMedia)}))
	_, err = testDB.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, f.packedMedia)
	require.Error(t, err)
}

// The restore path asks through this: it must answer exactly what the save would accept.
func TestTechCardPackagingPickTargets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	f := newPackagingPickFixture(t, ctx)

	targets, err := f.st.TechCards().PackagingPickTargets(ctx, f.card,
		[]int{f.material, f.wrongClass, 999999999}, []int{f.aux, f.plain, 999999999})
	require.NoError(t, err)
	require.Equal(t, map[int]bool{f.material: true}, targets.Materials)
	require.Equal(t, map[int]bool{f.aux: true}, targets.AuxCards)
	require.Equal(t, map[int]bool{f.colorwayA: true, f.colorwayB: true}, targets.Colorways)
}

// Export and import through the real handlers, into the same base: the picks come back by passport and
// style number, the packed facts come along, the colourway scope drops with a report line, and the
// import does not fail over any of it.
func TestTechCardArchivePackagingPicksRoundTrip(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := newRTRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	bag, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: fmt.Sprintf("RT polybag %d", fx.seq), Code: ns(fmt.Sprintf("RT-BAG-%d", fx.seq)), Unit: ns("pcs"),
		Section: "packaging", MaterialClass: string(entity.MaterialClassPackaging), CreatedBy: "t", UpdatedBy: "t",
	})
	require.NoError(t, err)
	photo := rtSeedMedia(t, ctx, rig.store, rig.objs, fmt.Sprintf("rt-packed-%d", fx.seq), rtPNG(t, 9, 9, 200), false)

	res, err := testDB.ExecContext(ctx, `
		INSERT INTO product (sku, color, color_code, sku_color_token, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (CONCAT('SS26-00001-', LEFT(MD5(RAND()), 3)), 'c', 'BLK', 'PCK', '#000000', 'US', ?, ?)`, photo, fx.cardID)
	require.NoError(t, err)
	colorway64, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM product WHERE id = ?`, colorway64)
	})

	card, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	edit := card.TechCardInsert
	edit.LabelsAware = true
	edit.PackagingItems = []entity.TechCardPackagingItem{
		{Key: "polybag", QtyPerGarment: 1, MaterialId: packagingPickInt(bag), ColorwayIds: []int{int(colorway64)}},
		{Key: "swing tag", QtyPerGarment: 1, AuxTechCardId: packagingPickInt(fx.auxID), PerOrder: true},
		{Key: "tissue", QtyPerGarment: 1, MediaIds: []int{photo}},
	}
	sheet := *edit.Packaging
	sheet.PackedSize = ns("30x20x4")
	sheet.PackedMediaId = packagingPickInt(photo)
	edit.Packaging = &sheet
	require.NoError(t, T.UpdateTechCard(ctx, fx.cardID, &edit, card.LockVersion))

	zip, resp := rig.export(t, fx.cardID)
	require.NotEmpty(t, resp.GetManifest().GetCounters())
	arch, err := techcardarchive.OpenArchive(bytes.NewReader(zip), int64(len(zip)))
	require.NoError(t, err)
	require.Equal(t, fx.auxNumber, arch.Manifest.IDMaps.AuxTechCards[fmt.Sprint(fx.auxID)])
	require.Empty(t, arch.Manifest.ExportHoles, "a picked material and card both have something to travel by")

	imported, report := rig.importArchive(t, zip)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), imported) })
	require.NotEqual(t, fx.cardID, imported)
	rtRequireReportReason(t, report, techcardarchive.EntityColorway, "packaging_items[0]", string(techcardarchive.ReasonColorwaysNotApplied))

	got, err := T.GetTechCardById(ctx, imported)
	require.NoError(t, err)
	require.Len(t, got.PackagingItems, 3)
	require.Equal(t, packagingPickInt(bag), got.PackagingItems[0].MaterialId, "the passport matched this base's article")
	require.Contains(t, got.PackagingItems[0].PickedName, "RT polybag", "the far side reads its own name")
	require.Empty(t, got.PackagingItems[0].ColorwayIds, "colourways do not travel")
	require.Equal(t, packagingPickInt(fx.auxID), got.PackagingItems[1].AuxTechCardId, "the style number found the card")
	require.True(t, got.PackagingItems[1].PerOrder)
	require.NotEmpty(t, got.PackagingItems[2].MediaIds)
	require.Equal(t, "30x20x4", got.Packaging.PackedSize.String)
	require.True(t, got.Packaging.PackedMediaId.Valid, "the packed photo travels as a media slot")
	require.Contains(t, got.TechCardInsert.LabelMediaIds(), int(got.Packaging.PackedMediaId.Int32))
}
