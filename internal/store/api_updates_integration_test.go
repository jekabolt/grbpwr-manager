package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestAPIUpdatesIntegration exercises the five-feature batch (task fitting link + start
// times, fitting callouts, tech-card media split, costing redo) against a real MySQL, to
// catch any INSERT/SELECT column-name drift the unit tests can't. Throwaway harness.
func TestAPIUpdatesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Ids created below; cleaned up in reverse-dependency order so this test leaves the
	// shared DB as it found it (else the seed media's FK refs break TestMedia's cleanup).
	var mediaID, fid, tid, tcID, smID int
	defer func() {
		if smID != 0 {
			_ = s.Samples().DeleteSample(ctx, smID)
		}
		if tcID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, tcID)
		}
		if tid != 0 {
			_ = s.Tasks().DeleteTask(ctx, tid)
		}
		if fid != 0 {
			_ = s.Fittings().DeleteFitting(ctx, fid)
		}
		if mediaID != 0 {
			_ = s.Media().DeleteMediaById(ctx, mediaID)
		}
	}()

	// seed one media row (FK target for callouts / task media / tech-card media).
	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	nd := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}

	// ---- fitting callouts round-trip ----
	fid, err = s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		Callouts: []entity.FittingCallout{
			{Number: 1, Note: sql.NullString{String: "shoulder tight", Valid: true},
				MediaId: sql.NullInt32{Int32: int32(mediaID), Valid: true}, PosX: nd("0.25"), PosY: nd("0.5")},
		},
	})
	require.NoError(t, err)
	fit, err := s.Fittings().GetFittingById(ctx, fid)
	require.NoError(t, err)
	require.Len(t, fit.Callouts, 1)
	require.Equal(t, "shoulder tight", fit.Callouts[0].Note.String)
	require.Equal(t, int32(mediaID), fit.Callouts[0].MediaId.Int32)
	require.True(t, fit.Callouts[0].PosX.Valid)
	// update replaces callouts.
	require.NoError(t, s.Fittings().UpdateFitting(ctx, fid, &entity.FittingInsert{
		FittingDate: time.Now().UTC(), Status: entity.FittingDone, Verdict: entity.FittingApproved,
		Callouts: []entity.FittingCallout{
			{Number: 1, Note: sql.NullString{String: "a", Valid: true}},
			{Number: 2, Note: sql.NullString{String: "b", Valid: true}},
		},
	}))
	fit, err = s.Fittings().GetFittingById(ctx, fid)
	require.NoError(t, err)
	require.Len(t, fit.Callouts, 2)

	// ---- task: fitting link + planned start + auto started_at on first in_progress ----
	start := sql.NullTime{Time: time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC), Valid: true}
	tid, err = s.Tasks().AddTask(ctx, &entity.Task{
		TaskInsert: entity.TaskInsert{
			Title:     "sew",
			Priority:  entity.TaskPriorityUnknown,
			StartDate: start,
			FittingId: sql.NullInt32{Int32: int32(fid), Valid: true},
			MediaIds:  []int{mediaID},
		},
		Board:  entity.TaskBoardDevelopment,
		Status: entity.TaskStatusTodo,
	})
	require.NoError(t, err)
	task, err := s.Tasks().GetTaskById(ctx, tid)
	require.NoError(t, err)
	require.Equal(t, int32(fid), task.FittingId.Int32)
	require.True(t, task.StartDate.Valid)
	require.False(t, task.StartedAt.Valid, "started_at must be NULL before in_progress")

	require.NoError(t, s.Tasks().MoveTask(ctx, tid, "", entity.TaskStatusInProgress, 0))
	task, err = s.Tasks().GetTaskById(ctx, tid)
	require.NoError(t, err)
	require.True(t, task.StartedAt.Valid, "started_at stamped on first in_progress")
	firstStart := task.StartedAt.Time

	// move away and back — started_at must NOT change.
	require.NoError(t, s.Tasks().MoveTask(ctx, tid, "", entity.TaskStatusReview, 0))
	require.NoError(t, s.Tasks().MoveTask(ctx, tid, "", entity.TaskStatusInProgress, 0))
	task, err = s.Tasks().GetTaskById(ctx, tid)
	require.NoError(t, err)
	require.WithinDuration(t, firstStart, task.StartedAt.Time, time.Second, "started_at is not re-stamped")

	// ---- tech card: media split (moodboard/technical) + costing without pricing columns ----
	tcID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "IT-001", Valid: true},
		Name:            "Coat",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
		Media: []entity.TechCardMediaItem{
			{MediaId: mediaID, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaReference},
			{MediaId: mediaID, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
		},
		SizeQuantities: []entity.TechCardSizeQuantity{{SizeId: 4, OrderQty: 100}},
		BomItems:       []entity.TechCardBomItem{{Section: entity.BomSectionFabric, Name: "shell", UnitPrice: nd("2"), Currency: sql.NullString{String: "EUR", Valid: true}}},
		Colorways: []entity.TechCardColorway{{Name: "Black", LabDipStatus: entity.LabDipPending, Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: sql.NullInt32{Int32: 0, Valid: true}, Quantity: nd("3")},
		}}},
		Costing: &entity.TechCardCosting{CmtCost: nd("10"), Currency: sql.NullString{String: "EUR", Valid: true}},
	})
	require.NoError(t, err)
	card, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)

	var mood, tech int
	for _, m := range card.Media {
		switch m.Category {
		case entity.TechCardMediaCategoryMoodboard:
			mood++
		case entity.TechCardMediaCategoryTechnical:
			tech++
		}
	}
	require.Equal(t, 1, mood, "one moodboard media")
	require.Equal(t, 1, tech, "one technical media")
	require.Len(t, card.ResolvedMedia, 2)
	require.NotNil(t, card.Costing)
	require.True(t, card.Costing.CmtCost.Valid)
	require.Equal(t, "EUR", card.Costing.Currency.String)
	require.Len(t, card.Colorways, 1)
	require.Len(t, card.Colorways[0].Usages, 1)

	// ---- sample: photo media (B-6) + pattern link (B-3/gap-03) round-trip ----
	smID, err = s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: tcID, Purpose: entity.SamplePurposeProto, Status: entity.SampleStatusInSewing,
		FabricSource: entity.SampleFabricSample,
		PatternUrl:   sql.NullString{String: "https://x/pattern-v2.pdf", Valid: true},
		PatternNote:  sql.NullString{String: "выкройка v2, размер S", Valid: true},
		MediaIds:     []int{mediaID},
	})
	require.NoError(t, err)
	sm, err := s.Samples().GetSampleById(ctx, smID)
	require.NoError(t, err)
	require.Equal(t, "https://x/pattern-v2.pdf", sm.PatternUrl.String)
	require.Equal(t, "выкройка v2, размер S", sm.PatternNote.String)
	require.Len(t, sm.Media, 1, "sample photo media resolved")
	require.Equal(t, mediaID, sm.Media[0].Id)
	require.Equal(t, "https://x/t.jpg", sm.Media[0].ThumbnailMediaURL)
	// update full-replaces media: empty list clears it.
	require.NoError(t, s.Samples().UpdateSample(ctx, smID, &entity.SampleInsert{
		TechCardId: tcID, Purpose: entity.SamplePurposeProto, Status: entity.SampleStatusDone,
		FabricSource: entity.SampleFabricSample, MediaIds: nil,
	}))
	sm, err = s.Samples().GetSampleById(ctx, smID)
	require.NoError(t, err)
	require.Empty(t, sm.Media, "media cleared on full-replace update")

	// ---- B-9: ListTechCards resolves a preview thumbnail (proto card → technical media) ----
	cards, _, err := s.TechCards().ListTechCards(ctx, 100, 0, entity.Descending, entity.TechCardListFilter{Name: "IT-001"})
	require.NoError(t, err)
	var found bool
	for i := range cards {
		if cards[i].Id == tcID {
			found = true
			require.Equal(t, "https://x/t.jpg", cards[i].PreviewURL, "proto card preview = technical media thumbnail")
		}
	}
	require.True(t, found, "the seeded card is in the list")

	// ---- gap-01: GetStylePipeline returns all 6 lifecycle columns in order; proto column counts ours ----
	cols, err := s.TechCards().GetStylePipeline(ctx, 50)
	require.NoError(t, err)
	require.Len(t, cols, 6, "one column per lifecycle stage")
	wantOrder := []entity.TechCardStage{
		entity.TechCardStageIdea, entity.TechCardStageProto, entity.TechCardStageFit,
		entity.TechCardStageSMS, entity.TechCardStagePP, entity.TechCardStageProd,
	}
	for i, c := range cols {
		require.Equal(t, wantOrder[i], c.Stage, "columns in lifecycle order")
	}
	proto := cols[1]
	require.Equal(t, entity.TechCardStageProto, proto.Stage)
	require.GreaterOrEqual(t, proto.Count, 1, "at least our proto card")
	var inColumn bool
	for i := range proto.Cards {
		if proto.Cards[i].Id == tcID {
			inColumn = true
			require.Equal(t, "https://x/t.jpg", proto.Cards[i].PreviewURL, "pipeline card carries preview")
		}
	}
	require.True(t, inColumn, "our freshly-updated proto card is among the column's recent cards")
}
