package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateCampaignTestRecipientsAllowlist(t *testing.T) {
	t.Run("enforces configured allowlist case-insensitively", func(t *testing.T) {
		allowlist := parseCampaignTestRecipientAllowlist(" first@example.com, ALLOWED@example.com ")

		recipients, err := validateCampaignTestRecipients(
			[]string{" Allowed@Example.com "},
			allowlist,
		)
		require.NoError(t, err)
		require.Equal(t, []string{"Allowed@Example.com"}, recipients)

		_, err = validateCampaignTestRecipients(
			[]string{"outside@example.com"},
			allowlist,
		)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, err.Error(), "outside@example.com")
	})

	t.Run("empty allowlist fails open", func(t *testing.T) {
		recipients, err := validateCampaignTestRecipients(
			[]string{" outside@example.com "},
			parseCampaignTestRecipientAllowlist(""),
		)
		require.NoError(t, err)
		require.Equal(t, []string{"outside@example.com"}, recipients)
	})
}

func TestSendCampaignTestRecipientsSkipsSuppressed(t *testing.T) {
	ctx := context.Background()
	repo := mocks.NewMockRepository(t)
	mailRepository := mocks.NewMockMail(t)
	mailer := mocks.NewMockMailer(t)
	server := &Server{repo: repo, mailer: mailer}

	repo.EXPECT().Mail().Return(mailRepository).Once()
	mailRepository.EXPECT().IsSuppressed(ctx, "skip@example.com").Return(true, nil).Once()
	mailRepository.EXPECT().IsSuppressed(ctx, "send@example.com").Return(false, nil).Once()
	mailer.EXPECT().SendCampaignTest(
		ctx,
		repo,
		"send@example.com",
		"[TEST] Subject",
		"<p>HTML</p>",
		"Text",
	).Return(nil).Once()

	err := server.sendCampaignTestRecipients(
		ctx,
		[]string{"skip@example.com", "send@example.com"},
		"[TEST] Subject",
		"<p>HTML</p>",
		"Text",
	)
	require.NoError(t, err)
}

func campaignBlocksAtDepth(depth int) []*pb_common.EmailBlock {
	if depth <= 1 {
		return []*pb_common.EmailBlock{{
			Type:   pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_SPACER,
			Spacer: &pb_common.EmailSpacerBlock{Height: 1},
		}}
	}
	return []*pb_common.EmailBlock{{
		Type: pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_TWO_COLUMN,
		TwoColumn: &pb_common.EmailTwoColumnBlock{
			Left: campaignBlocksAtDepth(depth - 1),
		},
	}}
}

func TestValidateEmailCampaignBlockDepth(t *testing.T) {
	require.NoError(t, validateEmailCampaignBlockDepth(&pb_common.EmailCampaignInsert{
		Body: campaignBlocksAtDepth(maxEmailCampaignBlockDepth),
	}))

	err := validateEmailCampaignBlockDepth(&pb_common.EmailCampaignInsert{
		Variants: []*pb_common.EmailCampaignVariant{{
			Body: campaignBlocksAtDepth(maxEmailCampaignBlockDepth + 1),
		}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "maximum block nesting depth")
}
