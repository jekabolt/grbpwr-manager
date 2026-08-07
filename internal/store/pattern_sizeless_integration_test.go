package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPatternSizeless covers 0281: a выкройка may be filed under NO size (size_id NULL, entity 0),
// which is what a graded DXF actually is — its sizes live in the file's block names — and what makes
// it uploadable while the card's size range is still empty.
//
// The rules pinned here: a sizeless sheet saves on a card with no range at all; it is NOT projected
// away when a range later appears (the live-size filter is about sizes, and this row claims none);
// it keeps its identity, name and binding across saves exactly like a size-filed row; its revisions
// are numbered in their own bucket; and a sheet filed under a size the range does NOT contain is
// still dropped, which is the rule the sizeless row must not be confused with.
func TestPatternSizeless(t *testing.T) {
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
		fabKey  = "01SIZELESSFABRIC0000000001"
		sheetK  = "01SIZELESSSHEET00000000001"
		urlG1   = "https://cdn.example/base/tech-card-patterns/2026/august/graded1.dxf"
		urlG2   = "https://cdn.example/base/tech-card-patterns/2026/august/graded2.dxf"
		urlFlat = "https://cdn.example/base/tech-card-patterns/2026/august/flat.dxf"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	fabric := entity.TechCardBomItem{LineKey: fabKey, Section: entity.BomSectionFabric, Name: "Основная"}

	mkTC := func(sizes []int, patterns ...entity.TechCardSizePattern) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber: ns("SL-STYLE"), Name: "SL",
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         sizes,
			BomItems:        []entity.TechCardBomItem{fabric},
			Patterns:        patterns,
		}
	}

	// --- a card with NO size range at all: the state the конструктор's DXF arrives in ---
	id, err := T.AddTechCard(ctx, mkTC(nil,
		entity.TechCardSizePattern{URL: urlG1, LineKey: sheetK, Name: ns("градуированный"), BomLineKey: ns(fabKey)},
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
	storedSizes := func() []sql.NullInt64 {
		rows, err := testDB.QueryContext(ctx,
			"SELECT size_id FROM tech_card_size_pattern WHERE tech_card_id = ? ORDER BY display_order", id)
		require.NoError(t, err)
		defer rows.Close()
		var out []sql.NullInt64
		for rows.Next() {
			var v sql.NullInt64
			require.NoError(t, rows.Scan(&v))
			out = append(out, v)
		}
		require.NoError(t, rows.Err())
		return out
	}

	first := read()
	require.Len(t, first, 1, "a sizeless sheet must save on a card with no size range")
	require.Equal(t, 0, first[0].SizeId)
	require.Equal(t, sheetK, first[0].LineKey)
	require.Equal(t, "градуированный", first[0].Name.String)
	require.Equal(t, fabKey, first[0].BomLineKey.String)
	require.Equal(t, 1, first[0].Version)
	// The column really holds NULL, not 0 — a literal 0 would not survive the FK on size(id).
	require.Equal(t, []sql.NullInt64{{}}, storedSizes())

	// --- the size range appears later; the sizeless row must not be projected away ---
	// Sent KEYLESS, as a stale client would: adoption by (size, url) has to work in the 0 bucket too.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA, szB},
		entity.TechCardSizePattern{URL: urlG1},
	), lock())
	require.NoError(t, err)
	afterRange := read()
	require.Len(t, afterRange, 1, "adding a size range must not drop the sizeless sheet")
	require.Equal(t, sheetK, afterRange[0].LineKey, "keyless re-save must adopt, not re-mint")
	require.Equal(t, "градуированный", afterRange[0].Name.String)
	require.Equal(t, fabKey, afterRange[0].BomLineKey.String)
	require.Equal(t, 1, afterRange[0].Version)

	// --- sizeless and size-filed sheets coexist, and their revisions are numbered apart ---
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA, szB},
		entity.TechCardSizePattern{URL: urlG1, LineKey: sheetK},
		entity.TechCardSizePattern{SizeId: szA, URL: urlFlat},
	), lock())
	require.NoError(t, err)
	mixed := read()
	require.Len(t, mixed, 2)
	require.Equal(t, 1, mixed[0].Version, "the sizeless sheet keeps its revision")
	require.Equal(t, 0, mixed[0].SizeId)
	require.Equal(t, szA, mixed[1].SizeId)
	require.Equal(t, 1, mixed[1].Version, "a size's numbering starts at 1 regardless of the sizeless bucket")

	// --- replacing the sizeless FILE: same key, new url, name/binding absent → all survive, MAX+1 ---
	orphans, err := T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA, szB},
		entity.TechCardSizePattern{URL: urlG2, LineKey: sheetK},
		entity.TechCardSizePattern{SizeId: szA, URL: urlFlat},
	), lock())
	require.NoError(t, err)
	require.Equal(t, []string{urlG1}, orphans)
	replaced := read()[0]
	require.Equal(t, sheetK, replaced.LineKey)
	require.Equal(t, urlG2, replaced.URL)
	require.Equal(t, "градуированный", replaced.Name.String, "a keyed replacement must not lose the name")
	require.Equal(t, fabKey, replaced.BomLineKey.String, "a keyed replacement must not lose the binding")
	require.Equal(t, 2, replaced.Version)

	// --- RE-FILING a size-filed sheet to «no size», KEYED: identity, name, binding and REVISION all
	// survive. The revision matters beyond cosmetics: the 0280 size index fingerprints (line_key, url,
	// version), so renumbering a sheet that did not change would stale the index and drop the run gate
	// to UNKNOWN for a move that touched no geometry.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA, szB},
		entity.TechCardSizePattern{URL: urlG2, LineKey: sheetK},
		entity.TechCardSizePattern{SizeId: szA, URL: urlFlat, Name: ns("плоский")},
	), lock())
	require.NoError(t, err)
	flatKey := read()[1].LineKey
	require.NotEmpty(t, flatKey)
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA, szB},
		entity.TechCardSizePattern{URL: urlG2, LineKey: sheetK},
		entity.TechCardSizePattern{URL: urlFlat, LineKey: flatKey, Version: 1},
	), lock())
	require.NoError(t, err)
	refiled := read()
	require.Len(t, refiled, 2)
	require.Equal(t, 0, refiled[1].SizeId, "the sheet is now filed under no size")
	require.Equal(t, flatKey, refiled[1].LineKey)
	require.Equal(t, "плоский", refiled[1].Name.String)
	require.Equal(t, 1, refiled[1].Version, "a move is not a new revision — the version must not rewind")

	// --- the same re-file from a KEYLESS (legacy) client: the url alone must still find the row ---
	// Without the sizeless adoption the stored row would be deleted and re-inserted, losing its key,
	// its name and its binding, and the operator would see the sheet «reset» for no visible reason.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA, szB},
		entity.TechCardSizePattern{URL: urlG2, LineKey: sheetK},
		entity.TechCardSizePattern{SizeId: szA, URL: urlFlat, LineKey: flatKey},
	), lock())
	require.NoError(t, err)
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA, szB},
		entity.TechCardSizePattern{URL: urlG2},
		entity.TechCardSizePattern{URL: urlFlat},
	), lock())
	require.NoError(t, err)
	adopted := read()
	require.Len(t, adopted, 2)
	require.Equal(t, sheetK, adopted[0].LineKey, "keyless sizeless save adopts the stored graded row")
	require.Equal(t, flatKey, adopted[1].LineKey, "keyless sizeless save adopts the re-filed row by url")
	require.Equal(t, "плоский", adopted[1].Name.String, "adoption keeps the name a stale client never sent")
	require.Equal(t, fabKey, adopted[0].BomLineKey.String, "adoption keeps the fabric binding")

	// --- the rule the sizeless row must NOT be confused with: a size OUTSIDE the range is dropped ---
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]int{szA},
		entity.TechCardSizePattern{URL: urlG2, LineKey: sheetK},
		entity.TechCardSizePattern{SizeId: szB, URL: urlFlat, LineKey: flatKey},
	), lock())
	require.NoError(t, err)
	projected := read()
	require.Len(t, projected, 1, "a sheet filed under a size the range does not contain is still projected away")
	require.Equal(t, 0, projected[0].SizeId)
}

