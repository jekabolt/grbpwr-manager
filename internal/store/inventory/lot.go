package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// upsertLotOnReceipt opens (or tops up) a structured lot for a receipt that names a lot code (gap-07
// v2 D) and returns the lot id, so the receipt movement can reference it. Adding to an existing lot
// (same material + code) accumulates received_qty and remaining_qty; a first receipt creates it.
// The unit_cost is stored informationally — it is NOT a valuation basis (that stays moving-average).
func upsertLotOnReceipt(ctx context.Context, db dependency.DB, ins entity.MaterialReceiptInsert) (sql.NullInt32, error) {
	code := strings.TrimSpace(ins.Lot.String)
	if !ins.Lot.Valid || code == "" {
		return sql.NullInt32{}, nil
	}
	// A top-up also un-archives the lot: a fresh receipt under this code means the roll is back on
	// the shelf, and silently accumulating into a hidden (archived) lot would make the stock
	// invisible in the default lot list (g25-06).
	// measured_width_cm / shade_code (Ф5а.1) follow the same COALESCE(VALUES(x), x) rule as the rest
	// of the top-up fields: a later receipt into the same lot that leaves them blank must not erase
	// the measurement the first receipt recorded, while a receipt that DOES carry a new measurement
	// overwrites it — re-measuring a roll is a correction, not a second roll.
	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO material_lot
			(material_id, lot_code, supplier_doc, received_qty, remaining_qty, unit_cost, currency, received_at,
			 measured_width_cm, shade_code)
		VALUES (:material_id, :lot_code, :supplier_doc, :qty, :qty, :unit_cost, :currency, :received_at,
			 :measured_width_cm, :shade_code)
		ON DUPLICATE KEY UPDATE
			id                = LAST_INSERT_ID(id),
			received_qty      = received_qty + VALUES(received_qty),
			remaining_qty     = remaining_qty + VALUES(remaining_qty),
			supplier_doc      = COALESCE(VALUES(supplier_doc), supplier_doc),
			unit_cost         = COALESCE(VALUES(unit_cost), unit_cost),
			currency          = COALESCE(VALUES(currency), currency),
			received_at       = COALESCE(VALUES(received_at), received_at),
			measured_width_cm = COALESCE(VALUES(measured_width_cm), measured_width_cm),
			shade_code        = COALESCE(VALUES(shade_code), shade_code),
			archived          = FALSE`,
		map[string]any{
			"material_id":       ins.MaterialId,
			"lot_code":          code,
			"supplier_doc":      ins.SupplierDoc,
			"qty":               ins.Quantity.Round(qtyScale),
			"unit_cost":         nullDecimal(ins.UnitCost),
			"currency":          currencyNull(ins.Currency),
			"received_at":       ins.OccurredAt,
			"measured_width_cm": nullDecimal(ins.MeasuredWidthCm),
			"shade_code":        trimmedNull(ins.ShadeCode),
		})
	if err != nil {
		return sql.NullInt32{}, fmt.Errorf("open material lot %q: %w", code, err)
	}
	return sql.NullInt32{Int32: int32(id), Valid: true}, nil
}

// drawFromLot moves quantity off (issue) or back onto (return) a structured lot's remaining balance
// within the caller's transaction (gap-07 v2 D). It validates the lot belongs to the material and,
// for an issue, guards against drawing more than the lot has remaining.
func drawFromLot(ctx context.Context, db dependency.DB, lotID, materialID int, qty decimal.Decimal, isReturn bool) error {
	lot, err := storeutil.QueryNamedOne[struct {
		MaterialId int             `db:"material_id"`
		Received   decimal.Decimal `db:"received_qty"`
		Remaining  decimal.Decimal `db:"remaining_qty"`
	}](ctx, db, `SELECT material_id, received_qty, remaining_qty FROM material_lot WHERE id = :id FOR UPDATE`,
		map[string]any{"id": lotID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: lot %d", entity.ErrMaterialLotMismatch, lotID)
		}
		return fmt.Errorf("read material lot %d: %w", lotID, err)
	}
	if lot.MaterialId != materialID {
		return fmt.Errorf("%w: lot %d is material %d, not %d", entity.ErrMaterialLotMismatch, lotID, lot.MaterialId, materialID)
	}
	newRemaining := lot.Remaining.Sub(qty)
	if isReturn {
		newRemaining = lot.Remaining.Add(qty)
		// A lot can never hold more than was ever received into it — a return naming the wrong lot
		// would otherwise mint phantom roll stock (g25-05). The target-level outstanding cap has
		// already passed; this guards the per-lot ledger.
		if newRemaining.GreaterThan(lot.Received) {
			return fmt.Errorf("%w: lot %d has %s remaining of %s received", entity.ErrExcessiveMaterialReturn, lotID, lot.Remaining.String(), lot.Received.String())
		}
	} else if lot.Remaining.LessThan(qty) {
		return fmt.Errorf("%w: lot %d has %s remaining", entity.ErrInsufficientMaterialLot, lotID, lot.Remaining.String())
	}
	if err := storeutil.ExecNamed(ctx, db,
		`UPDATE material_lot SET remaining_qty = :r WHERE id = :id`,
		map[string]any{"r": newRemaining.Round(qtyScale), "id": lotID}); err != nil {
		return fmt.Errorf("update material lot %d remaining: %w", lotID, err)
	}
	return nil
}

// ListMaterialLots returns a material's lots (roll / dye-lot registry), active-only unless
// includeArchived, most-recently-received first.
func (s *Store) ListMaterialLots(ctx context.Context, materialID int, includeArchived bool) ([]entity.MaterialLot, error) {
	where := "WHERE material_id = :m AND archived = FALSE"
	if includeArchived {
		where = "WHERE material_id = :m"
	}
	rows, err := storeutil.QueryListNamed[entity.MaterialLot](ctx, s.DB, `
		SELECT id, material_id, lot_code, supplier_doc, received_qty, remaining_qty, unit_cost,
		       currency, received_at, note, archived, measured_width_cm, shade_code
		FROM material_lot `+where+`
		ORDER BY received_at DESC, id DESC`, map[string]any{"m": materialID})
	if err != nil {
		return nil, fmt.Errorf("list material lots: %w", err)
	}
	return rows, nil
}

// NarrowestMeasuredLotWidths returns, per material, the NARROWEST measured roll width among the lots
// that still have stock (Ф5а.1 / Ф6.2). Materials with no measured, non-empty, non-archived lot are
// simply absent from the map — «nobody measured it» is not «it matches the nominal», and an absent
// key is what makes the readiness gate fall back to the catalogue width instead of inventing one.
//
// THE NARROWEST WINS because that is the width the shop has to lay on: a marker made for 150 cm does
// not fit the 148 cm roll sitting next to it, and the batch is cut from whatever arrived. The same
// rule by which the workshop makes the marker in the first place.
//
// The number is the FULL measured roll width, кромка included — deliberately NOT the cutting width.
// The subtraction belongs to the comparison rule (entity.NormWidthVsArticle), which needs the raw
// figure to be able to SAY «measured 148, less 1 cm of кромка each side = 146 of cutting width»;
// doing it here would hide the arithmetic from the one message that has to show it.
//
// Batched over the whole article union of a card, because the alternative is one query per slot per
// colourway inside a modal.
func (s *Store) NarrowestMeasuredLotWidths(ctx context.Context, materialIDs []int) (map[int]decimal.NullDecimal, error) {
	out := make(map[int]decimal.NullDecimal, len(materialIDs))
	if len(materialIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		MaterialId int             `db:"material_id"`
		WidthCm    decimal.Decimal `db:"width_cm"`
	}](ctx, s.DB, `
		SELECT material_id, MIN(measured_width_cm) AS width_cm
		FROM material_lot
		WHERE material_id IN (:ids)
		  AND archived = FALSE
		  AND measured_width_cm IS NOT NULL
		  AND remaining_qty > 0
		GROUP BY material_id`, map[string]any{"ids": materialIDs})
	if err != nil {
		return nil, fmt.Errorf("load narrowest measured lot widths: %w", err)
	}
	for _, r := range rows {
		out[r.MaterialId] = decimal.NullDecimal{Decimal: r.WidthCm, Valid: true}
	}
	return out, nil
}

// trimmedNull turns a (possibly blank) NullString into one that is NULL when it holds only
// whitespace — so a receipt sending an empty shade_code stores "unrecorded", not an empty string
// that the top-up COALESCE would then treat as a real value and refuse to fill in later.
func trimmedNull(s sql.NullString) sql.NullString {
	v := strings.TrimSpace(s.String)
	if !s.Valid || v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

// currencyNull turns a (possibly empty) currency string into an upper-cased NullString (empty → NULL).
func currencyNull(c string) sql.NullString {
	c = strings.ToUpper(strings.TrimSpace(c))
	if c == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: c, Valid: true}
}
