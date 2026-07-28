package dto

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func campaignStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func campaignIntPtr(value int32) *int {
	if value == 0 {
		return nil
	}
	v := int(value)
	return &v
}

func campaignTimePtr(unixSeconds int64) *time.Time {
	if unixSeconds <= 0 {
		return nil
	}
	value := time.Unix(unixSeconds, 0).UTC()
	return &value
}

func campaignUnix(value *time.Time) int64 {
	if value == nil || value.IsZero() {
		return 0
	}
	return value.Unix()
}

func convertPBEmailLinksToEntity(in []*pb_common.EmailLink) []entity.EmailLink {
	out := make([]entity.EmailLink, 0, len(in))
	for _, link := range in {
		if link == nil {
			continue
		}
		out = append(out, entity.EmailLink{Label: link.Label, URL: link.Url})
	}
	return out
}

func convertEntityEmailLinksToPB(in []entity.EmailLink) []*pb_common.EmailLink {
	out := make([]*pb_common.EmailLink, len(in))
	for i := range in {
		out[i] = &pb_common.EmailLink{Label: in[i].Label, Url: in[i].URL}
	}
	return out
}

func convertPBEmailBlockTranslationsToEntity(in []*pb_common.EmailBlockTranslation) []entity.EmailBlockTranslation {
	out := make([]entity.EmailBlockTranslation, 0, len(in))
	for _, translation := range in {
		if translation == nil {
			continue
		}
		out = append(out, entity.EmailBlockTranslation{
			LanguageID: int(translation.LanguageId),
			Heading:    translation.Heading,
			Subheading: translation.Subheading,
			Body:       translation.Body,
			Caption:    translation.Caption,
			CTALabel:   translation.CtaLabel,
			CTAURL:     translation.CtaUrl,
			AltText:    translation.AltText,
			Preheader:  translation.Preheader,
			Links:      convertPBEmailLinksToEntity(translation.Links),
		})
	}
	return out
}

func convertEntityEmailBlockTranslationsToPB(in []entity.EmailBlockTranslation) []*pb_common.EmailBlockTranslation {
	out := make([]*pb_common.EmailBlockTranslation, len(in))
	for i := range in {
		out[i] = &pb_common.EmailBlockTranslation{
			LanguageId: int32(in[i].LanguageID),
			Heading:    in[i].Heading,
			Subheading: in[i].Subheading,
			Body:       in[i].Body,
			Caption:    in[i].Caption,
			CtaLabel:   in[i].CTALabel,
			CtaUrl:     in[i].CTAURL,
			AltText:    in[i].AltText,
			Preheader:  in[i].Preheader,
			Links:      convertEntityEmailLinksToPB(in[i].Links),
		}
	}
	return out
}

func convertPBEmailBlocksToEntity(in []*pb_common.EmailBlock) ([]entity.EmailBlock, error) {
	out := make([]entity.EmailBlock, 0, len(in))
	for i, block := range in {
		if block == nil {
			return nil, fmt.Errorf("email block %d is nil", i)
		}
		converted, err := convertPBEmailBlockToEntity(block)
		if err != nil {
			return nil, fmt.Errorf("email block %d: %w", i, err)
		}
		out = append(out, converted)
	}
	return out, nil
}

