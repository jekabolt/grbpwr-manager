package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func seamDecisionReq(ds ...*pb_common.TechCardSeamDecision) *pb_admin.AddTechCardSeamDecisionsRequest {
	return &pb_admin.AddTechCardSeamDecisionsRequest{TechCardId: 7, Decisions: ds}
}

func seamDecisionAccept() *pb_common.TechCardSeamDecision {
	return &pb_common.TechCardSeamDecision{
		OperationNumber: 3,
		Action:          pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_ACCEPT,
		SeamKey:         strings.ToLower(seamHandlerKey),
		Tier:            pb_common.TechCardSeamGuessTier_TECH_CARD_SEAM_GUESS_TIER_CONFIDENT,
		Shown:           `{"a":[{"piece":"BP","k":2,"len":410}],"b":[{"piece":"FRONT_L","k":5,"len":408}]}`,
		ClientVersion:   "a1b2c3d",
	}
}

// The decisions reach the store converted, the key upper-cased, stamped with the caller.
func TestAddTechCardSeamDecisionsPassesCreatedBy(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	skip := &pb_common.TechCardSeamDecision{OperationNumber: 4,
		Action: pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_SKIP}
	techCards.EXPECT().AddTechCardSeamDecisions(mock.Anything, mock.MatchedBy(func(in entity.TechCardSeamDecisionsWrite) bool {
		return in.TechCardId == 7 && in.By == "ann" && len(in.Decisions) == 2 &&
			in.Decisions[0].Action == entity.TechCardSeamDecisionAccept &&
			in.Decisions[0].SeamKey == seamHandlerKey &&
			in.Decisions[0].OperationNumber == 3 &&
			in.Decisions[0].Tier == entity.TechCardSeamGuessTierConfident &&
			in.Decisions[0].ClientVersion == "a1b2c3d" &&
			in.Decisions[1].Action == entity.TechCardSeamDecisionSkip && in.Decisions[1].SeamKey == "" &&
			in.Decisions[1].Tier == entity.TechCardSeamGuessTierUnknown
	})).Return(2, nil)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "ann")
	resp, err := (&Server{repo: repo}).AddTechCardSeamDecisions(ctx, seamDecisionReq(seamDecisionAccept(), skip))
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.GetWritten())
}

// Malformed input is refused before the store (strict mock), with the field named.
func TestAddTechCardSeamDecisionsValidation(t *testing.T) {
	type mut func(d *pb_common.TechCardSeamDecision)
	cases := []struct {
		name  string
		mut   mut
		field string // "" = accepted
	}{
		{"accept ok", func(d *pb_common.TechCardSeamDecision) {}, ""},
		{"card-level review step 0", func(d *pb_common.TechCardSeamDecision) {
			d.OperationNumber = 0
			d.Action = pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_REFUSE
		}, ""},
		{"empty shown ok", func(d *pb_common.TechCardSeamDecision) { d.Shown = "" }, ""},
		{"own ok", func(d *pb_common.TechCardSeamDecision) {
			d.Action = pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_OWN
		}, ""},
		{"negative step", func(d *pb_common.TechCardSeamDecision) { d.OperationNumber = -1 }, "decisions[0].operation_number"},
		{"unknown action", func(d *pb_common.TechCardSeamDecision) {
			d.Action = pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_UNKNOWN
		}, "decisions[0].action"},
		{"out-of-range action", func(d *pb_common.TechCardSeamDecision) { d.Action = 99 }, "decisions[0].action"},
		{"out-of-range tier", func(d *pb_common.TechCardSeamDecision) { d.Tier = 99 }, "decisions[0].tier"},
		{"skip with key", func(d *pb_common.TechCardSeamDecision) {
			d.Action = pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_SKIP
		}, "decisions[0].seam_key"},
		{"accept without key", func(d *pb_common.TechCardSeamDecision) { d.SeamKey = "" }, "decisions[0].seam_key"},
		{"refuse without key", func(d *pb_common.TechCardSeamDecision) {
			d.Action = pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_REFUSE
			d.SeamKey = " "
		}, "decisions[0].seam_key"},
		{"key not a ULID", func(d *pb_common.TechCardSeamDecision) { d.SeamKey = "01JABCDEFGHJKMNPQRSTVWXYZI" }, "decisions[0].seam_key"},
		{"key too short", func(d *pb_common.TechCardSeamDecision) { d.SeamKey = "01JABC" }, "decisions[0].seam_key"},
		{"shown not JSON", func(d *pb_common.TechCardSeamDecision) { d.Shown = "{a:" }, "decisions[0].shown"},
		{"shown too big", func(d *pb_common.TechCardSeamDecision) {
			d.Shown = `"` + strings.Repeat("x", entity.TechCardSeamDecisionShownMaxBytes) + `"`
		}, "decisions[0].shown"},
		{"client_version too long", func(d *pb_common.TechCardSeamDecision) { d.ClientVersion = strings.Repeat("a", 41) }, "decisions[0].client_version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t) // strict: a store call on a refused case fails the test
			d := seamDecisionAccept()
			c.mut(d)
			if c.field == "" {
				techCards := mocks.NewMockTechCards(t)
				repo.EXPECT().TechCards().Return(techCards)
				techCards.EXPECT().AddTechCardSeamDecisions(mock.Anything, mock.Anything).Return(1, nil)
			}
			_, err := (&Server{repo: repo}).AddTechCardSeamDecisions(fullAccessCtx(), seamDecisionReq(d))
			if c.field == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), c.field)
		})
	}

	repo := mocks.NewMockRepository(t)
	_, err := (&Server{repo: repo}).AddTechCardSeamDecisions(fullAccessCtx(), &pb_admin.AddTechCardSeamDecisionsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "no card id")
	_, err = (&Server{repo: repo}).AddTechCardSeamDecisions(fullAccessCtx(), seamDecisionReq())
	require.Equal(t, codes.InvalidArgument, status.Code(err), "no decisions")
	_, err = (&Server{repo: repo}).AddTechCardSeamDecisions(fullAccessCtx(), seamDecisionReq(nil))
	require.Equal(t, codes.InvalidArgument, status.Code(err), "nil decision")
}

