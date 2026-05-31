package mail

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

// QueueTierUpgrade queues a tier upgrade (or backfill) notification. Transactional.
func (m *Mailer) QueueTierUpgrade(ctx context.Context, rep dependency.Repository, to string, data *dto.TierChangeEmail) error {
	tmpl := TierUpgrade
	ser, err := m.buildSendMailRequest(to, tmpl, data)
	if err != nil {
		return fmt.Errorf("can't build tier upgrade email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueTierDowngrade queues a tier downgrade notification. Transactional.
func (m *Mailer) QueueTierDowngrade(ctx context.Context, rep dependency.Repository, to string, data *dto.TierChangeEmail) error {
	ser, err := m.buildSendMailRequest(to, TierDowngrade, data)
	if err != nil {
		return fmt.Errorf("can't build tier downgrade email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueDowngradeReminder queues a "tier up for review" reminder. Transactional.
func (m *Mailer) QueueDowngradeReminder(ctx context.Context, rep dependency.Repository, to string, data *dto.TierChangeEmail) error {
	ser, err := m.buildSendMailRequest(to, DowngradeReminder, data)
	if err != nil {
		return fmt.Errorf("can't build downgrade reminder email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueTierRollback queues a refund-triggered tier rollback notification. Transactional.
func (m *Mailer) QueueTierRollback(ctx context.Context, rep dependency.Repository, to string, data *dto.TierChangeEmail) error {
	ser, err := m.buildSendMailRequest(to, TierRollbackAfterRefund, data)
	if err != nil {
		return fmt.Errorf("can't build tier rollback email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueFirstPurchaseThanks queues a first-purchase thank-you. Transactional.
func (m *Mailer) QueueFirstPurchaseThanks(ctx context.Context, rep dependency.Repository, to string, data *dto.TierChangeEmail) error {
	ser, err := m.buildSendMailRequest(to, FirstPurchaseThanks, data)
	if err != nil {
		return fmt.Errorf("can't build first purchase email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueUnsubscribeConfirmation queues a newsletter opt-out confirmation. Transactional.
func (m *Mailer) QueueUnsubscribeConfirmation(ctx context.Context, rep dependency.Repository, to string, data *dto.UnsubscribeConfirmationEmail) error {
	ser, err := m.buildSendMailRequest(to, UnsubscribeConfirmation, data)
	if err != nil {
		return fmt.Errorf("can't build unsubscribe confirmation email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueBirthdayGift queues a birthday gift email. Marketing.
func (m *Mailer) QueueBirthdayGift(ctx context.Context, rep dependency.Repository, to string, data *dto.BirthdayEmail) error {
	ser, err := m.buildSendMailRequest(to, BirthdayGift, data)
	if err != nil {
		return fmt.Errorf("can't build birthday email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueEventInvite queues an admin-authored event invite. Marketing.
func (m *Mailer) QueueEventInvite(ctx context.Context, rep dependency.Repository, to string, data *dto.MemberCustomEmail) error {
	ser, err := m.buildSendMailRequest(to, EventInvite, data)
	if err != nil {
		return fmt.Errorf("can't build event invite email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueHackerInvite queues a one-time hacker-tier invite link. Transactional.
func (m *Mailer) QueueHackerInvite(ctx context.Context, rep dependency.Repository, to string, data *dto.HackerInviteEmail) error {
	ser, err := m.buildSendMailRequest(to, HackerInvite, data)
	if err != nil {
		return fmt.Errorf("can't build hacker invite email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}
