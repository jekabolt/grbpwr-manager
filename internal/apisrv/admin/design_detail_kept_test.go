package admin

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── stale detail «keep» (0400) ───

func TestSetDesignDetailKeptPassesTheAskAndAnswersTheMark(t *testing.T) {
	s, design, _ := newJoinsSrv(t)
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	design.EXPECT().SetDetailKept(mock.Anything, mock.MatchedBy(func(r entity.DesignDetailKeptSet) bool {
		return r.TechCardId == 7 && r.SlotId == 9 && r.Keep && r.AgainstRunId == 50
	})).Return(&entity.DesignBenchSlot{
		Id: 9, ViewKey: entity.DesignViewDetail, Kind: entity.DesignPictureKindFlat,
		DetailName: sql.NullString{String: "strap", Valid: true},
		PictureId:  sql.NullInt32{Int32: 102, Valid: true},
		Stale:      true, Kept: true, StaleAgainstRunId: 50,
		KeptBy: "anna", KeptAt: sql.NullTime{Time: at, Valid: true},
	}, nil)
	resp, err := s.SetDesignDetailKept(context.Background(), &pb_admin.SetDesignDetailKeptRequest{
		TechCardId: 7, SlotId: 9, Keep: true, AgainstRunId: 50,
	})
	require.NoError(t, err)
	sl := resp.GetSlot()
	require.True(t, sl.GetStale())
	require.True(t, sl.GetKept())
	require.EqualValues(t, 50, sl.GetStaleAgainstRunId())
	require.Equal(t, "anna", sl.GetKeptBy())
	require.Equal(t, at, sl.GetKeptAt().AsTime())
}

func TestDesignSlotToPbHidesAStaleMarkThatNoLongerHolds(t *testing.T) {
	// stored mark, but views changed (Kept=false after ApplyDesignDetailStaleness): no byline on the wire
	pb := designSlotToPb(entity.DesignBenchSlot{
		Id: 9, ViewKey: entity.DesignViewDetail, Stale: true, StaleAgainstRunId: 60,
		KeptRunId: sql.NullInt32{Int32: 50, Valid: true}, KeptBy: "anna",
		KeptAt: sql.NullTime{Time: time.Now(), Valid: true},
	})
	require.True(t, pb.GetStale())
	require.False(t, pb.GetKept())
	require.Empty(t, pb.GetKeptBy())
	require.Nil(t, pb.GetKeptAt())
}

func TestSetDesignDetailKeptRefusals(t *testing.T) {
	s, design, _ := newJoinsSrv(t)
	_, err := s.SetDesignDetailKept(context.Background(), &pb_admin.SetDesignDetailKeptRequest{TechCardId: 7})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	for _, c := range []struct {
		err  error
		code codes.Code
	}{
		{entity.ErrDesignViewsChanged, codes.Aborted},
		{entity.ErrDesignDetailNotStale, codes.FailedPrecondition},
		{entity.ErrDesignNotAFlatDetail, codes.FailedPrecondition},
		{entity.ErrDesignDetailEmpty, codes.FailedPrecondition},
		{entity.ErrDesignNotFound, codes.NotFound},
	} {
		design.EXPECT().SetDetailKept(mock.Anything, mock.Anything).
			Return(nil, fmt.Errorf("%w: x", c.err)).Once()
		_, err := s.SetDesignDetailKept(context.Background(), &pb_admin.SetDesignDetailKeptRequest{
			TechCardId: 7, SlotId: 9, Keep: true,
		})
		require.Equal(t, c.code, status.Code(err), c.err.Error())
	}
}

// ─── T8: a detail run takes the accepted FRONT / BACK flats ───

func t8Bench() []entity.DesignBenchSlot {
	slot := func(id int, view string, media int) entity.DesignBenchSlot {
		return entity.DesignBenchSlot{
			Id: id, TechCardId: 7, ViewKey: view, Kind: entity.DesignPictureKindFlat,
			DetailName: sql.NullString{String: map[bool]string{true: "strap"}[view == entity.DesignViewDetail], Valid: view == entity.DesignViewDetail},
			PictureId:  sql.NullInt32{Int32: int32(media + 1000), Valid: true},
			Picture:    &entity.DesignPicture{Id: media + 1000, MediaId: media},
		}
	}
	return []entity.DesignBenchSlot{
		slot(1, entity.DesignViewFront, 11),
		slot(2, entity.DesignViewBack, 12),
		slot(3, entity.DesignViewSideL, 13),
		slot(4, entity.DesignViewSideR, 14),
		slot(5, entity.DesignViewDetail, 15),
	}
}

func TestADetailRunTakesTheAcceptedFrontAndBackFlats(t *testing.T) {
	slots, _ := designSelectBench(designInputSources{
		Kind:  entity.DesignRunKindFlat,
		Bench: t8Bench(),
		Params: &pb_common.DesignRunParams{
			Views: []string{entity.DesignViewDetail}, Layout: designLayoutOne, DetailSlotIds: []int32{5},
		},
	})
	var views []string
	for _, s := range slots {
		views = append(views, s.GetViewKey())
	}
	require.Equal(t, []string{entity.DesignViewFront, entity.DesignViewBack}, views,
		"a detail run reads the finished front and back — never the sides or the detail's own old plate")
}

func TestAViewsRunStillTakesNoBenchPlates(t *testing.T) {
	slots, _ := designSelectBench(designInputSources{
		Kind:  entity.DesignRunKindFlat,
		Bench: t8Bench(),
		Params: &pb_common.DesignRunParams{
			Views:  []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL, entity.DesignViewSideR},
			Layout: designLayoutOne,
		},
	})
	require.Empty(t, slots, "K-1: a views run must not be handed its own old flats")
}

func TestADetailRunWithUseFlatSlotsKeepsItsOwnMeaning(t *testing.T) {
	slots, _ := designSelectBench(designInputSources{
		Kind:  entity.DesignRunKindFlat,
		Bench: t8Bench(),
		Params: &pb_common.DesignRunParams{
			Views: []string{entity.DesignViewDetail}, DetailSlotIds: []int32{5},
			UseFlatSlots: true, FlatSlotIds: []int32{3},
		},
	})
	require.Len(t, slots, 1)
	require.Equal(t, entity.DesignViewSideL, slots[0].GetViewKey())
}

// ─── one sheet per press (wave 10): every route buys one picture ───

func TestFlatCandidateCountsPerRoute(t *testing.T) {
	sheet := []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL, entity.DesignViewSideR}
	for _, c := range []struct {
		name   string
		params *pb_common.DesignRunParams
		want   int
	}{
		{"photos", &pb_common.DesignRunParams{Views: sheet, Layout: designLayoutOne}, 1},
		{"photos named", &pb_common.DesignRunParams{Views: sheet, Layout: designLayoutOne,
			Flat: &pb_common.DesignFlatParams{Mode: "photos"}}, 1},
		{"hand_flat", &pb_common.DesignRunParams{Views: sheet, Layout: designLayoutOne,
			Flat: &pb_common.DesignFlatParams{Mode: "hand_flat"}}, 1},
		{"straps", &pb_common.DesignRunParams{Views: sheet, Layout: designLayoutOne,
			Flat: &pb_common.DesignFlatParams{Mode: "straps"}}, 1},
		{"detail", &pb_common.DesignRunParams{Views: []string{entity.DesignViewDetail}, Layout: designLayoutOne,
			DetailSlotIds: []int32{5}}, 1},
	} {
		require.Equal(t, c.want, designRequestedOutputs(entity.DesignRunKindFlat, c.params), c.name)
	}
}
