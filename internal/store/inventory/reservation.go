package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// This file implements packaging configuration resolution and the packaging reservation ledger
// (PLM rework Q3 / S22, 01-DOMAIN-MODEL §2.8):
//   reserve at order placement → consume at ship → release at cancel/refund
// The ledger NEVER moves on_hand — the physical decrement stays the ship-time material_stock_movement
// writeoff (reason=packaging); the ledger only tracks whether a claim is still open, giving a soft
//   available(material) = on_hand − Σ qty of OPEN claims.

// recipeRow is one packaging_recipe line used during resolution.
type recipeRow struct {
	MaterialId  int             `db:"material_id"`
	QtyPerOrder decimal.Decimal `db:"qty_per_order"`
	QtyPerItem  decimal.Decimal `db:"qty_per_item"`
}

// orderPackagingLine is one order line aggregated for packaging resolution: the colourway, its owning
// style, and the total units of that colourway in the order.
type orderPackagingLine struct {
	ProductId  int             `db:"product_id"`
	TechCardId sql.NullInt32   `db:"tech_card_id"`
	Qty        decimal.Decimal `db:"qty"`
}

// resolvedRecipe is the recipe that won resolution for one line, plus a stable identity key used to
// count qty_per_order (the box) only once per distinct recipe present on the order.
type resolvedRecipe struct {
	Key  string
	Rows []recipeRow
}

// decRow scans a single decimal aggregate.
type decRow struct {
	V decimal.Decimal `db:"v"`
}

// aggregatePackaging is the pure resolution math (unit-testable without a DB): for every order line,
// add qty_per_item × line units for each material in the line's resolved recipe; add qty_per_order
// once per DISTINCT resolved recipe (a box per recipe present on the order). In the common all-global
// case this reduces to exactly the legacy flat behaviour: one box + Σ per-item × total units.
func aggregatePackaging(lines []orderPackagingLine, resolve func(orderPackagingLine) (resolvedRecipe, error)) (map[int]decimal.Decimal, error) {
	req := map[int]decimal.Decimal{}
	seen := map[string]struct{}{}
	for _, ln := range lines {
		rr, err := resolve(ln)
		if err != nil {
			return nil, err
		}
		_, boxDone := seen[rr.Key]
		for _, r := range rr.Rows {
			if r.QtyPerItem.IsPositive() {
				req[r.MaterialId] = req[r.MaterialId].Add(r.QtyPerItem.Mul(ln.Qty))
			}
			if !boxDone && r.QtyPerOrder.IsPositive() {
				req[r.MaterialId] = req[r.MaterialId].Add(r.QtyPerOrder)
			}
		}
		seen[rr.Key] = struct{}{}
	}
	for m, q := range req {
		if q.LessThanOrEqual(decimal.Zero) {
			delete(req, m)
		}
	}
	return req, nil
}

// resolvePackagingRequirement computes, per material, the total packaging quantity an order needs. It
// reads the order's colourway lines and resolves each line's recipe most-specific-first (product →
// style → global; the first scope with any active row wins entirely), then applies aggregatePackaging.
func resolvePackagingRequirement(ctx context.Context, db dependency.DB, orderID int) (map[int]decimal.Decimal, error) {
	lines, err := storeutil.QueryListNamed[orderPackagingLine](ctx, db, `
		SELECT oi.product_id AS product_id, p.style_id AS tech_card_id, SUM(oi.quantity) AS qty
		FROM order_item oi JOIN product p ON p.id = oi.product_id
		WHERE oi.order_id = :order_id
		GROUP BY oi.product_id, p.style_id`, map[string]any{"order_id": orderID})
	if err != nil {
		return nil, fmt.Errorf("read order %d packaging lines: %w", orderID, err)
	}
	return aggregatePackaging(lines, func(ln orderPackagingLine) (resolvedRecipe, error) {
		return resolveRecipeRows(ctx, db, ln.ProductId, ln.TechCardId)
	})
}

