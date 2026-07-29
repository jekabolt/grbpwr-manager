package campaign

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/stretchr/testify/mock"
)

type engagementResult int64

func (r engagementResult) LastInsertId() (int64, error) { return 0, nil }
func (r engagementResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestRecordRecipientEngagementIdempotentTimestampSQL(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, time.July, 29, 12, 0, 0, 123000, time.UTC)
	tests := []struct {
		name      string
		kind      entity.EmailCampaignEngagementKind
		wantSQL   []string
		rows      engagementResult
		callCount int
	}{
		{
			name:      "unknown resend id is a no-op",
			kind:      entity.EmailCampaignEngagementDelivered,
			wantSQL:   []string{"COALESCE(delivered_at, :at)", "WHERE resend_email_id = :resend_email_id"},
			rows:      0,
			callCount: 1,
		},
		{
			name: "redelivered open keeps timestamps but bumps counter",
			kind: entity.EmailCampaignEngagementOpened,
			wantSQL: []string{
				"first_opened_at = COALESCE(first_opened_at, :at)",
				"GREATEST(COALESCE(last_opened_at, :at), :at)",
				"open_count = open_count + 1",
			},
			rows:      1,
			callCount: 2,
		},
		{
			name: "redelivered click keeps timestamps but bumps counter",
			kind: entity.EmailCampaignEngagementClicked,
			wantSQL: []string{
				"first_clicked_at = COALESCE(first_clicked_at, :at)",
				"GREATEST(COALESCE(last_clicked_at, :at), :at)",
				"click_count = click_count + 1",
			},
			rows:      1,
			callCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := mocks.NewMockDB(t)
			db.EXPECT().
				NamedExecContext(
					ctx,
					mock.MatchedBy(func(query string) bool {
						for _, fragment := range tt.wantSQL {
							if !strings.Contains(query, fragment) {
								return false
							}
						}
						return true
					}),
					mock.MatchedBy(func(params map[string]any) bool {
						return params["resend_email_id"] == "resend-123" &&
							params["at"] == at
					}),
				).
				Return(sql.Result(tt.rows), nil).
				Times(tt.callCount)
			store := New(storeutil.Base{DB: db, Now: time.Now}, nil)

			for range tt.callCount {
				if err := store.RecordRecipientEngagement(ctx, "resend-123", tt.kind, at); err != nil {
					t.Fatalf("RecordRecipientEngagement() error = %v", err)
				}
			}
		})
	}
}