func convertPBEmailBlockToEntity(in *pb_common.EmailBlock) (entity.EmailBlock, error) {
	out := entity.EmailBlock{
		Type:            entity.EmailBlockType(in.Type),
		BackgroundColor: in.BackgroundColor,
		Translations:    convertPBEmailBlockTranslationsToEntity(in.Translations),
	}

	switch in.Type {
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_HEADER:
		if in.Header != nil {
			out.Header = &entity.EmailHeaderBlock{LogoMediaID: int(in.Header.LogoMediaId)}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_IMAGE_LINK:
		if in.ImageLink != nil {
			out.ImageLink = &entity.EmailImageLinkBlock{MediaID: int(in.ImageLink.MediaId), URL: in.ImageLink.Url}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_RICH_TEXT:
		out.RichText = &entity.EmailRichTextBlock{}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_PRODUCT_CARD:
		if in.ProductCard != nil {
			out.ProductCard = &entity.EmailProductCardBlock{ProductID: int(in.ProductCard.ProductId)}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_PRODUCT_GRID:
		if in.ProductGrid != nil {
			productIDs := make([]int, len(in.ProductGrid.ProductIds))
			for i, id := range in.ProductGrid.ProductIds {
				productIDs[i] = int(id)
			}
			out.ProductGrid = &entity.EmailProductGridBlock{ProductIDs: productIDs, Columns: int(in.ProductGrid.Columns)}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_CTA_BUTTON:
		if in.CtaButton != nil {
			out.CTAButton = &entity.EmailCTAButtonBlock{Style: in.CtaButton.Style, Alignment: in.CtaButton.Alignment}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_DIVIDER:
		if in.Divider != nil {
			out.Divider = &entity.EmailDividerBlock{Color: in.Divider.Color, Height: int(in.Divider.Height)}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_SPACER:
		if in.Spacer != nil {
			out.Spacer = &entity.EmailSpacerBlock{Height: int(in.Spacer.Height)}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_TWO_COLUMN:
		if in.TwoColumn != nil {
			left, err := convertPBEmailBlocksToEntity(in.TwoColumn.Left)
			if err != nil {
				return entity.EmailBlock{}, fmt.Errorf("left column: %w", err)
			}
			right, err := convertPBEmailBlocksToEntity(in.TwoColumn.Right)
			if err != nil {
				return entity.EmailBlock{}, fmt.Errorf("right column: %w", err)
			}
			out.TwoColumn = &entity.EmailTwoColumnBlock{Left: left, Right: right}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_SOCIAL_LINKS:
		if in.SocialLinks != nil {
			links := make([]entity.EmailSocialLink, 0, len(in.SocialLinks.Links))
			for _, link := range in.SocialLinks.Links {
				if link != nil {
					links = append(links, entity.EmailSocialLink{Network: link.Network, URL: link.Url})
				}
			}
			out.SocialLinks = &entity.EmailSocialLinksBlock{Links: links}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_COUNTDOWN:
		if in.Countdown != nil {
			out.Countdown = &entity.EmailCountdownBlock{EndsAt: in.Countdown.EndsAt}
		}
	case pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_VIDEO_THUMB:
		if in.VideoThumb != nil {
			out.VideoThumb = &entity.EmailVideoThumbBlock{
				MediaID:  int(in.VideoThumb.MediaId),
				VideoURL: in.VideoThumb.VideoUrl,
			}
		}
	default:
		return entity.EmailBlock{}, fmt.Errorf("unsupported block type %d", in.Type)
	}

	return out, nil
}

func convertEntityEmailBlocksToPB(in []entity.EmailBlock) []*pb_common.EmailBlock {
	out := make([]*pb_common.EmailBlock, len(in))
	for i := range in {
		out[i] = convertEntityEmailBlockToPB(&in[i])
	}
	return out
}

func convertEntityEmailBlockToPB(in *entity.EmailBlock) *pb_common.EmailBlock {
	if in == nil {
		return nil
	}
	out := &pb_common.EmailBlock{
		Type:            pb_common.EmailBlockType(in.Type),
		BackgroundColor: in.BackgroundColor,
		Translations:    convertEntityEmailBlockTranslationsToPB(in.Translations),
	}
	if in.Header != nil {
		out.Header = &pb_common.EmailHeaderBlock{LogoMediaId: int32(in.Header.LogoMediaID)}
	}
	if in.ImageLink != nil {
		out.ImageLink = &pb_common.EmailImageLinkBlock{MediaId: int32(in.ImageLink.MediaID), Url: in.ImageLink.URL}
	}
	if in.RichText != nil {
		out.RichText = &pb_common.EmailRichTextBlock{}
	}
	if in.ProductCard != nil {
		out.ProductCard = &pb_common.EmailProductCardBlock{ProductId: int32(in.ProductCard.ProductID)}
	}
	if in.ProductGrid != nil {
		productIDs := make([]int32, len(in.ProductGrid.ProductIDs))
		for i, id := range in.ProductGrid.ProductIDs {
			productIDs[i] = int32(id)
		}
		out.ProductGrid = &pb_common.EmailProductGridBlock{
			ProductIds: productIDs,
			Columns:    int32(in.ProductGrid.Columns),
		}
	}
	if in.CTAButton != nil {
		out.CtaButton = &pb_common.EmailCTAButtonBlock{Style: in.CTAButton.Style, Alignment: in.CTAButton.Alignment}
	}
	if in.Divider != nil {
		out.Divider = &pb_common.EmailDividerBlock{Color: in.Divider.Color, Height: int32(in.Divider.Height)}
	}
	if in.Spacer != nil {
		out.Spacer = &pb_common.EmailSpacerBlock{Height: int32(in.Spacer.Height)}
	}
	if in.TwoColumn != nil {
		out.TwoColumn = &pb_common.EmailTwoColumnBlock{
			Left:  convertEntityEmailBlocksToPB(in.TwoColumn.Left),
			Right: convertEntityEmailBlocksToPB(in.TwoColumn.Right),
		}
	}
	if in.SocialLinks != nil {
		links := make([]*pb_common.EmailSocialLink, len(in.SocialLinks.Links))
		for i := range in.SocialLinks.Links {
			links[i] = &pb_common.EmailSocialLink{
				Network: in.SocialLinks.Links[i].Network,
				Url:     in.SocialLinks.Links[i].URL,
			}
		}
		out.SocialLinks = &pb_common.EmailSocialLinksBlock{Links: links}
	}
	if in.Countdown != nil {
		out.Countdown = &pb_common.EmailCountdownBlock{EndsAt: in.Countdown.EndsAt}
	}
	if in.VideoThumb != nil {
		out.VideoThumb = &pb_common.EmailVideoThumbBlock{
			MediaId:  int32(in.VideoThumb.MediaID),
			VideoUrl: in.VideoThumb.VideoURL,
		}
	}
	return out
}

func convertPBEmailCampaignTopic(in pb_common.EmailCampaignTopic) entity.EmailCampaignTopic {
	switch in {
	case pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_NEWSLETTER:
		return entity.EmailCampaignTopicNewsletter
	case pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_NEW_ARRIVALS:
		return entity.EmailCampaignTopicNewArrivals
	case pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_EVENTS:
		return entity.EmailCampaignTopicEvents
	default:
		return entity.EmailCampaignTopicUnknown
	}
}

func convertEntityEmailCampaignTopic(in entity.EmailCampaignTopic) pb_common.EmailCampaignTopic {
	switch in {
	case entity.EmailCampaignTopicNewsletter:
		return pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_NEWSLETTER
	case entity.EmailCampaignTopicNewArrivals:
		return pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_NEW_ARRIVALS
	case entity.EmailCampaignTopicEvents:
		return pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_EVENTS
	default:
		return pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_UNKNOWN
	}
}

func convertPBEmailCampaignStatus(in pb_common.EmailCampaignStatus) entity.EmailCampaignStatus {
	switch in {
	case pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_DRAFT:
		return entity.EmailCampaignStatusDraft
	case pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_SCHEDULED:
		return entity.EmailCampaignStatusScheduled
	case pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_SENDING:
		return entity.EmailCampaignStatusSending
	case pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_PAUSED:
		return entity.EmailCampaignStatusPaused
	case pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_SENT:
		return entity.EmailCampaignStatusSent
	case pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_CANCELLED:
		return entity.EmailCampaignStatusCancelled
	default:
		return entity.EmailCampaignStatusUnknown
	}
}

func convertEntityEmailCampaignStatus(in entity.EmailCampaignStatus) pb_common.EmailCampaignStatus {
	switch in {
	case entity.EmailCampaignStatusDraft:
		return pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_DRAFT
	case entity.EmailCampaignStatusScheduled:
		return pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_SCHEDULED
	case entity.EmailCampaignStatusSending:
		return pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_SENDING
	case entity.EmailCampaignStatusPaused:
		return pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_PAUSED
	case entity.EmailCampaignStatusSent:
		return pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_SENT
	case entity.EmailCampaignStatusCancelled:
		return pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_CANCELLED
	default:
		return pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_UNKNOWN
	}
}

func convertPBABDimension(in pb_common.ABDimension) entity.ABDimension {
	switch in {
	case pb_common.ABDimension_AB_DIMENSION_SUBJECT:
		return entity.ABDimensionSubject
	case pb_common.ABDimension_AB_DIMENSION_CONTENT:
		return entity.ABDimensionContent
	default:
		return entity.ABDimensionUnknown
	}
}

func convertEntityABDimension(in entity.ABDimension) pb_common.ABDimension {
	switch in {
	case entity.ABDimensionSubject:
		return pb_common.ABDimension_AB_DIMENSION_SUBJECT
	case entity.ABDimensionContent:
		return pb_common.ABDimension_AB_DIMENSION_CONTENT
	default:
		return pb_common.ABDimension_AB_DIMENSION_UNKNOWN
	}
}

func convertPBSubjectTranslationsToEntity(in []*pb_common.SubjectTranslation) []entity.SubjectTranslation {
	out := make([]entity.SubjectTranslation, 0, len(in))
	for _, translation := range in {
		if translation != nil {
			out = append(out, entity.SubjectTranslation{
				LanguageID: int(translation.LanguageId),
				Subject:    translation.Subject,
			})
		}
	}
	return out
}

func convertEntitySubjectTranslationsToPB(in []entity.SubjectTranslation) []*pb_common.SubjectTranslation {
	out := make([]*pb_common.SubjectTranslation, len(in))
	for i := range in {
		out[i] = &pb_common.SubjectTranslation{
			LanguageId: int32(in[i].LanguageID),
			Subject:    in[i].Subject,
		}
	}
	return out
}

func convertPBEmailCampaignVariantsToEntity(in []*pb_common.EmailCampaignVariant) ([]entity.EmailCampaignVariant, error) {
	out := make([]entity.EmailCampaignVariant, 0, len(in))
	for i, variant := range in {
		if variant == nil {
			return nil, fmt.Errorf("variant %d is nil", i)
		}
		body, err := convertPBEmailBlocksToEntity(variant.Body)
		if err != nil {
			return nil, fmt.Errorf("variant %d body: %w", i, err)
		}
		out = append(out, entity.EmailCampaignVariant{
			ID:          int(variant.Id),
			Label:       variant.Label,
			SubjectI18n: convertPBSubjectTranslationsToEntity(variant.SubjectI18N),
			Body:        body,
			IsWinner:    variant.IsWinner,
		})
	}
	return out, nil
}

func convertEntityEmailCampaignVariantsToPB(in []entity.EmailCampaignVariant) []*pb_common.EmailCampaignVariant {
	out := make([]*pb_common.EmailCampaignVariant, len(in))
	for i := range in {
		out[i] = &pb_common.EmailCampaignVariant{
			Id:          int32(in[i].ID),
			Label:       in[i].Label,
			SubjectI18N: convertEntitySubjectTranslationsToPB(in[i].SubjectI18n),
			Body:        convertEntityEmailBlocksToPB(in[i].Body),
			IsWinner:    in[i].IsWinner,
		}
	}
	return out
}

// ConvertPbEmailCampaignInsertToEntity converts the admin write shape into the
// aggregate persisted by the campaign store.
func ConvertPbEmailCampaignInsertToEntity(in *pb_common.EmailCampaignInsert) (*entity.EmailCampaignInsert, error) {
	if in == nil {
		return nil, fmt.Errorf("campaign is required")
	}
	body, err := convertPBEmailBlocksToEntity(in.Body)
	if err != nil {
		return nil, fmt.Errorf("campaign body: %w", err)
	}
	variants, err := convertPBEmailCampaignVariantsToEntity(in.Variants)
	if err != nil {
		return nil, err
	}
	var ab entity.ABConfig
	if in.AbConfig != nil {
		ab = entity.ABConfig{
			Enabled:              in.AbConfig.Enabled,
			Dimension:            convertPBABDimension(in.AbConfig.Dimension),
			TestPct:              int(in.AbConfig.TestPct),
			DecisionAfterMinutes: int(in.AbConfig.DecisionAfterMinutes),
			WinnerVariantID:      campaignIntPtr(in.AbConfig.WinnerVariantId),
		}
	}
	return &entity.EmailCampaignInsert{
		Name:            in.Name,
		Topic:           convertPBEmailCampaignTopic(in.Topic),
		Body:            body,
		BackgroundColor: in.BackgroundColor,
		FromName:        campaignStringPtr(in.FromName),
		FromEmail:       campaignStringPtr(in.FromEmail),
		ReplyTo:         campaignStringPtr(in.ReplyTo),
		ScheduleAt:      campaignTimePtr(in.ScheduleAt),
		ABConfig:        ab,
		Variants:        variants,
		Status:          convertPBEmailCampaignStatus(in.Status),
		SegmentID:       campaignIntPtr(in.SegmentId),
	}, nil
}

func ConvertEntityEmailCampaignFullToPb(in *entity.EmailCampaignFull) *pb_common.EmailCampaignFull {
	if in == nil {
		return nil
	}
	out := &pb_common.EmailCampaignFull{
		Id:              int32(in.ID),
		Name:            in.Name,
		Topic:           convertEntityEmailCampaignTopic(in.Topic),
		Body:            convertEntityEmailBlocksToPB(in.Body),
		BackgroundColor: in.BackgroundColor,
		ScheduleAt:      campaignUnix(in.ScheduleAt),
		AbConfig: &pb_common.ABConfig{
			Enabled:              in.ABConfig.Enabled,
			Dimension:            convertEntityABDimension(in.ABConfig.Dimension),
			TestPct:              int32(in.ABConfig.TestPct),
			DecisionAfterMinutes: int32(in.ABConfig.DecisionAfterMinutes),
		},
		Variants:               convertEntityEmailCampaignVariantsToPB(in.Variants),
		Status:                 convertEntityEmailCampaignStatus(in.Status),
		CreatedBy:              in.CreatedBy,
		CreatedAt:              in.CreatedAt.Unix(),
		UpdatedAt:              in.UpdatedAt.Unix(),
		SendingStartedAt:       campaignUnix(in.SendingStartedAt),
		SentAt:                 campaignUnix(in.SentAt),
		AudienceSnapshotAt:     campaignUnix(in.AudienceSnapshotAt),
		FanoutMaxAccountId:     int32(in.FanoutMaxAccountID),
		FanoutCursorAccountId:  int32(in.FanoutCursorAccountID),
		AudienceMaterializedAt: campaignUnix(in.AudienceMaterializedAt),
	}
	if in.FromName != nil {
		out.FromName = *in.FromName
	}
	if in.FromEmail != nil {
		out.FromEmail = *in.FromEmail
	}
	if in.ReplyTo != nil {
		out.ReplyTo = *in.ReplyTo
	}
	if in.SegmentID != nil {
		out.SegmentId = int32(*in.SegmentID)
	}
	if in.ABConfig.WinnerVariantID != nil {
		out.AbConfig.WinnerVariantId = int32(*in.ABConfig.WinnerVariantID)
	}
	if in.RecipientCount != nil {
		out.RecipientCount = int32(*in.RecipientCount)
	}
	if in.DispatchError != nil {
		out.DispatchError = *in.DispatchError
	}
	return out
}

func convertRecipientStatusToPB(
	in entity.EmailCampaignRecipientStatus,
) pb_common.EmailCampaignRecipientStatus {
	switch in {
	case entity.EmailCampaignRecipientStatusPending:
		return pb_common.EmailCampaignRecipientStatus_EMAIL_CAMPAIGN_RECIPIENT_STATUS_PENDING
	case entity.EmailCampaignRecipientStatusSent:
		return pb_common.EmailCampaignRecipientStatus_EMAIL_CAMPAIGN_RECIPIENT_STATUS_SENT
	case entity.EmailCampaignRecipientStatusFailed:
		return pb_common.EmailCampaignRecipientStatus_EMAIL_CAMPAIGN_RECIPIENT_STATUS_FAILED
	case entity.EmailCampaignRecipientStatusSkipped:
		return pb_common.EmailCampaignRecipientStatus_EMAIL_CAMPAIGN_RECIPIENT_STATUS_SKIPPED
	default:
		return pb_common.EmailCampaignRecipientStatus_EMAIL_CAMPAIGN_RECIPIENT_STATUS_UNKNOWN
	}
}

func convertCampaignCohortToPB(in entity.EmailCampaignCohort) pb_common.EmailCampaignCohort {
	switch in {
	case entity.EmailCampaignCohortAB:
		return pb_common.EmailCampaignCohort_EMAIL_CAMPAIGN_COHORT_AB
	case entity.EmailCampaignCohortRemainder:
		return pb_common.EmailCampaignCohort_EMAIL_CAMPAIGN_COHORT_REMAINDER
	default:
		return pb_common.EmailCampaignCohort_EMAIL_CAMPAIGN_COHORT_UNKNOWN
	}
}

func ConvertEntityEmailCampaignDispatchStatusToPB(
	in *entity.EmailCampaignDispatchStatus,
) *pb_common.EmailCampaignDispatchStatus {
	if in == nil {
		return nil
	}
	out := &pb_common.EmailCampaignDispatchStatus{
		CampaignId:             int32(in.CampaignID),
		Status:                 convertEntityEmailCampaignStatus(in.Status),
		AudienceMaterializedAt: campaignUnix(in.AudienceMaterializedAt),
		RecipientCount:         int32(in.Counts.RecipientCount),
		Pending:                int32(in.Counts.Pending),
		Accepted:               int32(in.Counts.Accepted),
		Failed:                 int32(in.Counts.Failed),
		Skipped:                int32(in.Counts.Skipped),
	}
	if in.DispatchError != nil {
		out.DispatchError = *in.DispatchError
	}
	return out
}

func ConvertEntityEmailCampaignRecipientToPB(
	in *entity.EmailCampaignRecipient,
) *pb_common.EmailCampaignRecipient {
	if in == nil {
		return nil
	}
	out := &pb_common.EmailCampaignRecipient{
		Id:            in.ID,
		CampaignId:    int32(in.CampaignID),
		Email:         in.Email,
		LanguageId:    int32(in.LanguageID),
		Cohort:        convertCampaignCohortToPB(in.Cohort),
		Status:        convertRecipientStatusToPB(in.Status),
		AttemptCount:  int32(in.AttemptCount),
		NextAttemptAt: campaignUnix(in.NextAttemptAt),
		SentAt:        campaignUnix(in.SentAt),
		CompletedAt:   campaignUnix(in.CompletedAt),
		CreatedAt:     in.CreatedAt.Unix(),
		UpdatedAt:     in.UpdatedAt.Unix(),
	}
	if in.AccountID != nil {
		out.AccountId = int32(*in.AccountID)
	}
	if in.VariantID != nil {
		out.VariantId = int32(*in.VariantID)
	}
	if in.ResendEmailID != nil {
		out.ResendEmailId = *in.ResendEmailID
	}
	if in.ErrorCode != nil {
		out.ErrorCode = *in.ErrorCode
	}
	if in.LastError != nil {
		out.LastError = *in.LastError
	}
	return out
}

func convertPBSegmentNodeToEntity(in *pb_common.SegmentNode) *entity.SegmentNode {
	if in == nil {
		return nil
	}
	children := make([]entity.SegmentNode, 0, len(in.Children))
	for _, child := range in.Children {
		if converted := convertPBSegmentNodeToEntity(child); converted != nil {
			children = append(children, *converted)
		}
	}
	return &entity.SegmentNode{
		Op:       entity.SegmentOp(in.Op),
		Children: children,
		Field:    in.Field,
		Operator: in.Operator,
		Values:   append([]string(nil), in.Values...),
	}
}

func convertEntitySegmentNodeToPB(in *entity.SegmentNode) *pb_common.SegmentNode {
	if in == nil {
		return nil
	}
	children := make([]*pb_common.SegmentNode, len(in.Children))
	for i := range in.Children {
		children[i] = convertEntitySegmentNodeToPB(&in.Children[i])
	}
	return &pb_common.SegmentNode{
		Op:       pb_common.SegmentOp(in.Op),
		Children: children,
		Field:    in.Field,
		Operator: in.Operator,
		Values:   append([]string(nil), in.Values...),
	}
}

func ConvertPbEmailSegmentToEntity(in *pb_common.EmailSegment) (*entity.EmailSegment, error) {
	if in == nil {
		return nil, fmt.Errorf("segment is required")
	}
	out := &entity.EmailSegment{
		ID:          int(in.Id),
		Name:        in.Name,
		Description: in.Description,
		LastCount:   campaignIntPtr(in.LastCount),
		LastCountAt: campaignTimePtr(in.LastCountAt),
	}
	if in.Predicate != nil {
		out.Predicate.Root = convertPBSegmentNodeToEntity(in.Predicate.Root)
	}
	return out, nil
}

func ConvertEntityEmailSegmentToPb(in *entity.EmailSegment) *pb_common.EmailSegment {
	if in == nil {
		return nil
	}
	out := &pb_common.EmailSegment{
		Id:          int32(in.ID),
		Name:        in.Name,
		Description: in.Description,
		Predicate: &pb_common.SegmentPredicate{
			Root: convertEntitySegmentNodeToPB(in.Predicate.Root),
		},
		LastCountAt: campaignUnix(in.LastCountAt),
	}
	if in.LastCount != nil {
		out.LastCount = int32(*in.LastCount)
	}
	return out
}

func ConvertPbEmailCampaignStatusFilter(in pb_common.EmailCampaignStatus) entity.EmailCampaignStatus {
	return convertPBEmailCampaignStatus(in)
}

func ConvertPbEmailCampaignTopicFilter(in pb_common.EmailCampaignTopic) entity.EmailCampaignTopic {
	return convertPBEmailCampaignTopic(in)
}
