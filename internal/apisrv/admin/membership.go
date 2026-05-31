package admin

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tiermanagement"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func base64Email(email string) string {
	return base64.StdEncoding.EncodeToString([]byte(email))
}

// adminActor labels manual admin actions in the audit log. The admin API is a
// single full-access role (no RBAC in v1), so a constant actor is sufficient.
const adminActor = "admin"

// hackerInviteBaseURL is where one-time hacker invite links point.
const hackerInviteBaseURL = "https://grbpwr.com/hacker/claim"

const defaultHackerInviteDays = 14

// tierDisplayNames returns a map of tier code -> display name from tier_config.
func (s *Server) tierDisplayNames(ctx context.Context) map[int16]string {
	out := map[int16]string{}
	configs, err := s.repo.Membership().ListTierConfig(ctx)
	if err != nil {
		return out
	}
	for _, c := range configs {
		out[c.TierCode] = c.DisplayName
	}
	return out
}

func (s *Server) ListMembers(ctx context.Context, req *pb_admin.ListMembersRequest) (*pb_admin.ListMembersResponse, error) {
	f := entity.MemberListFilter{
		Email:  strings.TrimSpace(req.GetEmail()),
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
	}
	if t := strings.TrimSpace(req.GetTier()); t != "" && entity.IsValidStorefrontAccountTier(t) {
		tier := entity.StorefrontAccountTier(t)
		f.Tier = &tier
	}
	if st := strings.TrimSpace(req.GetStatus()); st != "" && entity.IsValidStorefrontAccountStatus(st) {
		status := entity.StorefrontAccountStatus(st)
		f.Status = &status
	}
	if req.GetSpendMinEur() > 0 {
		f.SpendMinEUR = decimal.NullDecimal{Decimal: decimal.NewFromFloat(req.GetSpendMinEur()), Valid: true}
	}
	if req.GetSpendMaxEur() > 0 {
		f.SpendMaxEUR = decimal.NullDecimal{Decimal: decimal.NewFromFloat(req.GetSpendMaxEur()), Valid: true}
	}
	if req.GetRegisteredFrom() != nil {
		f.RegisteredFrom = sql.NullTime{Time: req.GetRegisteredFrom().AsTime(), Valid: true}
	}
	if req.GetRegisteredTo() != nil {
		f.RegisteredTo = sql.NullTime{Time: req.GetRegisteredTo().AsTime(), Valid: true}
	}
	if req.GetDaysUntilReviewMax() > 0 {
		f.DaysUntilReviewMax = sql.NullInt64{Int64: int64(req.GetDaysUntilReviewMax()), Valid: true}
	}

	members, total, err := s.repo.Membership().ListMembers(ctx, f)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list members", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list members")
	}
	names := s.tierDisplayNames(ctx)
	out := make([]*pb_admin.Member, 0, len(members))
	for i := range members {
		m := &members[i]
		out = append(out, dto.EntityMemberToPb(m, names[entity.TierCode(m.Account.Tier())]))
	}
	return &pb_admin.ListMembersResponse{Members: out, Total: int32(total)}, nil
}

func (s *Server) GetMember(ctx context.Context, req *pb_admin.GetMemberRequest) (*pb_admin.GetMemberResponse, error) {
	m, err := s.getMemberPb(ctx, int(req.GetUserId()))
	if err != nil {
		return nil, err
	}
	return &pb_admin.GetMemberResponse{Member: m}, nil
}

func (s *Server) getMemberPb(ctx context.Context, userID int) (*pb_admin.Member, error) {
	m, err := s.repo.Membership().GetMember(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "member not found")
		}
		slog.Default().ErrorContext(ctx, "can't get member", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get member")
	}
	names := s.tierDisplayNames(ctx)
	return dto.EntityMemberToPb(m, names[entity.TierCode(m.Account.Tier())]), nil
}

