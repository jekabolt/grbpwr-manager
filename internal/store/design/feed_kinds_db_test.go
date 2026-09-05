package design_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ЖИВАЯ ПРОБА «ЧЕРНОВИК УХОДИТ ИЗ ЛЕНТЫ И ОСТАЁТСЯ В РЕЕСТРЕ» (B-21).
//
// Владелец дословно: «генерация DRAFT OF THE CONSTRUCTION не долна попадать в историю генераций в
// принципе». Требование про ЭКРАН, но чинится оно на СЕРВЕРЕ, и проба обязана держать обе половины
// сразу, потому что порознь каждая из них выглядит выполненной:
//
//	ПОЛОВИНА 1 — ЛЕНТЫ НЕТ. Страница не отдаёт строку черновика, и ОБА счётчика заголовка
//	(всего / в архиве) её не считают. Одна страница без счётчиков дала бы ленту, чей заголовок
//	обещает строки, которых на ней нет: пейджер («страница 1 из N»), притязание firstRunId и полка
//	архива делят загруженные строки на числа, приходящие из этих же двух запросов.
//
//	ПОЛОВИНА 2 — РЕЕСТР ЦЕЛ. Черновик — ОПЛАЧЕННЫЙ вызов: у него есть строка, оценка и движение
//	дневного бюджета. Прогон, за который заплатили и которого нет в регистре, — это дыра в
//	бухгалтерии, и убрать его оттуда владелец не просил. Поэтому строка обязана читаться по id со
//	своей ценой, а дневной резерв — включать её.
//
// ⚠ МУТАЦИИ, КОТОРЫЕ ЭТО ОБЯЗАНЫ КРАСНИТЬ, ПОИМЕННО:
//   - снять designFeedKinds с listRunsTx — черновик вернётся на страницу;
//   - снять его с designCountRuns — TotalRuns станет 3 при двух строках на экране;
//   - снять его с designCountArchivedRuns — архив насчитает спрятанный черновик;
//   - дописать его в GetRun или в движение бюджета — красной станет вторая половина, то есть
//     сокрытие уедет из ленты в деньги.
func TestDesignDBDraftIdeaLeavesTheFeedAndStaysInTheLedger(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	ctx := context.Background()
	card := probeCard(t, raw)

	// ДВА КАРТИНОЧНЫХ ПРОГОНА — то, что лента показывать обязана.
	firstFlat := startProbeRun(t, rep, card, "0.20")
	secondFlat := startProbeRun(t, rep, card, "0.20")

	// ЧЕРНОВИК ИДЁТ ЧЕРЕЗ ТУ ЖЕ ДЕНЕЖНУЮ МАШИНУ, ЧТО И ОСТАЛЬНЫЕ, И ЭТО НАМЕРЕННО: проба, которая
	// клала бы его прямым INSERT, доказывала бы про ленту и молчала бы про деньги — то есть ровно
	// про ту половину, которую эта волна не имеет права сломать.
	draftEstimate := decimal.RequireFromString("0.035")
	draft, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId:       card,
		ClientRequestId:  uuid.NewString(),
		Kind:             entity.DesignRunKindDraftIdea,
		RequestedOutputs: 0, // текстовый прогон не рождает ни одного кадра
		// ЛИЗА ОБЯЗАТЕЛЬНА У ЭТОГО РОДА (см. design.HandlerLeaseFor): её считает тот, кто держит
		// клиента поставщика, и стор отказывает просьбе без неё.
		HandlerLease:  design.HandlerLeaseFor(0, entity.DesignDraftAnswerCeilings()...),
		PriceEstimate: decimal.NullDecimal{Decimal: draftEstimate, Valid: true},
		Author:        "probe",
	})
	require.NoError(t, err)

	// ─── ПОЛОВИНА 1: ЛЕНТЫ НЕТ ───

	band, err := rep.Design().GetBand(ctx, card, design.DefaultRunPageLimit)
	require.NoError(t, err)

	feedIDs := map[int]string{}
	for _, r := range band.Runs {
		feedIDs[r.Id] = r.Kind
	}
	require.Len(t, band.Runs, 2, "лента карточки обязана нести ровно два картиночных прогона, а не три строки")
	require.Contains(t, feedIDs, firstFlat.Run.Id)
	require.Contains(t, feedIDs, secondFlat.Run.Id)
	require.NotContains(t, feedIDs, draft.Run.Id,
		"черновик конструкции не имеет права попасть в ленту (B-21)")
	for id, kind := range feedIDs {
		require.NotEqual(t, entity.DesignRunKindDraftIdea, kind,
			"строка %d рода draft_idea приехала лентой", id)
	}
	require.Equal(t, 2, band.TotalRuns,
		"заголовок ленты считает ТО ЖЕ, что лента показывает: иначе пейджер обещает страницу, которой нет")
	require.Equal(t, 0, band.ArchivedRuns)

	// ПРОДОЛЖЕНИЕ СТРАНИЦЫ ЧИТАЕТ ТОТ ЖЕ НАБОР. Второй читатель ленты (ListRuns) ходит в ту же
	// listRunsTx, и проба зовёт его отдельно: предикат, поставленный только в GetBand, оставил бы
	// черновик на второй странице истории.
	page, err := rep.Design().ListRuns(ctx, entity.DesignRunPage{TechCardId: card, Limit: 50})
	require.NoError(t, err)
	require.Len(t, page.Runs, 2)
	for _, r := range page.Runs {
		require.NotEqual(t, draft.Run.Id, r.Id, "черновик вернулся продолжением страницы")
	}

	// АРХИВ СЧИТАЕТ ЛЕНТУ, А НЕ РЕЕСТР. Спрятанный черновик архивом не является — он и на полке не
	// нужен; спрятанный картиночный прогон является.
	_, err = rep.Design().ArchiveRun(ctx, draft.Run.Id, true, "probe")
	require.NoError(t, err)
	_, err = rep.Design().ArchiveRun(ctx, secondFlat.Run.Id, true, "probe")
	require.NoError(t, err)

	band, err = rep.Design().GetBand(ctx, card, design.DefaultRunPageLimit)
	require.NoError(t, err)
	require.Equal(t, 1, band.ArchivedRuns,
		"на полке архива обязан лежать один картиночный прогон, а не он же плюс черновик")
	require.Equal(t, 2, band.TotalRuns,
		"архивирование ленту не сокращает — GetBand берёт страницу вместе с архивными")

	// ─── ПОЛОВИНА 2: РЕЕСТР ЦЕЛ ───

	stored, err := rep.Design().GetRun(ctx, draft.Run.Id)
	require.NoError(t, err, "прямое чтение по id — не лента: строка обязана читаться")
	require.Equal(t, entity.DesignRunKindDraftIdea, stored.Kind)
	require.True(t, stored.PriceEstimate.Valid, "у оплаченного прогона обязана быть оценка")
	require.True(t, stored.PriceEstimate.Decimal.Equal(draftEstimate),
		"цена черновика обязана остаться его ценой: %s", stored.PriceEstimate.Decimal)

	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.Equal(decimal.RequireFromString("0.435")),
		"дневной резерв обязан включать черновик (0.20 + 0.20 + 0.035): %s", budget.Reserved)
}
