package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Saved раскладки (markers, tech_card_marker, migration 0257): the measured fabric layout of one
// size's pattern pieces, self-contained geometry included. A marker is a MEASUREMENT — costing
// reads consumption per garment off it (used_length_cm / sets) — not a structural reference, which
// is why its BOM link degrades to NULL instead of blocking BOM edits, and why nothing else FKs it.
//
// Writes are last-write-wins on purpose. Neither fitting_change_request nor
// tech_card_output_variant carries a lock_version; concurrency collapses inside the SERIALIZABLE
// write transaction. Marker writes must NOT bump tech_card.lock_version — saving a раскладка from
// the nesting modal would otherwise 409 the same operator's open card form.

// markerSummaryColumns is the explicit list every summary read uses. Explicit, not SELECT * —
// layout must never ride a summary query, and JSON columns read via * resurface the
// quoted-JSON-scalar bug (see UnquoteLegacyComposition).
const markerSummaryColumns = `
	m.id, m.tech_card_id, m.size_id, m.name, m.source, m.bom_item_id,
	b.line_key AS bom_line_key, b.name AS bom_item_name, b.unit AS bom_item_unit,
	m.fabric_width_cm, m.gap_cm, m.edge_margin_cm, m.selvedge_cm, m.allow_cross_grain, m.sets,
	m.used_length_cm, m.efficiency_pct, m.placed_count, m.total_count,
	m.created_by, m.updated_by, m.created_at, m.updated_at`

// ListMarkerSummaries returns a card's saved раскладки without their layout blobs, newest first
// within a size. Runs on the caller's connection so the single-card read sees one snapshot.
func listMarkerSummaries(ctx context.Context, db dependency.DB, techCardID int) ([]entity.TechCardMarkerSummary, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardMarkerSummary](ctx, db, `
		SELECT `+markerSummaryColumns+`
		FROM tech_card_marker m
		LEFT JOIN tech_card_bom_item b ON b.id = m.bom_item_id
		WHERE m.tech_card_id = :id
		ORDER BY m.size_id, m.updated_at DESC, m.id DESC`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("can't list tech card markers: %w", err)
	}
	return rows, nil
}

