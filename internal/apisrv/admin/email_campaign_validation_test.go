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

// TestValidateEmailCampaignVariantLabelsUnique guards the fix for the silent-data-loss
// path: two variants sharing a label would both resolve to one row in the store's id-less
// "resolve by label" sync (last-writer-wins), and the DB unique constraint never fires
// because the two UPDATEs touch the same row. Validation must reject duplicates up front.
func TestValidateEmailCampaignVariantLabelsUnique(t *testing.T) {
	campaign := func(labels ...string) *pb_common.EmailCampaignInsert {
		vs := make([]*pb_common.EmailCampaignVariant, 0, len(labels))
		for _, l := range labels {
			vs = append(vs, &pb_common.EmailCampaignVariant{Label: l})
		}
		c := &pb_common.EmailCampaignInsert{
			Name:     "Label uniqueness",
			Topic:    pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_EVENTS,
			Variants: vs,
		}
		// Multiple variants require an A/B config (a non-A/B campaign is capped at one
		// variant by an earlier rule). CONTENT dimension avoids the distinct-subjects
		// check so the label check is what we exercise.
		if len(labels) > 1 {
			c.AbConfig = &pb_common.ABConfig{
				Enabled:              true,
				Dimension:            pb_common.ABDimension_AB_DIMENSION_CONTENT,
				TestPct:              20,
				DecisionAfterMinutes: 30,
			}
		}
		return c
	}
	tests := []struct {
		name   string
		labels []string
		valid  bool
	}{
		{name: "distinct", labels: []string{"A", "B"}, valid: true},
		{name: "single", labels: []string{"A"}, valid: true},
		{name: "duplicate", labels: []string{"A", "A"}},
		// Trimmed collision: " A" and "A" are the same label after TrimSpace.
		{name: "duplicate after trim", labels: []string{"A", " A"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEmailCampaign(campaign(tt.labels...))
			if tt.valid {
				if err != nil {
					t.Fatalf("labels %v rejected: %v", tt.labels, err)
				}
				return
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("labels %v status = %v, want InvalidArgument: %v", tt.labels, status.Code(err), err)
			}
			if !strings.Contains(status.Convert(err).Message(), "unique") {
				t.Fatalf("labels %v message = %v, want a uniqueness message", tt.labels, err)
			}
		})
	}
}
