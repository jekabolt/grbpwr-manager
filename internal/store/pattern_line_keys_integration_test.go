package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPatternLineKeys covers the Ф9.2 upsert-diff of tech_card_size_pattern: legacy keyless payloads
// must behave byte-identically to the old delete-all path (versions, names, GC candidates), keyed
// rows must survive a url change (sheet replacement) keeping their name and fabric binding, and the
// bom_line_key presence rules must protect bindings from stale clients while refusing new bindings
// to slots that do not exist.
func TestPatternLineKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA, szB int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", szA).Scan(&szB))

	const (
		fabKey  = "01LINEKEYFABRIC00000000001"
		trimKey = "01LINEKEYTRIM0000000000001"
		sheetK1 = "01SHEETKEY0000000000000001"
		url1    = "https://cdn.example/base/tech-card-patterns/2026/august/lk1.pdf"
		url2    = "https://cdn.example/base/tech-card-patterns/2026/august/lk2.pdf"
		url3    = "https://cdn.example/base/tech-card-patterns/2026/august/lk3.dxf"
		url4    = "https://cdn.example/base/tech-card-patterns/2026/august/lk4.dxf"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	fabric := entity.TechCardBomItem{LineKey: fabKey, Section: entity.BomSectionFabric, Name: "Основная"}
	trim := entity.TechCardBomItem{LineKey: trimKey, Section: entity.BomSectionTrim, Name: "Бейка"}

	mkTC := func(items []entity.TechCardBomItem, patterns ...entity.TechCardSizePattern) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber: ns("LK-STYLE"), Name: "LK",
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{szA, szB},
			BomItems:        items,
			Patterns:        patterns,
		}
	}
	bom := []entity.TechCardBomItem{fabric, trim}

	id, err := T.AddTechCard(ctx, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szA, URL: url1, Name: ns("перед")},
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
	))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })

	read := func() []entity.TechCardSizePattern {
		tc, err := T.GetTechCardById(ctx, id)
		require.NoError(t, err)
		return tc.Patterns
	}
	lock := func() int {
		tc, err := T.GetTechCardById(ctx, id)
		require.NoError(t, err)
		return tc.LockVersion
	}
	bySize := func(ps []entity.TechCardSizePattern, size int) entity.TechCardSizePattern {
		for _, p := range ps {
			if p.SizeId == size {
				return p
			}
		}
		t.Fatalf("no pattern for size %d", size)
		return entity.TechCardSizePattern{}
	}

	// --- legacy keyless payload: server mints stable keys, versions/names behave as before ---
	first := read()
	require.Len(t, first, 2)
	pA, pB := bySize(first, szA), bySize(first, szB)
	require.NotEmpty(t, pA.LineKey)
	require.NotEmpty(t, pB.LineKey)
	require.NotEqual(t, pA.LineKey, pB.LineKey)
	require.Equal(t, 1, pA.Version)
	require.Equal(t, 1, pB.Version) // numbered per size
	require.Equal(t, "перед", pA.Name.String)

	// A stale-client re-save (keyless rows, absent name, absent binding) adopts the same stored rows:
	// keys, names and versions all survive — the old carry-forward, now with stable identity too.
	orphans, err := T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szA, URL: url1},
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
	), lock())
	require.NoError(t, err)
	require.Empty(t, orphans)
	second := read()
	require.Equal(t, pA.LineKey, bySize(second, szA).LineKey, "legacy re-save must adopt, not re-mint")
	require.Equal(t, "перед", bySize(second, szA).Name.String)
	require.Equal(t, 1, bySize(second, szA).Version)

	// Keyless url replacement = the OLD semantics: a brand-new row (new key, MAX+1, no name).
	orphans, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szA, URL: url2},
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
	), lock())
	require.NoError(t, err)
	require.Empty(t, orphans, "url1 is still referenced by the szB row")
	third := read()
	require.NotEqual(t, pA.LineKey, bySize(third, szA).LineKey)
	require.Equal(t, 2, bySize(third, szA).Version)
	require.False(t, bySize(third, szA).Name.Valid)

	// --- keyed row: binding validation, then survival across a url change ---
	// A NEW binding must name a fabric-section slot: a trim slot and a fantasy key are both refused.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
		entity.TechCardSizePattern{SizeId: szA, URL: url3, LineKey: sheetK1, Name: ns("сетка"), BomLineKey: ns(trimKey)},
	), lock())
	require.Error(t, err, "trim-section slot must not bind a pattern")
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
		entity.TechCardSizePattern{SizeId: szA, URL: url3, LineKey: sheetK1, Name: ns("сетка"), BomLineKey: ns("01NOSUCHSLOT00000000000001")},
	), lock())
	require.Error(t, err)

	// A valid fabric binding lands. The szA url2 row vanishes from the payload → its object orphans.
	orphans, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
		entity.TechCardSizePattern{SizeId: szA, URL: url3, LineKey: sheetK1, Name: ns("сетка"), BomLineKey: ns(fabKey)},
	), lock())
	require.NoError(t, err)
	require.Equal(t, []string{url2}, orphans)
	require.Equal(t, fabKey, bySize(read(), szA).BomLineKey.String)

	// Replace the sheet's FILE: same line_key, new url, name and binding ABSENT — identity, name and
	// binding all survive; the version bumps (a replacement is a new revision); the old object orphans.
	orphans, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
		entity.TechCardSizePattern{SizeId: szA, URL: url4, LineKey: sheetK1},
	), lock())
	require.NoError(t, err)
	require.Equal(t, []string{url3}, orphans)
	replaced := bySize(read(), szA)
	require.Equal(t, sheetK1, replaced.LineKey)
	require.Equal(t, url4, replaced.URL)
	require.Equal(t, "сетка", replaced.Name.String, "a keyed replacement must not lose the name")
	require.Equal(t, fabKey, replaced.BomLineKey.String, "a keyed replacement must not lose the binding")
	require.Equal(t, 4, replaced.Version) // szA history: url1 v1, url2 v2, url3 v3, replacement v4

	// A stale client (keyless, absent binding) re-saves the keyed row by (size, url): binding survives.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(bom,
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
		entity.TechCardSizePattern{SizeId: szA, URL: url4},
	), lock())
	require.NoError(t, err)
	require.Equal(t, fabKey, bySize(read(), szA).BomLineKey.String)

	// The fabric slot is deleted out from under the binding: an UNCHANGED round-trip still saves
	// (dangling = «слот удалён», a UI state), while present-empty explicitly unbinds.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]entity.TechCardBomItem{trim},
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
		entity.TechCardSizePattern{SizeId: szA, URL: url4, LineKey: sheetK1, BomLineKey: ns(fabKey)},
	), lock())
	require.NoError(t, err, "an unchanged dangling binding must not block the save")
	require.Equal(t, fabKey, bySize(read(), szA).BomLineKey.String)
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]entity.TechCardBomItem{trim},
		entity.TechCardSizePattern{SizeId: szB, URL: url1},
		entity.TechCardSizePattern{SizeId: szA, URL: url4, LineKey: sheetK1, BomLineKey: sql.NullString{String: "", Valid: true}},
	), lock())
	require.NoError(t, err)
	require.False(t, bySize(read(), szA).BomLineKey.Valid, "present-empty unbinds (stored as NULL)")
}
