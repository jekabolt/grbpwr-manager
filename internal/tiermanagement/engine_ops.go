package tiermanagement

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// oneLevelDown returns the tier one numeric level below cur (plus_plus->plus->member).
func oneLevelDown(cur entity.StorefrontAccountTier) entity.StorefrontAccountTier {
	switch cur {
	case entity.StorefrontAccountTierPlusPlus:
		return entity.StorefrontAccountTierPlus
	case entity.StorefrontAccountTierPlus:
		return entity.StorefrontAccountTierMember
	default:
		return entity.StorefrontAccountTierMember
	}
}

// RunDailyTierReview processes accounts whose review date has passed: it
// recomputes spend and either renews the tier (if still qualified) or downgrades
// exactly one level. Returns the number of accounts downgraded.
func (e *Engine) RunDailyTierReview(ctx context.Context) (int, error) {
	now := e.now()
	accs, err := e.repo.Membership().ListAccountsForDowngradeReview(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("list downgrade candidates: %w", err)
	}
	configs, byKey, err := e.loadConfigs(ctx)
	if err != nil {
		return 0, err
	}
	downgraded := 0
	for i := range accs {
		acc := &accs[i]
		if err := ctx.Err(); err != nil {
			return downgraded, err
		}
		spend, err := e.RecomputeSpend(ctx, acc)
		if err != nil {
			slog.Default().ErrorContext(ctx, "review: can't recompute spend", slog.String("email", acc.Email), slog.String("err", err.Error()))
			continue
		}
		cur := acc.Tier()
		qualified := entity.MaxTierQualified(spend, configs)
		// Still qualifies for current tier → renew the review window, no change.
		if entity.TierCode(qualified) >= entity.TierCode(cur) {
			if err := e.renewReview(ctx, acc, byKey[cur]); err != nil {
				slog.Default().ErrorContext(ctx, "review: can't renew", slog.String("email", acc.Email), slog.String("err", err.Error()))
			}
			continue
		}
		target := oneLevelDown(cur)
		if err := e.applyTransition(ctx, acc, target, entity.TierTriggerDowngrade, "tier maintenance cycle elapsed without qualifying spend", entity.ActorSystem, spend, byKey); err != nil {
			slog.Default().ErrorContext(ctx, "review: can't downgrade", slog.String("email", acc.Email), slog.String("err", err.Error()))
			continue
		}
		downgraded++
	}
	return downgraded, nil
}

// renewReview pushes the next review date forward by the tier's expiration window
// without changing the tier (member who maintained spend keeps their tier).
func (e *Engine) renewReview(ctx context.Context, acc *entity.StorefrontAccount, cfg entity.TierConfig) error {
	if cfg.ExpirationDays <= 0 {
		return nil
	}
	// Re-applying the same tier refreshes tier_upgrade_date + next_review_date.
	return e.repo.Membership().ApplyTierTransition(ctx, entity.TierTransition{
		AccountID: acc.ID,
		OldTier:   acc.Tier(),
		NewTier:   acc.Tier(),
		Trigger:   entity.TierTriggerUpgrade, // logged as a maintenance renewal
		Reason:    "tier maintained for another cycle",
		Actor:     entity.ActorSystem,
	})
}

// RunDowngradeReminders emails members whose review is reminderDays away.
// Returns the number of reminders queued.
func (e *Engine) RunDowngradeReminders(ctx context.Context) (int, error) {
	now := e.now()
	_, byKey, err := e.loadConfigs(ctx)
	if err != nil {
		return 0, err
	}
	reminderDays := maxReminderDays(byKey)
	accs, err := e.repo.Membership().ListAccountsForDowngradeReminder(ctx, now, reminderDays)
	if err != nil {
		return 0, fmt.Errorf("list reminder candidates: %w", err)
	}
	sent := 0
	for i := range accs {
		acc := &accs[i]
		spend, _ := e.repo.Membership().GetSpendCache(ctx, acc.ID)
		cur := acc.Tier()
		cfg := byKey[cur]
		data := &dto.TierChangeEmail{
			Preheader:   "Your GRBPWR tier is up for review",
			EmailB64:    " ",
			Name:        firstName(acc),
			TierDisplay: displayName(cfg, cur),
		}
		if acc.NextReviewDate.Valid {
			data.NextReview = acc.NextReviewDate.Time.Format("02 Jan 2006")
		}
		if spend != nil {
			data.SpendEUR = formatEUR(spend.AmountEUR)
		}
		if cfg.MinSpendEUR.Valid {
			data.ThresholdEUR = formatEUR(cfg.MinSpendEUR.Decimal)
		}
		if err := e.mailer.QueueDowngradeReminder(ctx, e.repo, acc.Email, data); err != nil {
			slog.Default().ErrorContext(ctx, "can't queue downgrade reminder", slog.String("email", acc.Email), slog.String("err", err.Error()))
			continue
		}
		sent++
	}
	return sent, nil
}

func maxReminderDays(byKey map[entity.StorefrontAccountTier]entity.TierConfig) int {
	d := 30
	for _, t := range []entity.StorefrontAccountTier{entity.StorefrontAccountTierPlus, entity.StorefrontAccountTierPlusPlus} {
		if c, ok := byKey[t]; ok && c.ReminderDaysBefore > d {
			d = c.ReminderDaysBefore
		}
	}
	return d
}

