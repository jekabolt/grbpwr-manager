package admin

import (
	"strconv"
	"strings"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validABCampaignForDecisionDelay(minutes int32) *pb_common.EmailCampaignInsert {
	return &pb_common.EmailCampaignInsert{
		Name:  "Delay validation",
		Topic: pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_EVENTS,
		AbConfig: &pb_common.ABConfig{
			Enabled:              true,
			Dimension:            pb_common.ABDimension_AB_DIMENSION_CONTENT,
			TestPct:              20,
			DecisionAfterMinutes: minutes,
		},
		Variants: []*pb_common.EmailCampaignVariant{
			{Label: "A"},
			{Label: "B"},
		},
	}
}

func TestValidateEmailCampaignABDecisionDelay(t *testing.T) {
	tests := []struct {
		minutes int32
		valid   bool
	}{
		{minutes: -1},
		{minutes: 0},
		{minutes: 29},
		{minutes: 30, valid: true},
		{minutes: 10080, valid: true},
		{minutes: 10081},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(int(tt.minutes)), func(t *testing.T) {
			err := validateEmailCampaign(validABCampaignForDecisionDelay(tt.minutes))
			if tt.valid {
				if err != nil {
					t.Fatalf("decision_after_minutes=%d rejected: %v", tt.minutes, err)
				}
				return
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf(
					"decision_after_minutes=%d status = %v, want InvalidArgument: %v",
					tt.minutes,
					status.Code(err),
					err,
				)
			}
			if !strings.Contains(status.Convert(err).Message(), "between 30 and 10080") {
				t.Fatalf("decision_after_minutes=%d message = %v", tt.minutes, err)
			}
		})
	}
}
