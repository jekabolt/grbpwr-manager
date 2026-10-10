package techcard

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// assemblyExampleMinJoins — a card with fewer join operations is no example of an assembly tree.
const assemblyExampleMinJoins = 4

// ListAssemblyExampleCards reads up to limit OTHER sellable tech cards (≠ excludeID) that hold at least
// assemblyExampleMinJoins join operations (a non-empty output_unit_key), the cards sharing
// excludeID's category first, then the most recently updated — with their joins in the card's
// operation order and each join's inputs in order. Read-only, two bounded queries; the house style
// of SuggestAssemblySkeleton (06-AI-STRUCTURE).
func (s *Store) ListAssemblyExampleCards(ctx context.Context, excludeID, limit int) ([]entity.AssemblyExampleCard, error) {
	if limit <= 0 {
		return nil, nil
	}
	cards, err := storeutil.QueryListNamed[struct {
		ID           int            `db:"id"`
		StyleNumber  sql.NullString `db:"style_number"`
		Name         string         `db:"name"`
		CategoryName sql.NullString `db:"category_name"`
		SameCategory bool           `db:"same_category"`
	}](ctx, s.DB, `
		SELECT t.id, t.style_number, t.name, c.name AS category_name,
		       (me.category_id IS NOT NULL AND t.category_id <=> me.category_id) AS same_category
		FROM tech_card t
		JOIN (SELECT tech_card_id
		      FROM tech_card_operation
		      WHERE output_unit_key IS NOT NULL AND output_unit_key <> ''
		      GROUP BY tech_card_id
		      HAVING COUNT(*) >= :min_joins) j ON j.tech_card_id = t.id
		LEFT JOIN category c ON c.id = t.category_id
		LEFT JOIN tech_card me ON me.id = :exclude
		WHERE t.id <> :exclude AND t.purpose = 'sellable'
		ORDER BY same_category DESC, t.updated_at DESC, t.id DESC
		LIMIT :limit`,
		map[string]any{"exclude": excludeID, "min_joins": assemblyExampleMinJoins, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("can't load assembly example cards: %w", err)
	}
	if len(cards) == 0 {
		return nil, nil
	}
	out := make([]entity.AssemblyExampleCard, len(cards))
	ids := make([]int, len(cards))
	at := make(map[int]int, len(cards))
	for i, c := range cards {
		out[i] = entity.AssemblyExampleCard{
			TechCardID: c.ID, StyleNumber: c.StyleNumber.String, Name: c.Name,
			CategoryName: c.CategoryName.String, SameCategory: c.SameCategory,
		}
		ids[i] = c.ID
		at[c.ID] = i
	}

	// One row per (join, input); a join with no input rows comes once with NULL input columns. The
	// operation order is the card's own (operation_number first, as the card read orders it).
	rows, err := storeutil.QueryListNamed[struct {
		CardID    int            `db:"tech_card_id"`
		OpID      int            `db:"op_id"`
		UnitKey   string         `db:"output_unit_key"`
		UnitName  sql.NullString `db:"output_unit_name"`
		InUnit    sql.NullString `db:"in_unit_key"`
		PieceName sql.NullString `db:"piece_name"`
		PieceKey  sql.NullString `db:"piece_line_key"`
	}](ctx, s.DB, `
		SELECT o.tech_card_id, o.id AS op_id, o.output_unit_key, o.output_unit_name,
		       l.unit_key AS in_unit_key, p.name AS piece_name, p.line_key AS piece_line_key
		FROM tech_card_operation o
		LEFT JOIN tech_card_operation_input l ON l.operation_id = o.id
		LEFT JOIN tech_card_piece p ON p.id = l.piece_id
		WHERE o.tech_card_id IN (:ids) AND o.output_unit_key IS NOT NULL AND o.output_unit_key <> ''
		ORDER BY o.tech_card_id, o.operation_number IS NULL, o.operation_number, o.display_order, o.id,
		         l.display_order, l.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load assembly example joins: %w", err)
	}
	lastOp := map[int]int{} // card → the op id of its last join appended
	for _, r := range rows {
		card := &out[at[r.CardID]]
		if lastOp[r.CardID] != r.OpID || len(card.Joins) == 0 {
			card.Joins = append(card.Joins, entity.AssemblyExampleJoin{OutputUnitKey: r.UnitKey, OutputUnitName: r.UnitName.String})
			lastOp[r.CardID] = r.OpID
		}
		j := &card.Joins[len(card.Joins)-1]
		switch {
		case r.InUnit.Valid && r.InUnit.String != "":
			j.Inputs = append(j.Inputs, entity.AssemblyExampleInput{UnitKey: r.InUnit.String})
		case r.PieceKey.Valid:
			j.Inputs = append(j.Inputs, entity.AssemblyExampleInput{PieceName: r.PieceName.String, PieceLineKey: r.PieceKey.String})
		}
	}
	return out, nil
}
