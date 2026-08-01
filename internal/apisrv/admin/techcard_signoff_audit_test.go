package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestCreateTechCardFreshSignoffFailsWhenCatalogLoadFails(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().ListMaterials(mock.Anything, "", true).Return(nil, errors.New("catalog unavailable"))

	s := &Server{repo: repo}
	_, err := s.CreateTechCard(fullAccessCtx(), &pb_admin.CreateTechCardRequest{TechCard: &pb_common.TechCardInsert{
		StyleNumber: "TC-80B-A",
		Name:        "approval failure guard",
		BomItems: []*pb_common.TechCardBomItem{{
			Section:    pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
			MaterialId: 42,
		}},
		Signoffs: []*pb_common.TechCardSignoff{{
			Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_MATERIALS,
			State:   pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
		}},
	}})
	require.Equal(t, codes.Internal, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "try again")
}

func TestRestampFreshSignoffDigestsSkipsCatalogWithoutFreshApproval(t *testing.T) {
	tc := &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{{MaterialId: sql.NullInt64{Int64: 42, Valid: true}}},
		Signoffs: []entity.TechCardSignoff{{
			Section:      entity.SignoffMaterials,
			State:        entity.SignoffStateApproved,
			SignedDigest: sql.NullString{String: "carried", Valid: true},
		}},
	}
	err := (&Server{}).restampFreshSignoffDigests(context.Background(), tc,
		map[entity.TechCardSignoffSection]string{entity.SignoffMaterials: "payload"})
	require.NoError(t, err)
}
