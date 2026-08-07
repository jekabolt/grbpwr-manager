package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// skipUnlessLocalContainer keeps this file away from the configured production DSN. A bare local
// `go test ./internal/store/...` uses config.toml, this suite runs Automigrate and its TestMain DROPS
// ALL TABLES on cleanup, so the guard is not a formality (see mysql_test.go / project memory).
func skipUnlessLocalContainer(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
}

// TestF6PatternSizeIndex is the acceptance test of Ф6.3 against the REAL table and the REAL sheet
// rows, and it has to be an integration test for one reason: the property the whole design rests on
// is that the FINGERPRINT IS THE SERVER'S. That is a statement about a transaction reading
// tech_card_size_pattern, and no unit test on a struct can make it.
//
// Four things are proved here, in the order they matter:
//
//  1. a parse over the scope's real sheets is stored and reads back as USABLE;
//  2. RE-UPLOADING a sheet makes the stored index STALE by itself, with nobody notified;
//  3. a client that names the wrong set of sheets is REFUSED, in both directions;
//  4. an EMPTY token set is stored and reads back as UNGRADED — a legal answer, not «no sizes».
func TestF6PatternSizeIndex(t *testing.T) {
	skipUnlessLocalContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	present := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	const (
		lineShell = "01F6LINESHELL00000000000A1"
		sheetA    = "01F6SHEETA00000000000000S1"
		sheetB    = "01F6SHEETB00000000000000S2"
		urlBase   = "https://cdn.example/base/tech-card-patterns/2026/august/"
	)
	bom := []entity.TechCardBomItem{{LineKey: lineShell, Section: entity.BomSectionFabric, Name: "Твил"}}
	pat := func(lineKey, file string) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{
			SizeId: sizeID, URL: urlBase + file, LineKey: lineKey,
			BomLineKey: present(lineShell),
		}
	}
	card := func(patterns []entity.TechCardSizePattern) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "F6 Pattern Size Index", StyleNumber: ns("F6-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems:        bom,
			Patterns:        patterns,
		}
	}

	tcID, err := T.AddTechCard(ctx, card([]entity.TechCardSizePattern{
		pat(sheetA, "f6-front.dxf"), pat(sheetB, "f6-back.dxf"),
	}))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})
	lock := 0
	resave := func(patterns []entity.TechCardSizePattern) {
		require.NoError(t, T.UpdateTechCard(ctx, tcID, card(patterns), lock))
		lock++
	}

	// The scope key of an UNSORTED card is the BOM line's key — entity.FabricScopeKey's legacy half,
	// the same value the generated column holds.
	scope := lineShell

	// ── 1. a parse over the scope's real sheets is stored ─────────────────────────────────────
	res, err := T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId:    tcID,
		ScopeKey:      scope,
		SheetLineKeys: []string{sheetA, sheetB},
		SizeTokens:    []string{"XS", "<S>", "m"}, // normalisation happens on the way in
		ParsedBy:      "tester",
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.SheetFingerprint)
	require.Equal(t, []string{"m", "s", "xs"}, res.StoredTokens,
		"tokens are normalised, de-duplicated and sorted so two runs of one audit produce one column")
	require.Equal(t, []int{sizeID}, res.CardSizeIds)

	idx, err := T.GetTechCardPatternSizeIndex(ctx, tcID)
	require.NoError(t, err)
	row, ok := idx[scope]
	require.True(t, ok, "the scope has an index row")

	sheetsNow := func() []entity.PatternSheetRef {
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		var out []entity.PatternSheetRef
		for _, p := range c.Patterns {
			if entity.FabricScopeKey(p.FabricPurpose.String, p.BomLineKey.String) != scope {
				continue
			}
			out = append(out, entity.PatternSheetRef{LineKey: p.LineKey, URL: p.URL, Version: p.Version})
		}
		return out
	}
	state, tokens := entity.PatternSizeIndexStatus(&row, entity.PatternSheetFingerprint(sheetsNow()))
	require.Equal(t, entity.PatternSizeIndexUsable, state,
		"a fresh parse over today's sheets is the only state that yields a verdict")
	require.True(t, tokens["xs"] && tokens["s"] && tokens["m"])

	// ── 2. re-uploading a sheet stales the index BY ITSELF ────────────────────────────────────
	// This is the property that makes the index safe to trust: nobody has to remember to invalidate
	// it. The url moves, the server's fingerprint moves with it, and the gate falls back to «no
	// verdict» — which is UNKNOWN, so it blocks nothing either.
	resave([]entity.TechCardSizePattern{pat(sheetA, "f6-front-v2.dxf"), pat(sheetB, "f6-back.dxf")})
	idx, err = T.GetTechCardPatternSizeIndex(ctx, tcID)
	require.NoError(t, err)
	row = idx[scope]
	state, _ = entity.PatternSizeIndexStatus(&row, entity.PatternSheetFingerprint(sheetsNow()))
	require.Equal(t, entity.PatternSizeIndexStale, state,
		"a replaced sheet must stale the stored index — this is the whole unforgeability argument")

	// ── 3. a wrong sheet set is refused, in BOTH directions ───────────────────────────────────
	// An index computed over the wrong files is worse than no index: it answers confidently about
	// sheets nobody parsed. And deriveBlockSizes is not compositional, so a PARTIAL parse is not a
	// subset of a full one — it is a different answer.
	_, err = T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: tcID, ScopeKey: scope, SheetLineKeys: []string{sheetA}, SizeTokens: []string{"m"},
	})
	require.Error(t, err, "a parse that saw only half the scope is refused")
	require.Contains(t, err.Error(), "sheet_set_mismatch")

	_, err = T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: tcID, ScopeKey: scope,
		SheetLineKeys: []string{sheetA, sheetB, "01F6SHEETGHOST0000000000S9"},
		SizeTokens:    []string{"m"},
	})
	require.Error(t, err, "a parse naming a sheet this scope does not hold is refused")
	require.Contains(t, err.Error(), "sheet_set_mismatch")

	// A scope with no sheets at all cannot be indexed: there is nothing to fingerprint, and an index
	// under an unknown key would answer for a cloth the card does not have.
	_, err = T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: tcID, ScopeKey: "01F6NOSUCHSCOPE000000000Z9", SizeTokens: []string{"m"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scope_has_no_sheets")

	// ── 4. an EMPTY token set is a legal answer ───────────────────────────────────────────────
	// One size per file yields no tokens at all. Storing it is what lets the gate say «the files
	// carry no size coding» instead of «nobody ran the audit» — two different sentences leading to
	// two different actions — and it must NEVER read as «no sizes are present», which would be a
	// false blocker on every size at once.
	res, err = T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: tcID, ScopeKey: scope, SheetLineKeys: []string{sheetA, sheetB},
		SizeTokens: nil, ParsedBy: "tester",
	})
	require.NoError(t, err, "an empty parse is recorded, not refused")
	require.Empty(t, res.StoredTokens)
	idx, err = T.GetTechCardPatternSizeIndex(ctx, tcID)
	require.NoError(t, err)
	row = idx[scope]
	state, _ = entity.PatternSizeIndexStatus(&row, entity.PatternSheetFingerprint(sheetsNow()))
	require.Equal(t, entity.PatternSizeIndexUngraded, state)
	require.Equal(t, entity.RunReadinessUnknown, entity.SizesInDxf(state, nil, "xs_44ta_m"),
		"an empty token set is NO VERDICT, never «this size is missing»")

	// Re-writing the same scope UPDATES rather than duplicating (PRIMARY KEY (tech_card_id, scope_key)).
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_pattern_size_index WHERE tech_card_id = ?", tcID).Scan(&n))
	require.Equal(t, 1, n, "one row per (card, scope), however many times the audit runs")

	// Deleting the card cascades the index away: it is a derived fact about that card and nothing else.
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", tcID)
	require.NoError(t, err)
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_pattern_size_index WHERE tech_card_id = ?", tcID).Scan(&n))
	require.Equal(t, 0, n, "fk_tcpsi_card ON DELETE CASCADE")
}

