package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
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

// ЛИЗА, УЕХАВШАЯ В СТОР, СЧИТАНА ОТ НАСТРОЕННОЙ БАЗЫ ТЕХ ТРАНСПОРТОВ, КОТОРЫЕ ПОЗВОНЯТ, И ПО ВСЕЙ
// ИХ ЦЕПОЧКЕ, А НЕ ОТ КОДОВОЙ БАЗЫ ОДНОГО ВЫЗОВА.
//
// ⚠ ЭТО ПРОБА НА ЖИВОМ ХЕНДЛЕРЕ, А НЕ НА ФУНКЦИИ, И ИМЕННО ЭТОГО НЕ ХВАТАЛО. Сверять
// HandlerLeaseFor саму с собой мало: дефект был в том, ЧТО ИМЕННО хендлер ей передаёт. Предмет
// утверждения — число, легшее в entity.DesignRunStart, то самое, из которого StartRun считает
// claim_expires_at, и оно выписано здесь ИЗ ПЕРВЫХ ПРИНЦИПОВ (aiprov.CompletionBudget по базе
// каждого кандидата), а не спрошено у роутера: спросив ChainBudget, проба сверяла бы его с собой.
//
// МОДЕЛЬ НЕ ЗОВЁТСЯ ВОВСЕ: стор отвечает идемпотентным повтором законченного прогона, поэтому
// хендлер возвращается сразу после StartRun. Адрес клиента при этом закрытый порт — попытка
// позвонить провалилась бы громко, а не молча зазеленела.
//
// МУТАЦИИ (замерены красными): в design_run.go лиза от aiprov.DefaultCompletionBudget (кодовая база,
// один вызов) → краснеют обе половины; от бюджета ОДНОГО вызова основного кандидата → краснеет
// цепочка; потолок ветки этой просьбы вместо DesignDraftLongestAnswerCeiling → краснеет прозаическая.
func TestTheHandlerLeaseFollowsTheConfiguredCallBudget(t *testing.T) {
	const configured = 240 * time.Second
	longest := entity.DesignDraftLongestAnswerCeiling()

	leaseSent := func(t *testing.T, ai *router.Router, key string) time.Duration {
		t.Helper()
		rig := newDraftIdeaRig(t, openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"}))
		rig.srv.ai = ai
		var sent entity.DesignRunStart
		prior := entity.DesignRun{
			Id: 900, TechCardId: designRunCardID, Kind: entity.DesignRunKindDraftIdea,
			Status:     entity.DesignRunDone,
			OutputText: sql.NullString{String: "A boxy coat with a storm flap.", Valid: true},
		}
		rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
			Run(func(_ context.Context, req entity.DesignRunStart) { sent = req }).
			Return(&entity.DesignRunStarted{Run: prior, Idempotent: true}, nil).Once()
		// Прозаическая просьба (без construction): её ветка потолка не ставит, и лиза обязана быть
		// посчитана всё равно от САМОЙ ДОРОГОЙ ветки — повтор может достаться чужой строке.
		_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
			TechCardId: designRunCardID, ClientRequestId: key,
		})
		require.NoError(t, err)
		require.NotZero(t, sent.HandlerLease,
			"хендлер не положил лизу в старт вовсе: стор откажет, и кнопка перестанет работать")
		return sent.HandlerLease
	}
	transport := func(base time.Duration) aiprov.Chatter {
		return openrouter.New(openrouter.Config{
			APIKey: "test-key", BaseURL: "http://127.0.0.1:1", HTTPTimeout: base,
		}).Transport()
	}

	t.Run("one candidate at the configured base", func(t *testing.T) {
		got := leaseSent(t, router.NewSingle(entity.AIProviderOpenRouter, transport(configured), "vendor/one"),
			"44444444-4444-4444-4444-444444444444")
		budget := aiprov.CompletionBudget(configured, longest)
		// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ЗАМЕРА: настроенная база ДЕЙСТВИТЕЛЬНО отличается от кодовой.
		require.Greater(t, budget, aiprov.DefaultCompletionBudget(longest),
			"стенд настроен так, что настроенная и кодовая базы совпадают — ось не измеряется вовсе")
		require.Greater(t, got, budget,
			"лиза (%s) короче платного вызова при OPENROUTER_HTTP_TIMEOUT=%s (%s)", got, configured, budget)
		// Два вызова, хотя кандидат один (FIX-G1): лиза на ПОТОЛОК цепочки draft_idea.
		require.Equal(t, designstore.HandlerLeaseFor(2*budget), got,
			"лиза не равна той, что выводится из базы ЭТОГО транспорта на потолок цепочки — между строкой "+
				"и проводом снова появилось второе число")
	})

	t.Run("two candidates, each at its own base", func(t *testing.T) {
		const second = 30 * time.Second
		got := leaseSent(t, router.NewStatic([]router.StaticCandidate{
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: transport(configured), Model: "vendor/one"},
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: transport(second), Model: "vendor/two"},
		}), "55555555-5555-5555-5555-555555555555")
		// Потолок × самый долгий вызов (FIX-G1): второй кандидат мог бы стоять и первым после правки
		// маршрута, поэтому оба вызова считаются по самому медленному транспорту.
		chain := 2 * max(aiprov.CompletionBudget(configured, longest), aiprov.CompletionBudget(second, longest))
		require.Equal(t, designstore.HandlerLeaseFor(chain), got,
			"лиза не покрывает цепочку: основной выбрал свой срок, запасной звонит, а строка "+
				"освобождается посреди второго вызова")
	})
}

