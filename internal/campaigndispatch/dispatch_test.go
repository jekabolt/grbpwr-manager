package campaigndispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
)

func TestDispatchOneQuarantinesUnsafeProviderReplay(t *testing.T) {
	tests := []struct {
		name         string
		firstAttempt time.Time
		attemptCount int
	}{
		{
			name:         "idempotency horizon reached",
			firstAttempt: time.Now().Add(-automaticRetryHorizon),
			attemptCount: 1,
		},
		{
			name:         "maximum attempts reached",
			firstAttempt: time.Now().Add(-time.Minute),
			attemptCount: 8,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			batch := &entity.EmailCampaignBatch{
				CampaignID: 7,
				BatchID:    "batch-7",
				ClaimToken: "claim-7",
				Recipients: []entity.EmailCampaignRecipient{{
					AttemptCount:           tt.attemptCount,
					FirstProviderAttemptAt: &tt.firstAttempt,
				}},
			}
			repo := mocks.NewMockRepository(t)
			campaigns := mocks.NewMockCampaigns(t)
			mailer := mocks.NewMockMailer(t)
			repo.EXPECT().Campaigns().Return(campaigns).Twice()
			campaigns.EXPECT().
				ClaimEmailCampaignBatch(ctx, 100, 2*time.Minute).
				Return(batch, nil).
				Once()
			mailer.EXPECT().CampaignSendingDisabled().Return(false).Once()
			campaigns.EXPECT().
				GetEmailCampaignByID(ctx, batch.CampaignID).
				Return(&entity.EmailCampaignFull{ID: batch.CampaignID}, nil).
				Once()
			campaigns.EXPECT().
				CompleteEmailCampaignBatch(
					ctx,
					batch.BatchID,
					batch.ClaimToken,
					entity.EmailCampaignRecipientStatusFailed,
					"ambiguous_provider_ack",
					mock.MatchedBy(func(message *string) bool {
						return message != nil &&
							strings.Contains(*message, "automatic replay blocked")
					}),
				).
				Return(nil).
				Once()

			config := DefaultConfig()
			worker := &Worker{repo: repo, mailer: mailer, c: &config}
			didWork, err := worker.dispatchOne(ctx)
			if err != nil {
				t.Fatalf("dispatchOne() error = %v", err)
			}
			if !didWork {
				t.Fatal("dispatchOne() didWork = false, want true")
			}
		})
	}
}

func TestBatchProviderAttemptStateTreatsAllNilAsFresh(t *testing.T) {
	batch := &entity.EmailCampaignBatch{
		Recipients: []entity.EmailCampaignRecipient{{
			AttemptCount: 99,
		}},
	}
	oldest, maxAttempt, attempted := batchProviderAttemptState(batch)
	if attempted {
		t.Fatal("batch with no first_provider_attempt_at was treated as a replay")
	}
	if !oldest.IsZero() {
		t.Fatalf("oldest attempt = %v, want zero", oldest)
	}
	if maxAttempt != 99 {
		t.Fatalf("max attempt = %d, want 99", maxAttempt)
	}
}