// TestF6RunReadinessBlockingSetting is the acceptance test of the mode switch against the real
// UPDATE, and the case that matters is the ABSENT one: run_readiness_blocking rides the same
// IF(:x_omitted, col, :x) mask as its neighbours, so a workshop screen from before this field
// existed must not disable the gate on every unrelated save. That defect would be invisible —
// «the run was created» looks the same as «the run was allowed».
func TestF6RunReadinessBlockingSetting(t *testing.T) {
	skipUnlessLocalContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	W := s.Workshop()

	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			"UPDATE workshop_settings SET run_readiness_blocking = NULL WHERE id = 1")
	})

	// Unconfigured is REPORT-ONLY, and this is the one setting in this house whose unset state has a
	// defined behaviour rather than «no verdict»: on the day Ф6 ships no card carries a conditioned
	// norm, so «no verdict ⇒ refuse» would have refused every run in the shop.
	got, err := W.GetSettings(ctx)
	require.NoError(t, err)
	require.False(t, got.RunReadinessBlocking.Valid, "the column starts unconfigured; 0279 back-fills nothing")
	require.False(t, entity.RunReadinessBlocking(got), "unset is report-only")

	on := true
	got, err = W.UpdateSettings(ctx, entity.WorkshopSettingsPatch{RunReadinessBlocking: &on}, "tester")
	require.NoError(t, err)
	require.True(t, entity.RunReadinessBlocking(got), "an explicit true turns the gate on")

	// A patch that does NOT name the mode leaves it alone — the stale-bundle case.
	length := decimal.NullDecimal{Decimal: decimal.RequireFromString("600"), Valid: true}
	got, err = W.UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &length}, "tester")
	require.NoError(t, err)
	require.True(t, entity.RunReadinessBlocking(got),
		"a save that does not mention the mode must not turn the gate off")
	require.True(t, got.CuttingTableLengthCm.Valid, "the rest of the save still lands")

	// A patch naming ONLY the mode is not «empty» — the trap 0272 named about IsEmpty listing its
	// fields by hand.
	off := false
	got, err = W.UpdateSettings(ctx, entity.WorkshopSettingsPatch{RunReadinessBlocking: &off}, "tester")
	require.NoError(t, err)
	require.True(t, got.RunReadinessBlocking.Valid && !got.RunReadinessBlocking.Bool)
	require.False(t, entity.RunReadinessBlocking(got), "an explicit false is report-only, same as unset")
}
