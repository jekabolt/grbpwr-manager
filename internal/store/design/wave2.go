package design

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// ГЕНЕРАТИВНАЯ ПОЛОВИНА ПОЛОСЫ — ДЕНЬГИ И ОЧЕРЕДЬ.
//
// ГДЕ ЧТО ЖИВЁТ. Здесь — открытие платного задания (StartRun), его отмена (CancelRun) и общие
// денежные примитивы; машина захвата (ClaimRuns / ReviveExpiredRuns / StartAttempt /
// FinishAttempt / CompleteRun / FailRun) — в queue.go; атомарный минт — в mint.go.
//
// ТРИ ГРАНИЦЫ, КОТОРЫЕ НЕЛЬЗЯ ДВИГАТЬ, потому что цена ошибки — деньги владельца:
//
//  1. РЕЗЕРВ И ВСТАВКА ПРОГОНА — ОДНА ТРАНЗАКЦИЯ. Падение между ними подвешивает резерв
//     НАВСЕГДА: строки, которая бы его сняла, не существует, а день закрывается только сменой
//     календарной даты. Поэтому проверка потолка стоит ВНУТРИ той же транзакции, ПОСЛЕ
//     прибавления резерва: два одновременных клика оба увидят сумму, включающую соседа, и
//     второй откатится целиком.
//  2. ВЫЗОВ ПОСТАВЩИКА — ВНЕ ЛЮБОЙ ТРАНЗАКЦИИ. Его делает воркер между StartAttempt и
//     FinishAttempt; ни один метод этого пакета в сеть не ходит.
//  3. БАЙТЫ ПОСТАВЩИКА КЛАДУТСЯ В БАКЕТ ДО ТРАНЗАКЦИИ, а сирот сметает компенсация по
//     OrphanedMedia — ровно как у разреза композита (см. шапку OrphanedMedia): CompleteRun
//     принимает УЖЕ загруженные media_id и возвращает то, что усыновил.
//
// РЕЗЕРВ СНИМАЕТСЯ РОВНО ОДИН РАЗ — НА ТЕРМИНАЛЬНОМ ПЕРЕХОДЕ (done | failed | cancelled), в
// размере price_estimate, и это отступление от эскиза плана («reserved − est_share на каждой
// закрытой попытке») названо вслух. Довод: частичное снятие по попыткам нечем свести. Колонки,
// которая помнила бы, сколько уже снято, нет; значит терминальный переход не может вычислить
// остаток и снял бы либо второй раз (полоса бюджета уехала бы ниже нуля и день пустил бы лишние
// прогоны), либо ничего (резерв висел бы до полуночи). Снятие в одном месте, под сторожем
// перехода `status IN ('pending','running')`, срабатывает ровно один раз по построению.
// entity.DesignAttemptFinish.EstShare этим стором поэтому НЕ ЧИТАЕТСЯ.

// HandlerLease — лиза, которую StartRun выдаёт САМ для kind=draft_idea.
//
// ЗАЧЕМ. Текстовый прогон исполняет ХЕНДЛЕР синхронно, а не воркер. Без выданного здесь захвата
// воркер забрал бы строку ровно в тот момент, когда хендлер зовёт модель: два платных вызова на
// одно задание и строка, навсегда застрявшая в running. Предикат `kind <> 'draft_idea'` в
// ClaimRuns — второй пояс к этому же, а не замена ему: пояс из предиката защищает от воркера, а
// лиза — ещё и от второго хендлера.
//
// ⚠ И ОНА ЖЕ — ЕДИНСТВЕННОЕ, ЧТО СТОИТ МЕЖДУ ОДНИМ client_request_id И ДВУМЯ ПЛАТЕЖАМИ. Повтор
// того же ключа (react-query `retry: 1`, ингресс со своим сроком ответа) проходит предикат
// designRunResumableSQL РОВНО ТОГДА, когда лиза истекла; resumeHandlerRun ротирует токен, второй
// хендлер доходит до StartAttempt и платит ВТОРОЙ РАЗ. Поймать это ниже нечем: у FinishAttempt
// сторожа захвата нет вовсе (там так и должно быть, см. её шапку), а chargeAlreadyBooked
// дедуплицирует по provider_request_id, которого дорога черновика не ставит НИКОГДА.
//
// ⚠ ПОЭТОМУ ОНА ВЫВЕДЕНА ИЗ БЮДЖЕТА ВЫЗОВА, А НЕ ВЫПИСАНА РЯДОМ С НИМ. Здесь стояло
// `5 * time.Minute` с просьбой в соседнем файле («она обязана превышать самый долгий вызов
// провайдера»). Просьба была ВЕРНА, пока http.Client.Timeout резал каждый вызов на 60-й секунде,
// и стала ЛОЖЬЮ НА 26.7 s в тот день, когда потолок ответа купил себе время:
// DefaultCompletionBudget(8000) = 60s + 8000/30 = 5m26.667s против лизы в 5m00s. Разошлись они
// молча и в ту сторону, где платят дважды. Теперь между ними нет ни одного второго числа: тот же
// потолок, та же функция бюджета, что и на проводе.
//
// ⚠ ЧТО ИМЕННО ПОКРЫВАЕТ ЗАПАС (designHandlerLeaseSlack) — ВСЁ, ЧТО НЕ ЕСТЬ САМ ВЫЗОВ. Лиза
// выдаётся в StartRun, то есть ДО сборки промпта (резолв медиа, словарь цвета, RecordRunPrompt,
// StartAttempt), и снимается закрывающей записью ПОСЛЕ разбора ответа. Вызов — самая длинная, но
// не единственная её часть.
//
// ⚠ ЧЕГО ЭТО НЕ ПОКРЫВАЕТ, И ЭТО СКАЗАНО ПРЯМО: OPENROUTER_HTTP_TIMEOUT. Заданная переменная
// удлиняет базу бюджета у КЛИЕНТА, а сюда конфигурация процесса не доезжает — стор её не видит и
// видеть не должен. Ни один из двух spec'ов (бета, прод) её не ставит, и DefaultCompletionBudget
// заведена ровно ради этого случая; тот, кто её однажды поставит, обязан прийти сюда. Запас в
// полторы минуты — не защита от этого, а буфер на сборку промпта.
var HandlerLease = openrouter.DefaultCompletionBudget(entity.DesignConstructionMaxTokens) +
	designHandlerLeaseSlack

