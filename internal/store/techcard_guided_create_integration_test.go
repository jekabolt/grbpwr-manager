package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardGuidedCreate is the store half of the guided create (0407): one replay key makes one
// card (the second call is a replay, created=false), the card reads back guided and in `setup` on the
// list, and ExitTechCardGuide clears guided idempotently without bumping lock_version.
func TestTechCardGuidedCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	const key = "guided-create-it-7f3a"
	card := func() *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Guided IT", Stage: entity.TechCardStageIdea,
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		}
	}
	opts := entity.TechCardCreateOpts{RequestId: key, Guided: true}

	id, created, err := T.AddTechCardWithOpts(ctx, card(), opts)
	require.NoError(t, err)
	require.True(t, created)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })

	again, created, err := T.AddTechCardWithOpts(ctx, card(), opts)
	require.NoError(t, err)
	require.False(t, created, "a second call under the same key is a replay")
	require.Equal(t, id, again)
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM tech_card WHERE create_request_id = ?", key).Scan(&n))
	require.Equal(t, 1, n)
	byKey, err := T.TechCardIdByCreateRequestId(ctx, key)
	require.NoError(t, err)
	require.Equal(t, id, byKey)

	got, err := T.GetTechCardById(ctx, id)
	require.NoError(t, err)
	require.True(t, got.Guided)
	before := got.LockVersion

	list, _, err := T.ListTechCards(ctx, 50, 0, entity.Descending, entity.TechCardListFilter{Name: "Guided IT"})
	require.NoError(t, err)
	var seen bool
	for _, c := range list {
		if c.Id == id {
			seen = true
			require.True(t, c.Guided)
			require.True(t, c.Setup, "guided, no board picture, no concept")
		}
	}
	require.True(t, seen)

	require.NoError(t, T.ExitTechCardGuide(ctx, id))
	require.NoError(t, T.ExitTechCardGuide(ctx, id), "a second exit changes no row and is still OK")
	got, err = T.GetTechCardById(ctx, id)
	require.NoError(t, err)
	require.False(t, got.Guided)
	require.Equal(t, before, got.LockVersion, "exit must not bump lock_version")

	require.ErrorIs(t, T.ExitTechCardGuide(ctx, 1<<30), sql.ErrNoRows)

	// A plain create (no key, not guided) keeps NULL and never collides with another plain create.
	plain1, err := T.AddTechCard(ctx, card())
	require.NoError(t, err)
	plain2, err := T.AddTechCard(ctx, card())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id IN (?, ?)", plain1, plain2)
	})
	got, err = T.GetTechCardById(ctx, plain1)
	require.NoError(t, err)
	require.False(t, got.Guided)
}
