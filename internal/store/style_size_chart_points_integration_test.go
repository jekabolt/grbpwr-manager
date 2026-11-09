package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestStyleSizeChartPatternProvenance drives points against real pattern sources. It proves the
// server stamps provenance and the staleness verdict moves with source geometry.
func TestStyleSizeChartPatternProvenance(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	rig := newRTRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()

	var measurementID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM measurement_name").Scan(&measurementID))
	chart0, err := T.GetStyleSizeChart(ctx, fx.cardID)
	require.NoError(t, err)
	cell := entity.StyleSizeChartCell{SizeID: fx.sizeA, MeasurementNameID: measurementID,
		Value: decimal.RequireFromString("50"), Source: entity.StyleSizeChartCellSourcePattern}
	point := entity.StyleSizeChartPoint{MeasurementNameID: measurementID, PomCode: "length-cb",
		Position: 1, TolMinusCm: decimal.RequireFromString("0.5"), TolPlusCm: decimal.RequireFromString("0.5"),
		InShop: true, BasisPieceLineKeys: []string{rtPieceFront}}
	chart, err := T.UpdateStyleSizeChart(ctx, fx.cardID, chart0.LockVersion,
		[]entity.StyleSizeChartCell{cell}, 0, nil, []entity.StyleSizeChartPoint{point}, true, nil, nil)
	require.NoError(t, err)
	require.Len(t, chart.Points, 1)
	require.False(t, chart.Points[0].Stale)

	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_size_pattern SET version = version + 1 WHERE tech_card_id = ? AND line_key = ?",
		fx.cardID, rtSheetGraded)
	require.NoError(t, err)
	moved, err := T.GetStyleSizeChart(ctx, fx.cardID)
	require.NoError(t, err)
	require.True(t, moved.Points[0].Stale)

	restamped, err := T.UpdateStyleSizeChart(ctx, fx.cardID, moved.LockVersion,
		[]entity.StyleSizeChartCell{cell}, 0, nil, nil, false, nil, nil)
	require.NoError(t, err)
	require.False(t, restamped.Points[0].Stale, "the next chart save stamps current pattern provenance")

	shrink := decimal.RequireFromString("3")
	withShrink, err := T.UpdateStyleSizeChart(ctx, fx.cardID, restamped.LockVersion,
		[]entity.StyleSizeChartCell{cell}, 0, nil, restamped.Points, true, &shrink, nil)
	require.NoError(t, err)
	legacy, err := T.UpdateStyleSizeChart(ctx, fx.cardID, withShrink.LockVersion,
		[]entity.StyleSizeChartCell{cell}, 0, nil, nil, false, nil, nil)
	require.NoError(t, err)
	require.True(t, legacy.ShrinkagePct.Valid, "a save without points keeps the card shrinkage override")
	require.True(t, legacy.ShrinkagePct.Decimal.Equal(shrink))

	// A cell with no point row gets one from the server. Only an old client's row is public.
	var otherID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT MIN(id) FROM measurement_name WHERE id > ?", measurementID).Scan(&otherID))
	other := entity.StyleSizeChartCell{SizeID: fx.sizeA, MeasurementNameID: otherID,
		Value: decimal.RequireFromString("40"), Source: entity.StyleSizeChartCellSourceManual}
	inShopOf := func(c entity.StyleSizeChart, id int) bool {
		for _, p := range c.Points {
			if p.MeasurementNameID == id {
				return p.InShop
			}
		}
		t.Fatalf("no point for measurement %d", id)
		return false
	}
	fresh, err := T.UpdateStyleSizeChart(ctx, fx.cardID, legacy.LockVersion,
		[]entity.StyleSizeChartCell{cell, other}, 0, nil, legacy.Points[:1], true, nil, nil)
	require.NoError(t, err)
	require.False(t, inShopOf(fresh, otherID), "a new client's unsent row stays off the shop")
	cleared, err := T.UpdateStyleSizeChart(ctx, fx.cardID, fresh.LockVersion,
		[]entity.StyleSizeChartCell{cell}, 0, nil, fresh.Points[:1], true, nil, nil)
	require.NoError(t, err)
	old, err := T.UpdateStyleSizeChart(ctx, fx.cardID, cleared.LockVersion,
		[]entity.StyleSizeChartCell{cell, other}, 0, nil, nil, false, nil, nil)
	require.NoError(t, err)
	require.True(t, inShopOf(old, otherID), "an old client's row stays public, as before points")
}

// TestStyleSizeChartPointsMigrationReplaysOverExistingCells simulates the important upgrade shape:
// chart cells already exist, the point table has no row for them, and the real migration is replayed
// under production's ANSI_QUOTES mode. It must backfill once and remain idempotent.
func TestStyleSizeChartPointsMigrationReplaysOverExistingCells(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	rig := newRTRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	var measurementID int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT MIN(id) FROM measurement_name m
		WHERE NOT EXISTS (SELECT 1 FROM tech_card_size_measurement c
		                  WHERE c.tech_card_id = ? AND c.measurement_name_id = m.id)`, fx.cardID).Scan(&measurementID))
	_, err := testDB.ExecContext(ctx, `
		INSERT INTO tech_card_size_measurement (tech_card_id, size_id, measurement_name_id, measurement_value)
		VALUES (?, ?, ?, 50)`, fx.cardID, fx.sizeA, measurementID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_size_chart_point WHERE tech_card_id = ?", fx.cardID)
	require.NoError(t, err)

	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	_, err = conn.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')")
	require.NoError(t, err)
	up := migrationUpBody(t, "0457_tech_card_size_chart_points.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "migration replay %d", i+1)
	}
	var points int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_size_chart_point
		WHERE tech_card_id = ? AND measurement_name_id = ?`, fx.cardID, measurementID).Scan(&points))
	require.Equal(t, 1, points)
	var source string
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT source FROM tech_card_size_measurement
		WHERE tech_card_id = ? AND measurement_name_id = ?`, fx.cardID, measurementID).Scan(&source))
	require.Equal(t, entity.StyleSizeChartCellSourceManual, source)
}
