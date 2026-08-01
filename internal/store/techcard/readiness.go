package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// techCardReadinessQuery gathers every readiness fact as correlated subselects hanging off the card
// row — the same single-round-trip shape as guardTechCardStageRegression. The checklist is advisory
// UI that a board can request for any card the operator opens, so it must not cost twenty queries.
//
// Notes that cannot live in the SQL text (a ':' inside a SQL comment breaks sqlx named binding):
//   - lab_dip_status is NULLable on product and the rest of the codebase reads NULL as 'pending'
//     (see materials.go), so COALESCE here keeps a never-submitted colourway counted as outstanding.
//   - a fitting may belong to a product only (tech_card_id NULL); the equality filters those out.
//   - the pattern subselect intersects with the live size range: a sheet left behind for a size that
//     has since been dropped from the grade must not count towards coverage.
//   - bom_linked_lines is pin-or-default (slots, 0221): a line with no default article still counts
//     as covered when there is at least one live colourway AND every live colourway both pins an
//     article for it and carries no unpinned usage of it — one pinned usage next to an unpinned one
//     would pass a weaker check while the production plan still blocks the unpinned row.
const techCardReadinessQuery = `SELECT
	tc.stage                                                                  AS stage,
	tc.approval_state                                                         AS approval_state,
	(tc.style_number IS NOT NULL AND tc.style_number <> '')                   AS has_style_number,
	(tc.category_id IS NOT NULL)                                              AS has_category,
	(tc.base_sample_size_id IS NOT NULL)                                      AS has_base_sample_size,
	(SELECT COUNT(*) FROM tech_card_size z WHERE z.tech_card_id = tc.id)       AS sizes,
	(SELECT COUNT(*) FROM tech_card_piece p WHERE p.tech_card_id = tc.id)      AS pieces,
	(SELECT COUNT(*) FROM tech_card_operation o WHERE o.tech_card_id = tc.id)  AS operations,
	(SELECT COUNT(*) FROM tech_card_bom_item b WHERE b.tech_card_id = tc.id)   AS bom_lines,
	(SELECT COUNT(*) FROM tech_card_bom_item b
		WHERE b.tech_card_id = tc.id AND b.section = 'fabric')                AS bom_fabric_lines,
	(SELECT COUNT(*) FROM tech_card_bom_item b
		WHERE b.tech_card_id = tc.id AND (b.material_id IS NOT NULL
		  OR (EXISTS (SELECT 1 FROM product pr2
		        WHERE pr2.style_id = tc.id AND pr2.lifecycle_status <> :archived)
		      AND NOT EXISTS (SELECT 1 FROM product pr3
		        WHERE pr3.style_id = tc.id AND pr3.lifecycle_status <> :archived
		          AND (NOT EXISTS (SELECT 1 FROM tech_card_colorway_usage u2
		            WHERE u2.colorway_id = pr3.id AND u2.bom_item_id = b.id
		              AND u2.material_id IS NOT NULL)
		            OR EXISTS (SELECT 1 FROM tech_card_colorway_usage u3
		              WHERE u3.colorway_id = pr3.id AND u3.bom_item_id = b.id
		                AND u3.material_id IS NULL))))))                     AS bom_linked_lines,
	(SELECT COUNT(*) FROM sample s
		WHERE s.tech_card_id = tc.id AND s.status <> 'scrapped')              AS samples,
	(SELECT COUNT(*) FROM sample s
		WHERE s.tech_card_id = tc.id AND s.status <> 'scrapped'
		  AND s.purpose = 'proto')                                            AS proto_samples,
	(SELECT COUNT(*) FROM sample s
		WHERE s.tech_card_id = tc.id AND s.status <> 'scrapped'
		  AND s.purpose = 'fit')                                              AS fit_samples,
	(SELECT COUNT(*) FROM sample s
		WHERE s.tech_card_id = tc.id AND s.status <> 'scrapped'
		  AND s.purpose = 'sms')                                              AS sms_samples,
	(SELECT COUNT(*) FROM sample s
		WHERE s.tech_card_id = tc.id AND s.status <> 'scrapped'
		  AND s.purpose = 'pp')                                               AS pp_samples,
	(SELECT COUNT(*) FROM fitting f WHERE f.tech_card_id = tc.id)              AS fittings,
	(SELECT COUNT(*) FROM fitting f
		WHERE f.tech_card_id = tc.id AND f.verdict = 'approved')              AS fittings_approved,
	(SELECT COUNT(*) FROM fitting_change_request cr
		JOIN fitting f ON f.id = cr.fitting_id
		WHERE f.tech_card_id = tc.id AND cr.status = 'open')                  AS open_change_requests,
	(SELECT COUNT(*) FROM product pr
		WHERE pr.style_id = tc.id AND pr.lifecycle_status <> :archived)       AS live_colorways,
	(SELECT COUNT(*) FROM product pr
		WHERE pr.style_id = tc.id AND pr.lifecycle_status <> :archived
		  AND COALESCE(pr.lab_dip_status, 'pending') <> 'approved')           AS lab_dip_pending_colorways,
	(SELECT COUNT(*) FROM production_run r WHERE r.tech_card_id = tc.id)       AS production_runs,
	(SELECT COUNT(*) FROM production_run r
		WHERE r.tech_card_id = tc.id AND r.status = 'received')               AS production_runs_received,
	(SELECT COUNT(DISTINCT sp.size_id) FROM tech_card_size_pattern sp
		WHERE sp.tech_card_id = tc.id
		  AND sp.size_id IN (SELECT z.size_id FROM tech_card_size z
		                     WHERE z.tech_card_id = tc.id))                   AS pattern_sizes,
	EXISTS(SELECT 1 FROM tech_card_costing c WHERE c.tech_card_id = tc.id)     AS has_costing,
	EXISTS(SELECT 1 FROM tech_card_costing c
		WHERE c.tech_card_id = tc.id
		  AND c.currency IS NOT NULL AND c.currency <> '')                    AS has_costing_currency,
	(SELECT COUNT(*) FROM tech_card_signoff g WHERE g.tech_card_id = tc.id)    AS signoffs,
	(SELECT COUNT(*) FROM tech_card_signoff g
		WHERE g.tech_card_id = tc.id AND g.state = 'approved')                AS signoffs_approved
FROM tech_card tc
WHERE tc.id = :id`

