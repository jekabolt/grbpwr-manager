package techcard

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// OPERATION → FOLD LINKS (tech_card_operation_fold, 0444): the piece folds that close on a step.
//
// Written with the operations (full replace, the rows cascade away with the old operations) and read
// with them, the seam links' shape (operation_seams.go). A step reorder needs no code: the keys ride
// the step. The folds are a side table written by their own RPCs, so the client upserts the fold first
// and then saves the card; a link the rule does not admit is refused field-addressed, never dropped.
// Deleting a fold removes its key from every step in the delete's own transaction, and a fold write
// that would reverse the links already stored is refused before it lands (requireFoldLinkOrderTx),
// otherwise the next card save would refuse a body nobody changed.

// techCardOperationFoldRefs reads the card's folds as the rule's input.
func techCardOperationFoldRefs(ctx context.Context, db dependency.DB, tcID int) (map[string]entity.TechCardFoldOrderRef, error) {
	rows, err := storeutil.QueryListNamed[struct {
		FoldKey      string `db:"fold_key"`
		PieceLineKey string `db:"piece_line_key"`
		Seq          int    `db:"seq"`
	}](ctx, db,
		`SELECT fold_key, piece_line_key, seq FROM tech_card_piece_fold WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return nil, fmt.Errorf("load folds of tech card %d: %w", tcID, err)
	}
	refs := make(map[string]entity.TechCardFoldOrderRef, len(rows))
	for _, r := range rows {
		refs[entity.NormalizeTechCardSeamKey(r.FoldKey)] = entity.TechCardFoldOrderRef{PieceLineKey: r.PieceLineKey, Seq: r.Seq}
	}
	return refs, nil
}

// foldLinkViolation words the refusal of a card save.
func foldLinkViolation(p entity.TechCardFoldLinkProblem) error {
	field := fmt.Sprintf("operations[%d].fold_keys[%d]", p.Step, p.Pos)
	switch p.Kind {
	case entity.TechCardFoldLinkUnknown:
		return entity.NewFieldViolation(field, string(p.Kind), "",
			"fold "+p.Key+" is not stored on this card — save the fold first (UpsertTechCardPieceFolds), or reload: it may have been deleted")
	case entity.TechCardFoldLinkTwoSteps:
		return entity.NewFieldViolation(field, string(p.Kind), fmt.Sprintf("operations[%d]", p.OtherStep),
			"a fold closes on one step only — take it off the other step first")
	case entity.TechCardFoldLinkOrder:
		return entity.NewFieldViolation(field, string(p.Kind), fmt.Sprintf("operations[%d]", p.OtherStep),
			"a later fold of this piece already closes on an earlier step — keep the piece's folds in their order")
	default:
		return entity.NewFieldViolation(field, string(p.Kind), "", "a step names each fold once")
	}
}

func insertTechCardOperationFolds(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation) error {
	steps := make([][]string, len(ops))
	wanted := false
	for i := range ops {
		steps[i] = ops[i].FoldKeys
		wanted = wanted || len(ops[i].FoldKeys) > 0
	}
	// A card with no fold steps costs this save no statement at all.
	if !wanted {
		return nil
	}
	refs, err := techCardOperationFoldRefs(ctx, db, tcID)
	if err != nil {
		return err
	}
	if problems := entity.CheckTechCardFoldLinks(steps, refs); len(problems) > 0 {
		return foldLinkViolation(problems[0])
	}

	opRows, err := storeutil.QueryListNamed[importedOperationRow](ctx, db,
		`SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for fold links: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, r := range opRows {
		opIDByOrder[r.DisplayOrder] = r.Id
	}

	rows := make([]map[string]any, 0)
	for i, o := range ops {
		if len(o.FoldKeys) == 0 {
			continue
		}
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		for pos, k := range o.FoldKeys {
			rows = append(rows, map[string]any{
				"operation_id":  opID,
				"fold_key":      entity.NormalizeTechCardSeamKey(k),
				"display_order": pos,
			})
		}
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_fold", rows); err != nil {
		return fmt.Errorf("failed to insert operation fold links: %w", err)
	}
	return nil
}

// loadOperationFoldKeys returns each operation's fold keys in order, keyed by operation id.
func loadOperationFoldKeys(ctx context.Context, db dependency.DB, cardIDs []int) (map[int][]string, error) {
	rows, err := storeutil.QueryListNamed[struct {
		OpID    int    `db:"op_id"`
		FoldKey string `db:"fold_key"`
	}](ctx, db, `
		SELECT o.id AS op_id, l.fold_key AS fold_key
		FROM tech_card_operation_fold l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": cardIDs})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card operation folds: %w", err)
	}
	out := make(map[int][]string)
	for _, r := range rows {
		out[r.OpID] = append(out[r.OpID], r.FoldKey)
	}
	return out, nil
}

// unlinkFoldFromOperationsTx drops a deleted fold's key from every step of the card. The caller holds
// the card row; the remaining links keep their relative order (display_order gaps are fine: the read
// orders by it and the next card save renumbers).
func unlinkFoldFromOperationsTx(ctx context.Context, db dependency.DB, techCardID int, key string) error {
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE l FROM tech_card_operation_fold l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id = :id AND l.fold_key = :k`,
		map[string]any{"id": techCardID, "k": key}); err != nil {
		return fmt.Errorf("unlink fold %s from the steps of tech card %d: %w", key, techCardID, err)
	}
	return nil
}

// requireFoldLinkOrderTx refuses a fold write after which the links already on the steps read in
// reverse: a seq swap, or a fold moved to another piece. Judged on the stored rows inside the write's
// transaction (the error rolls the write back). A card with no fold steps costs one read.
func requireFoldLinkOrderTx(ctx context.Context, db dependency.DB, techCardID int, folds []entity.TechCardPieceFoldInput) error {
	// The order the card reads in (techCardOperationsQuery), not the bare display_order.
	links, err := storeutil.QueryListNamed[struct {
		OpID    int    `db:"op_id"`
		FoldKey string `db:"fold_key"`
	}](ctx, db, `
		SELECT o.id AS op_id, l.fold_key AS fold_key
		FROM tech_card_operation_fold l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id = :id
		ORDER BY o.operation_number IS NULL, o.operation_number, o.display_order, l.display_order, l.id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("load fold links of tech card %d: %w", techCardID, err)
	}
	if len(links) == 0 {
		return nil
	}
	refs, err := techCardOperationFoldRefs(ctx, db, techCardID)
	if err != nil {
		return err
	}
	var steps [][]string
	last := 0
	for n, l := range links {
		if n == 0 || l.OpID != last {
			steps = append(steps, nil)
			last = l.OpID
		}
		steps[len(steps)-1] = append(steps[len(steps)-1], l.FoldKey)
	}
	for _, p := range entity.CheckTechCardFoldLinks(steps, refs) {
		if p.Kind != entity.TechCardFoldLinkOrder {
			continue
		}
		// Only a reversal this write has a hand in: a card that already holds one elsewhere (a fusing
		// reconciliation may move a fusing step) must not lose its other folds' writes to it.
		for i, f := range folds {
			if strings.EqualFold(f.FoldKey, p.Key) || strings.EqualFold(f.FoldKey, p.OtherKey) {
				return entity.NewFieldViolation(fmt.Sprintf("folds[%d].seq", i), string(p.Kind), "",
					"the assembly steps close this piece's folds in the other order — move the folds on their steps first, or keep the order")
			}
		}
	}
	return nil
}
