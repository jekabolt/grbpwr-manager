package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardReadExposesStyleFacts locks the read-projection added so the constructor can display
// and edit-in-place the style catalogue facts written via UpdateStyle (fit / legacy composition /
// care) — they live on the tech_card row but were not on the tech-card read contract. Composition
// must come back as PLAIN TEXT (M1: the column is native JSON, stored as a quoted scalar; the read
// JSON_UNQUOTEs it), never a JSON-encoded value.
func TestTechCardReadExposesStyleFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	tcID := seedSpineStyle(ctx, t, "STYLE-FACTS")

	// Read the current shared lock, then write the facts via UpdateStyle (the sole writer). Season
	// and gender must be a valid combination for the tech_card CHECK constraints.
	pre, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	const plainComposition = "80% wool, 20% polyamide"
	_, err = s.Products().UpdateStyle(ctx, tcID, pre.LockVersion, entity.StylePatch{
		TopCategoryId:    1,
		Season:           entity.SeasonEnum("SS"),
		TargetGender:     entity.GenderEnum("unisex"),
		Fit:              sql.NullString{String: "relaxed", Valid: true},
		Composition:      sql.NullString{String: plainComposition, Valid: true},
		CareInstructions: sql.NullString{String: "dry clean only", Valid: true},
	})
	require.NoError(t, err)

	tc, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, "relaxed", tc.Fit.String, "fit surfaces on the tech-card read")
	require.Equal(t, "dry clean only", tc.CareInstructions.String, "care surfaces on the tech-card read")
	require.Equal(t, plainComposition, tc.Composition.String,
		"composition surfaces as plain text on the tech-card read (M1: JSON scalar unquoted)")
}