// resolveConsumeRequirement determines what a ship should consume, in priority order:
//  1. the order's OPEN reservation claims — consume exactly what placement reserved (drift-proof: a
//     recipe change after placement can't change what this order is billed);
//  2. a fresh resolution from the order's lines — an unreserved real order (reserve failed / predates
//     the ledger) still gets its per-product/style packaging;
//  3. the flat global recipe × itemCount — an order with no persisted lines (a synthetic/legacy order),
//     preserving the pre-ledger behaviour.
func resolveConsumeRequirement(ctx context.Context, db dependency.DB, orderID, itemCount int) (map[int]decimal.Decimal, error) {
	claims, err := openClaimsForOrder(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	if len(claims) > 0 {
		req := make(map[int]decimal.Decimal, len(claims))
		for _, c := range claims {
			req[c.MaterialId] = req[c.MaterialId].Add(c.Qty) // one open claim per material (claim_key = order:material)
		}
		return req, nil
	}
	req, err := resolvePackagingRequirement(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	if len(req) > 0 {
		return req, nil
	}
	return globalRequirementByItemCount(ctx, db, itemCount)
}

// globalRequirementByItemCount is the legacy flat computation over the global recipe: qty_per_order
// once plus qty_per_item × itemCount. Used only as the last-resort fallback for an order with no
// resolvable lines and no reservation.
func globalRequirementByItemCount(ctx context.Context, db dependency.DB, itemCount int) (map[int]decimal.Decimal, error) {
	rows, err := queryRecipeRows(ctx, db, "scope = 'global'", map[string]any{})
	if err != nil {
		return nil, err
	}
	req := map[int]decimal.Decimal{}
	ic := decimal.NewFromInt(int64(itemCount))
	for _, r := range rows {
		q := r.QtyPerOrder.Add(r.QtyPerItem.Mul(ic))
		if q.IsPositive() {
			req[r.MaterialId] = req[r.MaterialId].Add(q)
		}
	}
	return req, nil
}

// resolveRecipeRows returns the active recipe rows that win for one colourway, product → style →
// global, together with a stable identity key for box-once de-duplication.
func resolveRecipeRows(ctx context.Context, db dependency.DB, productID int, techCardID sql.NullInt32) (resolvedRecipe, error) {
	rows, err := queryRecipeRows(ctx, db, "scope = 'product' AND product_id = :id", map[string]any{"id": productID})
	if err != nil {
		return resolvedRecipe{}, err
	}
	if len(rows) > 0 {
		return resolvedRecipe{Key: fmt.Sprintf("product:%d", productID), Rows: rows}, nil
	}
	if techCardID.Valid {
		rows, err = queryRecipeRows(ctx, db, "scope = 'style' AND tech_card_id = :id", map[string]any{"id": techCardID.Int32})
		if err != nil {
			return resolvedRecipe{}, err
		}
		if len(rows) > 0 {
			return resolvedRecipe{Key: fmt.Sprintf("style:%d", techCardID.Int32), Rows: rows}, nil
		}
	}
	rows, err = queryRecipeRows(ctx, db, "scope = 'global'", map[string]any{})
	if err != nil {
		return resolvedRecipe{}, err
	}
	return resolvedRecipe{Key: "global", Rows: rows}, nil
}

func queryRecipeRows(ctx context.Context, db dependency.DB, cond string, params map[string]any) ([]recipeRow, error) {
	rows, err := storeutil.QueryListNamed[recipeRow](ctx, db, fmt.Sprintf(`
		SELECT material_id, qty_per_order, qty_per_item FROM packaging_recipe
		WHERE active = TRUE AND %s`, cond), params)
	if err != nil {
		return nil, fmt.Errorf("resolve packaging recipe: %w", err)
	}
	return rows, nil
}

// openReservedQty returns Σ qty of a material's OPEN reservation claims — a 'reserve' row with no
// matching 'consume'/'release'. This is the soft hold: available = on_hand − openReservedQty.
func openReservedQty(ctx context.Context, db dependency.DB, materialID int) (decimal.Decimal, error) {
	v, err := storeutil.QueryNamedOne[decRow](ctx, db, `
		SELECT COALESCE(SUM(r.qty), 0) AS v FROM material_reservation_ledger r
		WHERE r.material_id = :m AND r.event = 'reserve'
		  AND NOT EXISTS (SELECT 1 FROM material_reservation_ledger x
		                  WHERE x.claim_key = r.claim_key AND x.event IN ('consume', 'release'))`,
		map[string]any{"m": materialID})
	if err != nil {
		return decimal.Zero, fmt.Errorf("read open reserved qty for material %d: %w", materialID, err)
	}
	return v.V, nil
}

// insertReservationEvent appends a reservation-ledger row idempotently: a repeat of the same
// (claim_key, event) is ignored (UNIQUE guard), so retries are no-ops. Reports whether a row was
// actually written.
func insertReservationEvent(ctx context.Context, db dependency.DB, materialID, orderID int, qty decimal.Decimal, event entity.MaterialReservationEvent, claimKey, username string) (bool, error) {
	n, err := storeutil.ExecNamedRows(ctx, db, `
		INSERT IGNORE INTO material_reservation_ledger (material_id, order_id, qty, event, claim_key, created_by)
		VALUES (:material_id, :order_id, :qty, :event, :claim_key, :created_by)`,
		map[string]any{
			"material_id": materialID,
			"order_id":    orderID,
			"qty":         qty.Round(qtyScale),
			"event":       string(event),
			"claim_key":   claimKey,
			"created_by":  username,
		})
	if err != nil {
		return false, fmt.Errorf("insert reservation %s for order %d material %d: %w", event, orderID, materialID, err)
	}
	return n > 0, nil
}

// openClaimsForOrder returns an order's currently OPEN reservation claims.
func openClaimsForOrder(ctx context.Context, db dependency.DB, orderID int) ([]entity.MaterialReservation, error) {
	rows, err := storeutil.QueryListNamed[entity.MaterialReservation](ctx, db, `
		SELECT r.material_id, r.order_id, r.qty, r.claim_key
		FROM material_reservation_ledger r
		WHERE r.order_id = :order_id AND r.event = 'reserve'
		  AND NOT EXISTS (SELECT 1 FROM material_reservation_ledger x
		                  WHERE x.claim_key = r.claim_key AND x.event IN ('consume', 'release'))`,
		map[string]any{"order_id": orderID})
	if err != nil {
		return nil, fmt.Errorf("read open reservations for order %d: %w", orderID, err)
	}
	return rows, nil
}

// releaseOpenClaimsInTx closes every still-open claim of an order with a 'release' row (no physical
// writeoff). Shared by ReleasePackagingForOrder (cancel/refund) and by the consume tail, which
// releases any claim the ship-time recipe no longer covers so a recipe change can't leak an open
// claim that would depress available forever.
func releaseOpenClaimsInTx(ctx context.Context, db dependency.DB, orderID int, username string) error {
	open, err := openClaimsForOrder(ctx, db, orderID)
	if err != nil {
		return err
	}
	for _, c := range open {
		if _, err := insertReservationEvent(ctx, db, c.MaterialId, orderID, c.Qty, entity.MaterialReservationRelease, c.ClaimKey, username); err != nil {
			return err
		}
	}
	return nil
}

// ReservePackagingForOrder soft-reserves the packaging an order needs at placement time (S22): it
// resolves the per-material requirement (product → style → global) and appends a 'reserve' claim per
// material, idempotently. It never blocks — a sale must not fail on packaging; an oversell is
// surfaced later via available, not refused here.
func (s *Store) ReservePackagingForOrder(ctx context.Context, orderID int, username string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		req, err := resolvePackagingRequirement(ctx, db, orderID)
		if err != nil {
			return err
		}
		for materialID, qty := range req {
			if qty.LessThanOrEqual(decimal.Zero) {
				continue
			}
			if _, err := insertReservationEvent(ctx, db, materialID, orderID, qty,
				entity.MaterialReservationReserve, entity.PackagingClaimKey(orderID, materialID), username); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReleasePackagingForOrder closes every open packaging claim of an order (cancel/refund) with a
// 'release' row — the soft hold is returned without any physical writeoff. Idempotent and a no-op for
// an order with no open claims.
func (s *Store) ReleasePackagingForOrder(ctx context.Context, orderID int, username string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		return releaseOpenClaimsInTx(ctx, rep.DB(), orderID, username)
	})
}

// MaterialAvailable returns a material's physical on-hand, its open-reserved quantity, and the soft
// available = on_hand − reserved (which may be negative when packaging is oversold).
func (s *Store) MaterialAvailable(ctx context.Context, materialID int) (entity.MaterialAvailability, error) {
	st, err := s.GetMaterialStock(ctx, materialID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.MaterialAvailability{}, fmt.Errorf("%w: material %d", entity.ErrMaterialNotFound, materialID)
		}
		return entity.MaterialAvailability{}, err
	}
	reserved, err := openReservedQty(ctx, s.DB, materialID)
	if err != nil {
		return entity.MaterialAvailability{}, err
	}
	return entity.MaterialAvailability{
		MaterialId: materialID,
		OnHand:     st.OnHand,
		Reserved:   reserved,
		Available:  st.OnHand.Sub(reserved),
	}, nil
}

// scopePredicate returns the WHERE fragment + params identifying one scope target for a full-replace.
func scopePredicate(scope entity.PackagingRecipeScope, techCardID, productID sql.NullInt32) (string, map[string]any, error) {
	switch scope {
	case entity.PackagingScopeGlobal:
		if techCardID.Valid || productID.Valid {
			return "", nil, fmt.Errorf("%w: global scope takes no target", entity.ErrPackagingRecipeInvalid)
		}
		return "scope = 'global'", map[string]any{}, nil
	case entity.PackagingScopeStyle:
		if !techCardID.Valid || productID.Valid {
			return "", nil, fmt.Errorf("%w: style scope needs exactly a tech_card_id", entity.ErrPackagingRecipeInvalid)
		}
		return "scope = 'style' AND tech_card_id = :tc", map[string]any{"tc": techCardID.Int32}, nil
	case entity.PackagingScopeProduct:
		if !productID.Valid || techCardID.Valid {
			return "", nil, fmt.Errorf("%w: product scope needs exactly a product_id", entity.ErrPackagingRecipeInvalid)
		}
		return "scope = 'product' AND product_id = :pid", map[string]any{"pid": productID.Int32}, nil
	default:
		return "", nil, fmt.Errorf("%w: unknown scope %q", entity.ErrPackagingRecipeInvalid, scope)
	}
}

// ListPackagingRecipe returns every packaging recipe row (all scopes) joined with material name/unit,
// ordered by scope then material name for display.
func (s *Store) ListPackagingRecipe(ctx context.Context) ([]entity.PackagingRecipe, error) {
	rows, err := storeutil.QueryListNamed[entity.PackagingRecipe](ctx, s.DB, `
		SELECT pr.id, pr.scope, pr.tech_card_id, pr.product_id, pr.material_id,
		       m.name AS material_name, m.unit AS material_unit,
		       pr.qty_per_order, pr.qty_per_item, pr.active, pr.lock_version, pr.created_by, pr.updated_by
		FROM packaging_recipe pr JOIN material m ON m.id = pr.material_id
		ORDER BY pr.scope, m.name, pr.id`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list packaging recipe: %w", err)
	}
	return rows, nil
}

// UpsertPackagingRecipe full-replaces the recipe lines of ONE scope target (the whole global set, or
// one style's set, or one product's set) in a single transaction — editing product A's recipe never
// touches global or product B (mirrors UpsertPackagingBom's full-replace, but scoped). An empty items
// slice clears that target's recipe.
func (s *Store) UpsertPackagingRecipe(ctx context.Context, scope entity.PackagingRecipeScope, techCardID, productID sql.NullInt32, items []entity.PackagingRecipeInsert, username string) error {
	pred, params, err := scopePredicate(scope, techCardID, productID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.MaterialId <= 0 {
			return fmt.Errorf("%w: a recipe line needs a material_id", entity.ErrPackagingRecipeInvalid)
		}
		if it.QtyPerOrder.IsNegative() || it.QtyPerItem.IsNegative() {
			return fmt.Errorf("%w: recipe quantities must be >= 0", entity.ErrPackagingRecipeInvalid)
		}
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := storeutil.ExecNamed(ctx, db,
			fmt.Sprintf(`DELETE FROM packaging_recipe WHERE %s`, pred), params); err != nil {
			return fmt.Errorf("clear packaging recipe: %w", err)
		}
		for _, it := range items {
			row := map[string]any{
				"scope":         string(scope),
				"tech_card_id":  techCardID,
				"product_id":    productID,
				"material_id":   it.MaterialId,
				"qty_per_order": it.QtyPerOrder,
				"qty_per_item":  it.QtyPerItem,
				"active":        it.Active,
				"created_by":    username,
				"updated_by":    username,
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO packaging_recipe
					(scope, tech_card_id, product_id, material_id, qty_per_order, qty_per_item, active, created_by, updated_by)
				VALUES
					(:scope, :tech_card_id, :product_id, :material_id, :qty_per_order, :qty_per_item, :active, :created_by, :updated_by)`,
				row); err != nil {
				return fmt.Errorf("insert packaging recipe material %d: %w", it.MaterialId, err)
			}
		}
		return nil
	})
}
