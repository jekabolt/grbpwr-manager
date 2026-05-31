// Package membership implements dependency.Membership: loyalty tier state,
// qualifying-spend computation, tier configuration, audit history, account
// lifecycle (soft-delete / erasure), and hacker invites.
package membership

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// TxFunc matches the store transaction callback used by MYSQLStore.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Membership.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a membership store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// qualifyingStatuses are the order statuses whose paid value counts toward
// loyalty spend. Placed/awaiting_payment/cancelled are excluded (not paid).
var qualifyingStatuses = []entity.OrderStatusName{
	entity.Confirmed,
	entity.Shipped,
	entity.Delivered,
	entity.PendingReturn,
	entity.RefundInProgress,
	"partially_refunded",
}

// qualifyingStatusIDs resolves the qualifying status names to ids via cache.
func qualifyingStatusIDs() []int {
	ids := make([]int, 0, len(qualifyingStatuses))
	for _, n := range qualifyingStatuses {
		if s, ok := cache.GetOrderStatusByName(n); ok {
			ids = append(ids, s.Status.Id)
		}
	}
	return ids
}

// ComputeQualifyingSpendEUR sums the EUR-equivalent net (after refunds) of all
// qualifying orders for the account's email placed at/after windowStart.
//
// Net per order = total_price_eur * (1 - refunded_amount / total_price), which
// is currency-independent because the refund ratio is taken in the order's own
// currency. Orders without an EUR snapshot are skipped (logged by the caller).
func (s *Store) ComputeQualifyingSpendEUR(ctx context.Context, email string, windowStart time.Time) (decimal.Decimal, error) {
	statusIDs := qualifyingStatusIDs()
	if len(statusIDs) == 0 {
		return decimal.Zero, fmt.Errorf("no qualifying order statuses resolved from cache")
	}
	type sumRow struct {
		Total decimal.NullDecimal `db:"total"`
	}
	q := `
		SELECT COALESCE(SUM(
			co.total_price_eur * (1 - (co.refunded_amount / NULLIF(co.total_price, 0)))
		), 0) AS total
		FROM customer_order co
		JOIN buyer b ON b.order_id = co.id
		WHERE b.email = :email
		  AND co.order_status_id IN (:statusIDs)
		  AND co.total_price_eur IS NOT NULL
		  AND co.placed >= :windowStart`
	r, err := storeutil.QueryNamedOne[sumRow](ctx, s.DB, q, map[string]any{
		"email":       email,
		"statusIDs":   statusIDs,
		"windowStart": windowStart,
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("compute qualifying spend: %w", err)
	}
	if !r.Total.Valid {
		return decimal.Zero, nil
	}
	return r.Total.Decimal, nil
}

// CountQualifyingOrders returns the number of qualifying (paid) orders for an email.
func (s *Store) CountQualifyingOrders(ctx context.Context, email string) (int, error) {
	statusIDs := qualifyingStatusIDs()
	if len(statusIDs) == 0 {
		return 0, fmt.Errorf("no qualifying order statuses resolved from cache")
	}
	q := `SELECT COUNT(*) FROM customer_order co JOIN buyer b ON b.order_id = co.id WHERE b.email = :email AND co.order_status_id IN (:statusIDs)`
	return storeutil.QueryCountNamed(ctx, s.DB, q, map[string]any{"email": email, "statusIDs": statusIDs})
}

// UpsertSpendCache writes the cached qualifying spend for an account.
func (s *Store) UpsertSpendCache(ctx context.Context, accountID int, amount decimal.Decimal, windowStart, windowEnd time.Time) error {
	q := `
		INSERT INTO qualifying_spend_cache (account_id, amount_eur, window_start, window_end)
		VALUES (:accountID, :amount, :ws, :we)
		ON DUPLICATE KEY UPDATE amount_eur = VALUES(amount_eur), window_start = VALUES(window_start), window_end = VALUES(window_end)`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{
		"accountID": accountID,
		"amount":    amount,
		"ws":        windowStart,
		"we":        windowEnd,
	})
}

