package admin

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func TestStampFreshTechCardSignoffAudit(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-24 * time.Hour)
	tc := &entity.TechCardInsert{Signoffs: []entity.TechCardSignoff{
		{
			State:        entity.SignoffStateApproved,
			SignedBy:     sql.NullString{String: "forged", Valid: true},
			SignedAt:     sql.NullTime{Time: oldTime, Valid: true},
			SignedDigest: sql.NullString{String: "freshly-stamped-digest", Valid: true},
		},
		{
			State:        entity.SignoffStateApproved,
			SignedBy:     sql.NullString{String: "original", Valid: true},
			SignedAt:     sql.NullTime{Time: oldTime, Valid: true},
			SignedDigest: sql.NullString{String: "carried-digest", Valid: true},
		},
		{
			State:    entity.SignoffStatePending,
			SignedBy: sql.NullString{String: "untouched", Valid: true},
			SignedAt: sql.NullTime{Time: oldTime, Valid: true},
		},
	}}
	incoming := []*pb_common.TechCardSignoff{
		{State: pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED},
		{State: pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED, SignedDigest: "carried-digest"},
		{State: pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_PENDING},
	}

	stampFreshTechCardSignoffAudit(tc, incoming, "alice", now)

	if got := tc.Signoffs[0]; got.SignedBy.String != "alice" || !got.SignedBy.Valid || !got.SignedAt.Valid || !got.SignedAt.Time.Equal(now) {
		t.Errorf("fresh approval audit was not server-stamped: %+v", got)
	}
	if got := tc.Signoffs[1]; got.SignedBy.String != "original" || !got.SignedAt.Time.Equal(oldTime) {
		t.Errorf("carried approval audit must remain unchanged: %+v", got)
	}
	if got := tc.Signoffs[2]; got.SignedBy.String != "untouched" || !got.SignedAt.Time.Equal(oldTime) {
		t.Errorf("non-approved signoff audit must remain unchanged: %+v", got)
	}
}
