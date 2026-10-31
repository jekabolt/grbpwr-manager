package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardSeamHints pins the store side of the seam hints (0440): a run per (card, digest,
// schema) that a rerun replaces, the latest run, overrides upserted / deleted and never touched by a
// run, the owner's code dictionary, the technical-sheet media filter, and everything dying with the
// card.
//
// Integration test: runs only against a real MySQL (TestMain connects + migrates).
func TestTechCardSeamHints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	cardID := seedSpineStyle(ctx, t, "G2-SEAM-HINTS")
	digA, digB := strings.Repeat("a", 64), strings.Repeat("b", 64)

	// --- runs ---
	_, found, err := tc.LatestTechCardSeamHintRun(ctx, cardID, 1)
	require.NoError(t, err)
	require.False(t, found)

	r1, err := tc.SaveTechCardSeamHintRun(ctx, entity.TechCardSeamHintRun{
		TechCardId: cardID, InputDigest: digA, SchemaVersion: 1, Model: "m1", Hints: `{"roles": []}`, CreatedBy: "anna",
	})
	require.NoError(t, err)
	require.NotZero(t, r1.Id)
	require.Equal(t, "anna", r1.CreatedBy)
	require.False(t, r1.CreatedAt.IsZero())

	time.Sleep(5 * time.Millisecond)
	r2, err := tc.SaveTechCardSeamHintRun(ctx, entity.TechCardSeamHintRun{
		TechCardId: cardID, InputDigest: digB, SchemaVersion: 1, Model: "m1", Hints: `{"roles": [1]}`, CreatedBy: "anna",
	})
	require.NoError(t, err)
	latest, found, err := tc.LatestTechCardSeamHintRun(ctx, cardID, 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, digB, latest.InputDigest)

	// A forced rerun of digest A replaces its row (one row per digest) and becomes the latest.
	time.Sleep(5 * time.Millisecond)
	r3, err := tc.SaveTechCardSeamHintRun(ctx, entity.TechCardSeamHintRun{
		TechCardId: cardID, InputDigest: digA, SchemaVersion: 1, Model: "m2", Hints: `{"roles": [2]}`, CreatedBy: "boris",
	})
	require.NoError(t, err)
	require.Equal(t, r1.Id, r3.Id)
	require.Equal(t, "m2", r3.Model)
	require.Equal(t, "boris", r3.CreatedBy)
	require.JSONEq(t, `{"roles": [2]}`, r3.Hints)
	require.True(t, r3.CreatedAt.After(r2.CreatedAt), "%v vs %v", r3.CreatedAt, r2.CreatedAt)
	got, found, err := tc.GetTechCardSeamHintRun(ctx, cardID, digA, 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, r3.Id, got.Id)
	_, found, err = tc.GetTechCardSeamHintRun(ctx, cardID, digA, 2)
	require.NoError(t, err)
	require.False(t, found, "another schema version is another run")
	latest, _, err = tc.LatestTechCardSeamHintRun(ctx, cardID, 1)
	require.NoError(t, err)
	require.Equal(t, digA, latest.InputDigest)

	var maxID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM tech_card").Scan(&maxID))
	_, err = tc.SaveTechCardSeamHintRun(ctx, entity.TechCardSeamHintRun{
		TechCardId: maxID + 1_000_000, InputDigest: digA, SchemaVersion: 1, Hints: `{}`,
	})
	require.True(t, s.IsErrForeignKeyViolation(err), "%v", err)

	// --- overrides ---
	list, err := tc.SetTechCardSeamHintOverride(ctx, entity.TechCardSeamHintOverride{
		TechCardId: cardID, Kind: "role", HintKey: "FLP_R_MAIN", State: "fixed",
		Value: sql.NullString{String: `{"role":"flap","hand":"R"}`, Valid: true}, UpdatedBy: "anna",
	})
	require.NoError(t, err)
	require.Len(t, list, 1)
	list, err = tc.SetTechCardSeamHintOverride(ctx, entity.TechCardSeamHintOverride{
		TechCardId: cardID, Kind: "join", HintKey: "870", State: "off", UpdatedBy: "anna",
	})
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Upsert: the same key changes in place.
	list, err = tc.SetTechCardSeamHintOverride(ctx, entity.TechCardSeamHintOverride{
		TechCardId: cardID, Kind: "role", HintKey: "FLP_R_MAIN", State: "ok",
		Value: sql.NullString{String: `{"role":"flap","hand":""}`, Valid: true}, UpdatedBy: "boris",
	})
	require.NoError(t, err)
	require.Len(t, list, 2)
	var role entity.TechCardSeamHintOverride
	for _, o := range list {
		if o.Kind == "role" {
			role = o
		}
	}
	require.Equal(t, "ok", role.State)
	require.Equal(t, "boris", role.UpdatedBy)
	require.JSONEq(t, `{"role":"flap","hand":""}`, role.Value.String)

	// A rerun never touches the overrides.
	_, err = tc.SaveTechCardSeamHintRun(ctx, entity.TechCardSeamHintRun{
		TechCardId: cardID, InputDigest: digA, SchemaVersion: 1, Model: "m3", Hints: `{}`, CreatedBy: "anna",
	})
	require.NoError(t, err)
	list, err = tc.ListTechCardSeamHintOverrides(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// An empty state deletes.
	list, err = tc.SetTechCardSeamHintOverride(ctx, entity.TechCardSeamHintOverride{TechCardId: cardID, Kind: "join", HintKey: "870"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	_, err = tc.SetTechCardSeamHintOverride(ctx, entity.TechCardSeamHintOverride{
		TechCardId: maxID + 1_000_000, Kind: "join", HintKey: "1", State: "off",
	})
	require.True(t, errors.Is(err, sql.ErrNoRows) || s.IsErrForeignKeyViolation(err), "%v", err)

	// --- the technical-sheet filter ---
	tech := insertTestMedia(t, "seam-hints-tech")
	mood := insertTestMedia(t, "seam-hints-mood")
	free := insertTestMedia(t, "seam-hints-free")
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_media (tech_card_id, media_id, category, kind, display_order)
		VALUES (?, ?, 'technical', 'front', 0), (?, ?, 'moodboard', 'moodboard', 1)`, cardID, tech, cardID, mood)
	require.NoError(t, err)
	ids, err := tc.TechCardTechnicalMediaIDs(ctx, cardID, []int{tech, mood, free})
	require.NoError(t, err)
	require.Equal(t, []int{tech}, ids)
	ids, err = tc.TechCardTechnicalMediaIDs(ctx, cardID, nil)
	require.NoError(t, err)
	require.Empty(t, ids)
	_, err = tc.TechCardTechnicalMediaIDs(ctx, maxID+1_000_000, []int{tech})
	require.True(t, errors.Is(err, sql.ErrNoRows), "%v", err)

	// --- the owner's code dictionary ---
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM piece_code_dictionary WHERE code IN ('ZZFLP', 'ZZWB')")
	})
	e, found, err := tc.SetPieceCodeDictionaryEntry(ctx, entity.PieceCodeDictionaryEntry{
		Code: "ZZFLP", Role: "flap", UpdatedBy: "anna",
	})
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, e.Hand.Valid)
	e, _, err = tc.SetPieceCodeDictionaryEntry(ctx, entity.PieceCodeDictionaryEntry{
		Code: "ZZFLP", Role: "flap", Hand: sql.NullString{String: "R", Valid: true}, UpdatedBy: "boris",
	})
	require.NoError(t, err)
	require.Equal(t, "R", e.Hand.String)
	require.Equal(t, "boris", e.UpdatedBy)
	_, _, err = tc.SetPieceCodeDictionaryEntry(ctx, entity.PieceCodeDictionaryEntry{Code: "ZZWB", Role: "waistband", UpdatedBy: "anna"})
	require.NoError(t, err)
	dict, err := tc.ListPieceCodeDictionary(ctx)
	require.NoError(t, err)
	codes := map[string]string{}
	for _, d := range dict {
		codes[d.Code] = d.Role
	}
	require.Equal(t, "flap", codes["ZZFLP"])
	require.Equal(t, "waistband", codes["ZZWB"])
	_, found, err = tc.SetPieceCodeDictionaryEntry(ctx, entity.PieceCodeDictionaryEntry{Code: "ZZWB"})
	require.NoError(t, err)
	require.False(t, found)
	dict, err = tc.ListPieceCodeDictionary(ctx)
	require.NoError(t, err)
	for _, d := range dict {
		require.NotEqual(t, "ZZWB", d.Code)
	}

	// --- join rows per (op, op digest) — 0442 ---
	digC := strings.Repeat("c", 64)
	join := sql.NullString{String: `{"op":720,"a":{"pieces":["PLCK_L"],"edge":"long"},"b":{"pieces":["P_L_MAIN"],"edge":"front_edge"},"relation":"boundary","conf":"high"}`, Valid: true}
	require.NoError(t, tc.SaveTechCardSeamHintJoins(ctx, []entity.TechCardSeamHintJoin{
		{TechCardId: cardID, OpNumber: 720, OpDigest: digA, SchemaVersion: 2, Outcome: entity.SeamHintJoinOutcomeJoin, Hint: join, Model: "m1", CreatedBy: "anna"},
		{TechCardId: cardID, OpNumber: 870, OpDigest: digA, SchemaVersion: 2, Outcome: entity.SeamHintJoinOutcomeNotInferred, Model: "m1", CreatedBy: "anna"},
		{TechCardId: cardID, OpNumber: 860, OpDigest: digA, SchemaVersion: 2, Outcome: entity.SeamHintJoinOutcomeNone, CreatedBy: "anna"},
	}))
	time.Sleep(5 * time.Millisecond)
	// An edited op gets a row of its new digest; the old one stays. A rerun of a digest replaces it.
	require.NoError(t, tc.SaveTechCardSeamHintJoins(ctx, []entity.TechCardSeamHintJoin{
		{TechCardId: cardID, OpNumber: 870, OpDigest: digC, SchemaVersion: 2, Outcome: entity.SeamHintJoinOutcomeNone, Model: "m2", CreatedBy: "boris"},
		{TechCardId: cardID, OpNumber: 720, OpDigest: digA, SchemaVersion: 2, Outcome: entity.SeamHintJoinOutcomeNotInferred, Model: "m2", CreatedBy: "boris"},
	}))
	require.NoError(t, tc.SaveTechCardSeamHintJoins(ctx, nil))
	joins, err := tc.ListTechCardSeamHintJoins(ctx, cardID, 2)
	require.NoError(t, err)
	require.Len(t, joins, 4)
	require.Equal(t, 720, joins[0].OpNumber)
	require.Equal(t, entity.SeamHintJoinOutcomeNotInferred, joins[0].Outcome, "replaced in place")
	require.False(t, joins[0].Hint.Valid)
	require.Equal(t, "boris", joins[0].CreatedBy)
	require.Equal(t, 860, joins[1].OpNumber)
	require.False(t, joins[1].Hint.Valid)
	require.Equal(t, 870, joins[2].OpNumber)
	require.Equal(t, digC, joins[2].OpDigest, "newest first within an op")
	require.Equal(t, digA, joins[3].OpDigest)
	none, err := tc.ListTechCardSeamHintJoins(ctx, cardID, 1)
	require.NoError(t, err)
	require.Empty(t, none, "another store version is another cache")
	require.NoError(t, tc.SaveTechCardSeamHintJoins(ctx, []entity.TechCardSeamHintJoin{
		{TechCardId: cardID, OpNumber: 900, OpDigest: digB, SchemaVersion: 2, Outcome: entity.SeamHintJoinOutcomeJoin, Hint: join},
	}))
	joins, err = tc.ListTechCardSeamHintJoins(ctx, cardID, 2)
	require.NoError(t, err)
	require.JSONEq(t, join.String, joins[len(joins)-1].Hint.String)
	err = tc.SaveTechCardSeamHintJoins(ctx, []entity.TechCardSeamHintJoin{
		{TechCardId: maxID + 1_000_000, OpNumber: 1, OpDigest: digA, SchemaVersion: 2, Outcome: entity.SeamHintJoinOutcomeNone},
	})
	require.True(t, s.IsErrForeignKeyViolation(err), "%v", err)

	// --- everything of the card dies with it ---
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", cardID)
	require.NoError(t, err)
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM tech_card_seam_hint_run WHERE tech_card_id = ?) +
		(SELECT COUNT(*) FROM tech_card_seam_hint_override WHERE tech_card_id = ?) +
		(SELECT COUNT(*) FROM tech_card_seam_hint_join WHERE tech_card_id = ?)`, cardID, cardID, cardID).Scan(&n))
	require.Zero(t, n)
}
