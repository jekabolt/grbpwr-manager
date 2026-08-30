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

const (
	// designHandlerLease — короткая лиза, которую StartRun выдаёт САМ для kind=draft_idea.
	//
	// ЗАЧЕМ. Текстовый прогон исполняет ХЕНДЛЕР синхронно, а не воркер. Без выданного здесь
	// захвата воркер забрал бы строку ровно в тот момент, когда хендлер зовёт модель: два
	// платных вызова на одно задание и строка, навсегда застрявшая в running. Предикат
	// `kind <> 'draft_idea'` в ClaimRuns — второй пояс к этому же, а не замена ему: пояс из
	// предиката защищает от воркера, а лиза — ещё и от второго хендлера.
	designHandlerLease = 5 * time.Minute

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
		if prior, ok, err := runByRequestID(ctx, db, req.ClientRequestId); err != nil {
			return err
		} else if ok {
			if prior.TechCardId != req.TechCardId {
				return fmt.Errorf("%w: client_request_id %q already opened a run of tech card %d",
					entity.ErrDesignInvalidArgument, req.ClientRequestId, prior.TechCardId)
			}
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

		// ─── 3. ДЕНЬГИ: РЕЗЕРВ И ПОТОЛОК В ОДНОЙ ТРАНЗАКЦИИ ───
		set, err := loadSettings(ctx, db)
		if err != nil {
			return err
		}
		currency := req.Currency
		if currency == "" {
			currency = set.Currency
		}
		day := DesignBudgetDayKey(s.Now(), set.BudgetTimezone)
		// НОЛЬ — ЭТО «СЕГОДНЯ НЕ ЗАПУСКАЕМ», а не «бесплатно можно». Так объявлена колонка
		// (0344), и отказ здесь называет причину, вместо того чтобы пропустить прогон, у
		// которого просто нет оценки.
		if !set.DailyBudget.IsPositive() {
			return fmt.Errorf("%w: today's cap is %s %s — the band is closed for the day",
				entity.ErrDesignBudgetExceeded, set.DailyBudget.String(), currency)
		}
		if err := moveBudgetDay(ctx, db, day, est, decimal.Zero, currency); err != nil {
			return err
		}
		budget, err := loadBudget(ctx, db, s.Now())
		if err != nil {
			return err
		}
		// ПРОВЕРКА ПОСЛЕ ПРИБАВЛЕНИЯ, А НЕ ДО. «Посмотрел, потом положил» пропускает два
		// одновременных клика: оба видят старую сумму. Здесь второй читает сумму, В КОТОРУЮ УЖЕ
		// ВОШЁЛ ПЕРВЫЙ, и откатывается целиком — вместе со своим резервом.
		if budget.Spent.Add(budget.Reserved).GreaterThan(budget.Cap) {
			return fmt.Errorf("%w: %s spent + %s reserved would pass the %s cap for %s",
				entity.ErrDesignBudgetExceeded, budget.Spent.String(), budget.Reserved.String(),
				budget.Cap.String(), budget.Day)
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
			claimExpires = s.Now().UTC().Add(designHandlerLease)
		}
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_run
				(tech_card_id, kind, status, client_request_id, provider_idempotency_key,
				 profile_name, profile_version, ask, params, inputs, fit_at_launch, rrev,
				 requested_outputs, price_estimate, currency, author, rerun_of,
				 claim_token, claim_expires_at, next_attempt_at)
			VALUES
				(:card, :kind, 'pending', :req, :pkey, :profile, :pver, :ask, :params, :inputs,
				 :fit, :rrev, :outputs, :est, :cur, :who, :rerun,
				 :claim, :claim_exp, UTC_TIMESTAMP(6))`,
			map[string]any{
				"card": req.TechCardId, "kind": req.Kind, "req": req.ClientRequestId,
				"pkey": uuid.NewString(), "profile": req.ProfileName, "pver": req.ProfileVersion,
				"ask": nullStr(req.Ask), "params": jsonOrNil(req.Params), "inputs": jsonOrNil(req.Inputs),
				"fit": nullStr(req.FitAtLaunch), "rrev": rrev, "outputs": req.RequestedOutputs,
				"est": req.PriceEstimate, "cur": currency, "who": req.Author, "rerun": rerun,
				"claim": claimToken, "claim_exp": claimExpires,
			})
		if err != nil {
			// ОСТАТОЧНЫЙ 1062 — ПОЯС, А НЕ МЕХАНИЗМ. Идемпотентность закрыта чтением выше, в
			// этой же SERIALIZABLE-транзакции; сюда попадает только гонка, разрешившаяся не
			// дедлоком. Ответ тот же самый — существующая строка с OK.
			if isDupKey(err) {
				prior, ok, rerr := runByRequestID(ctx, db, req.ClientRequestId)
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