// GetSpendCache returns the cached qualifying spend for an account (zero if absent).
func (s *Store) GetSpendCache(ctx context.Context, accountID int) (*entity.QualifyingSpend, error) {
	q := `SELECT account_id, amount_eur, window_start, window_end, computed_at FROM qualifying_spend_cache WHERE account_id = :id`
	r, err := storeutil.QueryNamedOne[entity.QualifyingSpend](ctx, s.DB, q, map[string]any{"id": accountID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &entity.QualifyingSpend{AccountID: accountID, AmountEUR: decimal.Zero}, nil
		}
		return nil, err
	}
	return &r, nil
}

// BackfillOrderEURSnapshots populates customer_order.total_price_eur for orders
// that lack it (one-time legacy job). EUR orders take their own total; non-EUR
// orders are re-priced from EUR product prices and scaled to the recorded total
// (distributing shipping/promo proportionally). Returns rows updated.
func (s *Store) BackfillOrderEURSnapshots(ctx context.Context) (int64, error) {
	var total int64
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// 1. EUR orders.
		q1, a1, err := storeutil.MakeQuery(
			`UPDATE customer_order SET total_price_eur = total_price WHERE currency = 'EUR' AND total_price_eur IS NULL`,
			map[string]any{})
		if err != nil {
			return err
		}
		res1, err := db.ExecContext(ctx, q1, a1...)
		if err != nil {
			return fmt.Errorf("backfill EUR orders: %w", err)
		}
		n1, _ := res1.RowsAffected()

		// 2. Non-EUR orders: scale EUR goods to the recorded total.
		q2, a2, err := storeutil.MakeQuery(`
			UPDATE customer_order co
			JOIN (
				SELECT oi.order_id,
				       SUM(pp.price * (1 - oi.product_sale_percentage/100) * oi.quantity) AS goods_eur,
				       SUM(oi.product_price * (1 - oi.product_sale_percentage/100) * oi.quantity) AS goods_ccy
				FROM order_item oi
				JOIN product_price pp ON pp.product_id = oi.product_id AND pp.currency = 'EUR'
				GROUP BY oi.order_id
			) g ON g.order_id = co.id
			SET co.total_price_eur = ROUND(g.goods_eur * (co.total_price / NULLIF(g.goods_ccy, 0)), 2)
			WHERE co.currency <> 'EUR' AND co.total_price_eur IS NULL AND g.goods_ccy > 0`,
			map[string]any{})
		if err != nil {
			return err
		}
		res2, err := db.ExecContext(ctx, q2, a2...)
		if err != nil {
			return fmt.Errorf("backfill non-EUR orders: %w", err)
		}
		n2, _ := res2.RowsAffected()
		total = n1 + n2
		return nil
	})
	return total, err
}

// ----- tier configuration -----

// ListTierConfig returns all tier configs ordered by code.
func (s *Store) ListTierConfig(ctx context.Context) ([]entity.TierConfig, error) {
	q := `SELECT tier_code, tier_key, display_name, min_spend_eur, expiration_days, reminder_days_before, is_invite_only, welcome_pack_slots, updated_at FROM tier_config ORDER BY tier_code`
	return storeutil.QueryListNamed[entity.TierConfig](ctx, s.DB, q, nil)
}

