package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/mail/campaignrender"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxCampaignTestRecipients = 10

func parseCampaignTestRecipientAllowlist(value string) map[string]struct{} {
	allowlist := make(map[string]struct{})
	for _, recipient := range strings.Split(value, ",") {
		recipient = strings.TrimSpace(recipient)
		if recipient != "" {
			allowlist[strings.ToLower(recipient)] = struct{}{}
		}
	}
	return allowlist
}

// validateCampaignTestRecipients normalizes and de-duplicates the requested test recipients and
// enforces the configured allowlist. It is deliberately fail-CLOSED: an unset
// MAILER_TEST_RECIPIENTS used to mean "any address", which turned this RPC into a way to send
// brand-domain mail to arbitrary external addresses with no ledger row.
func validateCampaignTestRecipients(
	recipients []string,
	allowlist map[string]struct{},
) ([]string, error) {
	if len(allowlist) == 0 {
		return nil, status.Error(
			codes.FailedPrecondition,
			"campaign test recipients are not configured: set MAILER_TEST_RECIPIENTS to the allowed addresses",
		)
	}
	seen := make(map[string]struct{}, len(recipients))
	out := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		offendingAddress := strings.TrimSpace(recipient)
		if offendingAddress == "" {
			return nil, status.Error(codes.InvalidArgument, "test recipient must not be empty")
		}
		normalized, err := mail.NormalizeEmailAddress(offendingAddress)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid test recipient %q: %v", offendingAddress, err)
		}
		key := strings.ToLower(normalized)
		if _, ok := allowlist[key]; !ok {
			return nil, status.Errorf(
				codes.PermissionDenied,
				"test recipient %q is not allowlisted",
				offendingAddress,
			)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func (s *Server) campaignForRender(
	ctx context.Context,
	campaignID int32,
	draft *pb_common.EmailCampaignInsert,
) (*entity.EmailCampaignInsert, error) {
	if draft != nil {
		if err := validateEmailCampaignBlockDepth(draft); err != nil {
			return nil, err
		}
		campaign, err := dto.ConvertPbEmailCampaignInsertToEntity(draft)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid campaign draft: %v", err)
		}
		return campaign, nil
	}
	if campaignID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign_id or draft is required")
	}
	campaign, err := s.repo.Campaigns().GetEmailCampaignByID(ctx, int(campaignID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email campaign not found")
		}
		slog.ErrorContext(ctx, "can't load email campaign for render", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load email campaign")
	}
	return &campaign.EmailCampaignInsert, nil
}

func selectCampaignVariant(
	campaign *entity.EmailCampaignInsert,
	variantID int32,
) ([]entity.EmailBlock, []entity.SubjectTranslation, error) {
	if campaign == nil {
		return nil, nil, status.Error(codes.InvalidArgument, "campaign is required")
	}
	if len(campaign.Variants) == 0 {
		// Partially-built inline drafts can still render their campaign body.
		return campaign.Body, nil, nil
	}

	variant := &campaign.Variants[0]
	if variantID > 0 {
		variant = nil
		for i := range campaign.Variants {
			if campaign.Variants[i].ID == int(variantID) {
				variant = &campaign.Variants[i]
				break
			}
		}
		if variant == nil {
			return nil, nil, status.Error(codes.InvalidArgument, "email campaign variant not found")
		}
	}

	blocks := campaign.Body
	if campaign.ABConfig.Enabled &&
		campaign.ABConfig.Dimension == entity.ABDimensionContent &&
		len(variant.Body) > 0 {
		blocks = variant.Body
	}
	return blocks, variant.SubjectI18n, nil
}

func (s *Server) renderCampaign(
	ctx context.Context,
	campaignID int32,
	draft *pb_common.EmailCampaignInsert,
	languageID, variantID int32,
) (campaignrender.Rendered, string, []campaignrender.Warning, error) {
	campaign, err := s.campaignForRender(ctx, campaignID, draft)
	if err != nil {
		return campaignrender.Rendered{}, "", nil, err
	}
	blocks, subjects, err := selectCampaignVariant(campaign, variantID)
	if err != nil {
		return campaignrender.Rendered{}, "", nil, err
	}
	langs := cache.GetLanguages()
	rendered, warnings, err := s.renderer.Render(ctx, s.repo, campaignrender.Input{
		Blocks:          blocks,
		BackgroundColor: campaign.BackgroundColor,
		LanguageID:      int(languageID),
		Langs:           langs,
		UnsubscribeURL:  "",
		Footer:          s.mailer.CampaignFooterStrings(int(languageID), langs),
	})
	if err != nil {
		slog.ErrorContext(ctx, "can't render email campaign", slog.String("err", err.Error()))
		return campaignrender.Rendered{}, "", nil, status.Error(codes.Internal, "can't render email campaign")
	}
	subject := campaignrender.SelectSubject(subjects, int(languageID), langs)
	return rendered, subject, warnings, nil
}