// TestPatternSizelessAdoptionIsOrderIndependent pins the guard on the 0281 keyless adoption: it may
// only take a stored row NOTHING else can claim. One combined sheet hung on two sizes leaves two
// candidates for its url, so a keyless sizeless row adopts neither — and a payload that still names
// one of those rows keeps it, whichever order the rows arrive in.
func TestPatternSizelessAdoptionIsOrderIndependent(t *testing.T) {
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

	const url = "https://cdn.example/base/tech-card-patterns/2026/august/combined.dxf"
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	mkTC := func(patterns ...entity.TechCardSizePattern) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber: ns("SL-AMBIG"), Name: "AMB",
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{szA, szB},
			Patterns:        patterns,
		}
	}

	// One combined sheet hung on BOTH sizes — two stored rows, one url.
	id, err := T.AddTechCard(ctx, mkTC(
		entity.TechCardSizePattern{SizeId: szA, URL: url, Name: ns("на A")},
		entity.TechCardSizePattern{SizeId: szB, URL: url, Name: ns("на B")},
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
	stored := read()
	require.Len(t, stored, 2)

	// A keyless sizeless row next to a keyless row that still names size A. The sizeless row must not
	// adopt anything (two candidates carry this url), and the size-A row keeps its stored identity.
	for _, order := range []string{"sizeless first", "sized first"} {
		payload := []entity.TechCardSizePattern{
			{URL: url},
			{SizeId: szA, URL: url},
		}
		if order == "sized first" {
			payload[0], payload[1] = payload[1], payload[0]
		}
		_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC(payload...), lock())
		require.NoErrorf(t, err, "%s", order)
		after := read()
		require.Lenf(t, after, 2, "%s: one sizeless row and the surviving size-A row", order)
		bySize := map[int]entity.TechCardSizePattern{}
		for _, p := range after {
			bySize[p.SizeId] = p
		}
		require.Containsf(t, bySize, szA, "%s: the row the payload still names must survive", order)
		require.Containsf(t, bySize, 0, "%s: the sizeless row must exist", order)
		require.Equalf(t, "на A", bySize[szA].Name.String, "%s: the size-A row keeps its name", order)
		// Rows are now (0, url) and (szA, url); the next iteration replays against that shape, which
		// is exactly the ambiguity guard's other half — the sizeless row is now adoptable by key.
	}
}