// GetTechCardReadiness returns the raw counts a style's stage/release checklist is scored against.
// sql.ErrNoRows when the card is absent (NOT_FOUND upstream).
//
// It deliberately stops at counting: interpreting a count ("an sms sample exists, therefore the card
// may enter pp") is the studio's rule, and lives in the apisrv layer with the labels it produces.
func (s *Store) GetTechCardReadiness(ctx context.Context, techCardID int) (entity.TechCardReadinessFacts, error) {
	return loadTechCardReadiness(ctx, s.DB, techCardID)
}

// GetTechCardReadinessSnapshot loads the raw readiness facts and the card whose current section
// digests the API compares with signed_digest in one REPEATABLE READ transaction. Keeping both reads
// in one snapshot prevents a concurrent save from pairing pre-save counts with post-save content.
func (s *Store) GetTechCardReadinessSnapshot(ctx context.Context, techCardID int) (entity.TechCardReadinessFacts, *entity.TechCard, error) {
	var facts entity.TechCardReadinessFacts
	var card *entity.TechCard
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		facts, err = loadTechCardReadiness(ctx, rep.DB(), techCardID)
		if err != nil {
			return err
		}
		card, err = rep.TechCards().GetTechCardById(ctx, techCardID)
		return err
	})
	if err != nil {
		return entity.TechCardReadinessFacts{}, nil, fmt.Errorf("can't load readiness snapshot for tech card %d: %w", techCardID, err)
	}
	return facts, card, nil
}

func loadTechCardReadiness(ctx context.Context, db dependency.DB, techCardID int) (entity.TechCardReadinessFacts, error) {
	f, err := storeutil.QueryNamedOne[entity.TechCardReadinessFacts](ctx, db, techCardReadinessQuery,
		map[string]any{"id": techCardID, "archived": uint8(entity.ColorwayStatusArchived)})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.TechCardReadinessFacts{}, sql.ErrNoRows
		}
		return entity.TechCardReadinessFacts{}, fmt.Errorf("can't gather readiness facts for tech card %d: %w", techCardID, err)
	}
	return f, nil
}
