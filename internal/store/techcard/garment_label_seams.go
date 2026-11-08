package techcard

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// reconcileRemovedGarmentLabelSeams handles the soft seam anchor before the delete+insert label
// write. A pose exists only to place the label, so it goes with the label; a real seam is authored
// construction and must be removed explicitly.
func reconcileRemovedGarmentLabelSeams(ctx context.Context, db dependency.DB, tcID int, tc *entity.TechCardInsert) error {
	if tc == nil || !tc.LabelsAware || tc.RestoreSeams != nil {
		return nil
	}
	stored, err := storeutil.QueryScalarListNamed[string](ctx, db, `
		SELECT line_key FROM tech_card_garment_label
		WHERE tech_card_id = :id AND line_key IS NOT NULL`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load garment label keys of tech card %d: %w", tcID, err)
	}
	if len(stored) == 0 {
		return nil
	}
	kept := make(map[string]bool, len(tc.GarmentLabels))
	for i := range tc.GarmentLabels {
		kept[garmentLabelLineKey(tc.GarmentLabels[i].LineKey)] = true
	}
	doomed := make(map[string]bool)
	for _, raw := range stored {
		key := garmentLabelLineKey(raw)
		if !kept[key] {
			doomed[key] = true
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		SeamKey string `db:"seam_key"`
		Kind    string `db:"kind"`
		SideA   []byte `db:"side_a"`
		SideB   []byte `db:"side_b"`
	}](ctx, db, `SELECT seam_key, kind, side_a, side_b FROM tech_card_seam WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load seam anchors of tech card %d: %w", tcID, err)
	}
	poseKeys := make([]string, 0)
	seenPose := map[string]bool{}
	for _, row := range rows {
		usesRemoved := false
		for _, raw := range [][]byte{row.SideA, row.SideB} {
			var anchors []entity.TechCardSeamAnchor
			if err := json.Unmarshal(raw, &anchors); err != nil {
				continue
			}
			for _, anchor := range anchors {
				usesRemoved = usesRemoved || doomed[garmentLabelLineKey(anchor.PieceLineKey)]
			}
		}
		if !usesRemoved {
			continue
		}
		if entity.TechCardSeamKind(row.Kind) != entity.TechCardSeamKindPose {
			return entity.NewFieldViolation("garment_labels", "garment_label_in_use", "",
				"a seam uses this label; remove it from the seam first")
		}
		key := entity.NormalizeTechCardSeamKey(row.SeamKey)
		if !seenPose[key] {
			seenPose[key] = true
			poseKeys = append(poseKeys, key)
		}
	}
	if len(poseKeys) > 0 {
		if _, err := deleteTechCardSeamsTx(ctx, db, tcID, poseKeys); err != nil {
			return err
		}
	}
	return nil
}
