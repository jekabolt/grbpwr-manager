package techcard

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fusingops"
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

// foldLinkViolation words the refusal of a card save. addressOf holds the address of a link the
// reconciliation moved in the body as it was sent: the client never saw the position it landed on.
func foldLinkViolation(p entity.TechCardFoldLinkProblem, addressOf map[string]string) error {
	field := addressOf[p.Key]
	if field == "" {
		field = fmt.Sprintf("operations[%d].fold_keys[%d]", p.Step, p.Pos)
	}
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

// placeDisplacedFoldLinks puts the links the FUSING reconciliation took off their steps on the steps
// that take them over, and returns each placed key's address in the body. A link with no new step is
// refused by that address; soft (a snapshot restore) leaves the fold flat instead. A fold whose piece
// left the card goes with its piece: no step can close it, and refusing would block deleting the piece.
func placeDisplacedFoldLinks(displaced []entity.TechCardDisplacedFoldKey, steps [][]string, views []fusingops.FoldStep,
	refs map[string]entity.TechCardFoldOrderRef, piecesOnCard []string, soft bool,
) (map[string]string, error) {
	addressOf := make(map[string]string, len(displaced))
	named := make(map[string]bool)
	for _, keys := range steps {
		for _, k := range keys {
			named[entity.NormalizeTechCardSeamKey(k)] = true
		}
	}
	for _, link := range displaced {
		key := entity.NormalizeTechCardSeamKey(link.Key)
		if named[key] {
			continue // the body already closes it on a step that stays
		}
		ref, known := refs[key]
		if !known {
			if soft {
				continue
			}
			return nil, entity.NewFieldViolation(link.Field, string(entity.TechCardFoldLinkUnknown), "",
				"fold "+key+" is not stored on this card — save the fold first (UpsertTechCardPieceFolds), or reload: it may have been deleted")
		}
		onCard := false
		for _, piece := range piecesOnCard {
			onCard = onCard || strings.EqualFold(strings.TrimSpace(piece), strings.TrimSpace(ref.PieceLineKey))
		}
		if !onCard {
			continue
		}
		home := fusingops.FoldHome(ref.PieceLineKey, link.Pieces, views)
		if home < 0 {
			if soft {
				continue
			}
			return nil, entity.NewFieldViolation(link.Field, string(entity.TechCardFoldLinkStepGone), "",
				"the step closing this fold is removed by this save and no other step takes its piece — move the fold to another step first")
		}
		steps[home] = append(slices.Clip(steps[home]), key)
		named[key] = true
		addressOf[key] = link.Field
	}
	return addressOf, nil
}

// settleFoldLinks holds the rule against the steps in their final order: the first problem refuses the
// save, or with soft (a snapshot restore) the offending links are cut.
func settleFoldLinks(steps [][]string, refs map[string]entity.TechCardFoldOrderRef, addressOf map[string]string, soft bool) ([][]string, error) {
	if soft {
		cut, _ := entity.CutTechCardFoldLinks(steps, refs)
		return cut, nil
	}
	if problems := entity.CheckTechCardFoldLinks(steps, refs); len(problems) > 0 {
		return nil, foldLinkViolation(problems[0], addressOf)
	}
	return steps, nil
}

// foldLinkRows is the table's rows for the steps' keys; opIDs is parallel to steps.
func foldLinkRows(steps [][]string, opIDs []int) []map[string]any {
	rows := make([]map[string]any, 0)
	for i, keys := range steps {
		for pos, k := range keys {
			rows = append(rows, map[string]any{
				"operation_id":  opIDs[i],
				"fold_key":      entity.NormalizeTechCardSeamKey(k),
				"display_order": pos,
			})
		}
	}
	return rows
}

func insertTechCardOperationFolds(ctx context.Context, db dependency.DB, tcID int, tc *entity.TechCardInsert,
	dropped map[string]bool,
) error {
	// A fold this same save removed with its piece is skipped, not refused as unknown: the body that
	// deleted the piece may still name it on a step.
	keep := func(keys []string) []string {
		if len(dropped) == 0 {
			return keys
		}
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			if !dropped[entity.NormalizeTechCardSeamKey(k)] {
				out = append(out, k)
			}
		}
		return out
	}
	ops := tc.Operations
	steps := make([][]string, len(ops))
	displaced := make([]entity.TechCardDisplacedFoldKey, 0, len(tc.DisplacedFoldKeys))
	for _, link := range tc.DisplacedFoldKeys {
		if !dropped[entity.NormalizeTechCardSeamKey(link.Key)] {
			displaced = append(displaced, link)
		}
	}
	wanted := len(displaced) > 0
	for i := range ops {
		steps[i] = keep(ops[i].FoldKeys)
		wanted = wanted || len(steps[i]) > 0
	}
	// A card with no fold steps costs this save no statement at all.
	if !wanted {
		return nil
	}
	refs, err := techCardOperationFoldRefs(ctx, db, tcID)
	if err != nil {
		return err
	}
	piecesOnCard := make([]string, 0, len(tc.Pieces))
	for i := range tc.Pieces {
		piecesOnCard = append(piecesOnCard, tc.Pieces[i].LineKey)
	}
	addressOf, err := placeDisplacedFoldLinks(displaced, steps, fusingops.FoldStepsOf(ops, tc.Pieces),
		refs, piecesOnCard, tc.RestoreFolds)
	if err != nil {
		return err
	}
	if steps, err = settleFoldLinks(steps, refs, addressOf, tc.RestoreFolds); err != nil {
		return err
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
	opIDs := make([]int, len(steps))
	for i, keys := range steps {
		if len(keys) == 0 {
			continue
		}
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		opIDs[i] = opID
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_fold", foldLinkRows(steps, opIDs)); err != nil {
		return fmt.Errorf("failed to insert operation fold links: %w", err)
	}
	return nil
}

// liftDisplacedFoldLinks takes the fold links off the steps a plan deletes or splits (the DELETE of a
// step would cascade them away) and returns them. A plan that does neither costs no statement, which
// is every save whose write model was already reconciled.
func liftDisplacedFoldLinks(ctx context.Context, db dependency.DB, plan fusingops.Plan, operations []fusingops.Operation,
	rowByID map[int]fusingOperationRow, pieceKeyByID map[int]string,
) ([]entity.TechCardDisplacedFoldKey, error) {
	ids := append([]int(nil), plan.DeletedIDs...)
	for _, op := range plan.Operations {
		if op.ID == 0 && op.CopyOf > 0 && !slices.Contains(ids, op.CopyOf) {
			ids = append(ids, op.CopyOf)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	links, err := storeutil.QueryListNamed[struct {
		OpID    int    `db:"operation_id"`
		FoldKey string `db:"fold_key"`
	}](ctx, db, `
		SELECT operation_id, fold_key FROM tech_card_operation_fold
		WHERE operation_id IN (:ids)
		ORDER BY operation_id, display_order, id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("load fold links of reconciled operations: %w", err)
	}
	if len(links) == 0 {
		return nil, nil
	}
	// operations is in display_order, the order the body was written in.
	var out []entity.TechCardDisplacedFoldKey
	for index, op := range operations {
		pos := 0
		for _, l := range links {
			if l.OpID != op.ID {
				continue
			}
			pieces := make([]string, 0, len(op.PieceIDs)+1)
			for _, id := range op.PieceIDs {
				pieces = append(pieces, pieceKeyByID[id])
			}
			if owner := rowByID[op.ID].AutoFusingPieceKey; owner.Valid {
				pieces = append(pieces, owner.String)
			}
			out = append(out, entity.TechCardDisplacedFoldKey{
				Key: l.FoldKey, Field: fmt.Sprintf("operations[%d].fold_keys[%d]", index, pos), Pieces: pieces,
			})
			pos++
		}
	}
	if err := storeutil.ExecNamed(ctx, db, `DELETE FROM tech_card_operation_fold WHERE operation_id IN (:ids)`,
		map[string]any{"ids": ids}); err != nil {
		return nil, fmt.Errorf("lift fold links of reconciled operations: %w", err)
	}
	return out, nil
}

// settleReconciledFoldLinks runs after the reconciliation has its final ids and order: it places the
// links liftDisplacedFoldLinks took and holds the rule against the new order. Nothing moved, no
// statement. soft is a snapshot restore: it cuts where a save refuses.
func settleReconciledFoldLinks(ctx context.Context, db dependency.DB, techCardID int, plan fusingops.Plan,
	displaced []entity.TechCardDisplacedFoldKey, pieceKeyByID map[int]string, soft bool,
) error {
	current, err := loadOperationFoldKeys(ctx, db, []int{techCardID})
	if err != nil {
		return err
	}
	if len(displaced) == 0 && len(current) == 0 {
		return nil
	}
	refs, err := techCardOperationFoldRefs(ctx, db, techCardID)
	if err != nil {
		return err
	}
	steps := make([][]string, len(plan.Operations))
	views := make([]fusingops.FoldStep, len(plan.Operations))
	opIDs := make([]int, len(plan.Operations))
	for i, op := range plan.Operations {
		steps[i], opIDs[i] = current[op.ID], op.ID
		for _, id := range op.PieceIDs {
			views[i].Pieces = append(views[i].Pieces, pieceKeyByID[id])
		}
		if op.Kind == fusingops.KindAuto && !op.IsOrphan() {
			views[i].Owner = pieceKeyByID[op.Owner]
		}
	}
	piecesOnCard := make([]string, 0, len(pieceKeyByID))
	for _, key := range pieceKeyByID {
		piecesOnCard = append(piecesOnCard, key)
	}
	addressOf, err := placeDisplacedFoldLinks(displaced, steps, views, refs, piecesOnCard, soft)
	if err != nil {
		return err
	}
	settled, err := settleFoldLinks(steps, refs, addressOf, soft)
	if err != nil {
		return err
	}
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE l FROM tech_card_operation_fold l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id = :id`, map[string]any{"id": techCardID}); err != nil {
		return fmt.Errorf("clear fold links of tech card %d: %w", techCardID, err)
	}
	if rows := foldLinkRows(settled, opIDs); len(rows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_fold", rows); err != nil {
			return fmt.Errorf("failed to insert reconciled fold links: %w", err)
		}
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
