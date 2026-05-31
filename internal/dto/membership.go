package dto

import (
	"database/sql"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PbTierCode maps a numeric tier code to the admin proto enum.
func PbTierCode(code int16) pb_admin.TierCode {
	switch code {
	case entity.TierCodePlus:
		return pb_admin.TierCode_TIER_CODE_PLUS
	case entity.TierCodePlusPlus:
		return pb_admin.TierCode_TIER_CODE_PLUS_PLUS
	case entity.TierCodeHacker:
		return pb_admin.TierCode_TIER_CODE_HACKER
	default:
		return pb_admin.TierCode_TIER_CODE_MEMBER
	}
}

// EntityTierCodeFromPb maps the admin proto enum to a numeric tier code.
func EntityTierCodeFromPb(c pb_admin.TierCode) int16 {
	switch c {
	case pb_admin.TierCode_TIER_CODE_PLUS:
		return entity.TierCodePlus
	case pb_admin.TierCode_TIER_CODE_PLUS_PLUS:
		return entity.TierCodePlusPlus
	case pb_admin.TierCode_TIER_CODE_HACKER:
		return entity.TierCodeHacker
	default:
		return entity.TierCodeMember
	}
}

func nullTimeToPb(t sql.NullTime) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time)
}

func decimalToFloat(d decimal.Decimal) float64 {
	f, _ := d.Float64()
	return f
}

// EntityMemberToPb converts a membership aggregate to the admin proto Member.
func EntityMemberToPb(m *entity.Member, displayName string) *pb_admin.Member {
	if m == nil {
		return nil
	}
	a := m.Account
	name := a.FirstName
	if a.LastName != "" {
		if name != "" {
			name += " "
		}
		name += a.LastName
	}
	pb := &pb_admin.Member{
		UserId:                  int64(a.ID),
		Email:                   a.Email,
		Name:                    name,
		CurrentTier:             PbTierCode(entity.TierCode(a.Tier())),
		CurrentTierDisplay:      displayName,
		QualifyingSpendEur_12Mo: decimalToFloat(m.QualifyingSpendEUR),
		TierUpgradeDate:         nullTimeToPb(a.TierUpgradeDate),
		NextReviewDate:          nullTimeToPb(a.NextReviewDate),
		Status:                  string(a.Status),
		SubscribeNewsletter:     a.SubscribeNewsletter,
		SubscribeNewArrivals:    a.SubscribeNewArrivals,
		SubscribeEvents:         a.SubscribeEvents,
		CreatedAt:               timestamppb.New(a.CreatedAt),
		BirthDate:               nullTimeToPb(a.BirthDate),
		LastOrderDate:           nullTimeToPb(m.LastOrderDate),
	}
	if a.Phone.Valid {
		pb.Phone = a.Phone.String
	}
	return pb
}

// EntityTierConfigToPb converts a tier config row to proto.
func EntityTierConfigToPb(c entity.TierConfig) *pb_admin.TierConfigEntry {
	e := &pb_admin.TierConfigEntry{
		TierCode:           PbTierCode(c.TierCode),
		TierKey:            c.TierKey,
		DisplayName:        c.DisplayName,
		ExpirationDays:     int32(c.ExpirationDays),
		ReminderDaysBefore: int32(c.ReminderDaysBefore),
		IsInviteOnly:       c.IsInviteOnly,
	}
	if c.MinSpendEUR.Valid {
		e.HasMinSpend = true
		e.MinSpendEur = decimalToFloat(c.MinSpendEUR.Decimal)
	}
	if c.WelcomePackSlots.Valid {
		e.HasWelcomePackSlots = true
		e.WelcomePackSlots = int32(c.WelcomePackSlots.Int64)
	}
	return e
}

// PbTierConfigToUpdate converts a proto entry to a tier config update.
func PbTierConfigToUpdate(e *pb_admin.TierConfigEntry) entity.TierConfigUpdate {
	upd := entity.TierConfigUpdate{
		TierCode:           EntityTierCodeFromPb(e.GetTierCode()),
		DisplayName:        e.GetDisplayName(),
		ExpirationDays:     int(e.GetExpirationDays()),
		ReminderDaysBefore: int(e.GetReminderDaysBefore()),
	}
	if e.GetHasMinSpend() {
		upd.MinSpendEUR = decimal.NullDecimal{Decimal: decimal.NewFromFloat(e.GetMinSpendEur()), Valid: true}
	}
	if e.GetHasWelcomePackSlots() {
		upd.WelcomePackSlots = sql.NullInt64{Int64: int64(e.GetWelcomePackSlots()), Valid: true}
	}
	return upd
}

// EntityTierHistoryToPb converts a tier history row to proto.
func EntityTierHistoryToPb(h entity.TierHistoryEntry) *pb_admin.TierHistoryEntry {
	e := &pb_admin.TierHistoryEntry{
		Id:          h.ID,
		OldTier:     h.OldTier,
		NewTier:     h.NewTier,
		TriggerType: h.TriggerType,
		Actor:       h.Actor,
		CreatedAt:   timestamppb.New(h.CreatedAt),
	}
	if h.Reason.Valid {
		e.Reason = h.Reason.String
	}
	if h.SpendEURAtChange.Valid {
		e.SpendEurAtChange = decimalToFloat(h.SpendEURAtChange.Decimal)
	}
	return e
}

// EntityHackerInviteToPb converts a hacker invite row to proto.
func EntityHackerInviteToPb(h entity.HackerInvite, now time.Time) *pb_admin.HackerInvite {
	e := &pb_admin.HackerInvite{
		Id:        h.ID,
		CreatedBy: h.CreatedBy,
		ExpiresAt: timestamppb.New(h.ExpiresAt),
		CreatedAt: timestamppb.New(h.CreatedAt),
		Active:    h.IsActive(now),
	}
	if h.Email.Valid {
		e.Email = h.Email.String
	}
	e.ConsumedAt = nullTimeToPb(h.ConsumedAt)
	e.RevokedAt = nullTimeToPb(h.RevokedAt)
	return e
}