// GetTierConfig returns the config for a tier code.
func (s *Store) GetTierConfig(ctx context.Context, code int16) (*entity.TierConfig, error) {
	q := `SELECT tier_code, tier_key, display_name, min_spend_eur, expiration_days, reminder_days_before, is_invite_only, welcome_pack_slots, updated_at FROM tier_config WHERE tier_code = :code`
	r, err := storeutil.QueryNamedOne[entity.TierConfig](ctx, s.DB, q, map[string]any{"code": code})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateTierConfig updates the editable fields of a tier config row.
func (s *Store) UpdateTierConfig(ctx context.Context, upd entity.TierConfigUpdate) error {
	q := `
		UPDATE tier_config
		SET display_name = :displayName,
		    min_spend_eur = :minSpend,
		    expiration_days = :expDays,
		    reminder_days_before = :reminderDays,
		    welcome_pack_slots = :welcomeSlots
		WHERE tier_code = :code`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{
		"code":         upd.TierCode,
		"displayName":  upd.DisplayName,
		"minSpend":     upd.MinSpendEUR,
		"expDays":      upd.ExpirationDays,
		"reminderDays": upd.ReminderDaysBefore,
		"welcomeSlots": upd.WelcomePackSlots,
	})
}

// ----- members -----

const memberSelectCols = `
	sa.id, sa.email, sa.first_name, sa.last_name, sa.birth_date, sa.shopping_preference,
	sa.phone, sa.account_tier, sa.subscribe_newsletter, sa.subscribe_new_arrivals, sa.subscribe_events,
	sa.default_country, sa.default_language, sa.status, sa.tier_upgrade_date, sa.next_review_date,
	sa.deleted_at, sa.created_at, sa.updated_at`

type memberRow struct {
	entity.StorefrontAccount
	QualifyingSpendEUR decimal.NullDecimal `db:"qualifying_spend_eur"`
	LastOrderDate      sql.NullTime        `db:"last_order_date"`
}

func (r memberRow) toMember() entity.Member {
	spend := decimal.Zero
	if r.QualifyingSpendEUR.Valid {
		spend = r.QualifyingSpendEUR.Decimal
	}
	return entity.Member{
		Account:            r.StorefrontAccount,
		QualifyingSpendEUR: spend,
		LastOrderDate:      r.LastOrderDate,
	}
}

// ListMembers returns members matching the filter plus the total (unpaged) count.
func (s *Store) ListMembers(ctx context.Context, f entity.MemberListFilter) ([]entity.Member, int, error) {
	where := []string{"1=1"}
	params := map[string]any{}
	if f.Tier != nil {
		where = append(where, "sa.account_tier = :tier")
		params["tier"] = string(*f.Tier)
	}
	if f.Status != nil {
		where = append(where, "sa.status = :status")
		params["status"] = string(*f.Status)
	}
	if f.Email != "" {
		where = append(where, "sa.email LIKE :email")
		params["email"] = "%" + f.Email + "%"
	}
	if f.RegisteredFrom.Valid {
		where = append(where, "sa.created_at >= :regFrom")
		params["regFrom"] = f.RegisteredFrom.Time
	}
	if f.RegisteredTo.Valid {
		where = append(where, "sa.created_at <= :regTo")
		params["regTo"] = f.RegisteredTo.Time
	}
	if f.DaysUntilReviewMax.Valid {
		where = append(where, "sa.next_review_date IS NOT NULL AND sa.next_review_date <= DATE_ADD(NOW(), INTERVAL :reviewDays DAY)")
		params["reviewDays"] = f.DaysUntilReviewMax.Int64
	}
	// Spend filters operate on the cache table.
	having := []string{}
	if f.SpendMinEUR.Valid {
		having = append(having, "qualifying_spend_eur >= :spendMin")
		params["spendMin"] = f.SpendMinEUR.Decimal
	}
	if f.SpendMaxEUR.Valid {
		having = append(having, "qualifying_spend_eur <= :spendMax")
		params["spendMax"] = f.SpendMaxEUR.Decimal
	}
	whereClause := strings.Join(where, " AND ")
	havingClause := ""
	if len(having) > 0 {
		havingClause = "HAVING " + strings.Join(having, " AND ")
	}

	base := fmt.Sprintf(`
		FROM storefront_account sa
		LEFT JOIN qualifying_spend_cache qsc ON qsc.account_id = sa.id
		LEFT JOIN (SELECT b.email, MAX(co.placed) AS last_order_date FROM buyer b JOIN customer_order co ON co.id = b.order_id GROUP BY b.email) lo ON lo.email = sa.email
		WHERE %s`, whereClause)

	listQ := fmt.Sprintf(`
		SELECT %s, COALESCE(qsc.amount_eur, 0) AS qualifying_spend_eur, lo.last_order_date AS last_order_date
		%s
		%s
		ORDER BY sa.id DESC
		LIMIT :limit OFFSET :offset`, memberSelectCols, base, havingClause)

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	params["limit"] = limit
	params["offset"] = f.Offset

	rows, err := storeutil.QueryListNamed[memberRow](ctx, s.DB, listQ, params)
	if err != nil {
		return nil, 0, fmt.Errorf("list members: %w", err)
	}
	members := make([]entity.Member, 0, len(rows))
	for _, r := range rows {
		members = append(members, r.toMember())
	}

	// Count: when spend filters are present we must count via a subquery.
	countParams := map[string]any{}
	for k, v := range params {
		if k == "limit" || k == "offset" {
			continue
		}
		countParams[k] = v
	}
	var countQ string
	if havingClause != "" {
		countQ = fmt.Sprintf(`
			SELECT COUNT(*) FROM (
				SELECT sa.id, COALESCE(qsc.amount_eur, 0) AS qualifying_spend_eur
				%s
				%s
			) t`, base, havingClause)
	} else {
		countQ = "SELECT COUNT(*) " + base
	}
	total, err := storeutil.QueryCountNamed(ctx, s.DB, countQ, countParams)
	if err != nil {
		return nil, 0, fmt.Errorf("count members: %w", err)
	}
	return members, total, nil
}

