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

// TestStyleSizeChartPicture drives the size chart picture (0459) through the real store: an icon with
// free lines, a client that never sends the picture keeping it, a switch to an upload with its url,
// every validation refusal leaving the stored chart and lock alone, the season clone carrying both
// tables, a deleted image leaving the picture without media, and the empty picture clearing it.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestStyleSizeChartPicture(t *testing.T) {
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

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "SIZE CHART PICTURE", Stage: entity.TechCardStageProto, StyleNumber: ns("SCPIC-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	nameID := func(name string) int {
		t.Helper()
		var id int
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM measurement_name WHERE name = ?", name).Scan(&id))
		return id
	}
	head, peak := nameID("head"), nameID("peak")
	addMedia := func(url string) int {
		t.Helper()
		id, err := st.Media().AddMedia(ctx, &entity.MediaItem{
			FullSizeMediaURL: url, FullSizeWidth: 100, FullSizeHeight: 100,
			ThumbnailMediaURL: url + "?t", ThumbnailWidth: 10, ThumbnailHeight: 10,
			CompressedMediaURL: url + "?c", CompressedWidth: 50, CompressedHeight: 50,
		})
		require.NoError(t, err)
		return id
	}
	save := func(lock int, picture *entity.StyleSizeChartPicture) (entity.StyleSizeChart, error) {
		return T.UpdateStyleSizeChart(ctx, cardID, lock, nil, 0, nil, nil, true, nil, picture)
	}
	lineCount := func(id int) int {
		t.Helper()
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_size_chart_line WHERE tech_card_id = ?", id).Scan(&n))
		return n
	}

	chart0, err := T.GetStyleSizeChart(ctx, cardID)
	require.NoError(t, err)
	require.Nil(t, chart0.Picture, "a card without a picture reads none")

	// ── icon + two free lines: one named, one not picked yet; sent out of order, with a stray
	// media id and more decimals than the column keeps ──
	icon := &entity.StyleSizeChartPicture{
		Source: " icon ", IconKey: "bucket_hat", MediaID: 77,
		Lines: []entity.StyleSizeChartLine{
			{MeasurementNameID: 0, X1: 0.1, Y1: 0.2, X2: 0.3, Y2: 0.4, Position: 9},
			{MeasurementNameID: head, X1: 0, Y1: 0.123456, X2: 1, Y2: 0.5, Position: 2},
		},
	}
	chart, err := save(chart0.LockVersion, icon)
	require.NoError(t, err)
	require.Equal(t, chart0.LockVersion+1, chart.LockVersion)
	require.NotNil(t, chart.Picture)
	pic := *chart.Picture
	require.Equal(t, entity.StyleSizeChartPictureSourceIcon, pic.Source)
	require.Equal(t, "bucket_hat", pic.IconKey)
	require.Zero(t, pic.MediaID, "an icon keeps no image")
	require.Empty(t, pic.MediaURL)
	require.Equal(t, []entity.StyleSizeChartLine{
		{MeasurementNameID: head, X1: 0, Y1: 0.12346, X2: 1, Y2: 0.5, Position: 1},
		{MeasurementNameID: 0, X1: 0.1, Y1: 0.2, X2: 0.3, Y2: 0.4, Position: 2},
	}, pic.Lines, "lines come back in position order, renumbered, rounded to five decimals")
	var nullNames int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_size_chart_line
		WHERE tech_card_id = ? AND measurement_name_id IS NULL`, cardID).Scan(&nullNames))
	require.Equal(t, 1, nullNames, "a line without a picked point is stored NULL, not 0")

	// ── a client that never sends the picture keeps it ──
	kept, err := T.UpdateStyleSizeChart(ctx, cardID, chart.LockVersion, nil, 0, nil, nil, false, nil, nil)
	require.NoError(t, err)
	require.Equal(t, chart.Picture, kept.Picture)

	// ── switch to an upload: the url resolves, the icon key goes ──
	uploadID := addMedia("https://x/size-chart-upload.jpg")
	upload, err := save(kept.LockVersion, &entity.StyleSizeChartPicture{
		Source: entity.StyleSizeChartPictureSourceUpload, IconKey: "bucket_hat", MediaID: uploadID,
		Lines: []entity.StyleSizeChartLine{{MeasurementNameID: peak, X1: 0.25, Y1: 0.25, X2: 0.75, Y2: 0.75}},
	})
	require.NoError(t, err)
	require.Equal(t, entity.StyleSizeChartPictureSourceUpload, upload.Picture.Source)
	require.Empty(t, upload.Picture.IconKey)
	require.Equal(t, uploadID, upload.Picture.MediaID)
	require.Equal(t, "https://x/size-chart-upload.jpg", upload.Picture.MediaURL)
	require.Len(t, upload.Picture.Lines, 1, "the lines are replaced, not merged")
	require.Equal(t, 1, lineCount(cardID))

	// ── refusals: each is a field violation, and nothing moves ──
	var missingMedia, missingName int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) + 1000 FROM media").Scan(&missingMedia))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) + 1000 FROM measurement_name").Scan(&missingName))
	line := func(x float64) []entity.StyleSizeChartLine {
		return []entity.StyleSizeChartLine{{X1: x, Y1: 0, X2: 0.5, Y2: 0.5}}
	}
	for name, bad := range map[string]*entity.StyleSizeChartPicture{
		"unknown source":       {Source: "photo", MediaID: uploadID},
		"icon without key":     {Source: entity.StyleSizeChartPictureSourceIcon},
		"icon key charset":     {Source: entity.StyleSizeChartPictureSourceIcon, IconKey: "Bucket Hat"},
		"sketch without media": {Source: entity.StyleSizeChartPictureSourceSketch},
		"missing media":        {Source: entity.StyleSizeChartPictureSourceSketch, MediaID: missingMedia},
		"coordinate above one": {Source: entity.StyleSizeChartPictureSourceUpload, MediaID: uploadID, Lines: line(1.5)},
		"negative coordinate":  {Source: entity.StyleSizeChartPictureSourceUpload, MediaID: uploadID, Lines: line(-0.1)},
		"unknown point": {Source: entity.StyleSizeChartPictureSourceUpload, MediaID: uploadID,
			Lines: []entity.StyleSizeChartLine{{MeasurementNameID: missingName, X2: 1, Y2: 1}}},
		"lines without picture": {Lines: line(0.5)},
	} {
		_, err := save(upload.LockVersion, bad)
		var ve *entity.ValidationError
		require.True(t, errors.As(err, &ve), "%s: want a field violation, got %v", name, err)
	}
	after, err := T.GetStyleSizeChart(ctx, cardID)
	require.NoError(t, err)
	require.Equal(t, upload.LockVersion, after.LockVersion, "a refused save does not bump the lock")
	require.Equal(t, upload.Picture, after.Picture, "a refused save leaves the picture")

	// ── a stale lock is a conflict, not a picture write ──
	_, err = save(upload.LockVersion-1, icon)
	require.ErrorIs(t, err, entity.ErrTechCardConflict)

	// ── the season clone carries the picture and its lines ──
	card, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	clone := card.TechCardInsert
	clone.StyleNumber = ns("SCPIC-2")
	cloneID, err := T.CloneTechCardForSeason(ctx, cardID, card.LockVersion, &clone)
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cloneID) })
	cloned, err := T.GetStyleSizeChart(ctx, cloneID)
	require.NoError(t, err)
	require.Equal(t, after.Picture, cloned.Picture)

	// ── the image deleted from the library: the picture stays, without media ──
	_, err = testDB.ExecContext(ctx, "DELETE FROM media WHERE id = ?", uploadID)
	require.NoError(t, err)
	orphan, err := T.GetStyleSizeChart(ctx, cardID)
	require.NoError(t, err)
	require.NotNil(t, orphan.Picture)
	require.Equal(t, entity.StyleSizeChartPictureSourceUpload, orphan.Picture.Source)
	require.Zero(t, orphan.Picture.MediaID)
	require.Empty(t, orphan.Picture.MediaURL)
	require.Len(t, orphan.Picture.Lines, 1)

	// ── the empty picture clears both tables ──
	cleared, err := save(orphan.LockVersion, &entity.StyleSizeChartPicture{})
	require.NoError(t, err)
	require.Nil(t, cleared.Picture)
	require.Zero(t, lineCount(cardID))

	// ── deleting the card takes the clone's picture rows with it ──
	require.NoError(t, T.DeleteTechCard(ctx, cloneID))
	var leftovers int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM tech_card_size_chart_picture WHERE tech_card_id = ?)
		     + (SELECT COUNT(*) FROM tech_card_size_chart_line WHERE tech_card_id = ?)`, cloneID, cloneID).Scan(&leftovers))
	require.Zero(t, leftovers)
}

// TestStyleSizeChartPictureMigrationReplays replays 0459 under production's ANSI_QUOTES mode twice
// over the migrated schema: it must stay idempotent and leave every icon point in the dictionary once.
func TestStyleSizeChartPictureMigrationReplays(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()

	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	_, err = conn.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')")
	require.NoError(t, err)
	up := migrationUpBody(t, "0459_tech_card_size_chart_picture.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "migration replay %d", i+1)
	}
	names := []any{"brim", "flap-width", "foot-length", "handle-drop", "head", "heel-height", "hood-height",
		"peak", "shaft-height", "shaft-width", "strap-drop", "strap-length", "underbust"}
	var found int
	require.NoError(t, conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM measurement_name WHERE name IN (?"+strings.Repeat(", ?", len(names)-1)+")",
		names...).Scan(&found))
	require.Equal(t, len(names), found)
}
