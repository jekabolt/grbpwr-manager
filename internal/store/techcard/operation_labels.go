package techcard

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

func garmentLabelLineKey(key string) string { return strings.ToUpper(strings.TrimSpace(key)) }

// prepareGarmentLabelKeys is the store fallback for direct callers that bypass DTO conversion. It
// runs before operation rows are written, so every operation link can be validated against final keys.
func prepareGarmentLabelKeys(tc *entity.TechCardInsert) error {
	if tc == nil {
		return nil
	}
	taken := make(map[string]string, len(tc.Pieces)+len(tc.CutStrips)+len(tc.GarmentLabels))
	for i := range tc.Pieces {
		if key := garmentLabelLineKey(tc.Pieces[i].LineKey); key != "" {
			taken[key] = fmt.Sprintf("pieces[%d].line_key", i)
		}
	}
	for i := range tc.CutStrips {
		if key := garmentLabelLineKey(tc.CutStrips[i].LineKey); key != "" {
			taken[key] = fmt.Sprintf("cut_strips[%d].line_key", i)
		}
	}
	for i := range tc.GarmentLabels {
		field := fmt.Sprintf("garment_labels[%d].line_key", i)
		key := garmentLabelLineKey(tc.GarmentLabels[i].LineKey)
		if key == "" {
			key = newLineKey()
		}
		if !entity.IsValidProductionRunLineKey(key) {
			return entity.NewFieldViolation(field, "invalid", tc.GarmentLabels[i].LineKey, "a 26-character ULID")
		}
		if owner, ok := taken[key]; ok {
			return entity.NewFieldViolation(field, "duplicate", owner, "each piece, cut strip and label has its own key")
		}
		taken[key] = field
		tc.GarmentLabels[i].LineKey = key
	}
	return nil
}

func insertTechCardOperationLabels(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation,
	labels []entity.TechCardGarmentLabel,
) error {
	wanted := false
	for i := range ops {
		wanted = wanted || len(ops[i].LabelKeys) > 0
	}
	if !wanted {
		return nil
	}
	known := make(map[string]bool, len(labels))
	for i := range labels {
		known[garmentLabelLineKey(labels[i].LineKey)] = true
	}
	// An older labels-unaware save preserves the stored label rows instead of passing them to the
	// insert phase. Its carried operation links still need the effective stored key set.
	if len(labels) == 0 {
		keys, err := storeutil.QueryScalarListNamed[string](ctx, db,
			`SELECT line_key FROM tech_card_garment_label WHERE tech_card_id = :id AND line_key IS NOT NULL`,
			map[string]any{"id": tcID})
		if err != nil {
			return fmt.Errorf("load garment label keys for operation links: %w", err)
		}
		for _, key := range keys {
			known[garmentLabelLineKey(key)] = true
		}
	}
	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db, `SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for garment label links: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, row := range opRows {
		opIDByOrder[row.DisplayOrder] = row.Id
	}
	rows := make([]map[string]any, 0)
	for i := range ops {
		if len(ops[i].LabelKeys) == 0 {
			continue
		}
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		seen := make(map[string]bool, len(ops[i].LabelKeys))
		for j, raw := range ops[i].LabelKeys {
			key := garmentLabelLineKey(raw)
			if seen[key] {
				continue
			}
			if !known[key] {
				return entity.NewFieldViolation(fmt.Sprintf("operations[%d].label_keys[%d]", i, j),
					"label_missing", key, "a garment label of this card")
			}
			seen[key] = true
			rows = append(rows, map[string]any{"operation_id": opID, "label_key": key, "display_order": j})
		}
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_label", rows); err != nil {
		return fmt.Errorf("failed to insert operation garment label links: %w", err)
	}
	return nil
}

func loadOperationLabelKeys(ctx context.Context, db dependency.DB, cardIDs []int) (map[int][]string, error) {
	rows, err := storeutil.QueryListNamed[struct {
		OpID     int    `db:"op_id"`
		LabelKey string `db:"label_key"`
	}](ctx, db, `
		SELECT o.id AS op_id, l.label_key
		FROM tech_card_operation_label l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.label_key`, map[string]any{"ids": cardIDs})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card operation garment labels: %w", err)
	}
	out := make(map[int][]string)
	for _, row := range rows {
		out[row.OpID] = append(out[row.OpID], row.LabelKey)
	}
	return out, nil
}
