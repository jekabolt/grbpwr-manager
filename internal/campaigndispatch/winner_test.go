package campaigndispatch

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
)

func TestSelectABWinnerByDimension(t *testing.T) {
	variants := []entity.EmailCampaignABVariantResult{
		{VariantID: 1, Delivered: 100, UniqueOpened: 60, UniqueClicked: 10},
		{VariantID: 2, Delivered: 100, UniqueOpened: 40, UniqueClicked: 30},
	}
	subjectWinner, err := SelectABWinner(entity.ABDimensionSubject, variants)
	if err != nil {
		t.Fatal(err)
	}
	if subjectWinner != 1 {
		t.Fatalf("subject winner = %d, want 1", subjectWinner)
	}
	contentWinner, err := SelectABWinner(entity.ABDimensionContent, variants)
	if err != nil {
		t.Fatal(err)
	}
	if contentWinner != 2 {
		t.Fatalf("content winner = %d, want 2", contentWinner)
	}
}

func TestSelectABWinnerUsesRatesAndTieBreaks(t *testing.T) {
	tests := []struct {
		name      string
		dimension entity.ABDimension
		variants  []entity.EmailCampaignABVariantResult
		want      int
	}{
		{
			name:      "rate beats raw count",
			dimension: entity.ABDimensionSubject,
			variants: []entity.EmailCampaignABVariantResult{
				{VariantID: 1, Delivered: 100, UniqueOpened: 50},
				{VariantID: 2, Delivered: 20, UniqueOpened: 12},
			},
			want: 2,
		},
		{
			name:      "subject tie uses click rate",
			dimension: entity.ABDimensionSubject,
			variants: []entity.EmailCampaignABVariantResult{
				{VariantID: 1, Delivered: 100, UniqueOpened: 50, UniqueClicked: 5},
				{VariantID: 2, Delivered: 20, UniqueOpened: 10, UniqueClicked: 3},
			},
			want: 2,
		},
		{
			name:      "content tie uses open rate",
			dimension: entity.ABDimensionContent,
			variants: []entity.EmailCampaignABVariantResult{
				{VariantID: 1, Delivered: 100, UniqueOpened: 30, UniqueClicked: 10},
				{VariantID: 2, Delivered: 20, UniqueOpened: 10, UniqueClicked: 2},
			},
			want: 2,
		},
		{
			name:      "final tie uses earliest id",
			dimension: entity.ABDimensionSubject,
			variants: []entity.EmailCampaignABVariantResult{
				{VariantID: 9, Delivered: 10, UniqueOpened: 5, UniqueClicked: 2},
				{VariantID: 3, Delivered: 20, UniqueOpened: 10, UniqueClicked: 4},
			},
			want: 3,
		},
		{
			name:      "zero signal always resolves",
			dimension: entity.ABDimensionContent,
			variants: []entity.EmailCampaignABVariantResult{
				{VariantID: 8},
				{VariantID: 2},
				{VariantID: 5},
			},
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectABWinner(tt.dimension, tt.variants)
			if err != nil {
				t.Fatalf("SelectABWinner() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("SelectABWinner() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunOncePromotesWinnerOnceAndImmediatelyReclaimsRemainder(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	campaigns := mocks.NewMockCampaigns(t)
	config := DefaultConfig()
	repo.EXPECT().Campaigns().Return(campaigns).Times(6)
	campaigns.EXPECT().
		PromoteDueEmailCampaign(mock.Anything).
		Return(0, nil).
		Once()
	campaigns.EXPECT().
		AdvanceEmailCampaignFanout(mock.Anything, config.FanoutPageSize, mock.Anything).
		Return(nil, nil).
		Once()
	// The first empty claim settles the test-cohort drain. After the guarded
	// winner promotion releases remainder variant_ids, the second claim proves
	// the worker re-enters the normal claim path in the same tick.
	campaigns.EXPECT().
		ClaimEmailCampaignBatch(mock.Anything, config.BatchSize, config.ClaimLease).
		Return(nil, nil).
		Twice()
	campaigns.EXPECT().
		PromoteEmailCampaignABWinners(mock.Anything, mock.Anything).
		Return(1, nil).
		Once()
	campaigns.EXPECT().
		FinalizeEmailCampaigns(mock.Anything).
		Return(1, nil).
		Once()
	worker := &Worker{
		repo: repo,
		c:    &config,
	}

	if ok := worker.runOnce(context.Background()); !ok {
		t.Fatal("runOnce() = false, want true")
	}
}

func TestWorkerBackoffUnchangedForWinnerPhase(t *testing.T) {
	if got := workerBackoff(1); got != 30*time.Second {
		t.Fatalf("workerBackoff(1) = %v", got)
	}
}
