package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardOperationDraftRoundTrip — черновик каркаса сборки (0410) проходит через стор: записал
// draft=true на одном шаге и false на другом -> прочитал -> метки на своих шагах; полная замена
// списка (ровно то, что делает автосейв формы) переносит метку и снимает её, когда клиент прислал
// false. Держит четыре списка (ALTER 0410, named-map INSERT'а, SELECT операций, поле entity).
//
// SAFE ONLY against a local container DSN — see mysql_test.go / project memory
// (store-tests-drop-prod-db: the non-CI TestMain talks to the configured prod DB and DROPs tables).
func TestTechCardOperationDraftRoundTrip(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	{
		di, derr := s.Cache().GetDictionaryInfo(ctx)
		require.NoError(t, derr)
		hf, herr := s.Hero().GetHero(ctx)
		require.NoError(t, herr)
		require.NoError(t, cache.InitConsts(ctx, di, hf))
	}
	T := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

	drafted := entity.TechCardOperation{
		OperationNumber: ni(10), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
		Note: ns("written by the skeleton"), Draft: true,
	}
	reviewed := entity.TechCardOperation{
		OperationNumber: ni(20), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
		Note: ns("written by a person"),
	}
	insert := func(ops ...entity.TechCardOperation) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Operation Draft Style", Stage: entity.TechCardStageProto,
			StyleNumber: ns("OPD-RT-1"), Purpose: entity.TechCardPurposeSellable,
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SeasonCode: ns("SS"), SeasonYear: ni(2026),
			Operations: ops,
		}
	}

	tcID, err := T.AddTechCard(ctx, insert(drafted, reviewed))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	draftByNumber := func(card *entity.TechCard) map[int32]bool {
		out := map[int32]bool{}
		for _, o := range card.Operations {
			out[o.OperationNumber.Int32] = o.Draft
		}
		return out
	}

	got, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, map[int32]bool{10: true, 20: false}, draftByNumber(got),
		"draft must come back on the step that carried it and only there")

	// Full replacement (the form's autosave): the mark travels; a reviewed step comes back false.
	drafted.Draft = false
	reviewed.Draft = true
	require.NoError(t, T.UpdateTechCard(ctx, tcID, insert(drafted, reviewed), got.LockVersion))
	after, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, map[int32]bool{10: false, 20: true}, draftByNumber(after),
		"a full replacement writes exactly the marks the payload carried")
}