func (s *Server) OverrideTier(ctx context.Context, req *pb_admin.OverrideTierRequest) (*pb_admin.OverrideTierResponse, error) {
	if strings.TrimSpace(req.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	m, err := s.repo.Membership().GetMember(ctx, int(req.GetUserId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "member not found")
		}
		return nil, status.Error(codes.Internal, "can't get member")
	}
	target := entity.TierKeyFromCode(dto.EntityTierCodeFromPb(req.GetNewTier()))
	spend, _ := s.repo.Membership().GetSpendCache(ctx, m.Account.ID)
	var spendND decimal.NullDecimal
	if spend != nil {
		spendND = decimal.NullDecimal{Decimal: spend.AmountEUR, Valid: true}
	}
	if err := s.repo.Membership().ApplyTierTransition(ctx, entity.TierTransition{
		AccountID: m.Account.ID,
		OldTier:   m.Account.Tier(),
		NewTier:   target,
		Trigger:   entity.TierTriggerManual,
		Reason:    req.GetReason(),
		Actor:     adminActor,
		SpendEUR:  spendND,
	}); err != nil {
		slog.Default().ErrorContext(ctx, "can't override tier", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't override tier")
	}
	pb, err := s.getMemberPb(ctx, m.Account.ID)
	if err != nil {
		return nil, err
	}
	return &pb_admin.OverrideTierResponse{Member: pb}, nil
}

func (s *Server) SetMemberStatus(ctx context.Context, req *pb_admin.SetMemberStatusRequest) (*pb_admin.SetMemberStatusResponse, error) {
	st := entity.StorefrontAccountStatus(strings.TrimSpace(req.GetStatus()))
	if st != entity.StorefrontStatusActive && st != entity.StorefrontStatusFrozen {
		return nil, status.Error(codes.InvalidArgument, "status must be active or frozen")
	}
	if err := s.repo.Membership().SetAccountStatus(ctx, int(req.GetUserId()), st); err != nil {
		slog.Default().ErrorContext(ctx, "can't set member status", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set member status")
	}
	return &pb_admin.SetMemberStatusResponse{}, nil
}

func (s *Server) SoftDeleteMember(ctx context.Context, req *pb_admin.SoftDeleteMemberRequest) (*pb_admin.SoftDeleteMemberResponse, error) {
	if err := s.repo.Membership().SoftDeleteAccount(ctx, int(req.GetUserId())); err != nil {
		slog.Default().ErrorContext(ctx, "can't soft-delete member", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't soft-delete member")
	}
	return &pb_admin.SoftDeleteMemberResponse{}, nil
}

func (s *Server) HardEraseMember(ctx context.Context, req *pb_admin.HardEraseMemberRequest) (*pb_admin.HardEraseMemberResponse, error) {
	if !req.GetConfirm() {
		return nil, status.Error(codes.InvalidArgument, "confirm must be true for erasure")
	}
	if err := s.repo.Membership().HardEraseAccount(ctx, int(req.GetUserId())); err != nil {
		slog.Default().ErrorContext(ctx, "can't erase member", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't erase member")
	}
	return &pb_admin.HardEraseMemberResponse{}, nil
}

func (s *Server) GetTierHistory(ctx context.Context, req *pb_admin.GetTierHistoryRequest) (*pb_admin.GetTierHistoryResponse, error) {
	hist, err := s.repo.Membership().ListTierHistory(ctx, int(req.GetUserId()))
	if err != nil {
		return nil, status.Error(codes.Internal, "can't get tier history")
	}
	out := make([]*pb_admin.TierHistoryEntry, 0, len(hist))
	for _, h := range hist {
		out = append(out, dto.EntityTierHistoryToPb(h))
	}
	return &pb_admin.GetTierHistoryResponse{Entries: out}, nil
}

func (s *Server) SendMemberEmail(ctx context.Context, req *pb_admin.SendMemberEmailRequest) (*pb_admin.SendMemberEmailResponse, error) {
	m, err := s.repo.Membership().GetMember(ctx, int(req.GetUserId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "member not found")
		}
		return nil, status.Error(codes.Internal, "can't get member")
	}
	// event_invite is marketing — respect newsletter opt-out.
	if !m.Account.SubscribeNewsletter {
		return nil, status.Error(codes.FailedPrecondition, "member is not subscribed to marketing emails")
	}
	data := &dto.MemberCustomEmail{
		Preheader: req.GetHeading(),
		EmailB64:  base64Email(m.Account.Email),
		Name:      strings.TrimSpace(m.Account.FirstName),
		Heading:   req.GetHeading(),
		Body:      req.GetBody(),
		CTALabel:  req.GetCtaLabel(),
		CTAURL:    req.GetCtaUrl(),
	}
	if err := s.mailer.QueueEventInvite(ctx, s.repo, m.Account.Email, data); err != nil {
		slog.Default().ErrorContext(ctx, "can't queue member email", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't send email")
	}
	return &pb_admin.SendMemberEmailResponse{}, nil
}

func (s *Server) GetTierConfig(ctx context.Context, _ *pb_admin.GetTierConfigRequest) (*pb_admin.GetTierConfigResponse, error) {
	configs, err := s.repo.Membership().ListTierConfig(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't get tier config")
	}
	out := make([]*pb_admin.TierConfigEntry, 0, len(configs))
	for _, c := range configs {
		out = append(out, dto.EntityTierConfigToPb(c))
	}
	return &pb_admin.GetTierConfigResponse{Entries: out}, nil
}

func (s *Server) UpdateTierConfig(ctx context.Context, req *pb_admin.UpdateTierConfigRequest) (*pb_admin.UpdateTierConfigResponse, error) {
	for _, e := range req.GetEntries() {
		if err := s.repo.Membership().UpdateTierConfig(ctx, dto.PbTierConfigToUpdate(e)); err != nil {
			slog.Default().ErrorContext(ctx, "can't update tier config", slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't update tier config")
		}
	}
	return s.tierConfigResponse(ctx)
}

func (s *Server) tierConfigResponse(ctx context.Context) (*pb_admin.UpdateTierConfigResponse, error) {
	configs, err := s.repo.Membership().ListTierConfig(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't get tier config")
	}
	out := make([]*pb_admin.TierConfigEntry, 0, len(configs))
	for _, c := range configs {
		out = append(out, dto.EntityTierConfigToPb(c))
	}
	return &pb_admin.UpdateTierConfigResponse{Entries: out}, nil
}

func (s *Server) GenerateHackerInvite(ctx context.Context, req *pb_admin.GenerateHackerInviteRequest) (*pb_admin.GenerateHackerInviteResponse, error) {
	raw, err := tiermanagement.NewInviteToken()
	if err != nil {
		return nil, status.Error(codes.Internal, "can't generate token")
	}
	days := int(req.GetExpiresInDays())
	if days <= 0 {
		days = defaultHackerInviteDays
	}
	expiresAt := time.Now().UTC().AddDate(0, 0, days)
	var email sql.NullString
	if e := strings.TrimSpace(req.GetEmail()); e != "" {
		email = sql.NullString{String: strings.ToLower(e), Valid: true}
	}
	id, err := s.repo.Membership().CreateHackerInvite(ctx, tiermanagement.HashToken(raw), email, adminActor, expiresAt)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create hacker invite", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't create invite")
	}
	inviteURL := fmt.Sprintf("%s?token=%s", hackerInviteBaseURL, raw)

	// If pre-bound to an email, send the invite link directly.
	if email.Valid {
		data := &dto.HackerInviteEmail{
			Preheader: "Your GRBPWR HACKER invite",
			EmailB64:  " ",
			InviteURL: inviteURL,
			ExpiresAt: expiresAt.Format("02 Jan 2006"),
		}
		if err := s.mailer.QueueHackerInvite(ctx, s.repo, email.String, data); err != nil {
			slog.Default().ErrorContext(ctx, "can't queue hacker invite email", slog.String("err", err.Error()))
		}
	}
	return &pb_admin.GenerateHackerInviteResponse{
		InviteId:  id,
		InviteUrl: inviteURL,
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

func (s *Server) ListHackerInvites(ctx context.Context, req *pb_admin.ListHackerInvitesRequest) (*pb_admin.ListHackerInvitesResponse, error) {
	now := time.Now().UTC()
	invites, err := s.repo.Membership().ListHackerInvites(ctx, req.GetActiveOnly(), now)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't list invites")
	}
	out := make([]*pb_admin.HackerInvite, 0, len(invites))
	for _, inv := range invites {
		out = append(out, dto.EntityHackerInviteToPb(inv, now))
	}
	return &pb_admin.ListHackerInvitesResponse{Invites: out}, nil
}

func (s *Server) RevokeHackerInvite(ctx context.Context, req *pb_admin.RevokeHackerInviteRequest) (*pb_admin.RevokeHackerInviteResponse, error) {
	if err := s.repo.Membership().RevokeHackerInvite(ctx, req.GetInviteId()); err != nil {
		return nil, status.Error(codes.Internal, "can't revoke invite")
	}
	return &pb_admin.RevokeHackerInviteResponse{}, nil
}

func (s *Server) ListHackerAccounts(ctx context.Context, _ *pb_admin.ListHackerAccountsRequest) (*pb_admin.ListHackerAccountsResponse, error) {
	members, err := s.repo.Membership().ListHackerAccounts(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't list hacker accounts")
	}
	names := s.tierDisplayNames(ctx)
	out := make([]*pb_admin.Member, 0, len(members))
	for i := range members {
		m := &members[i]
		out = append(out, dto.EntityMemberToPb(m, names[entity.TierCode(m.Account.Tier())]))
	}
	return &pb_admin.ListHackerAccountsResponse{Members: out}, nil
}

func (s *Server) RevokeHackerStatus(ctx context.Context, req *pb_admin.RevokeHackerStatusRequest) (*pb_admin.RevokeHackerStatusResponse, error) {
	if err := tiermanagement.NewEngine(s.repo, s.mailer).RevokeHackerStatus(ctx, int(req.GetUserId()), adminActor); err != nil {
		slog.Default().ErrorContext(ctx, "can't revoke hacker status", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't revoke hacker status")
	}
	pb, err := s.getMemberPb(ctx, int(req.GetUserId()))
	if err != nil {
		return nil, err
	}
	return &pb_admin.RevokeHackerStatusResponse{Member: pb}, nil
}

func (s *Server) GetTierAuditLog(ctx context.Context, req *pb_admin.GetTierAuditLogRequest) (*pb_admin.GetTierAuditLogResponse, error) {
	f := entity.TierAuditFilter{
		Actor:   strings.TrimSpace(req.GetActor()),
		Trigger: strings.TrimSpace(req.GetTriggerType()),
		Limit:   int(req.GetLimit()),
		Offset:  int(req.GetOffset()),
	}
	if req.GetUserId() > 0 {
		id := int(req.GetUserId())
		f.AccountID = &id
	}
	if req.GetFrom() != nil {
		f.From = sql.NullTime{Time: req.GetFrom().AsTime(), Valid: true}
	}
	if req.GetTo() != nil {
		f.To = sql.NullTime{Time: req.GetTo().AsTime(), Valid: true}
	}
	entries, total, err := s.repo.Membership().ListAuditLog(ctx, f)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't get audit log")
	}
	out := make([]*pb_admin.TierHistoryEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, dto.EntityTierHistoryToPb(e))
	}
	return &pb_admin.GetTierAuditLogResponse{Entries: out, Total: int32(total)}, nil
}

func (s *Server) SetProductTierAccess(ctx context.Context, req *pb_admin.SetProductTierAccessRequest) (*pb_admin.SetProductTierAccessResponse, error) {
	minTier := dto.EntityTierCodeFromPb(req.GetMinTier())
	if err := s.repo.Products().SetProductTierAccess(ctx, int(req.GetProductId()), minTier, req.GetHiddenForNonQualified()); err != nil {
		slog.Default().ErrorContext(ctx, "can't set product tier access", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set product tier access")
	}
	if s.re != nil {
		if err := s.re.RevalidateAll(ctx, &dto.RevalidationData{}); err != nil {
			slog.Default().ErrorContext(ctx, "can't revalidate after tier access change", slog.String("err", err.Error()))
		}
	}
	return &pb_admin.SetProductTierAccessResponse{}, nil
}

func (s *Server) RunTierBackfill(ctx context.Context, req *pb_admin.RunTierBackfillRequest) (*pb_admin.RunTierBackfillResponse, error) {
	if !req.GetConfirm() {
		return nil, status.Error(codes.InvalidArgument, "confirm must be true to run backfill")
	}
	res, err := tiermanagement.NewEngine(s.repo, s.mailer).RunBackfill(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "tier backfill failed", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "tier backfill failed")
	}
	return &pb_admin.RunTierBackfillResponse{
		OrdersSnapshotted: res.OrdersSnapshotted,
		AccountsProcessed: int32(res.AccountsProcessed),
		AccountsUpgraded:  int32(res.AccountsUpgraded),
	}, nil
}
