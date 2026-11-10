package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPatternSlotStoredDuplicateStaysWritable pins slots lane 0 (beta card 58, SS26-005): a card
// whose one fabric slot ALREADY holds two pattern files — stored before the one-file rule — must
// accept every save that does not add a file beside another. Before, the rule re-checked the whole
// stored set on each save and every edit of such a card came back 400, whatever was edited.
//
//   - an unrelated edit (the card name) saves with both files in place;
//   - a third file in the doubled slot is refused, and the refusal names the line by its material;
//   - removing one of the two saves;
//   - a card with nothing stored still cannot get two files into one slot;
//   - a BOM edit that sorts two singly-filled lines into one purpose is still refused.
func TestPatternSlotStoredDuplicateStaysWritable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	const (
		fabKey      = "01SLOTDUPFABRIC00000000001"
		contrastKey = "01SLOTDUPFABRIC00000000002"
		sheetA      = "01SLOTDUPSHEET000000000001"
		sheetB      = "01SLOTDUPSHEET000000000002"
		urlA        = "https://cdn.example/base/tech-card-patterns/2026/october/allsizes-l-m-s-xl-xs.dxf"
		urlB        = "https://cdn.example/base/tech-card-patterns/2026/october/import-sheet.dxf"
		urlC        = "https://cdn.example/base/tech-card-patterns/2026/october/third.dxf"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	fabric := entity.TechCardBomItem{LineKey: fabKey, Section: entity.BomSectionFabric, Name: "Cotton twill"}
	contrast := entity.TechCardBomItem{LineKey: contrastKey, Section: entity.BomSectionFabric, Name: "Nylon ripstop"}

	mkTC := func(name string, bom []entity.TechCardBomItem, patterns ...entity.TechCardSizePattern) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber: ns(strings.ReplaceAll(name, " ", "-")), Name: name,
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			BomItems:        bom,
			Patterns:        patterns,
		}
	}
	sheet := func(lineKey, url, bomLine string) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{URL: url, LineKey: lineKey, Name: ns(url[strings.LastIndex(url, "/")+1:]), BomLineKey: ns(bomLine)}
	}
	addCard := func(tc *entity.TechCardInsert) (int, error) {
		id, err := T.AddTechCard(ctx, tc)
		if err == nil {
			t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })
		}
		return id, err
	}

	// --- card 58's shape: two sheets on one unsorted fabric line. The second row is written behind
	// the rule's back, as it was before the rule existed. ---
	id, err := addCard(mkTC("SLOTDUP", []entity.TechCardBomItem{fabric, contrast}, sheet(sheetA, urlA, fabKey)))
	require.NoError(t, err)
	var cols []string
	rows, err := testDB.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'tech_card_size_pattern'
		  AND column_name NOT IN ('id', 'line_key', 'url', 'name') AND extra NOT LIKE '%GENERATED%'`)
	require.NoError(t, err)
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, "`"+c+"`")
	}
	require.NoError(t, rows.Err())
	rows.Close()
	list := strings.Join(cols, ", ")
	_, err = testDB.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO tech_card_size_pattern (line_key, url, name, %s) SELECT ?, ?, ?, %s FROM tech_card_size_pattern WHERE tech_card_id = ? AND line_key = ?",
		list, list), sheetB, urlB, "import-sheet.dxf", id, sheetA)
	require.NoError(t, err)

	read := func(id int) *entity.TechCard {
		tc, err := T.GetTechCardById(ctx, id)
		require.NoError(t, err)
		return tc
	}
	before := read(id)
	require.Len(t, before.Patterns, 2, "fixture: two files in one slot")
	both := []entity.TechCardSizePattern{sheet(sheetA, urlA, fabKey), sheet(sheetB, urlB, fabKey)}

	// Unrelated edit: the name changes, both files stay.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id,
		mkTC("SLOTDUP renamed", []entity.TechCardBomItem{fabric, contrast}, both...), before.LockVersion)
	require.NoError(t, err, "an unrelated edit of a card with a stored duplicate must save")
	afterEdit := read(id)
	require.Equal(t, "SLOTDUP renamed", afterEdit.Name)
	require.Len(t, afterEdit.Patterns, 2)

	// Third file in the doubled slot: refused, named by the line's material, never by its key.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id,
		mkTC("SLOTDUP renamed", []entity.TechCardBomItem{fabric, contrast},
			append(both, sheet("", urlC, fabKey))...), afterEdit.LockVersion)
	require.Error(t, err, "a third file in the doubled slot must be refused")
	require.Contains(t, err.Error(), `material slot "Cotton twill" already holds a pattern file`)
	require.NotContains(t, err.Error(), fabKey)
	require.Len(t, read(id).Patterns, 2, "the refused save must not write")

	// Removing one of the two saves.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id,
		mkTC("SLOTDUP renamed", []entity.TechCardBomItem{fabric, contrast}, sheet(sheetA, urlA, fabKey)),
		read(id).LockVersion)
	require.NoError(t, err, "removing one file of the doubled slot must save")
	afterRemove := read(id)
	require.Len(t, afterRemove.Patterns, 1)
	require.Equal(t, sheetA, afterRemove.Patterns[0].LineKey)

	// Once single, the slot is held to the rule again: the removed file cannot come back beside it.
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, id,
		mkTC("SLOTDUP renamed", []entity.TechCardBomItem{fabric, contrast}, sheet(sheetA, urlA, fabKey), sheet("", urlB, fabKey)),
		afterRemove.LockVersion)
	require.Error(t, err, "a second file in a single slot must be refused")

	// A fresh card cannot get two files into one slot.
	_, err = addCard(mkTC("SLOTDUP fresh", []entity.TechCardBomItem{fabric, contrast},
		sheet("", urlA, fabKey), sheet("", urlB, fabKey)))
	require.Error(t, err, "a fresh card adding a second file must be refused")
	require.Contains(t, err.Error(), "already holds a pattern file")

	// A BOM edit that sorts two singly-filled lines into one purpose merges two slots: refused.
	mergeID, err := addCard(mkTC("SLOTDUP merge", []entity.TechCardBomItem{fabric, contrast},
		sheet("", urlA, fabKey), sheet("", urlB, contrastKey)))
	require.NoError(t, err)
	merge := read(mergeID)
	fabricMain, contrastMain := fabric, contrast
	fabricMain.Purpose, contrastMain.Purpose = ns("main"), ns("main")
	keep := make([]entity.TechCardSizePattern, 0, len(merge.Patterns))
	for _, p := range merge.Patterns {
		keep = append(keep, entity.TechCardSizePattern{URL: p.URL, LineKey: p.LineKey, Name: p.Name, BomLineKey: p.BomLineKey})
	}
	_, err = T.UpdateTechCardAndListOrphanedPatternURLs(ctx, mergeID,
		mkTC("SLOTDUP merge", []entity.TechCardBomItem{fabricMain, contrastMain}, keep...), merge.LockVersion)
	require.Error(t, err, "sorting two filled lines into one purpose must be refused")
	require.Contains(t, err.Error(), `material slot "main" already holds a pattern file`)
}
