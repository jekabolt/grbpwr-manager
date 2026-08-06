package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Ф1.8 — the кампания Д1 worklist. See entity/fabric_direction_report.go for what is filtered and
// why; this file only reads, and it reads the WHOLE roll-goods BOM rather than the unset rows alone.
//
// Notes that cannot live in the SQL text (a ':' inside a SQL '--' comment is parsed by sqlx as a
// named parameter and fails the bind at request time):
//
//   - the families come from rollGoodsSectionList through rollGoodsSectionInOn — the same one list
//     the marker rule filters through. Two hand-written copies of «fabric, lining, interlining,
//     insulation» would drift silently and in the worst direction: the report would call the
//     campaign finished on a family whose saves still refuse. The alias is not decoration — the
//     material join carries a `section` column too, and the bare fragment would be ambiguous.
//   - UNSET is decided in GO, not in SQL, through entity.FabricDirectionIsUnknown. NULL is not the
//     only unknown (a value outside the closed vocabulary counts too, and the marker rule refuses on
//     it), and expressing that vocabulary a second time in SQL is exactly the drift this report
//     cannot afford. The cost is reading every cloth line instead of the unset ones — a few per
//     card, over a table of tens of thousands of rows.
//   - blocked_marker_count counts раскладки bound DIRECTLY to the line. That is provable rather than
//     approximate: whichever way a marker's scope resolves (назначение, else the line itself), the
//     scope contains the line the marker names, so an unset direction there refuses the save.
//     Markers on SIBLING lines under the same назначение are refused too, and are deliberately NOT
//     counted here — resolving назначение a second time in SQL would be the marker rule restated,
//     free to disagree with itself. The wider population rides as linked_marker_count instead.
//   - ORDER BY is the BOM tab's own order (display_order, id, per the bom load in materials.go) под
//     tech_card_id, so the list an operator works through does not reshuffle between loads and the
//     rows inside a card sit where the screen puts them.
var fabricDirectionGapsQuery = `
	SELECT tc.id                                     AS tech_card_id,
	       COALESCE(tc.style_number, '')             AS style_number,
	       tc.name                                   AS card_name,
	       tc.stage                                  AS stage,
	       tc.approval_state                         AS approval_state,
	       bi.id                                     AS bom_item_id,
	       COALESCE(bi.line_key, '')                 AS line_key,
	       COALESCE(NULLIF(bi.name, ''), m.name, '') AS name,
	       bi.section                                AS section,
	       COALESCE(bi.purpose, '')                  AS purpose,
	       bi.is_sample                              AS is_sample,
	       COALESCE(bi.fabric_direction, '')         AS fabric_direction,
	       (SELECT COUNT(*) FROM tech_card_marker mb
	          WHERE mb.bom_item_id = bi.id)          AS blocked_marker_count,
	       (SELECT COUNT(*) FROM tech_card_marker ml
	          WHERE ml.tech_card_id = tc.id
	            AND ml.bom_item_id IS NOT NULL)      AS linked_marker_count,
	       EXISTS(SELECT 1 FROM tech_card_size_pattern sp
	          WHERE sp.tech_card_id = tc.id)         AS has_patterns
	FROM tech_card_bom_item bi
	JOIN tech_card tc ON tc.id = bi.tech_card_id
	LEFT JOIN material m ON m.id = bi.material_id
	WHERE ` + rollGoodsSectionInOn("bi") + `
	  AND (:card = 0 OR tc.id = :card)
	ORDER BY tc.id, bi.display_order, bi.id`

type fabricDirectionGapRow struct {
	TechCardID         int    `db:"tech_card_id"`
	StyleNumber        string `db:"style_number"`
	CardName           string `db:"card_name"`
	Stage              string `db:"stage"`
	ApprovalState      string `db:"approval_state"`
	BomItemID          int64  `db:"bom_item_id"`
	LineKey            string `db:"line_key"`
	Name               string `db:"name"`
	Section            string `db:"section"`
	Purpose            string `db:"purpose"`
	IsSample           bool   `db:"is_sample"`
	FabricDirection    string `db:"fabric_direction"`
	BlockedMarkerCount int    `db:"blocked_marker_count"`
	LinkedMarkerCount  int    `db:"linked_marker_count"`
	HasPatterns        bool   `db:"has_patterns"`
}

// ListFabricDirectionGaps returns every tech card carrying roll-goods BOM lines whose направление
// nobody has set, with the lines themselves and the counts an owner triages by. techCardID 0 reads
// all cards; a card id re-checks one card after the fix.
//
// It returns EVERY such card, released and obsolete ones included, and stops there: which of them
// belong on a worklist is entity.BuildFabricDirectionGapReport's call, made once, in a place a test
// can argue with — the same division the readiness read makes between counting and interpreting.
// A card whose cloth lines all carry a direction is absent, not present-and-empty.
func (s *Store) ListFabricDirectionGaps(ctx context.Context, techCardID int) ([]entity.FabricDirectionGapCard, error) {
	rows, err := storeutil.QueryListNamed[fabricDirectionGapRow](ctx, s.DB, fabricDirectionGapsQuery,
		rollGoodsSectionArgs(map[string]any{"card": techCardID}))
	if err != nil {
		return nil, fmt.Errorf("can't load fabric direction gaps (tech card %d): %w", techCardID, err)
	}
	out := make([]entity.FabricDirectionGapCard, 0)
	byCard := make(map[int]int, len(rows)) // tech_card_id -> index in out, so card order stays the query's
	for _, r := range rows {
		if !entity.FabricDirectionIsUnknown(r.FabricDirection) {
			continue
		}
		i, ok := byCard[r.TechCardID]
		if !ok {
			out = append(out, entity.FabricDirectionGapCard{
				TechCardID:        r.TechCardID,
				StyleNumber:       r.StyleNumber,
				Name:              r.CardName,
				Stage:             r.Stage,
				ApprovalState:     r.ApprovalState,
				LinkedMarkerCount: r.LinkedMarkerCount,
				HasPatterns:       r.HasPatterns,
			})
			i = len(out) - 1
			byCard[r.TechCardID] = i
		}
		out[i].Lines = append(out[i].Lines, entity.FabricDirectionGapLine{
			BomItemID:          r.BomItemID,
			LineKey:            r.LineKey,
			Name:               r.Name,
			Section:            r.Section,
			Purpose:            r.Purpose,
			IsSample:           r.IsSample,
			BlockedMarkerCount: r.BlockedMarkerCount,
		})
	}
	return out, nil
}
