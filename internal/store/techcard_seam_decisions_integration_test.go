package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// listSeamDecisionsForTest reads a card's decision log back in insert order (no read RPC exists).
func listSeamDecisionsForTest(ctx context.Context, t *testing.T, cardID int) []entity.TechCardSeamDecision {
	t.Helper()
	rows, err := testDB.QueryContext(ctx, `
		SELECT id, tech_card_id, operation_number, action, seam_key, tier, shown, client_version, created_by, created_at
		FROM tech_card_seam_decision WHERE tech_card_id = ? ORDER BY id`, cardID)
	require.NoError(t, err)
	defer rows.Close()
	var out []entity.TechCardSeamDecision
	for rows.Next() {
		var d entity.TechCardSeamDecision
		require.NoError(t, rows.Scan(&d.Id, &d.TechCardId, &d.OperationNumber, &d.Action, &d.SeamKey, &d.Tier,
			&d.ShownJSON, &d.ClientVersion, &d.CreatedBy, &d.CreatedAt))
		out = append(out, d)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestTechCardSeamDecisions pins the store side of the seam decision log (0439): a batch is
// appended in order with created_by, an empty shown is NULL, a re-send appends (never upserts),
// an unknown card is sql.ErrNoRows, and the log dies with its card.
//
// Integration test: runs only against a real MySQL (TestMain connects + migrates).
func TestTechCardSeamDecisions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	cardID := seedSpineStyle(ctx, t, "G2-SEAM-DECISIONS")
	const key = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	shown := `{"a": [{"k": 2, "len": 410, "piece": "BP"}], "b": [{"k": 5, "len": 408, "piece": "FRONT_L"}]}`

	n, err := tc.AddTechCardSeamDecisions(ctx, entity.TechCardSeamDecisionsWrite{
		TechCardId: cardID, By: "anna",
		Decisions: []entity.TechCardSeamDecisionInput{
			{OperationNumber: 3, Action: entity.TechCardSeamDecisionAccept, SeamKey: "01jabcdefghjkmnpqrstvwxyz0",
				Tier: entity.TechCardSeamGuessTierConfident, Shown: shown, ClientVersion: "a1b2c3d"},
			{OperationNumber: 4, Action: entity.TechCardSeamDecisionSkip, Tier: entity.TechCardSeamGuessTierNone},
			{OperationNumber: 0, Action: entity.TechCardSeamDecisionRefuse, SeamKey: key},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 3, n)

	got := listSeamDecisionsForTest(ctx, t, cardID)
	require.Len(t, got, 3)
	require.Equal(t, cardID, got[0].TechCardId)
	require.Equal(t, 3, got[0].OperationNumber)
	require.Equal(t, entity.TechCardSeamDecisionAccept, got[0].Action)
	require.Equal(t, key, got[0].SeamKey, "key normalised to upper case")
	require.Equal(t, entity.TechCardSeamGuessTierConfident, got[0].Tier)
	require.True(t, got[0].ShownJSON.Valid)
	require.JSONEq(t, shown, got[0].ShownJSON.String)
	require.Equal(t, "a1b2c3d", got[0].ClientVersion)
	require.Equal(t, "anna", got[0].CreatedBy)
	require.False(t, got[0].CreatedAt.IsZero())

	require.Equal(t, entity.TechCardSeamDecisionSkip, got[1].Action)
	require.Equal(t, "", got[1].SeamKey)
	require.Equal(t, entity.TechCardSeamGuessTierNone, got[1].Tier)
	require.False(t, got[1].ShownJSON.Valid, "empty shown is stored NULL")

	require.Equal(t, 0, got[2].OperationNumber, "0 = card-level seam review")
	require.Equal(t, entity.TechCardSeamGuessTierUnknown, got[2].Tier)
	require.Less(t, got[0].Id, got[1].Id)

	// --- append-only: the same decision again is a new row ---
	n, err = tc.AddTechCardSeamDecisions(ctx, entity.TechCardSeamDecisionsWrite{
		TechCardId: cardID, By: "boris",
		Decisions:  []entity.TechCardSeamDecisionInput{{OperationNumber: 3, Action: entity.TechCardSeamDecisionOwn, SeamKey: key}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Len(t, listSeamDecisionsForTest(ctx, t, cardID), 4)

	// --- the store re-validates ---
	_, err = tc.AddTechCardSeamDecisions(ctx, entity.TechCardSeamDecisionsWrite{
		TechCardId: cardID, Decisions: []entity.TechCardSeamDecisionInput{{OperationNumber: 1, Action: entity.TechCardSeamDecisionSkip, SeamKey: key}},
	})
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "%v", err)

	// --- an unknown card is sql.ErrNoRows (handler → NotFound) ---
	var maxID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM tech_card").Scan(&maxID))
	_, err = tc.AddTechCardSeamDecisions(ctx, entity.TechCardSeamDecisionsWrite{
		TechCardId: maxID + 1_000_000, Decisions: []entity.TechCardSeamDecisionInput{{OperationNumber: 1, Action: entity.TechCardSeamDecisionSkip}},
	})
	require.True(t, errors.Is(err, sql.ErrNoRows), "%v", err)

	// --- the log dies with its card ---
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", cardID)
	require.NoError(t, err)
	require.Empty(t, listSeamDecisionsForTest(ctx, t, cardID))
}
