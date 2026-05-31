// Package tiermanagement implements the loyalty tier engine: real-time upgrades
// on paid orders, immediate rollback on refunds, the daily downgrade/reminder
// review worker, birthday gifts, hacker invites, and the one-time legacy backfill.
package tiermanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// SpendWindowMonths is the rolling window for qualifying-spend.
const SpendWindowMonths = 12

// Engine evaluates and applies tier transitions. It is cheap to construct and
// holds no mutable state, so it can be created per-call where a repo + mailer
// are already in hand (payment hooks, admin handlers) or once for the worker.
type Engine struct {
	repo   dependency.Repository
	mailer dependency.Mailer
}

// NewEngine builds a tier engine.
func NewEngine(repo dependency.Repository, mailer dependency.Mailer) *Engine {
	return &Engine{repo: repo, mailer: mailer}
}

func (e *Engine) now() time.Time { return time.Now().UTC() }

func windowStart(now time.Time) time.Time { return now.AddDate(0, -SpendWindowMonths, 0) }

// loadConfigs returns the tier configs and a key->config map.
func (e *Engine) loadConfigs(ctx context.Context) ([]entity.TierConfig, map[entity.StorefrontAccountTier]entity.TierConfig, error) {
	configs, err := e.repo.Membership().ListTierConfig(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load tier configs: %w", err)
	}
	byKey := make(map[entity.StorefrontAccountTier]entity.TierConfig, len(configs))
	for _, c := range configs {
		byKey[c.Tier()] = c
	}
	return configs, byKey, nil
}

// RecomputeSpend computes and caches qualifying spend for an account.
func (e *Engine) RecomputeSpend(ctx context.Context, acc *entity.StorefrontAccount) (decimal.Decimal, error) {
	now := e.now()
	ws := windowStart(now)
	spend, err := e.repo.Membership().ComputeQualifyingSpendEUR(ctx, acc.Email, ws)
	if err != nil {
		return decimal.Zero, err
	}
	if err := e.repo.Membership().UpsertSpendCache(ctx, acc.ID, spend, ws, now); err != nil {
		return decimal.Zero, fmt.Errorf("cache spend: %w", err)
	}
	return spend, nil
}

// getActiveAccount loads an account by email; returns (nil, nil) for unknown
// (guest checkout) or non-active accounts — both are silently skipped.
func (e *Engine) getActiveAccount(ctx context.Context, email string) (*entity.StorefrontAccount, error) {
	acc, err := e.repo.StorefrontAccount().GetAccountByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if acc.Status != entity.StorefrontStatusActive && acc.Status != "" {
		return nil, nil
	}
	return acc, nil
}

// EvaluateAfterOrderPaid recomputes spend and, for spend-managed tiers, upgrades
// the member if they now qualify for a higher tier. Sends a first-purchase email
// on the member's first paid order. Safe for guest checkouts (no-op).
func (e *Engine) EvaluateAfterOrderPaid(ctx context.Context, email string) error {
	acc, err := e.getActiveAccount(ctx, email)
	if err != nil || acc == nil {
		return err
	}
	spend, err := e.RecomputeSpend(ctx, acc)
	if err != nil {
		return err
	}
	configs, byKey, err := e.loadConfigs(ctx)
	if err != nil {
		return err
	}
	cur := acc.Tier()

	// First-purchase thank-you (best effort, non-fatal).
	if cnt, cerr := e.repo.Membership().CountQualifyingOrders(ctx, acc.Email); cerr == nil && cnt == 1 {
		e.sendFirstPurchase(ctx, acc, byKey[cur])
	}

	// Hacker is invite-only and never spend-managed.
	if cur == entity.StorefrontAccountTierHacker {
		return nil
	}
	target := entity.MaxTierQualified(spend, configs)
	if entity.TierCode(target) <= entity.TierCode(cur) {
		return nil
	}
	return e.applyTransition(ctx, acc, target, entity.TierTriggerUpgrade, "", entity.ActorSystem, spend, byKey)
}

