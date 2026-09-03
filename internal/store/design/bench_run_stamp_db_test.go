package design_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ═══ ПЛИТА СЛОТА ПРИЕЗЖАЕТ СО ШТАМПОМ СВОЕГО ПРОГОНА (J-25/J-26) ══════════════════════════════
//
// ЧТО БЫЛО СЛОМАНО, И ПОЧЕМУ ЭТОГО НЕЛЬЗЯ БЫЛО УВИДЕТЬ ИЗ БЕЗОПАСНЫХ ПАКЕТОВ. Слот отдаёт плиту
// целиком — контракт объясняет это тем, что «плита в слоте РЕГУЛЯРНО СТАРШЕ ПЕРВОЙ СТРАНИЦЫ
// ПРОГОНОВ». Ровно тот же довод верен и для СТРОКИ ПРОГОНА за `picture.run_id`, и вывод из него
// сделан не был: id уезжал, а строки за ним в ответе не было — полоса везёт двенадцать свежих
// (design.DefaultRunPageLimit). Клиент, спрашивающий «какой ревизии эта сторона», получал ноль на
// всякой карточке с историей, и отказ 3D «four sides of ONE revision» переставал срабатывать
// вовсе: множество ревизий схлопывалось в пустое. Сторож, которому подают нули, выглядит
// покрытием и не охраняет ничего.
//
// ПРОБА ДОКАЗЫВАЕТ ИМЕННО ЭТО, А НЕ «ПОЛЕ ЗАПОЛНЕНО»: прогон намеренно ВЫТАЛКИВАЕТСЯ за первую
// страницу двенадцатью соседями, и после этого штамп у слота обязан остаться.
func TestDesignDBBenchSlotCarriesTheRunStampOfItsPlate(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	out := probeMedia(t, raw)
	handMedia := probeMedia(t, raw)
	ctx := context.Background()

	// ─── прогон РЕНДЕРА: только он минтует rrev (queue/wave2: MAX+1 среди render-прогонов) ───
	started, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindRender,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
	})
	require.NoError(t, err)
	require.Positive(t, started.Run.Rrev, "рендер-прогон обязан получить ревизию: без неё нечего штамповать")

	token := uuid.NewString()
	_, err = rep.Design().ClaimRuns(ctx, 8, time.Minute, token)
	require.NoError(t, err)
	done, err := rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: started.Run.Id, ClaimToken: token,
		Outputs: []entity.DesignPictureInsert{{MediaId: out, Ordinal: 0}},
	})
	require.NoError(t, err)
	require.Len(t, done.Pictures, 1)
	plate := done.Pictures[0]
	require.Equal(t, entity.DesignPictureKindRender, plate.Kind,
		"выход рендер-прогона — рендер: иначе он и в рендер-слот не встанет")

	// ─── постановка на РЕНДЕР-верстак ───
	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
		},
		PictureId: plate.Id, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignRunKindRender, slot.RunKind,
		"ответ постановки обязан нести штамп: экран перерисовывает плитку из него, не перечитывая полосу")
	require.Equal(t, started.Run.Rrev, slot.RunRrev)

	// ─── ДВЕНАДЦАТЬ СОСЕДЕЙ ВЫТАЛКИВАЮТ РЕНДЕР-ПРОГОН ЗА ПЕРВУЮ СТРАНИЦУ ───
	//
	// Это и есть замер, ради которого проба существует. Без него она была бы зелена и на сервере,
	// который штамп не резолвит вовсе: на свежей карточке прогон лежит на первой странице, и
	// клиент нашёл бы ревизию сам.
	for i := 0; i < 12; i++ {
		startProbeRun(t, rep, card, "0.10")
	}

	band, err := rep.Design().GetBand(ctx, card, 12)
	require.NoError(t, err)
	for _, r := range band.Runs {
		require.NotEqual(t, started.Run.Id, r.Id,
			"положительный контроль замера: прогон обязан УЙТИ со страницы, иначе доказывать нечего")
	}

	var found bool
	for _, s := range band.Bench {
		if s.Id != slot.Id {
			continue
		}
		found = true
		require.NotNil(t, s.Picture, "плита слота резолвится по тому же доводу, что и её прогон")
		require.Equal(t, entity.DesignRunKindRender, s.RunKind,
			"род прогона — единственное, чем перекрашенный кадр отличается от фабрик-рендера на этом верстаке")
		require.Equal(t, started.Run.Rrev, s.RunRrev,
			"ревизия обязана пережить уход прогона со страницы: на этом стоит отказ «four sides of ONE revision»")
	}
	require.True(t, found, "слот обязан быть в полосе")

	// ─── КОНТРОЛЬ ВТОРОГО РОДА: ПЛИТА БЕЗ ПРОГОНА ШТАМПА НЕ ВЫДУМЫВАЕТ ───
	//
	// Без него проба выше зеленела бы на реализации, которая подставляет род и ревизию «первого
	// попавшегося» прогона карточки — а такая подстановка и есть ложная атрибуция, от которой эта
	// полоса отказывалась дважды.
	batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: handMedia, Kind: entity.DesignPictureKindRender}},
	})
	require.NoError(t, err)
	hand, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewBack, Kind: entity.DesignPictureKindRender,
		},
		PictureId: batch.Pictures[0].Id, Actor: "probe",
	})
	require.NoError(t, err)
	require.Empty(t, hand.RunKind, "никто её не генерил — штампа нет и выдумывать его нечем")
	require.Zero(t, hand.RunRrev)
}
