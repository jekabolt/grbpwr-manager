package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail/campaignrender"
	"github.com/jekabolt/grbpwr-manager/internal/segment"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxEmailCampaignBlockDepth = 6

func emailCampaignBlocksExceedMaxDepth(blocks []*pb_common.EmailBlock, depth int) bool {
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if depth > maxEmailCampaignBlockDepth {
			return true
		}
		if block.TwoColumn != nil &&
			(emailCampaignBlocksExceedMaxDepth(block.TwoColumn.Left, depth+1) ||
				emailCampaignBlocksExceedMaxDepth(block.TwoColumn.Right, depth+1)) {
			return true
		}
	}
	return false
}

func validateEmailCampaignBlockDepth(in *pb_common.EmailCampaignInsert) error {
	if in == nil {
		return status.Error(codes.InvalidArgument, "campaign is required")
	}
	if emailCampaignBlocksExceedMaxDepth(in.Body, 1) {
		return status.Errorf(
			codes.InvalidArgument,
			"campaign body exceeds maximum block nesting depth of %d",
			maxEmailCampaignBlockDepth,
		)
	}
	for i, variant := range in.Variants {
		if variant != nil && emailCampaignBlocksExceedMaxDepth(variant.Body, 1) {
			return status.Errorf(
				codes.InvalidArgument,
				"variant %d body exceeds maximum block nesting depth of %d",
				i,
				maxEmailCampaignBlockDepth,
			)
		}
	}
	return nil
}

func (s *Server) UpsertEmailSegment(ctx context.Context, req *pb_admin.UpsertEmailSegmentRequest) (*pb_admin.UpsertEmailSegmentResponse, error) {
	if req.Id < 0 {
		return nil, status.Error(codes.InvalidArgument, "segment id must be non-negative")
	}
	segment, err := dto.ConvertPbEmailSegmentToEntity(req.Segment)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	segment.Name = strings.TrimSpace(segment.Name)
	if segment.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "segment name is required")
	}
	segment.CreatedBy = authsrv.GetAdminUsername(ctx)

	id, err := s.repo.Campaigns().UpsertEmailSegment(ctx, int(req.Id), segment)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email segment not found")
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "an email segment with this name already exists")
		}
		slog.ErrorContext(ctx, "can't upsert email segment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert email segment")
	}
	return &pb_admin.UpsertEmailSegmentResponse{Id: int32(id)}, nil
}

func (s *Server) GetEmailSegment(ctx context.Context, req *pb_admin.GetEmailSegmentRequest) (*pb_admin.GetEmailSegmentResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "segment id is required")
	}
	segment, err := s.repo.Campaigns().GetEmailSegmentByID(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email segment not found")
		}
		slog.ErrorContext(ctx, "can't get email segment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get email segment")
	}
	return &pb_admin.GetEmailSegmentResponse{Segment: dto.ConvertEntityEmailSegmentToPb(segment)}, nil
}

func (s *Server) ListEmailSegments(ctx context.Context, _ *pb_admin.ListEmailSegmentsRequest) (*pb_admin.ListEmailSegmentsResponse, error) {
	segments, err := s.repo.Campaigns().ListEmailSegments(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "can't list email segments", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list email segments")
	}
	out := make([]*pb_common.EmailSegment, len(segments))
	for i := range segments {
		out[i] = dto.ConvertEntityEmailSegmentToPb(&segments[i])
	}
	return &pb_admin.ListEmailSegmentsResponse{Segments: out}, nil
}

func (s *Server) DeleteEmailSegment(ctx context.Context, req *pb_admin.DeleteEmailSegmentRequest) (*pb_admin.DeleteEmailSegmentResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "segment id is required")
	}
	if err := s.repo.Campaigns().DeleteEmailSegment(ctx, int(req.Id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email segment not found")
		}
		slog.ErrorContext(ctx, "can't delete email segment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete email segment")
	}
	return &pb_admin.DeleteEmailSegmentResponse{}, nil
}

