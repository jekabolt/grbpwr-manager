package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// TierTrigger identifies what caused a tier transition (stored in tier_history.trigger_type).
type TierTrigger string

const (
	TierTriggerUpgrade        TierTrigger = "upgrade"
	TierTriggerDowngrade      TierTrigger = "downgrade"
	TierTriggerRefundRollback TierTrigger = "refund_rollback"
	TierTriggerManual         TierTrigger = "manual"
	TierTriggerBackfill       TierTrigger = "backfill"
	TierTriggerHackerGrant    TierTrigger = "hacker_grant"
	TierTriggerHackerRevoke   TierTrigger = "hacker_revoke"
)

// ActorSystem is the actor recorded for automated (non-admin) transitions.
const ActorSystem = "system"

// TierConfig is a row in tier_config (admin-editable tier definition).
type TierConfig struct {
	TierCode           int16               `db:"tier_code"`
	TierKey            string              `db:"tier_key"`
	DisplayName        string              `db:"display_name"`
	MinSpendEUR        decimal.NullDecimal `db:"min_spend_eur"`
	ExpirationDays     int                 `db:"expiration_days"`
	ReminderDaysBefore int                 `db:"reminder_days_before"`
	IsInviteOnly       bool                `db:"is_invite_only"`
	WelcomePackSlots   sql.NullInt64       `db:"welcome_pack_slots"`
	UpdatedAt          time.Time           `db:"updated_at"`
}

// Tier returns the typed tier key.
func (c *TierConfig) Tier() StorefrontAccountTier { return StorefrontAccountTier(c.TierKey) }

// TierConfigUpdate carries editable fields for a single tier from the admin screen.
type TierConfigUpdate struct {
	TierCode           int16
	DisplayName        string
	MinSpendEUR        decimal.NullDecimal
	ExpirationDays     int
	ReminderDaysBefore int
	WelcomePackSlots   sql.NullInt64
}

// TierHistoryEntry is a row in tier_history.
type TierHistoryEntry struct {
	ID               int64               `db:"id"`
	AccountID        int                 `db:"account_id"`
	OldTier          string              `db:"old_tier"`
	NewTier          string              `db:"new_tier"`
	TriggerType      string              `db:"trigger_type"`
	Reason           sql.NullString      `db:"reason"`
	Actor            string              `db:"actor"`
	SpendEURAtChange decimal.NullDecimal `db:"spend_eur_at_change"`
	CreatedAt        time.Time           `db:"created_at"`
}

// TierTransition is the input describing a tier change to record + apply.
type TierTransition struct {
	AccountID int
	OldTier   StorefrontAccountTier
	NewTier   StorefrontAccountTier
	Trigger   TierTrigger
	Reason    string
	Actor     string // admin email, or ActorSystem
	SpendEUR  decimal.NullDecimal
}

// HackerInvite is a row in hacker_invite.
type HackerInvite struct {
	ID                  int64          `db:"id"`
	TokenHash           string         `db:"token_hash"`
	Email               sql.NullString `db:"email"`
	CreatedBy           string         `db:"created_by"`
	ExpiresAt           time.Time      `db:"expires_at"`
	ConsumedAt          sql.NullTime   `db:"consumed_at"`
	ConsumedByAccountID sql.NullInt64  `db:"consumed_by_account_id"`
	RevokedAt           sql.NullTime   `db:"revoked_at"`
	CreatedAt           time.Time      `db:"created_at"`
}

// IsActive reports whether the invite can still be consumed at time t.
func (h *HackerInvite) IsActive(t time.Time) bool {
	return !h.ConsumedAt.Valid && !h.RevokedAt.Valid && h.ExpiresAt.After(t)
}

// QualifyingSpend is a row in qualifying_spend_cache.
type QualifyingSpend struct {
	AccountID   int             `db:"account_id"`
	AmountEUR   decimal.Decimal `db:"amount_eur"`
	WindowStart time.Time       `db:"window_start"`
	WindowEnd   time.Time       `db:"window_end"`
	ComputedAt  time.Time       `db:"computed_at"`
}

// Member is the admin-facing aggregate of an account + its membership state.
type Member struct {
	Account            StorefrontAccount
	QualifyingSpendEUR decimal.Decimal
	LastOrderDate      sql.NullTime
}

// MemberListFilter holds the filters for the admin members list.
type MemberListFilter struct {
	Tier               *StorefrontAccountTier
	Status             *StorefrontAccountStatus
	SpendMinEUR        decimal.NullDecimal
	SpendMaxEUR        decimal.NullDecimal
	RegisteredFrom     sql.NullTime
	RegisteredTo       sql.NullTime
	DaysUntilReviewMax sql.NullInt64
	Email              string
	Limit              int
	Offset             int
}

// TierAuditFilter filters the global tier_history audit log.
type TierAuditFilter struct {
	AccountID *int
	Actor     string
	Trigger   string
	From      sql.NullTime
	To        sql.NullTime
	Limit     int
	Offset    int
}

// MaxTierQualified returns the highest spend-based tier whose threshold is met by
// spendEUR, given the tier configs. Hacker/invite-only tiers are never returned.
func MaxTierQualified(spendEUR decimal.Decimal, configs []TierConfig) StorefrontAccountTier {
	best := StorefrontAccountTierMember
	bestCode := TierCodeMember
	for _, c := range configs {
		if c.IsInviteOnly || !c.MinSpendEUR.Valid {
			continue
		}
		if spendEUR.GreaterThanOrEqual(c.MinSpendEUR.Decimal) && c.TierCode > bestCode {
			bestCode = c.TierCode
			best = c.Tier()
		}
	}
	return best
}
