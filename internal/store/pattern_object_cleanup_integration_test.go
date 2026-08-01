package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPatternObjectCleanupCandidates covers the DB half of audit #61. Mutation methods collect URLs
// before full-replace/CASCADE, but return only bucket-owned objects with no remaining reference in
// either pattern table. Object deletion itself is intentionally an admin-layer post-commit effect.
func TestPatternObjectCleanupCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	styleID := insertSeasonedTestStyle(ctx, t, "PATTERN-ORPHAN", "SS", "SS26", 2026)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size ORDER BY id LIMIT 1`).Scan(&sizeID))

	sharedURL := "https://cdn.example/base/tech-card-patterns/2026/august/shared.pdf"
	externalURL := "https://files.example/not-managed.pdf"
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_size_pattern (tech_card_id, size_id, url, display_order)
		VALUES (?, ?, ?, 0), (?, ?, ?, 1)`,
		styleID, sizeID, sharedURL, styleID, sizeID, externalURL)
	require.NoError(t, err)

	fittingID, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		Patterns:    []entity.FittingPattern{{URL: sharedURL}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM fitting WHERE id = ?`, fittingID) })

	// The card's CASCADE removes both of its rows. The arbitrary external URL is never eligible, and
	// the managed URL remains referenced by the fitting, so neither may be handed to object storage.
	orphaned, err := s.TechCards().DeleteTechCardAndListOrphanedPatternURLs(ctx, styleID)
	require.NoError(t, err)
	require.Empty(t, orphaned)

	// Removing the fitting's last copy makes the shared managed object eligible after commit.
	orphaned, err = s.Fittings().UpdateFittingAndListOrphanedPatternURLs(ctx, fittingID, &entity.FittingInsert{
		FittingDate: time.Now().UTC(), Status: entity.FittingDone, Verdict: entity.FittingApproved,
	}, 0)
	require.NoError(t, err)
	require.Equal(t, []string{sharedURL}, orphaned)

	// The fitting CASCADE path follows the same contract.
	deleteURL := "https://cdn.example/base/tech-card-patterns/2026/august/delete.pdf"
	deleteFittingID, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		Patterns:    []entity.FittingPattern{{URL: deleteURL}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM fitting WHERE id = ?`, deleteFittingID)
	})
	orphaned, err = s.Fittings().DeleteFittingAndListOrphanedPatternURLs(ctx, deleteFittingID)
	require.NoError(t, err)
	require.Equal(t, []string{deleteURL}, orphaned)
}