func isSegmentPredicateError(err error) bool {
	for _, target := range []error{
		segment.ErrMalformedNode,
		segment.ErrEmptyGroup,
		segment.ErrUnknownField,
		segment.ErrUnknownOperator,
		segment.ErrBadValue,
		segment.ErrArity,
		segment.ErrTooDeep,
		segment.ErrTooManyNodes,
		segment.ErrTooManyInValues,
		segment.ErrUnknownTopic,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// PreviewEmailSegment counts the compliant audience for the supplied predicate,
// including unsaved editor state. Saved segments also cache the latest count.
func (s *Server) PreviewEmailSegment(
	ctx context.Context,
	req *pb_admin.PreviewEmailSegmentRequest,
) (*pb_admin.PreviewEmailSegmentResponse, error) {
	if req == nil || req.Segment == nil {
		return nil, status.Error(codes.InvalidArgument, "segment is required")
	}
	if req.Segment.Id < 0 {
		return nil, status.Error(codes.InvalidArgument, "segment id must be non-negative")
	}
	emailSegment, err := dto.ConvertPbEmailSegmentToEntity(req.Segment)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	campaignStore := s.repo.Campaigns()
	count, err := campaignStore.PreviewSegmentCount(ctx, emailSegment.Predicate)
	if err != nil {
		if isSegmentPredicateError(err) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid segment predicate: %v", err)
		}
		slog.ErrorContext(ctx, "can't preview email segment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't preview email segment")
	}
	if emailSegment.ID > 0 {
		if err := campaignStore.SaveSegmentCount(ctx, emailSegment.ID, count); err != nil {
			slog.ErrorContext(ctx, "can't save email segment preview count",
				slog.Int("segment_id", emailSegment.ID),
				slog.String("err", err.Error()),
			)
			return nil, status.Error(codes.Internal, "can't save email segment preview count")
		}
	}
	return &pb_admin.PreviewEmailSegmentResponse{Count: int32(count)}, nil
}

func validateEmailCampaign(in *pb_common.EmailCampaignInsert) error {
	if in == nil {
		return status.Error(codes.InvalidArgument, "campaign is required")
	}
	if err := validateEmailCampaignBlockDepth(in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" {
		return status.Error(codes.InvalidArgument, "campaign name is required")
	}
	switch in.Topic {
	case pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_NEWSLETTER,
		pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_NEW_ARRIVALS,
		pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_EVENTS:
	default:
		return status.Error(codes.InvalidArgument, "campaign topic is required")
	}
	switch in.Status {
	case pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_UNKNOWN,
		pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_DRAFT:
	default:
		return status.Error(codes.InvalidArgument, "campaign status is server-owned; drafts only")
	}
	abEnabled := in.AbConfig != nil && in.AbConfig.Enabled
	if !abEnabled && len(in.Variants) != 1 {
		return status.Error(codes.InvalidArgument, "a non-A/B campaign must have exactly one variant")
	}
	if abEnabled {
		if len(in.Variants) < 2 {
			return status.Error(codes.InvalidArgument, "an A/B campaign must have at least two variants")
		}
		if in.AbConfig.Dimension != pb_common.ABDimension_AB_DIMENSION_SUBJECT &&
			in.AbConfig.Dimension != pb_common.ABDimension_AB_DIMENSION_CONTENT {
			return status.Error(codes.InvalidArgument, "A/B dimension is required")
		}
		if in.AbConfig.TestPct < 1 || in.AbConfig.TestPct > 100 {
			return status.Error(codes.InvalidArgument, "A/B test percentage must be between 1 and 100")
		}
		if in.AbConfig.DecisionAfterMinutes < 30 || in.AbConfig.DecisionAfterMinutes > 10080 {
			return status.Error(
				codes.InvalidArgument,
				"A/B decision_after_minutes must be between 30 and 10080",
			)
		}
		if in.AbConfig.Dimension == pb_common.ABDimension_AB_DIMENSION_SUBJECT {
			for i := 0; i < len(in.Variants); i++ {
				for j := i + 1; j < len(in.Variants); j++ {
					if emailCampaignSubjectsEqual(in.Variants[i], in.Variants[j]) {
						return status.Error(
							codes.InvalidArgument,
							"subject A/B variants must have distinct subjects",
						)
					}
				}
			}
		}
	}
	seenLabels := make(map[string]struct{}, len(in.Variants))
	for i, variant := range in.Variants {
		if variant == nil {
			return status.Errorf(codes.InvalidArgument, "variant %d is required", i)
		}
		label := strings.TrimSpace(variant.Label)
		if label == "" || len(variant.Label) > 8 {
			return status.Errorf(codes.InvalidArgument, "variant %d label must be 1..8 characters", i)
		}
		// Labels must be unique within a campaign (enforced by the DB unique index too).
		// Reject duplicates here so the store's id-less "resolve by label" never silently
		// collapses two same-labelled variants onto one row (last-writer-wins data loss),
		// and so the client gets a clear message instead of the store's opaque error.
		if _, dup := seenLabels[label]; dup {
			return status.Errorf(codes.InvalidArgument, "variant labels must be unique: %q is used more than once", label)
		}
		seenLabels[label] = struct{}{}
	}
	return nil
}

func emailCampaignSubjectsEqual(left, right *pb_common.EmailCampaignVariant) bool {
	if left == nil || right == nil || len(left.SubjectI18N) != len(right.SubjectI18N) {
		return false
	}
	subjects := make(map[int32]string, len(left.SubjectI18N))
	for _, translation := range left.SubjectI18N {
		if translation != nil {
			subjects[translation.LanguageId] = strings.TrimSpace(translation.Subject)
		}
	}
	if len(subjects) != len(right.SubjectI18N) {
		return false
	}
	for _, translation := range right.SubjectI18N {
		if translation == nil {
			return false
		}
		if subjects[translation.LanguageId] != strings.TrimSpace(translation.Subject) {
			return false
		}
	}
	return true
}

func (s *Server) UpsertEmailCampaign(ctx context.Context, req *pb_admin.UpsertEmailCampaignRequest) (*pb_admin.UpsertEmailCampaignResponse, error) {
	if req.Id < 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id must be non-negative")
	}
	if err := validateEmailCampaign(req.Campaign); err != nil {
		return nil, err
	}
	campaign, err := dto.ConvertPbEmailCampaignInsertToEntity(req.Campaign)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	campaign.Name = strings.TrimSpace(campaign.Name)
	campaign.Status = entity.EmailCampaignStatusDraft
	campaign.ScheduleAt = nil
	campaign.CreatedBy = authsrv.GetAdminUsername(ctx)
	campaignrender.SanitizeBlocks(campaign.Body)
	for i := range campaign.Variants {
		campaignrender.SanitizeBlocks(campaign.Variants[i].Body)
	}

	id, err := s.repo.Campaigns().UpsertEmailCampaign(ctx, int(req.Id), campaign)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email campaign or variant not found")
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "email campaign variant labels must be unique")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "segment_id does not reference an email segment")
		}
		slog.ErrorContext(ctx, "can't upsert email campaign", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert email campaign")
	}
	return &pb_admin.UpsertEmailCampaignResponse{Id: int32(id)}, nil
}