// GetMember returns a single member by account id.
func (s *Store) GetMember(ctx context.Context, accountID int) (*entity.Member, error) {
	q := fmt.Sprintf(`
		SELECT %s, COALESCE(qsc.amount_eur, 0) AS qualifying_spend_eur,
		       (SELECT MAX(co.placed) FROM buyer b JOIN customer_order co ON co.id = b.order_id WHERE b.email = sa.email) AS last_order_date
		FROM storefront_account sa
		LEFT JOIN qualifying_spend_cache qsc ON qsc.account_id = sa.id
		WHERE sa.id = :id`, memberSelectCols)
	r, err := storeutil.QueryNamedOne[memberRow](ctx, s.DB, q, map[string]any{"id": accountID})
	if err != nil {
		return nil, err
	}
	m := r.toMember()
	return &m, nil
}

// ----- tier transitions -----

// ApplyTierTransition updates the account tier, refreshes membership dates from
// tier_config, and writes a tier_history audit row — all in one transaction.
func (s *Store) ApplyTierTransition(ctx context.Context, t entity.TierTransition) error {
	if !entity.IsValidStorefrontAccountTier(string(t.NewTier)) {
		return fmt.Errorf("invalid target tier %q", t.NewTier)
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		now := time.Now().UTC()

		// Compute new membership dates from the target tier config.
		var upgradeDate, nextReview any = now, nil
		cfgRow, err := storeutil.QueryNamedOne[entity.TierConfig](ctx, db,
			`SELECT tier_code, tier_key, display_name, min_spend_eur, expiration_days, reminder_days_before, is_invite_only, welcome_pack_slots, updated_at FROM tier_config WHERE tier_key = :key`,
			map[string]any{"key": string(t.NewTier)})
		if err != nil {
			return fmt.Errorf("load target tier config: %w", err)
		}
		// Base tier (member) and invite-only (hacker) don't expire on a spend cycle.
		if entity.IsNumericTier(t.NewTier) && t.NewTier != entity.StorefrontAccountTierMember {
			nextReview = now.AddDate(0, 0, cfgRow.ExpirationDays)
		}

		upd := `
			UPDATE storefront_account
			SET account_tier = :tier, tier_upgrade_date = :upgradeDate, next_review_date = :nextReview, updated_at = CURRENT_TIMESTAMP
			WHERE id = :id`
		if err := storeutil.ExecNamed(ctx, db, upd, map[string]any{
			"tier":        string(t.NewTier),
			"upgradeDate": upgradeDate,
			"nextReview":  nextReview,
			"id":          t.AccountID,
		}); err != nil {
			return fmt.Errorf("update account tier: %w", err)
		}

		actor := t.Actor
		if actor == "" {
			actor = entity.ActorSystem
		}
		var reason any
		if t.Reason != "" {
			reason = t.Reason
		}
		ins := `
			INSERT INTO tier_history (account_id, old_tier, new_tier, trigger_type, reason, actor, spend_eur_at_change)
			VALUES (:accountID, :oldTier, :newTier, :trigger, :reason, :actor, :spend)`
		if err := storeutil.ExecNamed(ctx, db, ins, map[string]any{
			"accountID": t.AccountID,
			"oldTier":   string(t.OldTier),
			"newTier":   string(t.NewTier),
			"trigger":   string(t.Trigger),
			"reason":    reason,
			"actor":     actor,
			"spend":     t.SpendEUR,
		}); err != nil {
			return fmt.Errorf("insert tier history: %w", err)
		}
		return nil
	})
}

