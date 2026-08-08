package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

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
	req, err := claimedRequirement(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	if len(req) > 0 {
		return req, nil
	}
	req, err = resolvePackagingRequirement(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	if len(req) > 0 {
		return req, nil
	}
	return globalRequirementByItemCount(ctx, db, itemCount)
}

// claimedRequirement folds an order's OPEN reservation claims into a per-material requirement — what
// placement actually reserved, immune to a later recipe edit. There is exactly one open claim per
// material (claim_key = order:material), so the fold is a straight copy. nil when nothing is claimed.
// Shared by the ship-time consume and by the packer's spec so the two can never tell a different story
// about the same order.
func claimedRequirement(ctx context.Context, db dependency.DB, orderID int) (map[int]decimal.Decimal, error) {
	claims, err := openClaimsForOrder(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	if len(claims) == 0 {
		return nil, nil
	}
	req := make(map[int]decimal.Decimal, len(claims))
	for _, c := range claims {
		req[c.MaterialId] = req[c.MaterialId].Add(c.Qty)
	}
	return req, nil
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

// reservationOwner names the ONE owner of a ledger claim. Since 0286 the ledger has two kinds:
// a customer order holding packaging, and a production run holding fabric. The DB CHECK
// chk_material_reservation_owner_xor admits exactly one of the two, so this struct is the Go-side
// shape of that invariant — every write goes through a constructor that can only produce a legal
// owner, instead of two loose ints a caller could fill both of (or neither).
type reservationOwner struct {
	OrderId sql.NullInt32
	RunId   sql.NullInt32
	// LotId pins a run's claim to one lot (Ф5б.6, recut out of the same dye lot). It is not an
	// owner — the claim still belongs to the run — so it is carried alongside, never instead.
	LotId sql.NullInt32
}

// orderOwner is the packaging claim's owner: a customer order (0164, S22).
func orderOwner(orderID int) reservationOwner {
	return reservationOwner{OrderId: sql.NullInt32{Int32: int32(orderID), Valid: true}}
}

// runOwner is the fabric claim's owner: a production run, optionally pinned to a lot (Ф5б.4/Ф5б.6).
func runOwner(runID int, lotID sql.NullInt32) reservationOwner {
	return reservationOwner{RunId: sql.NullInt32{Int32: int32(runID), Valid: true}, LotId: lotID}
}

// describe names the owner for an error message; the ledger's two owners read differently in a log.
func (o reservationOwner) describe() string {
	switch {
	case o.OrderId.Valid:
		return fmt.Sprintf("order %d", o.OrderId.Int32)
	case o.RunId.Valid:
		return fmt.Sprintf("run %d", o.RunId.Int32)
	default:
		return "unowned claim"
	}
}

// insertReservationEvent appends a reservation-ledger row idempotently: a repeat of the same
// (claim_key, event) is ignored (UNIQUE guard), so retries are no-ops. Reports whether a row was
// actually written — false means the row was already there, which is idempotency, not failure
// (RowsAffected counts CHANGED rows; INSERT IGNORE on a duplicate changes none).
func insertReservationEvent(ctx context.Context, db dependency.DB, materialID int, owner reservationOwner, qty decimal.Decimal, event entity.MaterialReservationEvent, claimKey, username string) (bool, error) {
	n, err := storeutil.ExecNamedRows(ctx, db, `
		INSERT IGNORE INTO material_reservation_ledger (material_id, order_id, run_id, lot_id, qty, event, claim_key, created_by)
		VALUES (:material_id, :order_id, :run_id, :lot_id, :qty, :event, :claim_key, :created_by)`,
		map[string]any{
			"material_id": materialID,
			"order_id":    owner.OrderId,
			"run_id":      owner.RunId,
			"lot_id":      owner.LotId,
			"qty":         qty.Round(qtyScale),
			"event":       string(event),
			"claim_key":   claimKey,
			"created_by":  username,
		})
	if err != nil {
		return false, fmt.Errorf("insert reservation %s for %s material %d: %w", event, owner.describe(), materialID, err)
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

// ReleaseOpenClaimsInTx closes every still-open claim of an order with a 'release' row (no physical
// writeoff). Shared by ReleasePackagingForOrder (cancel/refund) and by the consume tail, which
// releases any claim the ship-time recipe no longer covers so a recipe change can't leak an open
// claim that would depress available forever. Exported (L2 fix, review 04-MAZE-FLYOVER/review-plm-
// backend.md): store/order's cancelOrder choke point calls this directly instead of duplicating the
// open-claim SQL — the two definitions had to "stay in sync" by convention; now there is one.
// Plain statement on the caller's transaction (db), no nested tx — same shape order's own helper had.
func ReleaseOpenClaimsInTx(ctx context.Context, db dependency.DB, orderID int, username string) error {
	open, err := openClaimsForOrder(ctx, db, orderID)
	if err != nil {
		return err
	}
	for _, c := range open {
		if _, err := insertReservationEvent(ctx, db, c.MaterialId, orderOwner(orderID), c.Qty, entity.MaterialReservationRelease, c.ClaimKey, username); err != nil {
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
			if _, err := insertReservationEvent(ctx, db, materialID, orderOwner(orderID), qty,
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
		return ReleaseOpenClaimsInTx(ctx, rep.DB(), orderID, username)
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

// ResolveOrderPackaging returns, for the packer/QC packing spec (WS7 scope 3), the packaging materials an
// order needs, joined with material name/unit and ordered by material id. It follows the SAME precedence
// as the ship-time consume (resolveConsumeRequirement): the order's OPEN reservation claims win, and only
// an order with no claim left is resolved fresh (product → style → global). Otherwise an order reserved
// under recipe v1 whose recipe then changed would tell the packer to put in the new box while the ledger
// consumes the old one — physical and accounting reality diverging with no error anywhere.
// READ-ONLY: it neither reserves nor consumes anything (WS2 owns the reservation ledger), so it can never
// move on_hand or cross the sales/warehouse streams.
func (s *Store) ResolveOrderPackaging(ctx context.Context, orderID int) ([]entity.OrderPackingSpecPackaging, error) {
	req, err := claimedRequirement(ctx, s.DB, orderID)
	if err != nil {
		return nil, err
	}
	if len(req) == 0 {
		if req, err = resolvePackagingRequirement(ctx, s.DB, orderID); err != nil {
			return nil, err
		}
	}
	if len(req) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(req))
	for id := range req {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	nameRows, err := storeutil.QueryListNamed[struct {
		Id   int            `db:"id"`
		Name string         `db:"name"`
		Unit sql.NullString `db:"unit"`
	}](ctx, s.DB, `SELECT id, name, unit FROM material WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("load packaging material names: %w", err)
	}
	nameByID := make(map[int]struct {
		name string
		unit sql.NullString
	}, len(nameRows))
	for _, n := range nameRows {
		nameByID[n.Id] = struct {
			name string
			unit sql.NullString
		}{n.Name, n.Unit}
	}
	out := make([]entity.OrderPackingSpecPackaging, 0, len(ids))
	for _, id := range ids {
		info := nameByID[id]
		out = append(out, entity.OrderPackingSpecPackaging{
			MaterialId:   id,
			MaterialName: info.name,
			MaterialUnit: info.unit,
			Qty:          req[id],
		})
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────────────────────────
// Ф5б.4 + Ф5б.6 — FABRIC HELD BY A PRODUCTION RUN.
//
// The ledger's second owner. Everything below writes run-owned claims into the SAME table the
// packaging claims live in, and that is the whole point: available(material) = on_hand − Σ open
// claims has to be ONE number over BOTH owners. openReservedQty above sums by material_id without
// asking who owns the claim, so it started counting fabric holds the moment 0286 landed — it was not
// edited, and it must not be. Needing to edit it would have meant the shared-table decision was
// wrong and a second table with an honest union was required instead.
//
// Three writes make up the life of a fabric hold, and the missing one is always the expensive one:
//
//	run created                → SetRunMaterialReservationsInTx (hold the NORM requirement)
//	lays displace the norm     → SetRunMaterialReservationsInTx (same call, new numbers)
//	material issued to the run → ConsumeRunReservationInTx      (the hold becomes a physical decrement)
//	run closed OR cancelled    → ReleaseRunReservationsInTx     (the hold goes back)
//	run deleted                → FK ON DELETE CASCADE (0286)    (no code, no chance to forget)
//
// A skipped release is not a cosmetic miss: an abandoned run holds its cloth forever, and every
// later run reads a shortage that does not physically exist.
// ─────────────────────────────────────────────────────────────────────────────────────────────────

// openRunClaims returns a run's currently OPEN claims — one per material by construction of the
// claim key, ordered by material for a deterministic write order.
func openRunClaims(ctx context.Context, db dependency.DB, runID int) ([]entity.MaterialReservationClaim, error) {
	rows, err := storeutil.QueryListNamed[entity.MaterialReservationClaim](ctx, db, `
		SELECT r.id, r.material_id, r.order_id, r.run_id, r.lot_id, r.qty, r.claim_key, r.created_by, r.created_at
		FROM material_reservation_ledger r
		WHERE r.run_id = :run_id AND r.event = 'reserve'
		  AND NOT EXISTS (SELECT 1 FROM material_reservation_ledger x
		                  WHERE x.claim_key = r.claim_key AND x.event IN ('consume', 'release'))
		ORDER BY r.material_id`,
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("read open reservations for run %d: %w", runID, err)
	}
	return rows, nil
}

// nextRunGeneration returns the first unused generation for a (run, material) claim slot.
//
// It scans EVERY 'reserve' row of that slot, open or already closed, not just the open one: a
// generation that was released is spent, and reusing its key would hit UNIQUE(claim_key, event) and
// vanish into the INSERT IGNORE — a reserve that reports success and holds nothing. Keys that are
// not run keys of the current shape are skipped rather than guessed at (ParseRunReservationGeneration).
// lockRunForReservation takes the run's row so that reservation reconciles for ONE run serialise.
//
// Строка прогона, а не строки реестра: удерживать надо ВЫБОР ПОКОЛЕНИЯ, а он делается до того, как
// появится строка, которую можно было бы заблокировать. Прогон — единственный объект, существующий
// на всём протяжении пересчёта.
//
// Отсутствие прогона — НЕ ошибка. Прогон могли удалить между коммитом основной записи и этим
// вызовом; его претензии унесло каскадом (fk_material_reservation_run ON DELETE CASCADE), удерживать
// больше нечего, и пересчёт по пустому набору — честный ответ, а не отказ.
func lockRunForReservation(ctx context.Context, db dependency.DB, runID int) error {
	_, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, db, `SELECT id FROM production_run WHERE id = :id FOR UPDATE`, map[string]any{"id": runID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock production run %d for reservation: %w", runID, err)
	}
	return nil
}

func nextRunGeneration(ctx context.Context, db dependency.DB, runID, materialID int) (int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ClaimKey string `db:"claim_key"`
	}](ctx, db, `
		SELECT claim_key FROM material_reservation_ledger
		WHERE run_id = :run_id AND material_id = :material_id AND event = 'reserve'`,
		map[string]any{"run_id": runID, "material_id": materialID})
	if err != nil {
		return 0, fmt.Errorf("read reservation generations for run %d material %d: %w", runID, materialID, err)
	}
	next := 0
	for _, r := range rows {
		gen, ok := entity.ParseRunReservationGeneration(r.ClaimKey)
		if ok && gen >= next {
			next = gen + 1
		}
	}
	return next, nil
}

// nullInt32Equal compares two optional ids, treating "both unset" as equal.
func nullInt32Equal(a, b sql.NullInt32) bool {
	if a.Valid != b.Valid {
		return false
	}
	return !a.Valid || a.Int32 == b.Int32
}

// SetRunMaterialReservationsInTx makes a run's open fabric claims equal `required` — the run holds
// exactly these materials in exactly these quantities afterwards, and nothing else.
//
// It is ONE function for two moments of §4.1 because they are the same operation with different
// numbers: the hold placed when the run is created (requirement from the NORM) and the correction
// when the lays displace that norm (requirement from the lays). A caller that recomputes the plan
// and calls this again is always correct, whatever the run held before.
//
// A correction is release + reserve of the NEXT generation, never an UPDATE: the ledger is
// append-only and UNIQUE(claim_key, event) forbids a second 'reserve' on one key. So the old claim
// is closed with a 'release' row and the new quantity opens under a fresh key. available therefore
// moves by exactly the difference — the old hold is fully returned before the new one is taken.
//
// Idempotent: a material already held at the same quantity and lot is left completely alone, so
// re-running the same plan writes nothing at all (not even a release/reserve pair that would churn
// generations and make the ledger unreadable).
//
// `required` is the run's OUTSTANDING need, not its gross requirement. Anything already issued to
// the run has left on_hand physically; holding it a second time in the ledger would count it twice
// and manufacture a shortage. The caller subtracts what is issued — see the note on
// SetRunMaterialReservations for where the two numbers come from.
//
// Runs on the caller's transaction so the hold commits or rolls back with whatever moved it.
func SetRunMaterialReservationsInTx(ctx context.Context, db dependency.DB, runID int, required map[int]entity.RunMaterialRequirement, username string) error {
	if runID <= 0 {
		return fmt.Errorf("run reservation needs a positive run id, got %d", runID)
	}
	// ПРОГОН БЕРЁТСЯ ПОД ЗАМОК ПЕРВЫМ, И БЕЗ ЭТОГО УДЕРЖАНИЕ МОЖЕТ ИСЧЕЗНУТЬ МОЛЧА.
	//
	// Пересчёт — это read-modify-write: прочитать открытые претензии, снять их, выписать новые под
	// СЛЕДУЮЩИМ поколением. Поколение выбирается по уже записанным ключам, а реестр append-only с
	// UNIQUE(claim_key, event) и вставкой через INSERT IGNORE. Два пересчёта одного прогона внахлёст
	// (сохранение двух настилов подряд, создание прогона рядом с правкой) читают одно и то же
	// «следующее» поколение; первый его занимает, второй молча схлопывается в IGNORE — а release
	// СВОЕЙ старой претензии он к этому моменту уже выписал. Итог: претензия снята, новая не
	// записана, ткань не удержана, и никто об этом не узнал.
	//
	// Замок на строке прогона сериализует пересчёты по прогону и делает выбор поколения безопасным.
	// Берётся ДО первого чтения реестра: замок после чтения не защищает ничего — он бы пришёл, когда
	// устаревшее решение уже принято. Вызовы приходят из apisrv УЖЕ ПОСЛЕ коммита основной записи,
	// поэтому замка сохранения настила здесь нет и рассчитывать на него нельзя.
	if err := lockRunForReservation(ctx, db, runID); err != nil {
		return err
	}
	open, err := openRunClaims(ctx, db, runID)
	if err != nil {
		return err
	}
	openByMaterial := make(map[int]entity.MaterialReservationClaim, len(open))
	for _, c := range open {
		openByMaterial[c.MaterialId] = c
	}
	// The union of "held now" and "needed now": a material that dropped out of the requirement has
	// to be RELEASED, and iterating only over `required` is precisely how that hold gets orphaned.
	materials := make([]int, 0, len(open)+len(required))
	for id := range openByMaterial {
		materials = append(materials, id)
	}
	for id := range required {
		if _, held := openByMaterial[id]; !held {
			materials = append(materials, id)
		}
	}
	sort.Ints(materials)

	for _, materialID := range materials {
		want := required[materialID] // zero value (no hold) when the material dropped out
		wantQty := want.Qty.Round(qtyScale)
		cur, held := openByMaterial[materialID]
		if held && wantQty.Equal(cur.Qty) && nullInt32Equal(cur.LotId, want.LotId) {
			continue
		}
		if held {
			if _, err := insertReservationEvent(ctx, db, materialID, runOwner(runID, cur.LotId), cur.Qty,
				entity.MaterialReservationRelease, cur.ClaimKey, username); err != nil {
				return err
			}
		}
		if wantQty.LessThanOrEqual(decimal.Zero) {
			continue
		}
		gen, err := nextRunGeneration(ctx, db, runID, materialID)
		if err != nil {
			return err
		}
		key := entity.RunReservationClaimKey(runID, materialID, gen)
		wrote, err := insertReservationEvent(ctx, db, materialID, runOwner(runID, want.LotId), wantQty,
			entity.MaterialReservationReserve, key, username)
		if err != nil {
			return err
		}
		if !wrote {
			// ВТОРОЙ ЭШЕЛОН ПОД ЗАМКОМ ВЫШЕ, и он существует потому, что цена молчания здесь —
			// неудержанная ткань. INSERT IGNORE возвращает «ноль строк» вместо ошибки, поэтому
			// пропавшая претензия неотличима от записанной, если не спросить. Замок делает эту ветку
			// недостижимой; если она всё-таки сработала — значит, замка не было (чужой путь записи,
			// вызов вне транзакции), и узнать об этом надо здесь, а не на складе.
			return fmt.Errorf("run %d material %d: reservation key %q already taken — the hold was NOT recorded",
				runID, materialID, key)
		}
	}
	return nil
}

// SetRunMaterialReservations is SetRunMaterialReservationsInTx in its own transaction.
//
// WHERE THE REQUIREMENT COMES FROM. This store deliberately does not compute it: the numbers live in
// ComputeProductionRunMaterialPlan, which needs the run, its tech card, the card's linked materials
// and the run's lays — a composition that already exists one layer up, in the admin service. Each
// MaterialPlanRow carries MaterialId, Required (Σ norm × planned_qty × gross-up) and Issued (net
// issued to this run), and the hold is
//
//	RunMaterialRequirement{Qty: max(0, Required − Issued)}
//
// which is why the same call serves both the NORM moment and the LAYS moment: the plan itself
// already knows which source won (plan_source NORM / LAYS / MIXED), and this store does not need to.
func (s *Store) SetRunMaterialReservations(ctx context.Context, runID int, required map[int]entity.RunMaterialRequirement, username string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		return SetRunMaterialReservationsInTx(ctx, rep.DB(), runID, required, username)
	})
}

// ConsumeRunReservationInTx converts a run's hold on one material into a physical issue: the claim
// is closed with a 'consume' row for what was actually issued, and any REMAINDER is immediately
// re-held under the next generation.
//
// The remainder matters. A claim closes on its first consume — that is the ledger's rule, enforced
// by UNIQUE(claim_key, event) — so a partial issue against a whole hold would otherwise drop the
// unissued part of the need on the floor, and the next run would read cloth as free that this run
// still has to cut. Re-reserving the remainder is what keeps issue-by-issue delivery honest.
//
// Why consume rather than release: at issue the material physically leaves on_hand. Closing the hold
// by exactly the issued amount keeps available = on_hand − reserved unchanged across the issue — the
// soft hold has simply become a hard decrement. Issuing MORE than was held closes the whole claim
// and lets available drop by the excess, which is the truth: that surplus was never planned for.
//
// No open claim is not an error — an issue against a run nobody reserved for (a run predating the
// hold, an ad-hoc issue) is legal and simply has nothing to close.
func ConsumeRunReservationInTx(ctx context.Context, db dependency.DB, runID, materialID int, issued decimal.Decimal, username string) error {
	if issued.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	cur, held, err := openRunClaimFor(ctx, db, runID, materialID)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	consumed := decimal.Min(issued, cur.Qty).Round(qtyScale)
	if consumed.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	if _, err := insertReservationEvent(ctx, db, materialID, runOwner(runID, cur.LotId), consumed,
		entity.MaterialReservationConsume, cur.ClaimKey, username); err != nil {
		return err
	}
	remainder := cur.Qty.Sub(consumed)
	if remainder.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	gen, err := nextRunGeneration(ctx, db, runID, materialID)
	if err != nil {
		return err
	}
	_, err = insertReservationEvent(ctx, db, materialID, runOwner(runID, cur.LotId), remainder,
		entity.MaterialReservationReserve, entity.RunReservationClaimKey(runID, materialID, gen), username)
	return err
}

// openRunClaimFor returns the run's open claim on one material, if it has one.
func openRunClaimFor(ctx context.Context, db dependency.DB, runID, materialID int) (entity.MaterialReservationClaim, bool, error) {
	rows, err := storeutil.QueryListNamed[entity.MaterialReservationClaim](ctx, db, `
		SELECT r.id, r.material_id, r.order_id, r.run_id, r.lot_id, r.qty, r.claim_key, r.created_by, r.created_at
		FROM material_reservation_ledger r
		WHERE r.run_id = :run_id AND r.material_id = :material_id AND r.event = 'reserve'
		  AND NOT EXISTS (SELECT 1 FROM material_reservation_ledger x
		                  WHERE x.claim_key = r.claim_key AND x.event IN ('consume', 'release'))
		ORDER BY r.id DESC`,
		map[string]any{"run_id": runID, "material_id": materialID})
	if err != nil {
		return entity.MaterialReservationClaim{}, false, fmt.Errorf("read open reservation for run %d material %d: %w", runID, materialID, err)
	}
	if len(rows) == 0 {
		return entity.MaterialReservationClaim{}, false, nil
	}
	return rows[0], true, nil
}

// ReleaseRunReservationsInTx closes every still-open claim of a run with a 'release' row — no
// physical movement, the soft hold simply goes back.
//
// Called on BOTH terminal transitions, closed and cancelled, and the second one is the one that is
// easy to forget: a cancelled run is exactly the run nobody will ever look at again, so a hold it
// keeps is a hold nobody will ever notice. Idempotent and a no-op for a run holding nothing.
//
// Deleting a run needs no call at all — the FK is ON DELETE CASCADE (0286), so its claims go with
// it, and order-owned claims are untouched because they hang off a different owner column entirely.
func ReleaseRunReservationsInTx(ctx context.Context, db dependency.DB, runID int, username string) error {
	open, err := openRunClaims(ctx, db, runID)
	if err != nil {
		return err
	}
	for _, c := range open {
		if _, err := insertReservationEvent(ctx, db, c.MaterialId, runOwner(runID, c.LotId), c.Qty,
			entity.MaterialReservationRelease, c.ClaimKey, username); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseRunReservations is ReleaseRunReservationsInTx in its own transaction, for callers outside a
// run mutation (an operator freeing an abandoned run's cloth by hand).
func (s *Store) ReleaseRunReservations(ctx context.Context, runID int, username string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		return ReleaseRunReservationsInTx(ctx, rep.DB(), runID, username)
	})
}

// ListMaterialReservations returns every OPEN claim on a material — both owners, with the owner and
// the claim's age — oldest first.
//
// This is the answer to "the cloth says it is short, who is holding it": a run parked in `planned`
// since March is a legitimate hold and a visible one, and the operator releases it by cancelling or
// closing the run. Σ Qty over the returned rows equals the Reserved of MaterialAvailable by
// construction — both fold the same open-claim predicate, so the list can never disagree with the
// number it explains.
func (s *Store) ListMaterialReservations(ctx context.Context, materialID int) ([]entity.MaterialReservationClaim, error) {
	rows, err := storeutil.QueryListNamed[entity.MaterialReservationClaim](ctx, s.DB, `
		SELECT r.id, r.material_id, r.order_id, r.run_id, r.lot_id, r.qty, r.claim_key, r.created_by, r.created_at
		FROM material_reservation_ledger r
		WHERE r.material_id = :m AND r.event = 'reserve'
		  AND NOT EXISTS (SELECT 1 FROM material_reservation_ledger x
		                  WHERE x.claim_key = r.claim_key AND x.event IN ('consume', 'release'))
		ORDER BY r.created_at, r.id`,
		map[string]any{"m": materialID})
	if err != nil {
		return nil, fmt.Errorf("list open reservations for material %d: %w", materialID, err)
	}
	return rows, nil
}

// LotAvailable answers "how much of THIS lot is still free" (Р7):
//
//	remaining_qty − Σ qty of OPEN claims naming this lot
//
// Called by the recut check alone. A recut has to come out of the same dye lot as the original cut —
// a month later out of another lot the difference is visible on the finished garment — so the recut
// is the one caller that has to ask about a roll rather than about a material.
//
// It is NOT wired into MaterialAvailable or any general path, and that is deliberate: the general
// question is "is there enough of this cloth", and answering it per lot would refuse a run holding
// plenty of cloth spread over several rolls. A lot-pinned claim still counts fully in
// available(material) — it is a hold on that material whatever roll it names.
func (s *Store) LotAvailable(ctx context.Context, lotID int) (entity.MaterialLotAvailability, error) {
	return lotAvailableInTx(ctx, s.DB, lotID)
}

// lotAvailableInTx is LotAvailable on the caller's transaction, so a recut check can read it inside
// the transaction that is about to place the hold.
func lotAvailableInTx(ctx context.Context, db dependency.DB, lotID int) (entity.MaterialLotAvailability, error) {
	lot, err := storeutil.QueryNamedOne[struct {
		MaterialId   int             `db:"material_id"`
		LotCode      string          `db:"lot_code"`
		RemainingQty decimal.Decimal `db:"remaining_qty"`
	}](ctx, db, `SELECT material_id, lot_code, remaining_qty FROM material_lot WHERE id = :id`,
		map[string]any{"id": lotID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.MaterialLotAvailability{}, fmt.Errorf("%w: lot %d", entity.ErrMaterialLotNotFound, lotID)
		}
		return entity.MaterialLotAvailability{}, fmt.Errorf("read material lot %d: %w", lotID, err)
	}
	reserved, err := storeutil.QueryNamedOne[decRow](ctx, db, `
		SELECT COALESCE(SUM(r.qty), 0) AS v FROM material_reservation_ledger r
		WHERE r.lot_id = :lot AND r.event = 'reserve'
		  AND NOT EXISTS (SELECT 1 FROM material_reservation_ledger x
		                  WHERE x.claim_key = r.claim_key AND x.event IN ('consume', 'release'))`,
		map[string]any{"lot": lotID})
	if err != nil {
		return entity.MaterialLotAvailability{}, fmt.Errorf("read open reserved qty for lot %d: %w", lotID, err)
	}
	return entity.MaterialLotAvailability{
		LotId:        lotID,
		MaterialId:   lot.MaterialId,
		LotCode:      lot.LotCode,
		RemainingQty: lot.RemainingQty,
		Reserved:     reserved.V,
		Available:    lot.RemainingQty.Sub(reserved.V),
	}, nil
}

// ListPackagingRecipe returns every packaging recipe row (all scopes) joined with material name/unit,
// ordered by scope then material name for display.
func (s *Store) ListPackagingRecipe(ctx context.Context) ([]entity.PackagingRecipe, error) {
	rows, err := storeutil.QueryListNamed[entity.PackagingRecipe](ctx, s.DB, `
		SELECT pr.id, pr.scope, pr.tech_card_id, pr.product_id, pr.material_id,
		       m.name AS material_name, m.unit AS material_unit,
		       pr.qty_per_order, pr.qty_per_item, pr.active, pr.created_by, pr.updated_by
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