func (s *Server) GetEmailCampaign(ctx context.Context, req *pb_admin.GetEmailCampaignRequest) (*pb_admin.GetEmailCampaignResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	campaign, err := s.repo.Campaigns().GetEmailCampaignByID(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email campaign not found")
		}
		slog.ErrorContext(ctx, "can't get email campaign", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get email campaign")
	}
	return &pb_admin.GetEmailCampaignResponse{Campaign: dto.ConvertEntityEmailCampaignFullToPb(campaign)}, nil
}

func (s *Server) ListEmailCampaignsPaged(ctx context.Context, req *pb_admin.ListEmailCampaignsPagedRequest) (*pb_admin.ListEmailCampaignsPagedResponse, error) {
	limit, offset := clampPagination(int(req.Limit), int(req.Offset))
	campaigns, total, err := s.repo.Campaigns().ListEmailCampaignsPaged(
		ctx,
		limit,
		offset,
		dto.ConvertPbEmailCampaignStatusFilter(req.Status),
		dto.ConvertPbEmailCampaignTopicFilter(req.Topic),
	)
	if err != nil {
		slog.ErrorContext(ctx, "can't list email campaigns", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list email campaigns")
	}
	out := make([]*pb_common.EmailCampaignFull, len(campaigns))
	for i := range campaigns {
		out[i] = dto.ConvertEntityEmailCampaignFullToPb(&campaigns[i])
	}
	return &pb_admin.ListEmailCampaignsPagedResponse{Campaigns: out, Total: int32(total)}, nil
}

func (s *Server) DeleteEmailCampaign(ctx context.Context, req *pb_admin.DeleteEmailCampaignRequest) (*pb_admin.DeleteEmailCampaignResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	if err := s.repo.Campaigns().DeleteEmailCampaign(ctx, int(req.Id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "email campaign not found")
		}
		slog.ErrorContext(ctx, "can't delete email campaign", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete email campaign")
	}
	return &pb_admin.DeleteEmailCampaignResponse{}, nil
}