// ListTierHistory returns the tier history for an account, newest first.
func (s *Store) ListTierHistory(ctx context.Context, accountID int) ([]entity.TierHistoryEntry, error) {
	q := `SELECT id, account_id, old_tier, new_tier, trigger_type, reason, actor, spend_eur_at_change, created_at FROM tier_history WHERE account_id = :id ORDER BY created_at DESC, id DESC`
	return storeutil.QueryListNamed[entity.TierHistoryEntry](ctx, s.DB, q, map[string]any{"id": accountID})
}

// ListAuditLog returns tier_history rows across all accounts with filters + total.
func (s *Store) ListAuditLog(ctx context.Context, f entity.TierAuditFilter) ([]entity.TierHistoryEntry, int, error) {
	where := []string{"1=1"}
	params := map[string]any{}
	if f.AccountID != nil {
		where = append(where, "account_id = :accountID")
		params["accountID"] = *f.AccountID
	}
	if f.Actor != "" {
		where = append(where, "actor = :actor")
		params["actor"] = f.Actor
	}
	if f.Trigger != "" {
		where = append(where, "trigger_type = :trigger")
		params["trigger"] = f.Trigger
	}
	if f.From.Valid {
		where = append(where, "created_at >= :from")
		params["from"] = f.From.Time
	}
	if f.To.Valid {
		where = append(where, "created_at <= :to")
		params["to"] = f.To.Time
	}
	wc := strings.Join(where, " AND ")
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	params["limit"] = limit
	params["offset"] = f.Offset
	listQ := fmt.Sprintf(`SELECT id, account_id, old_tier, new_tier, trigger_type, reason, actor, spend_eur_at_change, created_at FROM tier_history WHERE %s ORDER BY created_at DESC, id DESC LIMIT :limit OFFSET :offset`, wc)
	rows, err := storeutil.QueryListNamed[entity.TierHistoryEntry](ctx, s.DB, listQ, params)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit log: %w", err)
	}
	cp := map[string]any{}
	for k, v := range params {
		if k == "limit" || k == "offset" {
			continue
		}
		cp[k] = v
	}
	total, err := storeutil.QueryCountNamed(ctx, s.DB, "SELECT COUNT(*) FROM tier_history WHERE "+wc, cp)
	if err != nil {
		return nil, 0, fmt.Errorf("count audit log: %w", err)
	}
	return rows, total, nil
}

// ----- account lifecycle / GDPR -----

