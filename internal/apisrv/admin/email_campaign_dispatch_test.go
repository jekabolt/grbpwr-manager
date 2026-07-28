package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestABCampaignLaunchIsBlocked(t *testing.T) {
	tests := []struct {
		name string
		call func(*Server) error
	}{
		{
			name: "send now",
			call: func(server *Server) error {
				_, err := server.SendCampaignNow(
					context.Background(),
					&pb_admin.SendCampaignNowRequest{Id: 42},
				)
				return err
			},
		},
		{
			name: "schedule",
			call: func(server *Server) error {
				_, err := server.ScheduleCampaign(
					context.Background(),
					&pb_admin.ScheduleCampaignRequest{
						Id:         42,
						ScheduleAt: time.Now().Add(time.Hour).Unix(),
					},
				)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			campaigns := mocks.NewMockCampaigns(t)
			mailer := mocks.NewMockMailer(t)
			mailer.EXPECT().CampaignDispatchConfigured().Return(nil).Once()
			repo.EXPECT().Campaigns().Return(campaigns).Once()
			campaigns.EXPECT().
				GetEmailCampaignByID(context.Background(), 42).
				Return(&entity.EmailCampaignFull{
					ID: 42,
					EmailCampaignInsert: entity.EmailCampaignInsert{
						ABConfig: entity.ABConfig{Enabled: true},
					},
				}, nil).
				Once()

			err := tt.call(&Server{repo: repo, mailer: mailer})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("status code = %v, want %v: %v", status.Code(err), codes.FailedPrecondition, err)
			}
			if !strings.Contains(status.Convert(err).Message(), "A/B campaigns cannot be launched yet") {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestCampaignLaunchRequiresDefaultLanguage(t *testing.T) {
	campaign := &entity.EmailCampaignFull{}
	err := validateEmailCampaignLaunchPreconditions(campaign, []entity.Language{
		{Id: 1, Code: "en"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("status code = %v, want %v: %v", status.Code(err), codes.FailedPrecondition, err)
	}
	if !strings.Contains(status.Convert(err).Message(), "no default language") {
		t.Fatalf("unexpected error message: %v", err)
	}

	err = validateEmailCampaignLaunchPreconditions(campaign, []entity.Language{
		{Id: 1, Code: "en", IsDefault: true},
	})
	if err != nil {
		t.Fatalf("default language rejected: %v", err)
	}
}
