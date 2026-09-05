package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	designstore "github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ═══════ ТРЕТЬЯ ОСЬ ДРЕЙФА ЛИЗЫ: НАСТРОЕННАЯ БАЗА СРОКА ВЫЗОВА ═══════
//
// ЧТО БЫЛО. store/design считала лизу сама, через openrouter.DefaultCompletionBudget, то есть от
// КОДОВОЙ базы в 60 s. На проводе база приходит из конфигурации: openrouter.New кладёт
// cfg.HTTPTimeout в budgetBase, и postChatCompletion считает срок КАЖДОГО вызова от него. Два
// числа, третья ось — и все прежние пробы спрашивали ту же кодовую базу, поэтому оставались
// зелёными при любом значении переменной.
//
// АРИФМЕТИКА. OPENROUTER_HTTP_TIMEOUT = 240 s (значение, которое эта организация уже написала в
// спек соседнему клиенту — config/cfg_env_orimages_test.go): бюджет вызова 240 + 8000/30 =
// 506.67 s против лизы 60 + 266.67 + 90 = 416.67 s. На 90-й секунде ВНУТРИ платного вызова
// claim_expires_at уже прошёл, повтор того же client_request_id проходит designRunResumableSQL,
// ротирует токен, доходит до StartAttempt и ПЛАТИТ МОДЕЛИ ВТОРОЙ РАЗ. Ломается всё, начиная с
// базы в 150 s.

// ЛИЗА, УЕХАВШАЯ В СТОР, СЧИТАНА ОТ НАСТРОЕННОЙ БАЗЫ ЭТОГО КЛИЕНТА, А НЕ ОТ КОДОВОЙ.
//
// ⚠ ЭТО ПРОБА НА ЖИВОМ ХЕНДЛЕРЕ, А НЕ НА ФУНКЦИИ, И ИМЕННО ЭТОГО НЕ ХВАТАЛО. Сверять
// HandlerLeaseFor саму с собой мало: дефект был в том, ЧТО ИМЕННО хендлер ей передаёт. Здесь
// клиенту поставщика задан HTTPTimeout, и предмет утверждения — число, легшее в
// entity.DesignRunStart, то самое, из которого StartRun считает claim_expires_at.
//
// МОДЕЛЬ НЕ ЗОВЁТСЯ ВОВСЕ: стор отвечает идемпотентным повтором законченного прогона, поэтому
// хендлер возвращается сразу после StartRun. Адрес клиента при этом закрытый порт — попытка
// позвонить провалилась бы громко, а не молча зазеленела.
//
// МУТАЦИЯ: вернуть в design_run.go `design.HandlerLeaseFor(0, ...)` (ноль = кодовая база) либо
// заставить HandlerLeaseFor игнорировать аргумент базы → краснеет.
func TestTheHandlerLeaseFollowsTheConfiguredCallBudget(t *testing.T) {
	const configured = 240 * time.Second

	rig := newDraftIdeaRig(t, openrouter.New(openrouter.Config{
		APIKey: "test-key", BaseURL: "http://127.0.0.1:1", HTTPTimeout: configured,
	}))

	var sent entity.DesignRunStart
	prior := entity.DesignRun{
		Id: 900, TechCardId: designRunCardID, Kind: entity.DesignRunKindDraftIdea,
		Status:     entity.DesignRunDone,
		OutputText: sql.NullString{String: "A boxy coat with a storm flap.", Valid: true},
	}
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Run(func(_ context.Context, req entity.DesignRunStart) { sent = req }).
		Return(&entity.DesignRunStarted{Run: prior, Idempotent: true}, nil).Once()

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "44444444-4444-4444-4444-444444444444",
	})
	require.NoError(t, err)

	ceilings := entity.DesignDraftAnswerCeilings()
	worst := maxCeiling(ceilings)
	budget := openrouter.CompletionBudget(configured, worst)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ЗАМЕРА: настроенная база ДЕЙСТВИТЕЛЬНО отличается от кодовой, иначе
	// проба ничего не различала бы и зеленела бы на дефекте.
	require.Greater(t, budget, openrouter.DefaultCompletionBudget(worst),
		"стенд настроен так, что настроенная и кодовая базы совпадают — ось не измеряется вовсе")

	require.NotZero(t, sent.HandlerLease,
		"хендлер не положил лизу в старт вовсе: стор откажет, и кнопка перестанет работать")
	require.Greater(t, sent.HandlerLease, budget,
		"лиза (%s) короче платного вызова при OPENROUTER_HTTP_TIMEOUT=%s (%s): на %s ВНУТРИ вызова "+
			"строка свободна, и повтор того же client_request_id заплатит второй раз",
		sent.HandlerLease, configured, budget, budget-sent.HandlerLease)

	// И ЭТО НЕ ЛИЗА ПО УМОЛЧАНИЮ. Без этой половины проба была бы выполнима щедрым литералом,
	// который «на сегодня хватает», — то есть ровно тем, против чего вся эта линия и стоит.
	require.Equal(t, designstore.HandlerLeaseFor(configured, ceilings...), sent.HandlerLease,
		"лиза не равна той, что HandlerLeaseFor считает от базы ЭТОГО клиента — между строкой и "+
			"проводом снова появилось второе число")
	require.Greater(t, sent.HandlerLease, designstore.HandlerLeaseFor(0, ceilings...),
		"лиза не сдвинулась от настроенной базы: хендлер считает её от кодовой, и заданный "+
			"OPENROUTER_HTTP_TIMEOUT удлиняет ТОЛЬКО вызов")
}