// RunBirthdayGifts emails active, newsletter-subscribed members whose birthday is
// today (marketing — opt-out respected). Returns the number queued.
func (e *Engine) RunBirthdayGifts(ctx context.Context) (int, error) {
	now := e.now()
	accs, err := e.repo.Membership().ListAccountsWithBirthday(ctx, int(now.Month()), now.Day())
	if err != nil {
		return 0, fmt.Errorf("list birthday accounts: %w", err)
	}
	sent := 0
	for i := range accs {
		acc := &accs[i]
		if !acc.SubscribeNewsletter { // marketing gate
			continue
		}
		data := &dto.BirthdayEmail{
			Preheader: "A little something from GRBPWR",
			EmailB64:  base64.StdEncoding.EncodeToString([]byte(acc.Email)),
			Name:      firstName(acc),
		}
		if err := e.mailer.QueueBirthdayGift(ctx, e.repo, acc.Email, data); err != nil {
			slog.Default().ErrorContext(ctx, "can't queue birthday gift", slog.String("email", acc.Email), slog.String("err", err.Error()))
			continue
		}
		sent++
	}
	return sent, nil
}

// BackfillResult summarises a legacy backfill run.
type BackfillResult struct {
	OrdersSnapshotted int64
	AccountsProcessed int
	AccountsUpgraded  int
}

// RunBackfill is the one-time launch job: it snapshots EUR order totals, then for
// every account recomputes 12-month spend and assigns the qualifying tier, with
// tier_upgrade_date anchored at the launch date (now). Hacker accounts are left
// untouched. Upgraded members get a backfill notification.
func (e *Engine) RunBackfill(ctx context.Context) (*BackfillResult, error) {
	res := &BackfillResult{}
	n, err := e.repo.Membership().BackfillOrderEURSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("backfill EUR snapshots: %w", err)
	}
	res.OrdersSnapshotted = n

	configs, byKey, err := e.loadConfigs(ctx)
	if err != nil {
		return nil, err
	}

	const page = 200
	offset := 0
	for {
		members, _, err := e.repo.Membership().ListMembers(ctx, entity.MemberListFilter{Limit: page, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("list members for backfill: %w", err)
		}
		if len(members) == 0 {
			break
		}
		for i := range members {
			acc := &members[i].Account
			if err := ctx.Err(); err != nil {
				return res, err
			}
			res.AccountsProcessed++
			if acc.Status == entity.StorefrontStatusErased || acc.Status == entity.StorefrontStatusDeleted {
				continue
			}
			spend, err := e.RecomputeSpend(ctx, acc)
			if err != nil {
				slog.Default().ErrorContext(ctx, "backfill: recompute spend", slog.String("email", acc.Email), slog.String("err", err.Error()))
				continue
			}
			if acc.Tier() == entity.StorefrontAccountTierHacker {
				continue
			}
			target := entity.MaxTierQualified(spend, configs)
			if entity.TierCode(target) <= entity.TierCode(acc.Tier()) {
				continue
			}
			if err := e.applyTransition(ctx, acc, target, entity.TierTriggerBackfill, "legacy spend backfill at launch", entity.ActorSystem, spend, byKey); err != nil {
				slog.Default().ErrorContext(ctx, "backfill: apply", slog.String("email", acc.Email), slog.String("err", err.Error()))
				continue
			}
			res.AccountsUpgraded++
		}
		offset += page
	}
	return res, nil
}

// GrantHackerByInvite consumes a one-time invite token and moves the matching
// account to the hacker tier. The account is created if it doesn't exist yet.
func (e *Engine) GrantHackerByInvite(ctx context.Context, rawToken, email string) (*entity.StorefrontAccount, error) {
	acc, err := e.repo.StorefrontAccount().GetOrCreateAccountByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return nil, fmt.Errorf("get/create account: %w", err)
	}
	if _, err := e.repo.Membership().ConsumeHackerInvite(ctx, HashToken(rawToken), acc.ID, e.now()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInviteInvalid
		}
		return nil, fmt.Errorf("consume invite: %w", err)
	}
	_, byKey, err := e.loadConfigs(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.applyTransition(ctx, acc, entity.StorefrontAccountTierHacker, entity.TierTriggerHackerGrant, "hacker invite redeemed", entity.ActorSystem, decimal.Zero, byKey); err != nil {
		return nil, err
	}
	return acc, nil
}

// RevokeHackerStatus moves a hacker account back to the tier its spend qualifies for.
func (e *Engine) RevokeHackerStatus(ctx context.Context, accountID int, actor string) error {
	m, err := e.repo.Membership().GetMember(ctx, accountID)
	if err != nil {
		return fmt.Errorf("get member: %w", err)
	}
	acc := &m.Account
	if acc.Tier() != entity.StorefrontAccountTierHacker {
		return fmt.Errorf("account %d is not on the hacker tier", accountID)
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
	return e.applyTransition(ctx, acc, target, entity.TierTriggerHackerRevoke, "hacker status revoked", actor, spend, byKey)
}

// OnNewsletterUnsubscribed queues an opt-out confirmation (transactional).
func (e *Engine) OnNewsletterUnsubscribed(ctx context.Context, email string) error {
	acc, err := e.getActiveAccount(ctx, email)
	name := ""
	if err == nil && acc != nil {
		name = firstName(acc)
	}
	return e.mailer.QueueUnsubscribeConfirmation(ctx, e.repo, email, &dto.UnsubscribeConfirmationEmail{
		Preheader: "You've been unsubscribed",
		EmailB64:  " ",
		Name:      name,
	})
}

// ErrInviteInvalid is returned when a hacker invite token is unknown/expired/used.
var ErrInviteInvalid = errors.New("invite invalid or expired")
