package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// OPERATION → SEAM LINKS (tech_card_operation_seam, 0434): the seams a step sews, in sewing order.
//
// Written with the operations (full replace, the rows cascade away with the old operations) and read
// with them. A key must name a seam of THIS card: the seams are a side table written by their own
// RPCs, so the client upserts the seam first and then saves the card; a key the table does not hold
// is refused field-addressed (InvalidArgument), never dropped silently. Deleting a seam removes its
// key from every step in the delete's own transaction (deleteTechCardSeamsTx).
//
// One exception: a snapshot restore. There the seam table is brought to the snapshot's list later
// in the same transaction, so the keys are checked against THAT list, and a key the snapshot's own
// seam list lacks is dropped rather than refused — refusing it would make the snapshot
// unrestorable, and the restored table will not hold that seam anyway.

func insertTechCardOperationSeams(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation, restore *entity.TechCardSeamsRestore) error {
	wanted := false
	for i := range ops {
		if len(ops[i].SeamKeys) > 0 {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}

	known := map[string]bool{}
	if restore != nil {
		for _, seam := range restore.Seams {
			known[entity.NormalizeTechCardSeamKey(seam.SeamKey)] = true
		}
	} else {
		keys, err := storeutil.QueryScalarListNamed[string](ctx, db,
			`SELECT seam_key FROM tech_card_seam WHERE tech_card_id = :id`, map[string]any{"id": tcID})
		if err != nil {
			return fmt.Errorf("load seam keys of tech card %d: %w", tcID, err)
		}
		for _, k := range keys {
			known[entity.NormalizeTechCardSeamKey(k)] = true
		}
	}

	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db,
		`SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for seam links: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, r := range opRows {
		opIDByOrder[r.DisplayOrder] = r.Id
	}

	rows := make([]map[string]any, 0)
	for i, o := range ops {
		if len(o.SeamKeys) == 0 {
			continue
		}
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		pos := 0
		for j, k := range o.SeamKeys {
			k = entity.NormalizeTechCardSeamKey(k)
			if !known[k] {
				if restore != nil {
					continue
				}
				return entity.NewFieldViolation(fmt.Sprintf("operations[%d].seam_keys[%d]", i, j),
					"unknown_seam", "",
					"seam "+k+" is not stored on this card — save the seam first (UpsertTechCardSeams), or reload: it may have been deleted")
			}
			rows = append(rows, map[string]any{
				"operation_id":  opID,
				"seam_key":      k,
				"display_order": pos,
			})
			pos++
		}
	}
	if len(rows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_seam", rows); err != nil {
			return fmt.Errorf("failed to insert operation seam links: %w", err)
		}
	}
	return nil
}

// loadOperationSeamKeys returns each operation's seam keys in sewing order, keyed by operation id.
func loadOperationSeamKeys(ctx context.Context, db dependency.DB, cardIDs []int) (map[int][]string, error) {
	rows, err := storeutil.QueryListNamed[struct {
		OpID    int    `db:"op_id"`
		SeamKey string `db:"seam_key"`
	}](ctx, db, `
		SELECT o.id AS op_id, l.seam_key AS seam_key
		FROM tech_card_operation_seam l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": cardIDs})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card operation seams: %w", err)
	}
	out := make(map[int][]string)
	for _, r := range rows {
		out[r.OpID] = append(out[r.OpID], r.SeamKey)
	}
	return out, nil
}

// unlinkSeamFromOperationsTx drops a deleted seam's key from every step of the card. The caller
// holds the card row; the remaining links keep their relative order (display_order gaps are fine:
// the read orders by it and the next card save renumbers).
func unlinkSeamFromOperationsTx(ctx context.Context, db dependency.DB, techCardID int, key string) error {
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE l FROM tech_card_operation_seam l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id = :id AND l.seam_key = :k`,
		map[string]any{"id": techCardID, "k": key}); err != nil {
		return fmt.Errorf("unlink seam %s from the steps of tech card %d: %w", key, techCardID, err)
	}
	return nil
}