// SetAccountStatus updates the lifecycle status of an account.
func (s *Store) SetAccountStatus(ctx context.Context, accountID int, st entity.StorefrontAccountStatus) error {
	if !entity.IsValidStorefrontAccountStatus(string(st)) {
		return fmt.Errorf("invalid account status %q", st)
	}
	q := `UPDATE storefront_account SET status = :status, updated_at = CURRENT_TIMESTAMP WHERE id = :id`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{"status": string(st), "id": accountID})
}

// SoftDeleteAccount marks the account deleted and stamps deleted_at.
func (s *Store) SoftDeleteAccount(ctx context.Context, accountID int) error {
	q := `UPDATE storefront_account SET status = 'deleted', deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP), updated_at = CURRENT_TIMESTAMP WHERE id = :id`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{"id": accountID})
}

// HardEraseAccount anonymises PII in place and marks the account erased (GDPR
// right-to-erasure). Order/buyer history is retained for legal/accounting but
// the account row's personal data is cleared. Sessions are revoked.
func (s *Store) HardEraseAccount(ctx context.Context, accountID int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		anon := fmt.Sprintf("erased+%d@deleted.invalid", accountID)
		q := `
			UPDATE storefront_account
			SET email = :anon, first_name = '', last_name = '', birth_date = NULL, phone = NULL,
			    default_country = NULL, default_language = NULL,
			    subscribe_newsletter = 0, subscribe_new_arrivals = 0, subscribe_events = 0,
			    status = 'erased', deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP), updated_at = CURRENT_TIMESTAMP
			WHERE id = :id`
		if err := storeutil.ExecNamed(ctx, db, q, map[string]any{"anon": anon, "id": accountID}); err != nil {
			return fmt.Errorf("erase account: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, db, `DELETE FROM storefront_saved_address WHERE account_id = :id`, map[string]any{"id": accountID}); err != nil {
			return fmt.Errorf("erase saved addresses: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, db, `UPDATE storefront_refresh_token SET revoked_at = CURRENT_TIMESTAMP WHERE account_id = :id AND revoked_at IS NULL`, map[string]any{"id": accountID}); err != nil {
			return fmt.Errorf("revoke refresh tokens: %w", err)
		}
		return nil
	})
}

// ----- worker queries -----

// ListAccountsForDowngradeReview returns active accounts on a numeric tier above
// member whose next_review_date has passed (candidates for a -1 level downgrade).
func (s *Store) ListAccountsForDowngradeReview(ctx context.Context, now time.Time) ([]entity.StorefrontAccount, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM storefront_account sa
		WHERE sa.status = 'active'
		  AND sa.account_tier IN ('plus','plus_plus')
		  AND sa.next_review_date IS NOT NULL
		  AND sa.next_review_date <= :now`, accountCols("sa"))
	return storeutil.QueryListNamed[entity.StorefrontAccount](ctx, s.DB, q, map[string]any{"now": now})
}

// ListAccountsForDowngradeReminder returns accounts whose review date is exactly
// reminderDays away (day-granularity), so the reminder fires once per cycle.
func (s *Store) ListAccountsForDowngradeReminder(ctx context.Context, now time.Time, reminderDays int) ([]entity.StorefrontAccount, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM storefront_account sa
		WHERE sa.status = 'active'
		  AND sa.account_tier IN ('plus','plus_plus')
		  AND sa.next_review_date IS NOT NULL
		  AND DATE(sa.next_review_date) = DATE(DATE_ADD(:now, INTERVAL :days DAY))`, accountCols("sa"))
	return storeutil.QueryListNamed[entity.StorefrontAccount](ctx, s.DB, q, map[string]any{"now": now, "days": reminderDays})
}

