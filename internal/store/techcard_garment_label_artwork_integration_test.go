package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestGarmentLabelArtwork drives the sewn-in label fields (0447) through the real store: the line key
// of a BOM line born in the same save, the pin, both artwork modes, the files with their public link
// state, the delete-insert save keeping all of it and the colourway ids, and a library file refusing
// deletion while a row holds it.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestGarmentLabelArtwork(t *testing.T) {
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
		labelLine = "01LABELARTWORKLINE00000001"
		otherLine = "01LABELARTWORKLINE00000002"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	fileA := insertLibraryFileFixture(ctx, t, "label-a.pdf", 10, "test-super")
	fileB := insertLibraryFileFixture(ctx, t, "label-b.pdf", 10, "test-super")
	fileC := insertLibraryFileFixture(ctx, t, "label-c.pdf", 10, "test-super")

	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "LABEL ARTWORK", Stage: entity.TechCardStageProto, StyleNumber: ns("LBA-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		LabelsAware: true,
		BomItems: []entity.TechCardBomItem{
			{LineKey: labelLine, Section: entity.BomSectionLabel, Name: "satin ribbon"},
			{LineKey: otherLine, Section: entity.BomSectionLabel, Name: "woven label"},
		},
		GarmentLabels: []entity.TechCardGarmentLabel{
			// A line born in this very save: the row knows only its key.
			{Key: entity.GarmentLabelKeyComposition, QtyPerGarment: 1, BomLineKey: labelLine, Placement: ns("left side seam")},
			{Key: "size", QtyPerGarment: 1, CalloutNumber: 3, ArtworkPerSize: true, ArtworkPerColorway: true},
			{Key: "brand", QtyPerGarment: 1},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	// A colourway is a product of the style, created apart from the card.
	mediaID, err := st.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, sku_locked_at)
		VALUES ('LBA-1-CW', 'black', 'BLK', '#000000', 'IT', ?, ?, NOW())`, mediaID, cardID)
	require.NoError(t, err)
	colorway64, err := res.LastInsertId()
	require.NoError(t, err)
	colorway := int(colorway64)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM product WHERE id = ?`, colorway)
	})

	read := func() *entity.TechCard {
		t.Helper()
		card, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		return card
	}
	label := func(card *entity.TechCard, key string) *entity.TechCardGarmentLabel {
		t.Helper()
		for i := range card.GarmentLabels {
			if card.GarmentLabels[i].Key == key {
				return &card.GarmentLabels[i]
			}
		}
		t.Fatalf("no label %q", key)
		return nil
	}
	save := func(card *entity.TechCard, edit func(*entity.TechCardInsert)) {
		t.Helper()
		ins := card.TechCardInsert
		ins.LabelsAware = true
		ins.GarmentLabels = append([]entity.TechCardGarmentLabel(nil), card.GarmentLabels...)
		if edit != nil {
			edit(&ins)
		}
		require.NoError(t, T.UpdateTechCard(ctx, cardID, &ins, card.LockVersion))
	}

	// ── the key of a line born in the same save resolves, and the read returns id and key ──
	card := read()
	comp := label(card, entity.GarmentLabelKeyComposition)
	require.True(t, comp.BomItemId.Valid, "the key resolved to the id the save minted")
	require.Equal(t, labelLine, comp.BomLineKey, "the read returns the key")
	require.True(t, comp.IsComposition())
	size := label(card, "size")
	require.Equal(t, 3, size.CalloutNumber)
	require.True(t, size.ArtworkPerSize)
	require.True(t, size.ArtworkPerColorway)
	require.False(t, label(card, "brand").ArtworkPerColorway)
	require.Empty(t, label(card, "brand").BomLineKey, "no link, no key")

	// ── files go in, in any order, and read back in (colourway, size) order ──
	save(card, func(ins *entity.TechCardInsert) {
		for i := range ins.GarmentLabels {
			switch ins.GarmentLabels[i].Key {
			case "size":
				ins.GarmentLabels[i].Files = []entity.TechCardGarmentLabelFile{
					{ColorwayId: colorway, SizeId: 2, LibraryFileId: fileB, SourceHash: "hash-b"},
					{ColorwayId: 0, SizeId: 1, LibraryFileId: fileA},
				}
			case "brand":
				ins.GarmentLabels[i].Files = []entity.TechCardGarmentLabelFile{{LibraryFileId: fileC}}
			}
		}
	})
	card = read()
	size = label(card, "size")
	require.Len(t, size.Files, 2)
	require.Equal(t, 0, size.Files[0].ColorwayId, "an old file with no colour stays beside the coloured one")
	require.Equal(t, fileA, size.Files[0].LibraryFileId)
	require.Equal(t, colorway, size.Files[1].ColorwayId)
	require.Equal(t, fileB, size.Files[1].LibraryFileId)
	require.Equal(t, "hash-b", size.Files[1].SourceHash)
	require.Zero(t, size.Files[0].LinkEpoch, "a team file has no live link")
	require.Equal(t, 3, size.CalloutNumber)

	// ── a live link shows its epoch; revoked, expired or any other level reads as none ──
	_, err = testDB.ExecContext(ctx, `UPDATE library_file SET access_level = 'link' WHERE id = ?`, fileA)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO library_file_public_access (file_id, epoch) VALUES (?, 7)`, fileA)
	require.NoError(t, err)
	require.Equal(t, 7, label(read(), "size").Files[0].LinkEpoch)
	_, err = testDB.ExecContext(ctx, `UPDATE library_file_public_access SET expires_at = NOW() - INTERVAL 1 DAY WHERE file_id = ?`, fileA)
	require.NoError(t, err)
	require.Zero(t, label(read(), "size").Files[0].LinkEpoch, "expired")
	_, err = testDB.ExecContext(ctx, `UPDATE library_file_public_access SET expires_at = NULL, revoked_at = NOW() WHERE file_id = ?`, fileA)
	require.NoError(t, err)
	require.Zero(t, label(read(), "size").Files[0].LinkEpoch, "revoked")
	_, err = testDB.ExecContext(ctx, `UPDATE library_file_public_access SET revoked_at = NULL WHERE file_id = ?`, fileA)
	require.NoError(t, err)
	require.Equal(t, 7, label(read(), "size").Files[0].LinkEpoch)

	// ── a save of the card changes neither the colourway's id nor the file's address ──
	card = read()
	require.Len(t, card.Colorways, 1)
	require.Equal(t, colorway, card.Colorways[0].Id)
	for i := 0; i < 2; i++ {
		save(card, nil)
		card = read()
	}
	require.Len(t, card.Colorways, 1)
	require.Equal(t, colorway, card.Colorways[0].Id, "colourways are products, a card save does not recreate them")
	size = label(card, "size")
	require.Len(t, size.Files, 2, "the delete-insert save writes the files back")
	require.Equal(t, colorway, size.Files[1].ColorwayId)
	require.Equal(t, fileB, size.Files[1].LibraryFileId)
	require.Len(t, label(card, "brand").Files, 1)
	require.Equal(t, labelLine, label(card, entity.GarmentLabelKeyComposition).BomLineKey)

	// ── moving the link to another line by key alone: the key wins over the old id ──
	save(card, func(ins *entity.TechCardInsert) {
		for i := range ins.GarmentLabels {
			if ins.GarmentLabels[i].IsComposition() {
				ins.GarmentLabels[i].BomLineKey = otherLine
			}
		}
	})
	card = read()
	require.Equal(t, otherLine, label(card, entity.GarmentLabelKeyComposition).BomLineKey)

	// ── deleting a held file is refused by name, and the row keeps it ──
	_, err = st.Files().DeleteFile(superCtx(ctx), fileB)
	require.Error(t, err)
	require.True(t, errors.Is(err, entity.ErrLibraryFileOnTechCard), "got %v", err)
	require.Contains(t, err.Error(), "#")
	require.Len(t, label(read(), "size").Files, 2)
	_, err = testDB.ExecContext(ctx, `DELETE FROM library_file WHERE id = ?`, fileB)
	require.Error(t, err, "the foreign key is the backstop")

	// ── a save without the flag keeps the stored rows, files included ──
	card = read()
	ins := card.TechCardInsert
	ins.LabelsAware = false
	ins.GarmentLabels = nil
	ins.PackagingItems = nil
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &ins, card.LockVersion))
	require.Len(t, label(read(), "size").Files, 2)

	// ── a row dropped from the save frees its files ──
	card = read()
	save(card, func(ins *entity.TechCardInsert) {
		var kept []entity.TechCardGarmentLabel
		for _, l := range ins.GarmentLabels {
			if l.Key != "size" {
				kept = append(kept, l)
			}
		}
		ins.GarmentLabels = kept
	})
	var rows int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_garment_label_file WHERE library_file_id = ?`, fileB).Scan(&rows))
	require.Zero(t, rows)
	_, err = st.Files().DeleteFile(superCtx(ctx), fileB)
	require.NoError(t, err, "nothing holds the file any more")

	// ── the clone's payload: a key resolves on the new card, against the ids it just minted ──
	source := read()
	cloneIns := source.TechCardInsert
	cloneIns.StyleNumber = ns("LBA-2")
	cloneIns.Name = "LABEL ARTWORK COPY"
	cloneIns.GarmentLabels = []entity.TechCardGarmentLabel{
		{Key: entity.GarmentLabelKeyComposition, QtyPerGarment: 1, BomLineKey: otherLine},
	}
	cloneIns.BomItems = []entity.TechCardBomItem{{LineKey: otherLine, Section: entity.BomSectionLabel, Name: "woven label"}}
	cloneID, err := T.AddTechCard(ctx, &cloneIns)
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cloneID) })
	clone, err := T.GetTechCardById(ctx, cloneID)
	require.NoError(t, err)
	require.Len(t, clone.GarmentLabels, 1)
	require.True(t, clone.GarmentLabels[0].BomItemId.Valid)
	require.Equal(t, otherLine, clone.GarmentLabels[0].BomLineKey)
}