// EvaluateAfterRefund recomputes spend and immediately rolls a member down to the
// tier their (reduced) spend now qualifies for, if that is lower than current.
func (e *Engine) EvaluateAfterRefund(ctx context.Context, email string) error {
	acc, err := e.getActiveAccount(ctx, email)
	if err != nil || acc == nil {
		return err
	}
	cur := acc.Tier()
	// Member can't go lower; hacker isn't spend-managed.
	if cur == entity.StorefrontAccountTierHacker || cur == entity.StorefrontAccountTierMember {
		_, rerr := e.RecomputeSpend(ctx, acc)
		return rerr
	}
	spend, err := e.RecomputeSpend(ctx, acc)
	if err != nil {
		return err
	}
	configs, byKey, err := e.loadConfigs(ctx)
	if err != nil {
		return err
	}
	target := entity.MaxTierQualified(spend, configs)
	if entity.TierCode(target) >= entity.TierCode(cur) {
		return nil
	}
	return e.applyTransition(ctx, acc, target, entity.TierTriggerRefundRollback, "qualifying spend dropped below threshold after refund", entity.ActorSystem, spend, byKey)
}

// applyTransition persists the tier change + audit row and queues the matching email.
func (e *Engine) applyTransition(ctx context.Context, acc *entity.StorefrontAccount, target entity.StorefrontAccountTier, trigger entity.TierTrigger, reason, actor string, spend decimal.Decimal, byKey map[entity.StorefrontAccountTier]entity.TierConfig) error {
	cur := acc.Tier()
	if err := e.repo.Membership().ApplyTierTransition(ctx, entity.TierTransition{
		AccountID: acc.ID,
		OldTier:   cur,
		NewTier:   target,
		Trigger:   trigger,
		Reason:    reason,
		Actor:     actor,
		SpendEUR:  decimal.NullDecimal{Decimal: spend, Valid: true},
	}); err != nil {
		return fmt.Errorf("apply tier transition: %w", err)
	}

	targetCfg := byKey[target]
	data := &dto.TierChangeEmail{
		Preheader:       "Your GRBPWR membership",
		EmailB64:        " ", // transactional: no unsubscribe link
		Name:            firstName(acc),
		TierDisplay:     displayName(targetCfg, target),
		PrevTierDisplay: displayName(byKey[cur], cur),
		SpendEUR:        formatEUR(spend),
		NextReview:      nextReviewStr(targetCfg, target, e.now()),
	}
	if targetCfg.MinSpendEUR.Valid {
		data.ThresholdEUR = formatEUR(targetCfg.MinSpendEUR.Decimal)
	}

	var mailErr error
	switch trigger {
	case entity.TierTriggerUpgrade, entity.TierTriggerBackfill:
		data.IsBackfill = trigger == entity.TierTriggerBackfill
		mailErr = e.mailer.QueueTierUpgrade(ctx, e.repo, acc.Email, data)
	case entity.TierTriggerRefundRollback:
		mailErr = e.mailer.QueueTierRollback(ctx, e.repo, acc.Email, data)
	case entity.TierTriggerDowngrade:
		mailErr = e.mailer.QueueTierDowngrade(ctx, e.repo, acc.Email, data)
	}
	if mailErr != nil {
		slog.Default().ErrorContext(ctx, "can't queue tier transition email",
			slog.String("email", acc.Email), slog.String("trigger", string(trigger)), slog.String("err", mailErr.Error()))
	}
	return nil
}

func (e *Engine) sendFirstPurchase(ctx context.Context, acc *entity.StorefrontAccount, cfg entity.TierConfig) {
	data := &dto.TierChangeEmail{
		Preheader:   "Thank you for your first order",
		EmailB64:    " ",
		Name:        firstName(acc),
		TierDisplay: displayName(cfg, acc.Tier()),
	}
	if err := e.mailer.QueueFirstPurchaseThanks(ctx, e.repo, acc.Email, data); err != nil {
		slog.Default().ErrorContext(ctx, "can't queue first purchase email", slog.String("err", err.Error()))
	}
}

// ----- formatting helpers -----

func firstName(acc *entity.StorefrontAccount) string {
	return strings.TrimSpace(acc.FirstName)
}

func displayName(cfg entity.TierConfig, fallback entity.StorefrontAccountTier) string {
	if cfg.DisplayName != "" {
		return cfg.DisplayName
	}
	return string(fallback)
}

func nextReviewStr(cfg entity.TierConfig, tier entity.StorefrontAccountTier, now time.Time) string {
	if !entity.IsNumericTier(tier) || tier == entity.StorefrontAccountTierMember || cfg.ExpirationDays <= 0 {
		return ""
	}
	return now.AddDate(0, 0, cfg.ExpirationDays).Format("02 Jan 2006")
}

// formatEUR renders a decimal as an integer euro amount with thousands separators.
func formatEUR(d decimal.Decimal) string {
	s := d.Round(0).String()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	n := len(s)
	if n <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	pre := n % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if n > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < n; i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < n {
			b.WriteString(",")
		}
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