// ListAccountsWithBirthday returns active accounts whose DOB falls on month/day.
func (s *Store) ListAccountsWithBirthday(ctx context.Context, month, day int) ([]entity.StorefrontAccount, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM storefront_account sa
		WHERE sa.status = 'active' AND sa.birth_date IS NOT NULL
		  AND MONTH(sa.birth_date) = :m AND DAY(sa.birth_date) = :d`, accountCols("sa"))
	return storeutil.QueryListNamed[entity.StorefrontAccount](ctx, s.DB, q, map[string]any{"m": month, "d": day})
}

func accountCols(alias string) string {
	cols := []string{
		"id", "email", "first_name", "last_name", "birth_date", "shopping_preference",
		"phone", "account_tier", "subscribe_newsletter", "subscribe_new_arrivals", "subscribe_events",
		"default_country", "default_language", "status", "tier_upgrade_date", "next_review_date",
		"deleted_at", "created_at", "updated_at",
	}
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

// ----- hacker invites -----

// CreateHackerInvite inserts a new one-time invite and returns its id.
func (s *Store) CreateHackerInvite(ctx context.Context, tokenHash string, email sql.NullString, createdBy string, expiresAt time.Time) (int64, error) {
	q := `INSERT INTO hacker_invite (token_hash, email, created_by, expires_at) VALUES (:hash, :email, :createdBy, :expiresAt)`
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, q, map[string]any{
		"hash":      tokenHash,
		"email":     email,
		"createdBy": createdBy,
		"expiresAt": expiresAt,
	})
	return int64(id), err
}

// ListHackerInvites returns invites; when activeOnly, only unconsumed/unrevoked/unexpired.
func (s *Store) ListHackerInvites(ctx context.Context, activeOnly bool, now time.Time) ([]entity.HackerInvite, error) {
	q := `SELECT id, token_hash, email, created_by, expires_at, consumed_at, consumed_by_account_id, revoked_at, created_at FROM hacker_invite`
	params := map[string]any{}
	if activeOnly {
		q += ` WHERE consumed_at IS NULL AND revoked_at IS NULL AND expires_at > :now`
		params["now"] = now
	}
	q += ` ORDER BY created_at DESC`
	return storeutil.QueryListNamed[entity.HackerInvite](ctx, s.DB, q, params)
}

// ConsumeHackerInvite validates and marks an invite consumed by the given account.
// Returns sql.ErrNoRows if no active invite matches the token hash.
func (s *Store) ConsumeHackerInvite(ctx context.Context, tokenHash string, accountID int, now time.Time) (*entity.HackerInvite, error) {
	var out *entity.HackerInvite
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		q := `SELECT id, token_hash, email, created_by, expires_at, consumed_at, consumed_by_account_id, revoked_at, created_at FROM hacker_invite WHERE token_hash = :hash AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > :now FOR UPDATE`
		inv, err := storeutil.QueryNamedOne[entity.HackerInvite](ctx, db, q, map[string]any{"hash": tokenHash, "now": now})
		if err != nil {
			return err
		}
		upd := `UPDATE hacker_invite SET consumed_at = :now, consumed_by_account_id = :acc WHERE id = :id AND consumed_at IS NULL`
		if err := storeutil.ExecNamed(ctx, db, upd, map[string]any{"now": now, "acc": accountID, "id": inv.ID}); err != nil {
			return fmt.Errorf("consume invite: %w", err)
		}
		inv.ConsumedAt = sql.NullTime{Time: now, Valid: true}
		inv.ConsumedByAccountID = sql.NullInt64{Int64: int64(accountID), Valid: true}
		out = &inv
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeHackerInvite marks an unconsumed invite revoked.
func (s *Store) RevokeHackerInvite(ctx context.Context, id int64) error {
	q := `UPDATE hacker_invite SET revoked_at = CURRENT_TIMESTAMP WHERE id = :id AND consumed_at IS NULL AND revoked_at IS NULL`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{"id": id})
}

// ListHackerAccounts returns members currently on the hacker tier.
func (s *Store) ListHackerAccounts(ctx context.Context) ([]entity.Member, error) {
	tier := entity.StorefrontAccountTierHacker
	members, _, err := s.ListMembers(ctx, entity.MemberListFilter{Tier: &tier, Limit: 1000})
	return members, err
}
