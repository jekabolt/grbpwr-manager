package store

import (
	"context"
	"database/sql"
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

	sharedOriginURL := "https://patterns.fra1.digitaloceanspaces.com/base/tech-card-patterns/2026/august/shared.pdf"
	sharedCDNURL := "https://cdn.example/base/tech-card-patterns/2026/august/shared.pdf"
	externalURL := "https://files.example/not-managed.pdf"
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_size_pattern (tech_card_id, size_id, url, display_order)
		VALUES (?, ?, ?, 0), (?, ?, ?, 1)`,
		styleID, sizeID, sharedOriginURL, styleID, sizeID, externalURL)
	require.NoError(t, err)

	fittingID, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		Patterns:    []entity.FittingPattern{{URL: sharedCDNURL}},
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
	require.Equal(t, []string{sharedCDNURL}, orphaned)

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

// TestPatternNameCarryForward covers the DB round-trip of the presence-gated pattern display
// name across the full-replace save: a payload row whose name is absent inherits the stored
// name of the row it replaces (stale-client protection), a present name overwrites, and a
// present-empty name clears. Semantics live in storeutil.ResolvePatternName (unit-tested);
// this exercises the column wiring end to end through the fitting store.
func TestPatternNameCarryForward(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	url := "https://cdn.example/base/tech-card-patterns/2026/august/named.pdf"
	named := sql.NullString{String: "перед", Valid: true}

	fittingID, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		Patterns:    []entity.FittingPattern{{URL: url, Name: named}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM fitting WHERE id = ?`, fittingID) })

	readName := func() sql.NullString {
		f, err := s.Fittings().GetFittingById(ctx, fittingID)
		require.NoError(t, err)
		require.Len(t, f.Patterns, 1)
		return f.Patterns[0].Name
	}
	require.Equal(t, named, readName())

	resave := func(lockVersion int, name sql.NullString) {
		_, err := s.Fittings().UpdateFittingAndListOrphanedPatternURLs(ctx, fittingID, &entity.FittingInsert{
			FittingDate: time.Now().UTC(), Status: entity.FittingPlanned, Verdict: entity.FittingPending,
			Patterns: []entity.FittingPattern{{URL: url, Name: name}},
		}, lockVersion)
		require.NoError(t, err)
	}

	// A stale client re-saves without the field — the stored name survives the full-replace.
	resave(0, sql.NullString{})
	require.Equal(t, named, readName())

	// A present name overwrites.
	renamed := sql.NullString{String: "спинка", Valid: true}
	resave(1, renamed)
	require.Equal(t, renamed, readName())

	// Present-empty is an explicit clear, stored as NULL.
	resave(2, sql.NullString{String: "", Valid: true})
	require.False(t, readName().Valid)
}

// TestTechCardPatternNameCarryForward exercises the same presence-gated name semantics through
// the TECH-CARD store, where name is entangled with the version/uploaded_at carry-forward, the
// live-size projection and the payload dedupe. It also locks the deliberate asymmetry — across a
// size move uploaded_at survives (matched by url) but name does NOT (matched by size|url), since
// «перед» on one size says nothing about what the sheet is on another.
func TestTechCardPatternNameCarryForward(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var sizeA, sizeB int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeA))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", sizeA).Scan(&sizeB))

	url := "https://cdn.example/base/tech-card-patterns/2026/august/tc-named.pdf"
	mkTC := func(patterns ...entity.TechCardSizePattern) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber:     sql.NullString{String: "PN-STYLE", Valid: true},
			Name:            "PN",
			Stage:           entity.TechCardStageProto,
			ApprovalState:   entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeA, sizeB},
			Patterns:        patterns,
		}
	}

	id, err := s.TechCards().AddTechCard(ctx, mkTC(entity.TechCardSizePattern{
		SizeId: sizeA, URL: url, Name: sql.NullString{String: "перед", Valid: true},
	}))
	require.NoError(t, err)
	defer func() { _ = s.TechCards().DeleteTechCard(ctx, id) }()

	read := func() entity.TechCardSizePattern {
		tc, err := s.TechCards().GetTechCardById(ctx, id)
		require.NoError(t, err)
		require.Len(t, tc.Patterns, 1)
		return tc.Patterns[0]
	}
	first := read()
	require.Equal(t, "перед", first.Name.String)
	require.Equal(t, 1, first.Version)

	resave := func(lockVersion int, p entity.TechCardSizePattern) {
		require.NoError(t, s.TechCards().UpdateTechCard(ctx, id, mkTC(p), lockVersion))
	}

	// A stale client re-saves without the field — name AND version survive the full-replace.
	resave(0, entity.TechCardSizePattern{SizeId: sizeA, URL: url})
	after := read()
	require.Equal(t, "перед", after.Name.String)
	require.Equal(t, 1, after.Version)

	// Moving the sheet to another size keeps the upload identity but resets the name.
	resave(1, entity.TechCardSizePattern{SizeId: sizeB, URL: url})
	moved := read()
	require.Equal(t, sizeB, moved.SizeId)
	require.False(t, moved.Name.Valid, "name must not follow the sheet across sizes")
}
