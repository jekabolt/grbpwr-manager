package product

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// colorwayDevRow is the persisted development block, read before a patch is applied so the merged
// result (not the sparse patch) is what the round journal records.
type colorwayDevRow struct {
	DevCode            sql.NullString `db:"dev_code"`
	DevName            sql.NullString `db:"dev_name"`
	LabDipStatus       sql.NullString `db:"lab_dip_status"`
	DevComment         sql.NullString `db:"dev_comment"`
	Pantone            sql.NullString `db:"pantone"`
	PantoneSystem      sql.NullString `db:"pantone_system"`
	DevHex             sql.NullString `db:"dev_hex"`
	SwatchMediaId      sql.NullInt32  `db:"swatch_media_id"`
	LabDipRound        sql.NullInt32  `db:"lab_dip_round"`
	LabDipSubmittedAt  sql.NullTime   `db:"lab_dip_submitted_at"`
	LabDipDecidedAt    sql.NullTime   `db:"lab_dip_decided_at"`
	LabDipDecidedBy    sql.NullString `db:"lab_dip_decided_by"`
	LabDipRejectReason sql.NullString `db:"lab_dip_reject_reason"`
	DisplayOrder       int            `db:"display_order"`
}

// applyColorwayDevelopment merges a patch into the stored development block and writes it back,
// then records the resulting state in the lab-dip round journal.
//
// Until this existed, UpdateColorwayRequest.development was accepted on the wire and silently
// dropped — the admin's lab-dip panel wrote into nothing. The write is a MERGE rather than a
// replace because no read path returns the dev identity fields (dev_code/name/pantone/swatch), so a
// caller editing a lab-dip decision has no way to echo them back and a full replace would erase them.
func applyColorwayDevelopment(ctx context.Context, db dependency.DB, colorwayID int, patch *entity.ColorwayDevelopmentPatch) error {
	if patch.IsEmpty() {
		return nil
	}
	cur, err := storeutil.QueryNamedOne[colorwayDevRow](ctx, db, `
		SELECT dev_code, dev_name, lab_dip_status, dev_comment, pantone, pantone_system, dev_hex,
		       swatch_media_id, lab_dip_round, lab_dip_submitted_at, lab_dip_decided_at,
		       lab_dip_decided_by, lab_dip_reject_reason, display_order
		FROM product WHERE id = :id`, map[string]any{"id": colorwayID})
	if err != nil {
		return err // sql.ErrNoRows -> NOT_FOUND upstream
	}

	next := cur
	if patch.DevCode != nil {
		next.DevCode = nullableString(*patch.DevCode)
	}
	if patch.Name != nil {
		next.DevName = nullableString(*patch.Name)
	}
	if patch.LabDipStatus != nil {
		if !entity.IsValidTechCardLabDipStatus(*patch.LabDipStatus) {
			return fmt.Errorf("invalid lab dip status %q", *patch.LabDipStatus)
		}
		next.LabDipStatus = sql.NullString{String: string(*patch.LabDipStatus), Valid: true}
	}
	if patch.Comment != nil {
		next.DevComment = nullableString(*patch.Comment)
	}
	if patch.Pantone != nil {
		next.Pantone = nullableString(*patch.Pantone)
	}
	if patch.PantoneSystem != nil {
		next.PantoneSystem = nullableString(*patch.PantoneSystem)
	}
	if patch.DevHex != nil {
		next.DevHex = nullableString(*patch.DevHex)
	}
	if patch.SwatchMediaId != nil {
		next.SwatchMediaId = nullableInt32(*patch.SwatchMediaId)
	}
	if patch.LabDipRound != nil {
		next.LabDipRound = nullableInt32(*patch.LabDipRound)
	}
	if patch.LabDipSubmittedAt != nil {
		next.LabDipSubmittedAt = *patch.LabDipSubmittedAt
	}
	if patch.LabDipDecidedAt != nil {
		next.LabDipDecidedAt = *patch.LabDipDecidedAt
	}
	if patch.LabDipDecidedBy != nil {
		next.LabDipDecidedBy = nullableString(*patch.LabDipDecidedBy)
	}
	if patch.LabDipRejectReason != nil {
		next.LabDipRejectReason = nullableString(*patch.LabDipRejectReason)
	}
	if patch.DisplayOrder != nil {
		next.DisplayOrder = *patch.DisplayOrder
	}

	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE product SET
			dev_code = :dev_code, dev_name = :dev_name, lab_dip_status = :lab_dip_status,
			dev_comment = :dev_comment, pantone = :pantone, pantone_system = :pantone_system,
			dev_hex = :dev_hex, swatch_media_id = :swatch_media_id, lab_dip_round = :lab_dip_round,
			lab_dip_submitted_at = :lab_dip_submitted_at, lab_dip_decided_at = :lab_dip_decided_at,
			lab_dip_decided_by = :lab_dip_decided_by, lab_dip_reject_reason = :lab_dip_reject_reason,
			display_order = :display_order
		WHERE id = :id`, map[string]any{
		"id":                    colorwayID,
		"dev_code":              next.DevCode,
		"dev_name":              next.DevName,
		"lab_dip_status":        next.LabDipStatus,
		"dev_comment":           next.DevComment,
		"pantone":               next.Pantone,
		"pantone_system":        next.PantoneSystem,
		"dev_hex":               next.DevHex,
		"swatch_media_id":       next.SwatchMediaId,
		"lab_dip_round":         next.LabDipRound,
		"lab_dip_submitted_at":  next.LabDipSubmittedAt,
		"lab_dip_decided_at":    next.LabDipDecidedAt,
		"lab_dip_decided_by":    next.LabDipDecidedBy,
		"lab_dip_reject_reason": next.LabDipRejectReason,
		"display_order":         next.DisplayOrder,
	}); err != nil {
		return fmt.Errorf("can't update colourway %d development: %w", colorwayID, err)
	}

	if !patch.TouchesLabDip() {
		return nil
	}
	return recordLabDipRound(ctx, db, colorwayID, next)
}

// recordLabDipRound writes the merged lab-dip state into the round journal, keyed by round number.
// Re-deciding the SAME round updates that row (a rejection corrected to an approval is not a new
// round); moving to a higher round leaves the previous one exactly as it was, which is the whole
// point — the scalars on `product` keep being overwritten, the journal does not.
//
// A colourway with a lab-dip state but no round number is on round 1: the field was optional long
// before the journal existed and plenty of rows never set it.
func recordLabDipRound(ctx context.Context, db dependency.DB, colorwayID int, dev colorwayDevRow) error {
	if !dev.LabDipStatus.Valid && !dev.LabDipRound.Valid {
		return nil // nothing to record: the patch cleared the lab dip entirely
	}
	round := int(dev.LabDipRound.Int32)
	if round < 1 {
		round = 1
	}
	status := dev.LabDipStatus.String
	if status == "" {
		status = string(entity.LabDipPending)
	}
	return storeutil.ExecNamed(ctx, db, `
		INSERT INTO product_lab_dip_round
			(product_id, round_number, status, submitted_at, decided_at, decided_by, reject_reason,
			 comment, swatch_media_id)
		VALUES (:product_id, :round_number, :status, :submitted_at, :decided_at, :decided_by,
			:reject_reason, :comment, :swatch_media_id)
		ON DUPLICATE KEY UPDATE
			status = VALUES(status), submitted_at = VALUES(submitted_at), decided_at = VALUES(decided_at),
			decided_by = VALUES(decided_by), reject_reason = VALUES(reject_reason),
			comment = VALUES(comment), swatch_media_id = VALUES(swatch_media_id)`,
		map[string]any{
			"product_id":      colorwayID,
			"round_number":    round,
			"status":          status,
			"submitted_at":    dev.LabDipSubmittedAt,
			"decided_at":      dev.LabDipDecidedAt,
			"decided_by":      dev.LabDipDecidedBy,
			"reject_reason":   dev.LabDipRejectReason,
			"comment":         dev.DevComment,
			"swatch_media_id": dev.SwatchMediaId,
		})
}

// LabDipRoundsByStyleID returns every recorded lab-dip round of every colourway of a style, grouped
// by colourway id and ordered oldest first — one query for the whole style rather than one per
// colourway, so the style read stays a fixed number of round-trips.
func (s *Store) LabDipRoundsByStyleID(ctx context.Context, styleID int) (map[int][]entity.ColorwayLabDipRound, error) {
	rows, err := storeutil.QueryListNamed[entity.ColorwayLabDipRound](ctx, s.DB, `
		SELECT r.product_id, r.round_number, r.status, r.submitted_at, r.decided_at, r.decided_by,
		       r.reject_reason, r.comment, r.swatch_media_id, r.created_at
		FROM product_lab_dip_round r
		JOIN product p ON p.id = r.product_id
		WHERE p.style_id = :styleId
		ORDER BY r.product_id, r.round_number`, map[string]any{"styleId": styleID})
	if err != nil {
		return nil, fmt.Errorf("can't load lab dip rounds for style %d: %w", styleID, err)
	}
	out := make(map[int][]entity.ColorwayLabDipRound, len(rows))
	for _, r := range rows {
		out[r.ProductId] = append(out[r.ProductId], r)
	}
	return out, nil
}

func nullableString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func nullableInt32(v int) sql.NullInt32 {
	if v <= 0 {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(v), Valid: true}
}
