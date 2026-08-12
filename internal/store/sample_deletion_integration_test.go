package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestSampleDeletionBlockedByFittings — вторая половина границы удаления семпла (первую, про
// невозвращённый материал, держит TestMaterialWarehouse).
//
// Примерку схема бы пережила: fitting.sample_id — ON DELETE SET NULL, то есть MySQL молча оставил
// бы вердикт, снятый ни с чего. Отказ здесь — решение владельца, и проверять его надо на реальной
// базе: то, что запись «переживает» удаление, видно только в FK, а не в классификации.
//
// Заодно проверяется, что каскад (фотографии/замены) и сироты (задачи, следующие раунды) считаются
// РАЗНЫМИ категориями: диалог обязан различать «умрёт» и «потеряет семпл».
func TestSampleDeletionBlockedByFittings(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	var techCardID int
	var sampleIDs, fittingIDs []int
	defer func() {
		for _, id := range fittingIDs {
			_ = s.Fittings().DeleteFitting(ctx, id)
		}
		for _, id := range sampleIDs {
			_, _, _ = s.Samples().DeleteSample(ctx, id)
		}
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, techCardID)
		}
	}()

	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "SMP-DEL-1", Valid: true},
		Name:            "sample deletion",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)

	mkSample := func() (int, error) {
		return s.Samples().AddSample(ctx, &entity.SampleInsert{
			TechCardId:   techCardID,
			Purpose:      entity.SamplePurposeFit,
			Status:       entity.SampleStatusPlanned,
			FabricSource: entity.SampleFabricSample,
			CreatedBy:    "tester",
			UpdatedBy:    "tester",
		})
	}

	// Семпл без единой связи удаляется молча — и это тот самый случай, ради которого фича писалась.
	empty, err := mkSample()
	require.NoError(t, err)
	v, _, err := s.Samples().EvaluateSampleDeletion(ctx, empty)
	require.NoError(t, err)
	require.True(t, v.Deletable, "blockers: %s", v.BlockerSummary())
	require.Empty(t, v.Cascade)
	require.Empty(t, v.Orphans)
	_, _, err = s.Samples().DeleteSample(ctx, empty)
	require.NoError(t, err)

	// Семпл с примеркой держится ею, и отказ НАЗЫВАЕТ её.
	withFitting, err := mkSample()
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, withFitting)
	fittingID, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		TechCardId:  sql.NullInt32{Int32: int32(techCardID), Valid: true},
		SampleId:    sql.NullInt32{Int32: int32(withFitting), Valid: true},
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		CreatedBy:   "tester",
		UpdatedBy:   "tester",
	})
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, fittingID)

	// Следующий раунд ссылается на этот семпл (previous_sample_id проставляется автоматически) —
	// он удаление НЕ держит, он его переживает и теряет звено цепочки.
	next, err := mkSample()
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, next)

	v, scrapped, err := s.Samples().EvaluateSampleDeletion(ctx, withFitting)
	require.NoError(t, err)
	require.False(t, scrapped)
	require.False(t, v.Deletable)
	require.Len(t, v.Blockers, 1)
	require.Equal(t, entity.SampleBlockerFitting, v.Blockers[0].Reason)
	require.Equal(t, 1, v.Blockers[0].Count)
	requireEntry(t, v.Orphans, entity.SampleOrphanNextRound, 1)

	// Пере-проверка внутри транзакции решает то же самое — вердикт возвращается РЯДОМ с ошибкой,
	// иначе API-слою нечем было бы назвать причину.
	refused, _, err := s.Samples().DeleteSample(ctx, withFitting)
	require.ErrorIs(t, err, entity.ErrSampleNotDeletable)
	require.NotNil(t, refused)
	require.Equal(t, entity.SampleBlockerFitting, refused.Blockers[0].Reason)

	// Убрали примерку — и тот же семпл удаляется, ничего больше не трогая.
	require.NoError(t, s.Fittings().DeleteFitting(ctx, fittingID))
	fittingIDs = nil
	v, _, err = s.Samples().EvaluateSampleDeletion(ctx, withFitting)
	require.NoError(t, err)
	require.True(t, v.Deletable, "blockers: %s", v.BlockerSummary())
	_, _, err = s.Samples().DeleteSample(ctx, withFitting)
	require.NoError(t, err)

	// Следующий раунд пережил удаление предыдущего, как и обещал вердикт.
	survivor, err := s.Samples().GetSampleById(ctx, next)
	require.NoError(t, err)
	require.False(t, survivor.PreviousSampleId.Valid, "the chain link went to NULL, the round stayed")

	// Несуществующий семпл — это NotFound, а не «неудаляем»: разница видна пользователю.
	_, _, err = s.Samples().EvaluateSampleDeletion(ctx, withFitting)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func requireEntry(t *testing.T, entries []entity.SampleDeletionEntry, reason string, count int) {
	t.Helper()
	for _, e := range entries {
		if e.Reason == reason {
			require.Equal(t, count, e.Count, "entry %s", reason)
			return
		}
	}
	t.Fatalf("no %s entry in %+v", reason, entries)
}