const (
	// designHandlerLeaseSlack — запас лизы ПОВЕРХ бюджета платного вызова: сборка промпта до него
	// (резолв медиа, словарь цвета, две записи в стор) и разбор ответа с закрывающей записью после.
	// Полторы минуты — порядок величины с запасом: живые эти отрезки идут секунды.
	//
	// БОЛЬШЕ БЕЗОПАСНЕЕ, ЧЕМ МЕНЬШЕ, И НЕСИММЕТРИЧНО. Слишком длинная лиза стоит ЗАДЕРЖКИ: честный
	// повтор после умершего хендлера ждёт её истечения. Слишком короткая стоит ДЕНЕГ: два платных
	// вызова на одно нажатие. Поэтому запас выбран щедро, а не впритык.
	designHandlerLeaseSlack = 90 * time.Second

	// designMaxRequestedOutputs — потолок кадров, которые одно задание вправе попросить.
	// Не бюджетный лимит (тот в деньгах), а защита от разогнавшегося цикла: просьба на сотню
	// кадров — это не запрос, а авария, и отказать ей дешевле, чем оплатить.
	designMaxRequestedOutputs = 64

	// designMaxErrorText — потолок текста ошибки провайдера в last_error. Сырой ответ не
	// хранится (10 §3); в истории живёт усечённая строка, а не мегабайт HTML от прокси.
	designMaxErrorText = 4000
)