func campaignWarningsToPB(in []campaignrender.Warning) []*pb_common.RenderWarning {
	out := make([]*pb_common.RenderWarning, len(in))
	for i := range in {
		out[i] = &pb_common.RenderWarning{
			BlockIndex: int32(in[i].BlockIndex),
			Reason:     in[i].Reason,
		}
	}
	return out
}

func (s *Server) sendCampaignTestRecipients(
	ctx context.Context,
	recipients []string,
	subject, htmlBody, textBody string,
) error {
	mailRepository := s.repo.Mail()
	for _, recipient := range recipients {
		suppressed, err := mailRepository.IsSuppressed(ctx, recipient)
		if err != nil {
			slog.ErrorContext(ctx, "can't check email campaign test suppression",
				slog.String("recipient", recipient),
				slog.String("err", err.Error()),
			)
			return status.Error(codes.Internal, "can't check email campaign test recipient")
		}
		if suppressed {
			slog.InfoContext(ctx, "skipping suppressed email campaign test recipient",
				slog.String("recipient", recipient),
			)
			continue
		}
		if err := s.mailer.SendCampaignTest(
			ctx, s.repo, recipient, subject, htmlBody, textBody,
		); err != nil {
			slog.ErrorContext(ctx, "can't send email campaign test",
				slog.String("recipient", recipient),
				slog.String("err", err.Error()),
			)
			return status.Error(codes.Internal, "can't send email campaign test")
		}
	}
	return nil
}

// RenderCampaignPreview renders either a saved campaign or an unsaved draft.
// It is deliberately read-only and omits the recipient-specific unsubscribe URL.
func (s *Server) RenderCampaignPreview(
	ctx context.Context,
	req *pb_admin.RenderCampaignPreviewRequest,
) (*pb_admin.RenderCampaignPreviewResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	var campaignID int32
	var draft *pb_common.EmailCampaignInsert
	switch source := req.Source.(type) {
	case *pb_admin.RenderCampaignPreviewRequest_CampaignId:
		campaignID = source.CampaignId
	case *pb_admin.RenderCampaignPreviewRequest_Draft:
		draft = source.Draft
	default:
		return nil, status.Error(codes.InvalidArgument, "campaign_id or draft is required")
	}

	rendered, subject, warnings, err := s.renderCampaign(
		ctx, campaignID, draft, req.LanguageId, req.VariantId,
	)
	if err != nil {
		return nil, err
	}
	return &pb_admin.RenderCampaignPreviewResponse{
		Html:     rendered.HTML,
		Text:     rendered.Text,
		Subject:  subject,
		Warnings: campaignWarningsToPB(warnings),
	}, nil
}

// SendTestEmail renders through the same deterministic path as preview, then
// immediately dispatches a small, admin-requested recipient set.
func (s *Server) SendTestEmail(
	ctx context.Context,
	req *pb_admin.SendTestEmailRequest,
) (*pb_admin.SendTestEmailResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if len(req.ToEmails) == 0 || len(req.ToEmails) > maxCampaignTestRecipients {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"to_emails must contain 1..%d recipients",
			maxCampaignTestRecipients,
		)
	}
	recipients, err := validateCampaignTestRecipients(
		req.ToEmails,
		s.campaignTestRecipientAllowlist,
	)
	if err != nil {
		return nil, err
	}

	select {
	case s.campaignTestSem <- struct{}{}:
		defer func() { <-s.campaignTestSem }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "too many concurrent campaign test sends")
	}

	var campaignID int32
	var draft *pb_common.EmailCampaignInsert
	switch source := req.Source.(type) {
	case *pb_admin.SendTestEmailRequest_CampaignId:
		campaignID = source.CampaignId
	case *pb_admin.SendTestEmailRequest_Draft:
		draft = source.Draft
	default:
		return nil, status.Error(codes.InvalidArgument, "campaign_id or draft is required")
	}

	rendered, subject, _, err := s.renderCampaign(
		ctx, campaignID, draft, req.LanguageId, req.VariantId,
	)
	if err != nil {
		return nil, err
	}
	subject = "[TEST] " + strings.TrimSpace(subject)

	if err := s.sendCampaignTestRecipients(
		ctx, recipients, subject, rendered.HTML, rendered.Text,
	); err != nil {
		return nil, err
	}
	return &pb_admin.SendTestEmailResponse{}, nil
}
