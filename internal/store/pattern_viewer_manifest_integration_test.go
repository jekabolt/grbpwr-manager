package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPatternViewerManifest covers the two store halves of the card-level pattern viewer
// (0288): the narrow manifest read and the tech_card_pattern_viewer_access row lifecycle.
//
// The manifest fixture is built the only way the dangling display branches can exist in
// real data: bindings are VALID when first written (the write path refuses a NEW binding
// that names nothing), and then the BOM moves out from under them — a line loses its
// назначение, a line is deleted — while the sheets round-trip unchanged. The read must
// hand patternaccess exactly the raw halves that drive its display resolve: an own
// purpose nobody carries and a line key the card lost both come back verbatim, so the
// handler can file those sheets under _unbound.
func TestPatternViewerManifest(t *testing.T) {
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
	var szAName, szBName string
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT name FROM size WHERE id = ?", szA).Scan(&szAName))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT name FROM size WHERE id = ?", szB).Scan(&szBName))

	const (
		lineMain     = "01PVLINEMAIN00000000000001"
		linePocket   = "01PVLINEPOCKET000000000002"
		lineContrast = "01PVLINECONTRAST0000000003"
		lineGone     = "01PVLINEGONE00000000000004"
		urlBase      = "https://cdn.example/base/tech-card-patterns/2026/august/"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	bomLine := func(key, name, purpose string) entity.TechCardBomItem {
		return entity.TechCardBomItem{LineKey: key, Section: entity.BomSectionFabric, Name: name, Purpose: ns(purpose)}
	}
	sheets := func() []entity.TechCardSizePattern {
		return []entity.TechCardSizePattern{
			{LineKey: "01PVSHEETA0000000000000001", URL: urlBase + "a.pdf", Name: ns("перед"),
				FabricPurpose: ns("main")},
			{LineKey: "01PVSHEETB0000000000000002", URL: urlBase + "b.dxf", BomLineKey: ns(lineMain)},
			{LineKey: "01PVSHEETC0000000000000003", URL: urlBase + "c.pdf", BomLineKey: ns(linePocket)},
			{LineKey: "01PVSHEETD0000000000000004", URL: urlBase + "d.pdf", FabricPurpose: ns("contrast")},
			{LineKey: "01PVSHEETE0000000000000005", URL: urlBase + "e.pdf", BomLineKey: ns(lineGone)},
			{LineKey: "01PVSHEETF0000000000000006", URL: urlBase + "f.pdf"},
			{LineKey: "01PVSHEETG0000000000000007", URL: urlBase + "g.pdf", SizeId: szA},
		}
	}
	mkTC := func(bom []entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber: ns("PV-STYLE"), Name: "PV viewer",
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{szA, szB},
			BomItems:        bom,
			Patterns:        sheets(),
		}
	}

	// Create with every binding LIVE (contrast carried, lineGone present), plus a
	// hardware line the roll-goods read must exclude.
	id, err := T.AddTechCard(ctx, mkTC([]entity.TechCardBomItem{
		bomLine(lineMain, "Основная", "main"),
		bomLine(linePocket, "Карманка", ""),
		bomLine(lineContrast, "Контраст", "contrast"),
		bomLine(lineGone, "Уходящая", ""),
		{LineKey: "01PVLINEHW0000000000000005", Section: entity.BomSectionHardware, Name: "Молния"},
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })

	lock := func() int {
		tc, err := T.GetTechCardById(ctx, id)
		require.NoError(t, err)
		return tc.LockVersion
	}
	// The BOM moves: contrast loses its назначение, lineGone is deleted. The sheets
	// round-trip their stored bindings unchanged, which the write path tolerates.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id, mkTC([]entity.TechCardBomItem{
		bomLine(lineMain, "Основная", "main"),
		bomLine(linePocket, "Карманка", ""),
		bomLine(lineContrast, "Контраст", ""),
		{LineKey: "01PVLINEHW0000000000000005", Section: entity.BomSectionHardware, Name: "Молния"},
	}), lock())
	require.NoError(t, err)

	m, err := T.GetPatternViewerManifest(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, m.TechCardId)
	require.Equal(t, "PV-STYLE", m.StyleNumber)
	require.Equal(t, "PV viewer", m.StyleName)

	require.Equal(t, []entity.PatternViewerSize{{Id: szA, Name: szAName}, {Id: szB, Name: szBName}}, m.Sizes)

	// Roll-goods lines only, in BOM order, with the post-update purposes; the hardware
	// line and the deleted line are absent.
	require.Equal(t, []entity.PatternViewerBomLine{
		{LineKey: lineMain, Name: "Основная", Purpose: "main"},
		{LineKey: linePocket, Name: "Карманка", Purpose: ""},
		{LineKey: lineContrast, Name: "Контраст", Purpose: ""},
	}, m.BomLines)

	require.Len(t, m.Sheets, 7)
	byKey := map[string]entity.PatternViewerSheet{}
	for _, sh := range m.Sheets {
		require.True(t, sh.UploadedAt.Valid, "uploaded_at is server-stamped on insert")
		require.GreaterOrEqual(t, sh.Version, 1)
		byKey[sh.LineKey] = sh
	}
	// The binding halves come back verbatim — including the ones whose targets are gone,
	// which is what lets the handler resolve D and E to _unbound.
	require.Equal(t, "main", byKey["01PVSHEETA0000000000000001"].FabricPurpose)
	require.Equal(t, "", byKey["01PVSHEETA0000000000000001"].BomLineKey)
	require.Equal(t, lineMain, byKey["01PVSHEETB0000000000000002"].BomLineKey)
	require.Equal(t, "", byKey["01PVSHEETB0000000000000002"].FabricPurpose)
	require.Equal(t, linePocket, byKey["01PVSHEETC0000000000000003"].BomLineKey)
	require.Equal(t, "contrast", byKey["01PVSHEETD0000000000000004"].FabricPurpose,
		"a purpose no line carries any more must survive the read")
	require.Equal(t, lineGone, byKey["01PVSHEETE0000000000000005"].BomLineKey,
		"a line key the card lost must survive the read")
	require.Equal(t, "", byKey["01PVSHEETF0000000000000006"].BomLineKey)
	require.Equal(t, "", byKey["01PVSHEETF0000000000000006"].FabricPurpose)
	require.Equal(t, szA, byKey["01PVSHEETG0000000000000007"].SizeId)
	require.Equal(t, 0, byKey["01PVSHEETA0000000000000001"].SizeId, "sizeless sheet reads as 0")
	require.Equal(t, "перед", byKey["01PVSHEETA0000000000000001"].Name)
	require.Equal(t, urlBase+"a.pdf", byKey["01PVSHEETA0000000000000001"].URL)

	// Missing card → sql.ErrNoRows for the handler's uniform 404.
	_, err = T.GetPatternViewerManifest(ctx, id+1000000)
	require.ErrorIs(t, err, sql.ErrNoRows)

	// --- tech_card_pattern_viewer_access lifecycle ---
	P := s.PatternObjects()
	_, err = P.GetCardViewer(ctx, id)
	require.ErrorIs(t, err, sql.ErrNoRows, "no row until the first mint")

	row, err := P.EnsureCardViewer(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, row.TechCardId)
	require.Equal(t, 1, row.Epoch, "0288 starts card epochs at 1")
	require.False(t, row.RevokedAt.Valid)
	require.False(t, row.ExpiresAt.Valid, "no default TTL — owner decision В2")
	require.Zero(t, row.AccessCount)

	again, err := P.EnsureCardViewer(ctx, id)
	require.NoError(t, err)
	require.Equal(t, row.Epoch, again.Epoch, "ensure must not touch existing state")
	require.Equal(t, row.CreatedAt, again.CreatedAt)

	last := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, P.RecordCardViewerAccess(ctx,
		map[int]int64{id: 3}, map[int]time.Time{id: last}))
	counted, err := P.GetCardViewer(ctx, id)
	require.NoError(t, err)
	require.EqualValues(t, 3, counted.AccessCount)
	require.True(t, counted.LastAccessAt.Valid)

	// Ensure for a card that does not exist: INSERT IGNORE swallows the FK violation and
	// the reload reports the truth — no row, sql.ErrNoRows (the mint stays best-effort).
	_, err = P.EnsureCardViewer(ctx, id+1000000)
	require.ErrorIs(t, err, sql.ErrNoRows)

	// Deleting the card cascades the access row away (fk_tcpva_card).
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", id)
	require.NoError(t, err)
	_, err = P.GetCardViewer(ctx, id)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