// StartRun opens a paid job: budget reservation, input snapshot and the run row in one
// SERIALIZABLE transaction.
//
// ИДЕМПОТЕНТНОСТЬ — client_request_id, И ОНА НЕ ЗАМЕНЯЕТСЯ ИЗОЛЯЦИЕЙ. SERIALIZABLE упорядочивает
// двух писателей; он ничего не знает про ОДНОГО писателя, повторившего запрос после сетевого
// таймаута, — а это ровно тот случай, который заводит второй прогон и второй платёж. Повтор
// возвращает СУЩЕСТВУЮЩУЮ строку с OK и НЕ РЕЗЕРВИРУЕТ ВТОРОЙ РАЗ.
func (s *Store) StartRun(ctx context.Context, req entity.DesignRunStart) (*entity.DesignRunStarted, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ClientRequestId) == "" {
		return nil, fmt.Errorf("%w: client_request_id is required — without it a double click pays twice",
			entity.ErrDesignInvalidArgument)
	}
	if !entity.IsDesignRunKind(req.Kind) {
		return nil, fmt.Errorf("%w: unknown run kind %q", entity.ErrDesignInvalidArgument, req.Kind)
	}
	if req.ColorwayId < 0 {
		return nil, fmt.Errorf("%w: colorway_id must not be negative", entity.ErrDesignInvalidArgument)
	}
	// ОСЬ КОЛОРВЕЯ ЕСТЬ НЕ У ВСЯКОГО РОДА (0356): render/recolor генерят мультивью КОЛОРВЕЯ,
	// threed рендерит ЕГО верстак; флэт же — одна разметка на карточку (L-4), и колорвей при нём
	// ОТКАЗЫВАЕТСЯ, а не молча сбрасывается: принятая и не исполненная просьба разошлась бы с
	// записью без единого отказа.
	if req.ColorwayId > 0 && !entity.DesignRunKindTakesColorway(req.Kind) {
		return nil, fmt.Errorf("%w: a %s run has no colourway axis",
			entity.ErrDesignColorwayForbidden, req.Kind)
	}
	if req.RequestedOutputs < 0 || req.RequestedOutputs > designMaxRequestedOutputs {
		return nil, fmt.Errorf("%w: requested_outputs %d is outside 0..%d",
			entity.ErrDesignInvalidArgument, req.RequestedOutputs, designMaxRequestedOutputs)
	}
	est := decimal.Zero
	if req.PriceEstimate.Valid {
		est = req.PriceEstimate.Decimal
	}
	if est.IsNegative() {
		return nil, fmt.Errorf("%w: a price estimate cannot be negative", entity.ErrDesignInvalidArgument)
	}

	out := &entity.DesignRunStarted{}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Колбэк перезапускается после дедлока — каждая попытка начинает с чистого вердикта.
		*out = entity.DesignRunStarted{}
		db := rep.DB()

		// ─── 1. ПОВТОР ЛИ ЭТО — ПЕРВЫМ ДЕЛОМ И ДО ДЕНЕГ ───
		if prior, ok, err := priorStart(ctx, db, req); err != nil {
			return err
		} else if ok {
			resumed, run, err := resumeHandlerRun(ctx, db, prior)
			if err != nil {
				return err
			}
			budget, err := loadBudget(ctx, db, s.Now())
			if err != nil {
				return err
			}
			out.Run, out.Budget, out.Idempotent, out.Resumed = run, budget, true, resumed
			return nil
		}

		// ─── 2. РОДИТЕЛЬ РЕРАНА ───
		//
		// Проверяется здесь, а не на клиенте, потому что снимок входов рерана собирает СЕРВЕР:
		// клиентский снимок позволил бы истории утверждать входы, которых не было.
		var rerun any
		if req.RerunOf > 0 {
			parent, err := runByID(ctx, db, req.RerunOf)
			if err != nil {
				return err
			}
			if parent.TechCardId != req.TechCardId {
				return fmt.Errorf("%w: run %d belongs to tech card %d",
					entity.ErrDesignNotFound, parent.Id, parent.TechCardId)
			}
			rerun = parent.Id
		}

		// ─── 2б. ГРАНИЦА КАРТОЧКИ ДЛЯ КОЛОРВЕЯ — ДО ДЕНЕГ (0356) ───
		//
		// В той же транзакции, что резерв: чужой колорвей не должен ни занять деньги дня, ни
		// замёрзнуть в истории правдоподобной ложной атрибуцией.
		//
		// ⚠ УНАСЛЕДОВАННЫЙ КОЛОРВЕЙ, КОТОРОГО БОЛЬШЕ НЕТ, — НЕ ОТКАЗ, А ДЕГРАДАЦИЯ (F2), И ЭТО ТА
		// ЖЕ ГРАНИЦА, ЧТО У ДЕТАЛЕЙ И ПОЛОК В ХЕНДЛЕРЕ: «адрес, названный КЛИЕНТОМ, отвечает за
		// себя, унаследованный — нет». Реран без параметров наследует колорвей из ЗАМОРОЖЕННЫХ
		// params родителя; колорвей законно удаляют, FK гасит колонку родителя в NULL — и строгая
		// проверка сделала бы такой прогон НЕПОВТОРИМЫМ НАВСЕГДА, причём без единого написания
		// запроса, которое прошло бы: клиент не присылал ни params, ни колорвея, а отказ называл
		// бы ему `foreign_colorway`. Ровно тот исход, от которого соседние два сторожа отказались
		// дословно теми же словами.
		//
		// ДЕГРАДАЦИЯ СИММЕТРИЧНА ТОМУ, ЧТО СЛУЧИЛОСЬ С РОДИТЕЛЕМ: реран уезжает
		// НЕАТРИБУТИРОВАННЫМ, а его замороженные params по-прежнему называют просимый id — то
		// есть ровно та пара (колонка NULL, params помнят), в которой после удаления оказалась
		// строка родителя. Ничего не выдумано и ничего не потеряно.
		//
		// ЧУЖОЙ (существующий, но не этой карточки) ОТКАЗЫВАЕТСЯ И УНАСЛЕДОВАННЫЙ ТОЖЕ: это
		// состояние обратимо (колорвей можно вернуть на карточку), поэтому отказ не вечен, а тихо
		// рендерить чужой цвет — та самая ложная атрибуция, ради которой ось и заводилась.
		colorwayID := req.ColorwayId
		if colorwayID > 0 {
			owner, exists, err := colorwayOwnerCard(ctx, db, colorwayID)
			if err != nil {
				return err
			}
			switch {
			case exists && owner == req.TechCardId:
				// граница пройдена
			case !exists && !req.ColorwayStated && !entity.DesignRunKindReadsColorwayBench(req.Kind):
				colorwayID = 0
			case !exists && entity.DesignRunKindReadsColorwayBench(req.Kind):
				// ⚠ 3D ДЕГРАДИРОВАТЬ НЕЛЬЗЯ, И ЭТО НЕ ИСКЛЮЧЕНИЕ ИЗ ПРАВИЛА, А ЕГО ГРАНИЦА (N6).
				//
				// Деградация честна ровно потому, что у рендера колорвей — это АТРИБУЦИЯ: прогон
				// уезжает неатрибутированным, params помнят просимый id, и ни одно другое поле
				// строки о колорвее ничего не утверждало. У 3D он другой по устройству: колорвей
				// ВЫБИРАЕТ ВЕРСТАК, и к этому месту снимок входов УЖЕ ЗАМОРОЖЕН против верстака
				// названного цвета. Обнулив колорвей здесь, мы записали бы строку, чьи inputs
				// описывают верстак 5, а колонка говорит «безколорвейный», — и её выход лёг бы на
				// ЧУЖОЙ верстак. Это не потеря атрибуции, это ложь о том, из чего собран прогон.
				//
				// Окно узкое (колорвей удалён между чтением полосы хендлером и этой транзакцией) и
				// именно поэтому опасное: ворота 3D его уже прошли, деньги на подходе, а человек
				// увидел бы «запущено» на верстак, которого нет. Отказ здесь стоит одного
				// перезапуска экрана; молчаливое обнуление стоило бы оплаченного прогона, чей
				// результат нельзя ни истолковать, ни разобрать потом.
				return fmt.Errorf("%w: colourway %d was deleted while this 3D run was being started; "+
					"its inputs are already frozen against that colourway's bench",
					entity.ErrDesignForeignColorway, colorwayID)
			case !exists:
				return fmt.Errorf("%w: colourway %d does not exist",
					entity.ErrDesignForeignColorway, colorwayID)
			default:
				return fmt.Errorf("%w: colourway %d does not belong to tech card %d",
					entity.ErrDesignForeignColorway, colorwayID, req.TechCardId)
			}
		}

		// ─── 3. ДЕНЬГИ: РЕЗЕРВ. ПОТОЛКА БОЛЬШЕ НЕТ (0358, L-8) ───
		//
		// Здесь стояли ДВА отказа — «сегодня не запускаем» (daily_budget = 0) и «spent + reserved
		// вышли за cap», — и оба сняты вместе с самим понятием потолка. Слова владельца: «у нас в
		// принципе не должно быть потолка похуй чем он съеден убери потолок».
		//
		// РЕЗЕРВ ОСТАЁТСЯ, И ЭТО НЕ ОСТАТОК МЕХАНИЗМА, А ЕГО ИСХОДНЫЙ СМЫСЛ: он держит оценку
		// незакрытого задания, чтобы «сколько сегодня стоило» отвечало правду ДО того, как придёт
		// счёт. Раньше эта запись заодно кормила ворота; теперь она только считает. Снятие резерва
		// при закрытии (releaseRunReserve) и запись фактической цены не тронуты.
		set, err := loadSettings(ctx, db)
		if err != nil {
			return err
		}
		currency := req.Currency
		if currency == "" {
			currency = set.Currency
		}
		day := DesignBudgetDayKey(s.Now(), set.BudgetTimezone)
		if err := moveBudgetDay(ctx, db, day, est, decimal.Zero, currency); err != nil {
			return err
		}
		budget, err := loadBudget(ctx, db, s.Now())
		if err != nil {
			return err
		}

		// ─── 4. ПОДПИСЬ ИСТОРИИ ЦВЕТА ───
		rrev := 0
		if req.Kind == entity.DesignRunKindRender {
			if rrev, err = storeutil.QueryCountNamed(ctx, db, `
				SELECT COALESCE(MAX(rrev), 0) + 1 FROM design_run
				WHERE tech_card_id = :card AND kind = 'render'`,
				map[string]any{"card": req.TechCardId}); err != nil {
				return fmt.Errorf("failed to compute the next design rrev: %w", err)
			}
		}

		// ─── 5. СТРОКА ПРОГОНА ───
		//
		// ПОПЫТКИ ЗДЕСЬ НЕ РОЖДАЕТСЯ, и это решение против эскиза плана («INSERT attempt(0,
		// state='pending')»). design_run_attempt объявлена как ПОПЫТКА ПЛАТНОГО ВЫЗОВА, а её
		// словарь состояний — dispatching|accepted|delivered|failed|unknown (0340). Строка с
		// состоянием `pending` не только вне словаря — она утверждает, что вызов был, когда его
		// не было. «Задание ждёт» уже сказано полем design_run.status.
		var claimToken, claimExpires any
		if req.Kind == entity.DesignRunKindDraftIdea {
			claimToken = uuid.NewString()
			claimExpires = s.Now().UTC().Add(HandlerLease)
		}
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_run
				(tech_card_id, kind, status, client_request_id, provider_idempotency_key,
				 profile_name, profile_version, ask, params, inputs, fit_at_launch, rrev,
				 requested_outputs, price_estimate, currency, author, rerun_of, colorway_id,
				 claim_token, claim_expires_at, next_attempt_at)
			VALUES
				(:card, :kind, 'pending', :req, :pkey, :profile, :pver, :ask, :params, :inputs,
				 :fit, :rrev, :outputs, :est, :cur, :who, :rerun, :cw,
				 :claim, :claim_exp, UTC_TIMESTAMP(6))`,
			map[string]any{
				"card": req.TechCardId, "kind": req.Kind, "req": req.ClientRequestId,
				"pkey": uuid.NewString(), "profile": req.ProfileName, "pver": req.ProfileVersion,
				"ask": nullStr(req.Ask), "params": jsonOrNil(req.Params), "inputs": jsonOrNil(req.Inputs),
				"fit": nullStr(req.FitAtLaunch), "rrev": rrev, "outputs": req.RequestedOutputs,
				"est": req.PriceEstimate, "cur": currency, "who": req.Author, "rerun": rerun,
				"cw":    nullInt(colorwayID),
				"claim": claimToken, "claim_exp": claimExpires,
			})
		if err != nil {
			// ОСТАТОЧНЫЙ 1062 — ПОЯС, А НЕ МЕХАНИЗМ. Идемпотентность закрыта чтением выше, в
			// этой же SERIALIZABLE-транзакции; сюда попадает только гонка, разрешившаяся не
			// дедлоком. Ответ тот же самый — существующая строка с OK.
			if isDupKey(err) {
				// ⚠ ПОЯС ХОДИТ ТОЙ ЖЕ ДВЕРЬЮ, ЧТО И ГЛАВНЫЙ ПУТЬ (T10). Раньше он звал
				// runByRequestID напрямую и не сравнивал НИЧЕГО — а чтение выше сверяет и
				// карточку, и колорвей; две одновременные заявки с одним ключом, но разными
				// карточками либо колорвеями, разойдясь не дедлоком, а 1062, получали чужой
				// прогон с ответом OK. Починка не в том, чтобы добавить сюда сравнение (его
				// снова можно забыть, а проверить пробой нечем: путь недостижим из одного
				// соединения — пре-чтение под SERIALIZABLE берёт next-key lock, а две
				// транзакции расходятся дедлоком 1213 и повторяются в главный путь), а в том,
				// чтобы «найти прежний старт» существовало РОВНО В ОДНОМ виде. Состояние
				// «пояс забыл рассудить» теперь невыразимо.
				prior, ok, rerr := priorStart(ctx, db, req)
				if rerr != nil {
					return rerr
				}
				if ok {
					// Резерв этой попытки уезжает вместе с откатом транзакции — вернуть
					// существующую строку значит НЕ платить второй раз.
					return &designIdempotentStart{run: prior}
				}
			}
			return fmt.Errorf("failed to open design run: %w", err)
		}
		run, err := runByID(ctx, db, id)
		if err != nil {
			return err
		}
		out.Run, out.Budget = run, budget
		return nil
	})
	// Идемпотентный исход, пришедший из ОТКАЧЕННОЙ транзакции: строка чужая, деньги не списаны.
	var idem *designIdempotentStart
	if errors.As(err, &idem) {
		budget, berr := s.GetBudget(ctx)
		if berr != nil {
			return nil, berr
		}
		return &entity.DesignRunStarted{Run: idem.run, Budget: budget, Idempotent: true}, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// designIdempotentStart переносит «это повтор» ЧЕРЕЗ ОТКАТ транзакции.
//
// Зачем тип, а не флаг: вернуть строку из колбэка, который обязан откатиться, нечем — txFunc
// коммитит ровно тогда, когда колбэк вернул nil, а коммитить здесь нечего и нельзя (в
// транзакции висит лишний резерв). Ошибка — единственный канал, переживающий откат.
type designIdempotentStart struct{ run entity.DesignRun }

func (e *designIdempotentStart) Error() string {
	return fmt.Sprintf("design: run %d already exists for this client_request_id", e.run.Id)
}

// CancelRun stops a run: `pending` becomes `cancelled` and the day's reservation is released;
// `running` gets cancel_requested_at and the worker honours it either side of the dispatch.
//
// ДВА РАЗНЫХ АКТА ПОД ОДНИМ ИМЕНЕМ, и это не упрощение. Ждущий прогон никто не оплачивал —
// его можно закрыть здесь и вернуть деньги дню. Идущий прогон УЖЕ У ПРОВАЙДЕРА: закрыть его
// строкой значило бы выбросить оплаченный результат, который может прийти секундой позже.
// Поэтому running получает только просьбу, а решение принимает воркер — до отправки отменяет
// бесплатно, после ответа СОХРАНЯЕТ и оплачивает (10 Д20).
func (s *Store) CancelRun(ctx context.Context, runID int, actor string) (*entity.DesignRun, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("%w: run id is required", entity.ErrDesignInvalidArgument)
	}
	var out entity.DesignRun
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		run, err := runByID(ctx, db, runID)
		if err != nil {
			return err
		}
		switch run.Status {
		case entity.DesignRunPending:
			n, err := storeutil.ExecNamedRows(ctx, db, `
				UPDATE design_run
				SET status = 'cancelled',
				    cancel_requested_at = COALESCE(cancel_requested_at, UTC_TIMESTAMP(6)),
				    completed_at = UTC_TIMESTAMP(6),
				    claim_token = NULL,
				    claim_expires_at = NULL,
				    archived_by = COALESCE(archived_by, :who)
				WHERE id = :id AND status = 'pending'`,
				map[string]any{"id": runID, "who": nullStr(actor)})
			if err != nil {
				return fmt.Errorf("failed to cancel design run %d: %w", runID, err)
			}
			// Сторож перехода делает снятие резерва ОДНОКРАТНЫМ по построению: второй
			// одновременный отменяющий получит n = 0 и денег не тронет.
			if n == 1 {
				if err := releaseRunReserve(ctx, db, run); err != nil {
					return err
				}
			}
		case entity.DesignRunRunning:
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_run
				SET cancel_requested_at = COALESCE(cancel_requested_at, UTC_TIMESTAMP(6))
				WHERE id = :id AND status = 'running'`,
				map[string]any{"id": runID}); err != nil {
				return fmt.Errorf("failed to ask design run %d to cancel: %w", runID, err)
			}
		default:
			// Терминальная строка отменой не двигается, и это НЕ ошибка: человек нажал «отмена»
			// на строке, которая закончилась, пока он смотрел. Ответ — её текущее состояние.
		}
		out, err = runByID(ctx, db, runID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ─────────────────────────── деньги дня ───────────────────────────

// moveBudgetDay двигает счётчики ОДНОГО дня одним оператором: заводит строку, если её нет, и
// прибавляет дельты, если есть.
//
// GREATEST(…, 0) НА РЕЗЕРВЕ — НЕ КОСМЕТИКА. Снятие приходит отрицательной дельтой, и без пола
// повторное снятие (двух воркеров, чинящих одну строку) увело бы резерв ниже нуля, а
// отрицательный резерв ВЫЧИТАЕТСЯ из проверки потолка — то есть день начал бы пускать прогоны,
// на которые денег нет. Пол превращает вторую попытку снятия в отсутствие эффекта.
//
// ВАЛЮТА ПИШЕТСЯ ТОЛЬКО ПРИ РОЖДЕНИИ СТРОКИ. Сравнивать её в SQL нельзя даром: тесты идут в
// utf8mb4, прод — в utf8mb3, и сравнение строк разных наборов маскируется на одном и падает
// 1267 на другом.
func moveBudgetDay(ctx context.Context, db dependency.DB, day string, reserved, spent decimal.Decimal, currency string) error {
	if reserved.IsZero() && spent.IsZero() {
		return nil
	}
	err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO design_budget_day (day, reserved, spent, currency)
		VALUES (:day, GREATEST(:reserved, 0), GREATEST(:spent, 0), :cur)
		ON DUPLICATE KEY UPDATE
			reserved = GREATEST(reserved + :reserved, 0),
			spent    = GREATEST(spent + :spent, 0)`,
		map[string]any{"day": day, "reserved": reserved, "spent": spent, "cur": currency})
	if err != nil {
		return fmt.Errorf("failed to move the design budget of %s: %w", day, err)
	}
	return nil
}

// releaseRunReserve снимает резерв ЭТОГО прогона с ЕГО дня.
//
// ДЕНЬ БЕРЁТСЯ ИЗ created_at ПРОГОНА, А НЕ ИЗ «СЕГОДНЯ». Задание, начатое в 23:58 и закрытое в
// 00:03, зарезервировано на вчерашнем дне; снятие по «сегодня» оставило бы вчерашний резерв
// висеть навсегда (день закрывается только календарём) и заодно увело бы сегодняшний в ноль по
// GREATEST — то есть соврало бы дважды.
func releaseRunReserve(ctx context.Context, db dependency.DB, run entity.DesignRun) error {
	if !run.PriceEstimate.Valid || !run.PriceEstimate.Decimal.IsPositive() {
		return nil
	}
	set, err := loadSettings(ctx, db)
	if err != nil {
		return err
	}
	day := DesignBudgetDayKey(run.CreatedAt, set.BudgetTimezone)
	return moveBudgetDay(ctx, db, day, run.PriceEstimate.Decimal.Neg(), decimal.Zero, run.Currency)
}

// runByRequestID reads the run a client_request_id already opened, if any.
func runByRequestID(ctx context.Context, db dependency.DB, requestID string) (entity.DesignRun, bool, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db,
		`SELECT * FROM design_run WHERE client_request_id = :req`,
		map[string]any{"req": requestID})
	if err != nil {
		return entity.DesignRun{}, false, fmt.Errorf("failed to check design run idempotency: %w", err)
	}
	if len(rows) == 0 {
		return entity.DesignRun{}, false, nil
	}
	return rows[0], true, nil
}

// ─────────────────────────── снимок входов ───────────────────────────

// designRunParams / designRunInputs — РОВНО ТЕ ПОЛЯ снимка, которые стору нужны, и ничего сверх.
//
// ⚠ ИМЕНА snake_case, И ЭТО НЕСУЩЕЕ. protojson в этом репозитории пишет с UseProtoNames: true,
// поэтому в колонках лежат `extra_input_media_ids`, `media_id`, `fix_targets`. Дефолтный
// protojson написал бы lowerCamelCase, и разбор стал бы МОЛЧА пустым: ни одной ошибки, просто
// «входов нет» — то есть mixed_input никогда не поднялся бы, а composite_views никогда не
// записался. Пустой результат тут законен, поэтому тест этого сам по себе не ловит.
type designRunParams struct {
	Views              []string `json:"views"`
	Layout             string   `json:"layout"`
	ExtraInputMediaIds []int    `json:"extra_input_media_ids"`
	// ColorwayId — ПРОСЬБА, а не живое зеркало (0356). Колонка design_run.colorway_id гаснет в
	// NULL, когда колорвей удаляют (FK SET NULL); этот снимок не гаснет никогда и остаётся
	// единственным свидетельством того, для какого цвета прогон ЗАКАЗЫВАЛИ. Различитель повтора
	// (F7) спрашивает именно его.
	ColorwayId int `json:"colorway_id"`
	// Pattern — ПРОСЬБА ПРОГОНА ПАТТЕРНА, читаемая ровно одним читателем: посадкой плитки на полку
	// при закрытии прогона (keepPatternTx). Имя и родитель приезжают отсюда, потому что здесь они
	// ЗАМОРОЖЕНЫ: человек назвал плитку до денег, и переименование ассета завтра не имеет права
	// переписать то, чем прогон был запущен.
	Pattern *designRunPatternParams `json:"pattern"`
}

// designRunPatternParams — замороженная просьба прогона паттерна (DesignPatternParams).
//
// ⚠ ПОЛЯ ЧИТАЮТСЯ ИЗ params, А НЕ ИЗ КОЛОНОК, И ЭТО НЕ ЛЕНЬ. Строка design_run не несёт ни имени
// плитки, ни её родителя, и заводить под них две колонки значило бы завести второй дом факту,
// который уже заморожен в снимке параметров вместе со всем остальным, что человек назвал.
type designRunPatternParams struct {
	RepeatMM      int    `json:"repeat_mm"`
	Name          string `json:"name"`
	SourceAssetID int    `json:"source_asset_id"`
}

type designRunInputs struct {
	Refs  []designRunInputMedia `json:"refs"`
	Slots []designRunInputMedia `json:"slots"`
}

type designRunInputMedia struct {
	MediaId int `json:"media_id"`
}

// Раскладки кадра — словарь DesignRunParams.layout.
const (
	designLayoutOne     = "one"
	designLayoutPerView = "per_view"
)

// parseRunParams / parseRunInputs разбирают снимок ЩАДЯЩЕ: испорченный JSON не роняет прилёт
// оплаченного результата. Что теряется при этом — догадка о виде и о композитности, а не сама
// картинка; уронить прилёт значило бы выбросить то, за что уже заплачено.
// designSameStartRequest — ОДИН ЛИ ЭТО ЗАПРОС, что уже открыл найденную строку.
//
// ОДНО ПРАВИЛО НА ОБА ПУТИ ИДЕМПОТЕНТНОСТИ: чтение до вставки и остаточный 1062 после неё. Пояс,
// судивший иначе (а он не судил вовсе), отдавал бы в гонке чужой прогон с ответом OK — то есть
// ровно тот исход, который главный путь считает достойным отказа.
//
// КАРТОЧКА. Ключ, уже открывший прогон другой карточки, — коллизия, а не повтор; вернуть его
// строку значит соврать дважды.
//
// КОЛОРВЕЙ — ИЗ ЗАМОРОЖЕННЫХ params, А НЕ ИЗ ЖИВОЙ КОЛОНКИ. Колонку гасит FK SET NULL, когда
// колорвей удаляют, и настоящий ретрай того же запроса получал бы отказ «уже открыт для 0, просят
// 7» по причине, к запросу не относящейся. Порядок источников, а не два мнения: ноль в params
// значит «params про колорвей не говорят», и тогда колонка — единственный свидетель. Остаточное
// окно (вызывающий передал ColorwayId без Params — хендлер так не делает никогда) названо в
// шапке волны и закрыть его нечем: просьба нигде не записана.
// priorStart — ЕДИНСТВЕННЫЙ СПОСОБ НАЙТИ ПРЕЖНИЙ СТАРТ ПО КЛЮЧУ, и он же его СУДИТ.
//
// Поиск и сравнение соединены нарочно (T10): пока это были два вызова, один из двух путей
// идемпотентности (остаточный 1062) вызывал первый и забывал второй — и «пояс забыл рассудить»
// было выразимым состоянием, которое к тому же нечем проверить, потому что путь недостижим из
// одного соединения. Сложив их, мы убрали не дефект, а ВОЗМОЖНОСТЬ дефекта: тот же приём, что
// «сначала спроси, нельзя ли сделать неправильное состояние невыразимым».
//
// ok = false значит «такого ключа нет»; ошибка — либо чтение, либо ПРОТИВОРЕЧИЕ (другая карточка,
// другой колорвей), и вызывающему в обоих случаях полагается вернуть её как есть.
func priorStart(ctx context.Context, db dependency.DB, req entity.DesignRunStart) (entity.DesignRun, bool, error) {
	prior, ok, err := runByRequestID(ctx, db, req.ClientRequestId)
	if err != nil || !ok {
		return prior, ok, err
	}
	if err := designSameStartRequest(prior, req); err != nil {
		return prior, false, err
	}
	return prior, true, nil
}

func designSameStartRequest(prior entity.DesignRun, req entity.DesignRunStart) error {
	if prior.TechCardId != req.TechCardId {
		return fmt.Errorf("%w: client_request_id %q already opened a run of tech card %d",
			entity.ErrDesignInvalidArgument, req.ClientRequestId, prior.TechCardId)
	}
	was := entity.DesignColorwayOrNone(prior.ColorwayId)
	if asked := parseRunParams(prior.Params).ColorwayId; asked != 0 {
		was = asked
	}
	if was != req.ColorwayId {
		return fmt.Errorf("%w: client_request_id %q already opened run %d for colourway %d "+
			"and is now being reused for colourway %d (0 = none)",
			entity.ErrDesignColorwayMismatch, req.ClientRequestId, prior.Id, was, req.ColorwayId)
	}
	return nil
}

func parseRunParams(raw entity.RawJSON) designRunParams {
	var p designRunParams
	if len(raw) == 0 {
		return p
	}
	_ = json.Unmarshal(raw, &p)
	return p
}

func parseRunInputs(raw entity.RawJSON) designRunInputs {
	var in designRunInputs
	if len(raw) == 0 {
		return in
	}
	_ = json.Unmarshal(raw, &in)
	return in
}

// runInputMediaIDs собирает ВСЕ медиа, которые прогон читал: плиты верстака, референсы и
// дополнительные входы рендера. Порядок сохранён, дубликаты сняты.
func runInputMediaIDs(run entity.DesignRun) []int {
	in := parseRunInputs(run.Inputs)
	p := parseRunParams(run.Params)
	seen := map[int]struct{}{}
	out := make([]int, 0, len(in.Refs)+len(in.Slots)+len(p.ExtraInputMediaIds))
	add := func(id int) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, r := range in.Refs {
		add(r.MediaId)
	}
	for _, sl := range in.Slots {
		add(sl.MediaId)
	}
	for _, id := range p.ExtraInputMediaIds {
		add(id)
	}
	return out
}
