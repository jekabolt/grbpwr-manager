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
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

const (
	geometryLabelKey = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	geometryPieceKey = "01JABCDEFGHJKMNPQRSTVWXYZ2"
	geometryPoseKey  = "01JABCDEFGHJKMNPQRSTVWXYZ3"
	geometrySeamKey  = "01JABCDEFGHJKMNPQRSTVWXYZ4"
)

func requireGarmentLabelGeometryDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" && !strings.Contains(testCfg.DSN, "127.0.0.1") && !strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container")
	}
}

func geometryAnchor(key string) entity.TechCardSeamAnchor {
	return entity.TechCardSeamAnchor{
		PieceLineKey: key,
		Samples: []entity.TechCardSeamSample{
			{U: 0, V: 0}, {U: .25, V: .1}, {U: .5, V: .2}, {U: .75, V: .3}, {U: 1, V: .4},
		},
		PerimShare: .25, LenMm: 40,
	}
}

func TestTechCardGarmentLabelGeometryAndSeams(t *testing.T) {
	requireGarmentLabelGeometryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()
	ns := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	step := entity.TechCardOperation{
		OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"), Zone: entity.ZoneOuter,
		LabelKeys: []string{geometryLabelKey},
	}
	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "LABEL GEOMETRY", Stage: entity.TechCardStageProto, StyleNumber: ns("LBG-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft, LabelsAware: true,
		Pieces: []entity.TechCardPiece{{
			LineKey: geometryPieceKey, Name: "front", PiecesPerGarment: 1, Grainline: "lengthwise",
		}},
		GarmentLabels: []entity.TechCardGarmentLabel{{
			Key: "size", LineKey: strings.ToLower(geometryLabelKey), QtyPerGarment: 1,
			WidthMm:  decimal.NewNullDecimal(decimal.RequireFromString("40.25")),
			HeightMm: decimal.NewNullDecimal(decimal.RequireFromString("15")),
			Fold:     ns("half"),
		}},
		Operations: []entity.TechCardOperation{step},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	read, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, read.GarmentLabels, 1)
	require.Equal(t, geometryLabelKey, read.GarmentLabels[0].LineKey)
	require.Equal(t, "40.25", read.GarmentLabels[0].WidthMm.Decimal.String())
	require.Equal(t, "15", read.GarmentLabels[0].HeightMm.Decimal.String())
	require.Equal(t, "half", read.GarmentLabels[0].Fold.String)
	require.Equal(t, []string{geometryLabelKey}, read.Operations[0].LabelKeys)

	placed := geometryAnchor(geometryLabelKey)
	placed.Placement = &entity.TechCardSeamPlacement{XMm: 12, YMm: 6, RotDeg: 90}
	pose := entity.TechCardSeamInput{
		SeamKey: geometryPoseKey, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindPose,
		Direction: entity.TechCardSeamDirectionUnknown, Source: entity.TechCardSeamSourceManual,
		SideA: []entity.TechCardSeamAnchor{placed}, SideB: []entity.TechCardSeamAnchor{geometryAnchor(geometryPieceKey)},
	}
	_, _, err = T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{TechCardId: cardID, By: "ann", Seams: []entity.TechCardSeamInput{pose}})
	require.NoError(t, err, "a label key is a legal pose anchor")

	remove := read.TechCardInsert
	remove.LabelsAware = true
	remove.GarmentLabels = nil
	remove.Operations = append([]entity.TechCardOperation(nil), read.Operations...)
	remove.Operations[0].LabelKeys = nil
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &remove, read.LockVersion))
	afterPoseRemoval, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Empty(t, afterPoseRemoval.GarmentLabels)
	require.Empty(t, afterPoseRemoval.Seams, "removing a label deletes its pose row in the same transaction")

	readd := afterPoseRemoval.TechCardInsert
	readd.LabelsAware = true
	readd.GarmentLabels = []entity.TechCardGarmentLabel{{Key: "size", LineKey: geometryLabelKey, QtyPerGarment: 1}}
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &readd, afterPoseRemoval.LockVersion))
	withLabel, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	realSeam := entity.TechCardSeamInput{
		SeamKey: geometrySeamKey, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
		Direction: entity.TechCardSeamSame, Source: entity.TechCardSeamSourceManual,
		SideA: []entity.TechCardSeamAnchor{geometryAnchor(geometryLabelKey)},
		SideB: []entity.TechCardSeamAnchor{geometryAnchor(geometryPieceKey)},
	}
	_, _, err = T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{TechCardId: cardID, By: "ann", Seams: []entity.TechCardSeamInput{realSeam}})
	require.NoError(t, err)

	blocked := withLabel.TechCardInsert
	blocked.LabelsAware = true
	blocked.GarmentLabels = nil
	err = T.UpdateTechCard(ctx, cardID, &blocked, withLabel.LockVersion)
	var violation *entity.ValidationError
	require.True(t, errors.As(err, &violation), "got %v", err)
	require.Equal(t, "garment_labels", violation.Field)
	require.Equal(t, "garment_label_in_use", violation.Reason)
}

func TestMigration0454UpDownUp(t *testing.T) {
	requireGarmentLabelGeometryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	ns := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	cardID, err := st.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "MIGRATION 0454", Stage: entity.TechCardStageProto, StyleNumber: ns("M0454"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft, LabelsAware: true,
		GarmentLabels: []entity.TechCardGarmentLabel{{Key: "size", LineKey: geometryLabelKey, QtyPerGarment: 1, Fold: ns("half")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.TechCards().DeleteTechCard(context.Background(), cardID) })
	f, err := os.Open("sql/0454_tech_card_garment_label_geometry.sql")
	require.NoError(t, err)
	parsed, err := sqlparse.ParseMigration(f)
	require.NoError(t, f.Close())
	require.NoError(t, err)
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')`)
	require.NoError(t, err)
	run := func(name string, statements []string) {
		t.Helper()
		for i, statement := range statements {
			_, err := conn.ExecContext(ctx, statement)
			require.NoErrorf(t, err, "%s statement %d", name, i)
		}
	}

	// The suite starts fully migrated: this is the first Up. Down must preserve the canonical fold
	// in the legacy note; Up migrates it again, and the second Up is the partial-retry proof.
	var labelID int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT id FROM tech_card_garment_label WHERE tech_card_id = ? AND line_key IS NOT NULL`, cardID).Scan(&labelID))
	_, err = conn.ExecContext(ctx, `UPDATE tech_card_garment_label SET folding = NULL, fold = 'half' WHERE id = ?`, labelID)
	require.NoError(t, err)
	run("down", parsed.DownStatements)
	var folding sql.NullString
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT folding FROM tech_card_garment_label WHERE id = ?`, labelID).Scan(&folding))
	require.Equal(t, "half", folding.String)
	run("up", parsed.UpStatements)
	var fold, legacy sql.NullString
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT fold, folding FROM tech_card_garment_label WHERE id = ?`, labelID).Scan(&fold, &legacy))
	require.Equal(t, "half", fold.String)
	require.False(t, legacy.Valid)
	run("up retry", parsed.UpStatements)
}
