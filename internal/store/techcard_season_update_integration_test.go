package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestUpdateTechCardPersistsNormalizedSeason is the acceptance test for problem 010: UpdateTechCard
// must persist the normalized season_code/season_year supplied by the structured season contract,
// so a pair change (SS26 -> FW27) cannot leave the SKU-facing facts stale.
func TestUpdateTechCardPersistsNormalizedSeason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	mkTC := func(code entity.SeasonEnum, year int32) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber:     sql.NullString{String: "T10-STYLE", Valid: true},
			Name:            "T10",
			SeasonCode:      sql.NullString{String: string(code), Valid: true},
			SeasonYear:      sql.NullInt32{Int32: year, Valid: true},
			Stage:           entity.TechCardStageProto,
			ApprovalState:   entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
		}
	}

	id, err := s.TechCards().AddTechCard(ctx, mkTC(entity.SeasonSS, 2026))
	require.NoError(t, err)
	defer func() { _ = s.TechCards().DeleteTechCard(ctx, id) }()

	readSeason := func() (string, int64) {
		var code sql.NullString
		var year sql.NullInt64
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT season_code, season_year FROM tech_card WHERE id = ?", id).Scan(&code, &year))
		return code.String, year.Int64
	}

	code, year := readSeason()
	require.Equal(t, "SS", code, "add path normalizes season")
	require.Equal(t, int64(2026), year)

	var lockVersion int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT lock_version FROM tech_card WHERE id = ?", id).Scan(&lockVersion))

	require.NoError(t, s.TechCards().UpdateTechCard(ctx, id, mkTC(entity.SeasonFW, 2027), lockVersion))

	code, year = readSeason()
	require.Equal(t, "FW", code, "update must re-normalize season_code")
	require.Equal(t, int64(2027), year, "update must re-normalize season_year")
}