// At most 50 per call: 50 passes, 51 is refused before the store.
func TestAddTechCardSeamDecisionsLimit(t *testing.T) {
	batch := func(n int) []*pb_common.TechCardSeamDecision {
		out := make([]*pb_common.TechCardSeamDecision, n)
		for i := range out {
			out[i] = seamDecisionAccept()
		}
		return out
	}
	repo := mocks.NewMockRepository(t)
	_, err := (&Server{repo: repo}).AddTechCardSeamDecisions(fullAccessCtx(), seamDecisionReq(batch(51)...))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "decisions")

	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().AddTechCardSeamDecisions(mock.Anything, mock.MatchedBy(func(in entity.TechCardSeamDecisionsWrite) bool {
		return len(in.Decisions) == entity.TechCardSeamDecisionMaxPerCall
	})).Return(50, nil)
	resp, err := (&Server{repo: repo}).AddTechCardSeamDecisions(fullAccessCtx(), seamDecisionReq(batch(50)...))
	require.NoError(t, err)
	require.EqualValues(t, 50, resp.GetWritten())
}

func TestAddTechCardSeamDecisionsMapsStoreErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		fk   bool
		code codes.Code
	}{
		{"unknown card", fmt.Errorf("tech card 7: %w", sql.ErrNoRows), false, codes.NotFound},
		{"card deleted mid-write", errors.New("fk"), true, codes.NotFound},
		{"unexpected", errors.New("boom"), false, codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			techCards := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(techCards)
			techCards.EXPECT().AddTechCardSeamDecisions(mock.Anything, mock.Anything).Return(0, c.err)
			repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(c.fk).Maybe()
			_, err := (&Server{repo: repo}).AddTechCardSeamDecisions(fullAccessCtx(), seamDecisionReq(seamDecisionAccept()))
			require.Equal(t, c.code, status.Code(err))
		})
	}
}

// Same right as the seam write itself.
func TestTechCardSeamDecisionsRBAC(t *testing.T) {
	want, _, ok := rbac.Lookup(rbac.MethodPrefix + "UpsertTechCardSeams")
	require.True(t, ok)
	got, allowlisted, known := rbac.Lookup(rbac.MethodPrefix + "AddTechCardSeamDecisions")
	require.True(t, known)
	require.False(t, allowlisted)
	require.Equal(t, want, got)
	require.Equal(t, entity.AccessWrite, got.Access)
}