// GetMarker returns one marker WITH its layout blob — the only read that carries it.
func (s *Store) GetMarker(ctx context.Context, id int) (*entity.TechCardMarker, error) {
	row, err := storeutil.QueryNamedOne[entity.TechCardMarker](ctx, s.DB, `
		SELECT `+markerSummaryColumns+`, m.layout
		FROM tech_card_marker m
		LEFT JOIN tech_card_bom_item b ON b.id = m.bom_item_id
		WHERE m.id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: marker %d", entity.ErrMarkerNotFound, id)
		}
		return nil, fmt.Errorf("load marker %d: %w", id, err)
	}
	return &row, nil
}

// SaveMarker creates (id == 0) or fully replaces (id > 0) one saved раскладка and returns its id.
// The layout blob has no partial update — Ф5's manual adjustment re-saves the whole marker with
// source='manual'. Validation of the payload's FORM lives in dto; everything checked here is a
// fact only the database can witness: the card's approval state, the size's membership in the
// card's range, the BOM line's identity, the (card, size, name) uniqueness.
func (s *Store) SaveMarker(ctx context.Context, techCardID, id int, ins entity.TechCardMarkerInsert, username string) (int, error) {
	if id < 0 {
		return 0, entity.NewFieldViolation("id", "must_not_be_negative", "", "leave it 0 to save a new marker")
	}
	// An incomplete OR overfull layout is refused before the transaction even opens: only
	// placed == total is a consumption norm (placed > total would store a "complete" marker
	// against a wrong denominator).
	if ins.PlacedCount != ins.TotalCount {
		return 0, fmt.Errorf("%w: %d of %d pieces placed", entity.ErrMarkerIncomplete, ins.PlacedCount, ins.TotalCount)
	}
	var savedID int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// Content write to card-owned data: a released card refuses, inside the tx like every
		// sibling guard so a concurrent release cannot slip past the SERIALIZABLE read.
		if err := storeutil.RequireMutableTechCard(ctx, db, techCardID); err != nil {
			return err
		}
		// Ownership FIRST for an addressed id: a marker of another card must read as gone
		// before any validation detail (bom keys, sizes) leaks a differential answer. Resolved
		// with a SELECT, not RowsAffected on the UPDATE below — the driver counts rows CHANGED
		// (no clientFoundRows in the DSN), and that UPDATE has no guaranteed-changing column
		// (the lock_version bump was deliberately dropped), so a byte-identical re-save would
		// report 0 rows and read as a phantom 404.
		if id > 0 {
			if _, err := storeutil.QueryNamedOne[struct {
				Id int64 `db:"id"`
			}](ctx, db, `SELECT id FROM tech_card_marker WHERE id = :id AND tech_card_id = :tech_card_id`,
				map[string]any{"id": id, "tech_card_id": techCardID}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: marker %d is not a раскладка of tech card %d",
						entity.ErrMarkerNotFound, id, techCardID)
				}
				return fmt.Errorf("resolve marker %d: %w", id, err)
			}
		}
		// The size must be in the card's range AT SAVE TIME. Like pattern rows, a marker may
		// outlive its size leaving the range later — it stays a valid measurement — but minting a
		// new one against a foreign size is always a client bug.
		if err := requireCardSize(ctx, db, techCardID, ins.SizeId); err != nil {
			return err
		}
		bomItemID := sql.NullInt64{}
		if key := strings.TrimSpace(ins.BomLineKey); key != "" {
			row, err := storeutil.QueryNamedOne[struct {
				Id int64 `db:"id"`
			}](ctx, db, `SELECT id FROM tech_card_bom_item WHERE tech_card_id = :card AND line_key = :key`,
				map[string]any{"card": techCardID, "key": key})
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return entity.NewFieldViolation("bom_line_key", "not_found", key,
						"pick a BOM fabric line of this card, or leave the marker unlinked")
				}
				return fmt.Errorf("resolve bom line %q of tech card %d: %w", key, techCardID, err)
			}
			bomItemID = sql.NullInt64{Int64: row.Id, Valid: true}
		}
		params := map[string]any{
			"id":                id,
			"tech_card_id":      techCardID,
			"size_id":           ins.SizeId,
			"bom_item_id":       bomItemID,
			"name":              ins.Name,
			"source":            string(ins.Source),
			"fabric_width_cm":   ins.FabricWidthCm,
			"gap_cm":            ins.GapCm,
			"edge_margin_cm":    ins.EdgeMarginCm,
			"selvedge_cm":       ins.SelvedgeCm,
			"allow_cross_grain": ins.AllowCrossGrain,
			"sets":              ins.Sets,
			"used_length_cm":    ins.UsedLengthCm,
			"efficiency_pct":    ins.EfficiencyPct,
			"placed_count":      ins.PlacedCount,
			"total_count":       ins.TotalCount,
			"layout":            ins.Layout,
			"username":          username,
		}
		if id > 0 {
			// The addressed row must already be a marker of THIS card — a foreign id is reported as
			// gone, not silently adopted. Ownership is resolved with a SELECT, like the
			// output-variant upsert, NOT via RowsAffected on the UPDATE: the driver counts rows
			// CHANGED (no clientFoundRows in the DSN), and this UPDATE has no guaranteed-changing
			// column (the lock_version bump was deliberately dropped) — a byte-identical re-save
			// would report 0 rows and read as a phantom 404.
			if _, err := storeutil.QueryNamedOne[struct {
				Id int64 `db:"id"`
			}](ctx, db, `SELECT id FROM tech_card_marker WHERE id = :id AND tech_card_id = :tech_card_id`,
				map[string]any{"id": id, "tech_card_id": techCardID}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: marker %d is not a раскладка of tech card %d",
						entity.ErrMarkerNotFound, id, techCardID)
				}
				return fmt.Errorf("resolve marker %d: %w", id, err)
			}
			if _, err := storeutil.ExecNamedRows(ctx, db, `
				UPDATE tech_card_marker
				SET size_id = :size_id, bom_item_id = :bom_item_id, name = :name, source = :source,
				    fabric_width_cm = :fabric_width_cm, gap_cm = :gap_cm,
				    edge_margin_cm = :edge_margin_cm, selvedge_cm = :selvedge_cm,
				    allow_cross_grain = :allow_cross_grain,
				    sets = :sets, used_length_cm = :used_length_cm, efficiency_pct = :efficiency_pct,
				    placed_count = :placed_count, total_count = :total_count, layout = :layout,
				    updated_by = :username
				WHERE id = :id AND tech_card_id = :tech_card_id`, params); err != nil {
				return fmt.Errorf("update marker %d: %w", id, err)
			}
			savedID = id
			return nil
		}
		newID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_marker
				(tech_card_id, size_id, bom_item_id, name, source, fabric_width_cm, gap_cm,
				 edge_margin_cm, selvedge_cm, allow_cross_grain, sets, used_length_cm, efficiency_pct,
				 placed_count, total_count, layout, created_by, updated_by)
			VALUES (:tech_card_id, :size_id, :bom_item_id, :name, :source, :fabric_width_cm, :gap_cm,
				 :edge_margin_cm, :selvedge_cm, :allow_cross_grain, :sets, :used_length_cm, :efficiency_pct,
				 :placed_count, :total_count, :layout, :username, :username)`, params)
		if err != nil {
			return fmt.Errorf("create marker on tech card %d: %w", techCardID, err)
		}
		savedID = newID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return savedID, nil
}

// requireCardSize verifies the size belongs to the card's current range with a readable refusal
// (the FK alone would let ANY dictionary size in — membership is a card fact, not a dictionary one).
func requireCardSize(ctx context.Context, db dependency.DB, techCardID, sizeID int) error {
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM tech_card_size WHERE tech_card_id = :card AND size_id = :size`,
		map[string]any{"card": techCardID, "size": sizeID})
	if err != nil {
		return fmt.Errorf("check size %d on tech card %d: %w", sizeID, techCardID, err)
	}
	if n == 0 {
		return entity.NewFieldViolation("size_id", "not_on_card", fmt.Sprintf("size %d", sizeID),
			"the marker's size must be one of the card's sizes")
	}
	return nil
}

// DeleteMarker removes a saved раскладка. Nothing references markers, so the delete is plain —
// but it is still card content, so a released card refuses.
func (s *Store) DeleteMarker(ctx context.Context, id int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		row, err := storeutil.QueryNamedOne[struct {
			TechCardId int `db:"tech_card_id"`
		}](ctx, db, `SELECT tech_card_id FROM tech_card_marker WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: marker %d", entity.ErrMarkerNotFound, id)
			}
			return fmt.Errorf("load marker %d: %w", id, err)
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, row.TechCardId); err != nil {
			return err
		}
		rows, err := storeutil.ExecNamedRows(ctx, db,
			`DELETE FROM tech_card_marker WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("delete marker %d: %w", id, err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: marker %d", entity.ErrMarkerNotFound, id)
		}
		return nil
	})
}
