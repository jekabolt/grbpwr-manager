package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetPatternViewerManifest is the narrow read behind the public pattern viewer manifest
// (GET /api/pv/{token}): style header, named size range, every pattern sheet row and the
// roll-goods BOM lines, in four indexed queries.
//
// Deliberately NOT GetTechCardById: that read enriches the WHOLE card — BOM prices,
// costing, colourways — and its costing exposure is controlled per admin account (RBAC
// stripping in the API layer). This data lands on an unauthenticated endpoint where no
// account exists to strip for, so the safe shape is a read that never touches costing at
// all rather than a full card with fields removed after the fact.
//
// sql.ErrNoRows when the card is absent — the handler turns it into the uniform 404.
func (s *Store) GetPatternViewerManifest(ctx context.Context, techCardID int) (*entity.PatternViewerCard, error) {
	header, err := storeutil.QueryNamedOne[struct {
		StyleNumber string `db:"style_number"`
		Name        string `db:"name"`
	}](ctx, s.DB, `
		SELECT COALESCE(style_number, '') AS style_number, name
		FROM tech_card WHERE id = :id`, map[string]any{"id": techCardID})
	if err != nil {
		// sql.ErrNoRows passes through unwrapped: the handler's uniform-404 branch
		// distinguishes «card gone» from a query failure by errors.Is.
		return nil, err
	}

	sizes, err := storeutil.QueryListNamed[entity.PatternViewerSize](ctx, s.DB, `
		SELECT sz.id AS id, sz.name AS name
		FROM tech_card_size tcs
		JOIN size sz ON sz.id = tcs.size_id
		WHERE tcs.tech_card_id = :id
		ORDER BY tcs.display_order`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load viewer sizes of tech card %d: %w", techCardID, err)
	}

	// The size name comes off the DICTIONARY through a LEFT JOIN, not off the card's range:
	// a sheet may still be filed under a size the grade has since dropped, and looking its
	// name up in the range would return nothing — which the viewer cannot tell apart from
	// «filed under no size» (0281) and would caption as graded. LEFT, because size_id is
	// NULLable and the sizeless row must survive the join.
	sheets, err := storeutil.QueryListNamed[entity.PatternViewerSheet](ctx, s.DB, `
		SELECT COALESCE(p.line_key, '') AS line_key, COALESCE(p.size_id, 0) AS size_id,
		       COALESCE(sz.name, '') AS size_name, p.url,
		       COALESCE(p.filename, '') AS filename, COALESCE(p.name, '') AS name,
		       COALESCE(p.size_bytes, 0) AS size_bytes, p.version, p.uploaded_at,
		       COALESCE(p.bom_line_key, '') AS bom_line_key, COALESCE(p.fabric_purpose, '') AS fabric_purpose
		FROM tech_card_size_pattern p
		LEFT JOIN size sz ON sz.id = p.size_id
		WHERE p.tech_card_id = :id
		ORDER BY p.display_order`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load viewer sheets of tech card %d: %w", techCardID, err)
	}

	bomLines, err := storeutil.QueryListNamed[entity.PatternViewerBomLine](ctx, s.DB, `
		SELECT COALESCE(line_key, '') AS line_key, name, COALESCE(purpose, '') AS purpose
		FROM tech_card_bom_item
		WHERE tech_card_id = :id AND `+rollGoodsSectionIn+`
		ORDER BY display_order, id`,
		rollGoodsSectionArgs(map[string]any{"id": techCardID}))
	if err != nil {
		return nil, fmt.Errorf("load viewer bom lines of tech card %d: %w", techCardID, err)
	}

	return &entity.PatternViewerCard{
		TechCardId:  techCardID,
		StyleNumber: header.StyleNumber,
		StyleName:   header.Name,
		Sizes:       sizes,
		Sheets:      sheets,
		BomLines:    bomLines,
	}, nil
}