// leaseProbeChatter — транспорт, который запоминает срок контекста своего вызова, а базу бюджета
// отдаёт через base(): так проба может вырастить бюджет вызова ПОСЛЕ того, как лиза посчитана.
type leaseProbeChatter struct {
	base     func() time.Duration
	deadline time.Time
	hasDL    bool
}

func (c *leaseProbeChatter) Chat(ctx context.Context, _ string, _ aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	c.deadline, c.hasDL = ctx.Deadline()
	return &aiprov.ChatResult{Text: "A boxy coat with a storm flap.", FinishReason: "stop"}, nil
}
func (c *leaseProbeChatter) CompletionBase() time.Duration { return c.base() }
func (c *leaseProbeChatter) Enabled() bool                 { return true }

// ВЫЗОВ ЧЕРНОВИКА ОГРАНИЧЕН ЛИЗОЙ, КОТОРУЮ ХЕНДЛЕР ПОЛОЖИЛ В СТРОКУ (FIX-G1) — пояс и подтяжки.
//
// Роутер ограничивает каждый вызов своим бюджетом, и в согласованном мире лиза их переживает. Проба
// разводит числа нарочно: при расчёте лизы транспорт отвечает базой в 1 s, а к моменту вызова — в 10 h
// (любая будущая рассинхронизация, которой сегодня нет). Срок, с которым транспорт получает вызов,
// обязан быть не позже «до StartRun + лиза»: иначе строка освободится посреди оплаченного вызова.
//
// МУТАЦИЯ (замерена красной): вызов без context.WithDeadline(…, leaseFrom.Add(lease)) → срок вызова
// «сейчас + 10 h», далеко за лизой.
func TestTheDraftChatIsBoundByTheLease(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "unused: the probe transport answers")
	probe := &leaseProbeChatter{}
	probe.base = func() time.Duration {
		if rig.started.HandlerLease == 0 {
			return time.Second // the lease is being sized
		}
		return 10 * time.Hour // the call: the transport's budget has outgrown the lease
	}
	rig.srv.ai = router.NewSingle(entity.AIProviderOpenRouter, probe, "vendor/draft")

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)
	lease := rig.started.HandlerLease
	require.NotZero(t, lease)
	require.Less(t, lease, aiprov.CompletionBudget(10*time.Hour, 0),
		"положительный контроль: собственный бюджет вызова роутера ушёл бы далеко за лизу")
	require.True(t, probe.hasDL, "вызов без срока")
	claimExpires := rig.startedAt.Add(lease) // what StartRun writes: its «now» + the lease
	require.False(t, probe.deadline.After(claimExpires),
		"срок вызова на %s позже срока строки: строка освободится посреди оплаченного вызова",
		probe.deadline.Sub(claimExpires))
	require.WithinDuration(t, claimExpires, probe.deadline, time.Second,
		"срок вызова — это срок строки, а не что-то заметно короче: лиза и вызов — одно число")
	require.Equal(t, "A boxy coat with a storm flap.", rig.completedText)
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

	require.ErrorIs(t, start(designstore.HandlerLeaseFor(
		aiprov.DefaultCompletionBudget(entity.DesignDraftLongestAnswerCeiling()))),
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
