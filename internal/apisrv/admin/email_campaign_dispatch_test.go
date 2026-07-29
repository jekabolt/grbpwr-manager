package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestABCampaignLaunchAllowedWithDefaultLanguage(t *testing.T) {
	campaign := &entity.EmailCampaignFull{
		ID: 42,
		EmailCampaignInsert: entity.EmailCampaignInsert{
			ABConfig: entity.ABConfig{Enabled: true},
		},
	}
	err := validateEmailCampaignLaunchPreconditions(campaign, []entity.Language{
		{Id: 1, Code: "en", IsDefault: true},
	})
	if err != nil {
		t.Fatalf("A/B campaign launch rejected: %v", err)
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