// ДВЕРЬ СТОРА ОТКАЗЫВАЕТ ТЕКСТОВОМУ ПРОГОНУ БЕЗ ЛИЗЫ — ГРОМКО, А НЕ УМОЛЧАНИЕМ.
//
// ⚠ ЗАЧЕМ ОТКАЗ, А НЕ «ЕСЛИ НОЛЬ, ВОЗЬМИ КОДОВУЮ». Умолчание вернуло бы ровно тот дефект, ради
// которого поле заведено: незаполненное поле стало бы кодовой базой в 60 s, и разошлись бы они с
// проводом снова МОЛЧА. Отказ делает «забыл передать» отказом кнопки в первую же секунду, а не
// вторым платежом через месяц.
//
// ⚠ ЗЕРКАЛЬНАЯ ПОЛОВИНА НЕСУЩАЯ: та же просьба С лизой дверь ПРОХОДИТ (и доходит до транзакции,
// которой в этом стенде нет). Без неё проба была бы выполнима дверью, отказывающей всему подряд.
func TestTheStoreRefusesADraftIdeaStartThatCarriesNoLease(t *testing.T) {
	reachedTx := errors.New("the door let it through to the transaction")
	st := designstore.New(
		storeutil.Base{},
		func(context.Context, func(context.Context, dependency.Repository) error) error { return reachedTx },
		nil,
	)
	start := func(lease time.Duration) error {
		_, err := st.StartRun(context.Background(), entity.DesignRunStart{
			TechCardId:      designRunCardID,
			ClientRequestId: "44444444-4444-4444-4444-444444444444",
			Kind:            entity.DesignRunKindDraftIdea,
			HandlerLease:    lease,
		})
		return err
	}

	err := start(0)
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument,
		"текстовый прогон без лизы обязан быть ОТКАЗАН: молча взятое умолчание — это снова кодовая "+
			"база против настроенной, и повтор ключа платит дважды")
	require.NotErrorIs(t, err, reachedTx, "просьба без лизы дошла до транзакции — дверь её пропустила")
	require.Contains(t, err.Error(), "handler lease",
		"отказ обязан называть, чего не хватило, иначе он неотличим от соседних")

	require.ErrorIs(t, start(designstore.HandlerLeaseFor(0, entity.DesignDraftAnswerCeilings()...)),
		reachedTx, "просьба С лизой дверь не прошла: отказ вызван не отсутствием лизы, а чем-то соседним")
}

// РОДА, КОТОРЫЕ ИСПОЛНЯЕТ ВОРКЕР, ЛИЗЫ НЕ НОСЯТ И НЕ ОБЯЗАНЫ.
//
// Их строку забирает ClaimRuns СВОЕЙ лизой, и требовать поле у них значило бы уронить каждый
// картиночный прогон ради чужого инварианта. Проба держит именно границу: отказ ровно у
// draft_idea и ни у кого больше.
func TestOnlyTheHandlerRunNeedsALease(t *testing.T) {
	reachedTx := errors.New("the door let it through to the transaction")
	st := designstore.New(
		storeutil.Base{},
		func(context.Context, func(context.Context, dependency.Repository) error) error { return reachedTx },
		nil,
	)
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindThreed,
	} {
		_, err := st.StartRun(context.Background(), entity.DesignRunStart{
			TechCardId:      designRunCardID,
			ClientRequestId: "44444444-4444-4444-4444-444444444444",
			Kind:            kind,
		})
		require.ErrorIs(t, err, reachedTx,
			"род %s отказан за отсутствие лизы, которой у него нет по построению: его строку "+
				"забирает воркер своей", kind)
	}
}
