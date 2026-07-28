package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/campaign"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func campaignTransitionError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "email campaign not found")
	case errors.Is(err, campaign.ErrInvalidCampaignTransition):
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	default:
		return status.Error(codes.Internal, "email campaign transition failed")
	}
}

func (s *Server) validateEmailCampaignLaunch(ctx context.Context, campaignID int) error {
	value, err := s.repo.Campaigns().GetEmailCampaignByID(ctx, campaignID)
	if err != nil {
		slog.ErrorContext(ctx, "validate campaign launch failed", slog.String("err", err.Error()))
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "email campaign not found")
		}
		return status.Error(codes.Internal, "can't validate email campaign launch")
	}
	return validateEmailCampaignLaunchPreconditions(value, cache.GetLanguages())
}

func validateEmailCampaignLaunchPreconditions(
	campaign *entity.EmailCampaignFull,
	languages []entity.Language,
) error {
	if campaign == nil {
		return status.Error(codes.Internal, "can't validate email campaign launch")
	}
	if campaign.ABConfig.Enabled {
		return status.Error(
			codes.FailedPrecondition,
			"A/B campaigns cannot be launched yet — automatic winner selection ships in a later phase",
		)
	}
	for i := range languages {
		if languages[i].IsDefault {
			return nil
		}
	}
	return status.Error(
		codes.FailedPrecondition,
		"email campaigns cannot be launched because no default language is configured",
	)
}

// auditCampaignTransition emits a durable platform log entry for every manual
// lifecycle edge. DigitalOcean retains these JSON logs as the campaign operator
// audit trail; the actor and exact target state are server-stamped.
func auditCampaignTransition(ctx context.Context, campaignID int32, action string) {
	slog.InfoContext(ctx, "email campaign lifecycle transition",
		slog.String("audit_type", "email_campaign_transition"),
		slog.String("actor", authsrv.GetAdminUsername(ctx)),
		slog.Int("campaign_id", int(campaignID)),
		slog.String("action", action))
}

func (s *Server) SendCampaignNow(
	ctx context.Context,
	req *pb_admin.SendCampaignNowRequest,
) (*pb_admin.SendCampaignNowResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	if err := s.mailer.CampaignDispatchConfigured(); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	if err := s.validateEmailCampaignLaunch(ctx, int(req.Id)); err != nil {
		return nil, err
	}
	if err := s.repo.Campaigns().SendEmailCampaignNow(ctx, int(req.Id)); err != nil {
		slog.ErrorContext(ctx, "send campaign now failed", slog.String("err", err.Error()))
		return nil, campaignTransitionError(err)
	}
	auditCampaignTransition(ctx, req.Id, "send_now")
	return &pb_admin.SendCampaignNowResponse{}, nil
}

func (s *Server) ScheduleCampaign(
	ctx context.Context,
	req *pb_admin.ScheduleCampaignRequest,
) (*pb_admin.ScheduleCampaignResponse, error) {
	if req.Id <= 0 || req.ScheduleAt <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id and schedule_at are required")
	}
	if err := s.mailer.CampaignDispatchConfigured(); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	at := time.Unix(req.ScheduleAt, 0).UTC()
	if !at.After(time.Now().UTC()) {
		return nil, status.Error(codes.InvalidArgument, "schedule_at must be in the future")
	}
	if err := s.validateEmailCampaignLaunch(ctx, int(req.Id)); err != nil {
		return nil, err
	}
	if err := s.repo.Campaigns().ScheduleEmailCampaign(ctx, int(req.Id), at); err != nil {
		slog.ErrorContext(ctx, "schedule campaign failed", slog.String("err", err.Error()))
		return nil, campaignTransitionError(err)
	}
	auditCampaignTransition(ctx, req.Id, "schedule")
	return &pb_admin.ScheduleCampaignResponse{}, nil
}

func (s *Server) PauseCampaign(
	ctx context.Context,
	req *pb_admin.PauseCampaignRequest,
) (*pb_admin.PauseCampaignResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	if err := s.repo.Campaigns().PauseEmailCampaign(ctx, int(req.Id), nil); err != nil {
		return nil, campaignTransitionError(err)
	}
	auditCampaignTransition(ctx, req.Id, "pause")
	return &pb_admin.PauseCampaignResponse{}, nil
}

func (s *Server) ResumeCampaign(
	ctx context.Context,
	req *pb_admin.ResumeCampaignRequest,
) (*pb_admin.ResumeCampaignResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	if err := s.mailer.CampaignDispatchConfigured(); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	if err := s.repo.Campaigns().ResumeEmailCampaign(ctx, int(req.Id)); err != nil {
		return nil, campaignTransitionError(err)
	}
	auditCampaignTransition(ctx, req.Id, "resume")
	return &pb_admin.ResumeCampaignResponse{}, nil
}

func (s *Server) CancelCampaign(
	ctx context.Context,
	req *pb_admin.CancelCampaignRequest,
) (*pb_admin.CancelCampaignResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	if err := s.repo.Campaigns().CancelEmailCampaign(ctx, int(req.Id)); err != nil {
		return nil, campaignTransitionError(err)
	}
	auditCampaignTransition(ctx, req.Id, "cancel")
	return &pb_admin.CancelCampaignResponse{}, nil
}

func (s *Server) GetCampaignDispatchStatus(
	ctx context.Context,
	req *pb_admin.GetCampaignDispatchStatusRequest,
) (*pb_admin.GetCampaignDispatchStatusResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	value, err := s.repo.Campaigns().GetEmailCampaignDispatchStatus(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email campaign not found")
		}
		slog.ErrorContext(ctx, "get campaign dispatch status failed", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get campaign dispatch status")
	}
	return &pb_admin.GetCampaignDispatchStatusResponse{
		Status: dto.ConvertEntityEmailCampaignDispatchStatusToPB(value),
	}, nil
}

func (s *Server) GetCampaignRecipients(
	ctx context.Context,
	req *pb_admin.GetCampaignRecipientsRequest,
) (*pb_admin.GetCampaignRecipientsResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 250 {
		return nil, status.Error(codes.InvalidArgument, "limit must be in 1..250")
	}
	page, err := s.repo.Campaigns().GetEmailCampaignRecipients(
		ctx,
		int(req.Id),
		req.AfterId,
		limit,
	)
	if err != nil {
		slog.ErrorContext(ctx, "get campaign recipients failed", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get campaign recipients")
	}
	recipients := make([]*pb_common.EmailCampaignRecipient, len(page.Recipients))
	for i := range page.Recipients {
		recipients[i] = dto.ConvertEntityEmailCampaignRecipientToPB(&page.Recipients[i])
	}
	return &pb_admin.GetCampaignRecipientsResponse{
		Recipients: recipients,
		NextId:     page.NextID,
	}, nil
}
