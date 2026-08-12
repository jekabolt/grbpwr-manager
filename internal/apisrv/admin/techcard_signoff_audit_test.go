package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPrepareCreateTechCardSignoffsDiscardsClientAuditAndDigest(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-24 * time.Hour)
	tc := &entity.TechCardInsert{Signoffs: []entity.TechCardSignoff{{
		Section:      entity.SignoffDesign,
		State:        entity.SignoffStateApproved,
		SignedBy:     sql.NullString{String: "forged", Valid: true},
		SignedAt:     sql.NullTime{Time: oldTime, Valid: true},
		SignedDigest: sql.NullString{String: "client-supplied-digest", Valid: true},
	}}}

	fresh := prepareCreateTechCardSignoffs(tc, "alice", now)

	require.True(t, fresh[entity.SignoffDesign])
	got := tc.Signoffs[0]
	require.Equal(t, "alice", got.SignedBy.String)
	require.True(t, got.SignedAt.Valid)
	require.True(t, got.SignedAt.Time.Equal(now))
	require.True(t, got.SignedDigest.Valid)
	require.NotEqual(t, "client-supplied-digest", got.SignedDigest.String)
}

func TestUpdateTechCardCarriedApprovalUsesStoredAuditFields(t *testing.T) {
	storedAt := time.Date(2026, time.July, 1, 9, 0, 0, 0, time.UTC)
	forgedAt := storedAt.Add(-365 * 24 * time.Hour)
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(&entity.TechCard{
		TechCardInsert: entity.TechCardInsert{Signoffs: []entity.TechCardSignoff{{
			Section:      entity.SignoffMaterials,
			State:        entity.SignoffStateApproved,
			SignedBy:     sql.NullString{String: "original-approver", Valid: true},
			SignedAt:     sql.NullTime{Time: storedAt, Valid: true},
			SignedDigest: sql.NullString{String: "stored-digest", Valid: true},
		}}},
	}, nil)
	var written *entity.TechCardInsert
	techCards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, 7,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) {
			copy := *tc
			copy.Signoffs = append([]entity.TechCardSignoff(nil), tc.Signoffs...)
			written = &copy
		}).Return(nil, entity.ErrTechCardConflict)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "authenticated-admin")
	_, err := (&Server{repo: repo}).UpdateTechCard(ctx, &pb_admin.UpdateTechCardRequest{
		Id: 7, ExpectedLockVersion: 3,
		TechCard: &pb_common.TechCardInsert{
			StyleNumber: "TC-SIGNOFF",
			Name:        "signoff forgery guard",
			Signoffs: []*pb_common.TechCardSignoff{{
				Section:      pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_MATERIALS,
				State:        pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
				SignedBy:     "boss",
				SignedAt:     timestamppb.New(forgedAt),
				SignedDigest: "digest-copied-from-current-read",
			}},
		},
	})
	require.Equal(t, codes.Aborted, status.Code(err))
	require.NotNil(t, written)
	require.Len(t, written.Signoffs, 1)
	got := written.Signoffs[0]
	require.Equal(t, "original-approver", got.SignedBy.String)
	require.True(t, got.SignedAt.Time.Equal(storedAt))
	require.Equal(t, "stored-digest", got.SignedDigest.String)
}

func TestReconcileUpdateTechCardSignoffsMakesUnknownCarryFresh(t *testing.T) {
	now := time.Date(2026, time.August, 2, 10, 0, 0, 0, time.UTC)
	tc := &entity.TechCardInsert{Signoffs: []entity.TechCardSignoff{{
		Section:      entity.SignoffDesign,
		State:        entity.SignoffStateApproved,
		SignedBy:     sql.NullString{String: "boss", Valid: true},
		SignedDigest: sql.NullString{String: "forged-current-digest", Valid: true},
	}}}
	incoming := []*pb_common.TechCardSignoff{{
		Section:      pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_DESIGN,
		State:        pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
		SignedDigest: "forged-current-digest",
	}}

	fresh := reconcileUpdateTechCardSignoffs(tc, incoming, nil, "authenticated-admin", now)

	require.True(t, fresh[entity.SignoffDesign])
	require.Equal(t, "authenticated-admin", tc.Signoffs[0].SignedBy.String)
	require.True(t, tc.Signoffs[0].SignedAt.Time.Equal(now))
	require.NotEqual(t, "forged-current-digest", tc.Signoffs[0].SignedDigest.String)
}

func TestReconcileUpdateTechCardSignoffsKeepsLegacyCarryUnverifiable(t *testing.T) {
	tc := &entity.TechCardInsert{Signoffs: []entity.TechCardSignoff{{
		Section:      entity.SignoffDesign,
		State:        entity.SignoffStateApproved,
		SignedDigest: sql.NullString{String: "wire-carry-claim", Valid: true},
	}}}
	incoming := []*pb_common.TechCardSignoff{{
		Section:      pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_DESIGN,
		State:        pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
		SignedDigest: "wire-carry-claim",
	}}
	stored := []entity.TechCardSignoff{{
		Section:  entity.SignoffDesign,
		State:    entity.SignoffStateApproved,
		SignedBy: sql.NullString{String: "legacy-approver", Valid: true},
	}}

	fresh := reconcileUpdateTechCardSignoffs(tc, incoming, stored, "attacker", time.Now())

	require.False(t, fresh[entity.SignoffDesign])
	require.Equal(t, "legacy-approver", tc.Signoffs[0].SignedBy.String)
	require.False(t, tc.Signoffs[0].SignedDigest.Valid, "a stored legacy approval must remain stale")
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
	// nil fresh-set → nothing to restamp, so the card id is never dereferenced (0 is fine here).
	err := (&Server{}).restampFreshSignoffDigests(context.Background(), 0, tc, nil)
	require.NoError(t, err)
}

func TestUpdateTechCardRejectsFreshApprovalForAbsentPreservedSection(t *testing.T) {
	tests := []struct {
		name    string
		section pb_common.TechCardSignoffSection
	}{
		{"construction", pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_CONSTRUCTION},
		{"packaging", pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_PACKAGING},
		{"costing", pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COSTING},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			techCards := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(techCards)
			techCards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(&entity.TechCard{}, nil)

			_, err := (&Server{repo: repo}).UpdateTechCard(fullAccessCtx(), &pb_admin.UpdateTechCardRequest{
				Id: 7,
				TechCard: &pb_common.TechCardInsert{
					StyleNumber: "TC-ABSENT",
					Name:        "absent section guard",
					Signoffs: []*pb_common.TechCardSignoff{{
						Section: tt.section,
						State:   pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
					}},
				},
			})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "cannot approve "+tt.name)
			require.Contains(t, status.Convert(err).Message(), "include the section or drop the approval")
		})
	}
}
