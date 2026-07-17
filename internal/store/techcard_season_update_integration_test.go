package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardSeasonWriteOwnership is the acceptance test for problem 010's AddTechCard half plus the
// R4/§14.7 write-decomposition that superseded its UpdateTechCard half (fix(pr6-e), wave 5): AddTechCard
// still seeds+normalizes season_code/season_year/season from the structured season contract (a style is
// always born with a season), but season is now a catalogue-style/SKU fact owned SOLELY by UpdateStyle —
// UpdateTechCard writes PLM facts only and must leave season untouched even when the caller's
// TechCardInsert carries a different one, so a season change goes through UpdateStyle's frozen-sibling
// guard (techcard.go's UpdateTechCard doc comment) instead of silently re-minting here. Originally named
// TestUpdateTechCardPersistsNormalizedSeason and asserted the opposite of its second half; renamed and
// re-pointed at the current contract rather than reverted, since the ownership split is this wave's
// intentional design (283ff15), not a regression.
func TestTechCardSeasonWriteOwnership(t *testing.T) {
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

	// R4/§14.7: season is a style/SKU fact now written SOLELY by UpdateStyle. UpdateTechCard must
	// leave it exactly as it was, even though this update's TechCardInsert asks for SS26 -> FW27 —
	// PLM and style facts are never written by the same path.
	code, year = readSeason()
	require.Equal(t, "SS", code, "UpdateTechCard must NOT touch season_code — that's UpdateStyle's fact now")
	require.Equal(t, int64(2026), year, "UpdateTechCard must NOT touch season_year — that's UpdateStyle's fact now")
}
