package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ГЕНЕРАТИВНАЯ ПОЛОВИНА ПОЛОСЫ — ХЕНДЛЕРЫ.
//
// ЧТО ЭТОТ ФАЙЛ ДЕЛАЕТ И ЧЕГО НЕ ДЕЛАЕТ. Он разбирает запрос, стоит на воротах, СОБИРАЕТ СНИМОК
// ВХОДОВ и переводит отказы стора в коды gRPC. Он не резервирует деньги (это одна SERIALIZABLE
// транзакция внутри Design().StartRun — вторая обёртка вокруг неё подвесила бы резерв при падении
// между ними), не ходит к поставщику картинок (это воркер) и не пишет ни одной строки сам.
//
// ЧЕТЫРЕ ВОРОТ, И КАЖДЫЕ СТОЯТ ИМЕННО ЗДЕСЬ, А НЕ НА ЭКРАНЕ:
//
//  1. ФЛАГ DESIGN_GENERATION_ENABLED. Выключен — StartDesignRun отказывает и НЕ заводит строку.
//     Без воркера строку некому забрать: она навсегда останется pending, деньги останутся
//     зарезервированными до полуночи, а человек будет смотреть на вечное «идёт генерация».
//  2. W-13 — 3D ТОЛЬКО ПОСЛЕ РЕНДЕРА. Клиентское приглушение это подсказка, а не защита: вкладку
//     открывают ссылкой, а платит владелец.
//  3. W-15 — МУДБОРД НЕ УХОДИТ В ГЕНЕРАЦИЮ. Владелец процитировал строку прототипа дословно:
//     «the mood, not the prompt: nothing here is sent to generation». Экранного обещания
//     недостаточно; гарантия — designAssembleInputs, см. её шапку.
//  4. РЕРАН СОБИРАЕТ СЕРВЕР. Входы берутся из строки прогона-родителя, а не из того, что прислал
//     клиент: клиентский снимок позволил бы истории утверждать входы, которых не было.

// ─────────────────────────── отказы этого яруса ───────────────────────────

// designRefusal — отказ, РОЖДЁННЫЙ ХЕНДЛЕРОМ, а не переведённый со стора.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ ФУНКЦИЯ РЯДОМ С designError. designError — таблица переводов sentinel-ошибок
// стора; у ворот, стоящих ДО стора (флаг, W-13, чужой родитель рерана), никакой sentinel-ошибки
// нет и заводить её было бы враньём: стор о них не знает и знать не должен. Но домен и форма
// детали ОБЯЗАНЫ совпадать, иначе клиент разбирает две разные оболочки одной и той же новости.
//
// Деталь — бонус, а не полезная нагрузка: если её не удалось приложить, ОТКАЗ ВСЁ РАВНО УХОДИТ.
func designRefusal(code codes.Code, reason, msg string, metadata map[string]string) error {
	st := status.New(code, msg)
	md := map[string]string{"reason": reason}
	for k, v := range metadata {
		md[k] = v
	}
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason, Domain: designErrorDomain, Metadata: md,
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// ─────────────────────────── флаг ───────────────────────────

// designGenerationDisabledMsg — ОТКАЗ ВЫКЛЮЧЕННОГО ФЛАГА, ПРОЗОЙ.
//
// Он называет и переменную, и последствие, потому что и то и другое нужно разным читателям:
// человеку — почему кнопка не сработала, дежурному — что именно включить. Отказ приходит ДО
// стора, поэтому строки прогона не появляется вовсе и день не теряет ни цента резерва.
const designGenerationDisabledMsg = "design generation is switched off in this deployment " +
	"(DESIGN_GENERATION_ENABLED is unset): there is no worker to pick a run up, so a run opened " +
	"now would reserve money and sit in `pending` forever — it is refused instead"

// designReasonGenerationDisabled — машинная причина того же отказа, чтобы клиент отличал
// «выключено у нас» от «модель отвергла запрос», не разбирая английскую прозу.
const designReasonGenerationDisabled = "generation_disabled"

// SetDesignGenerationEnabled переключает генеративную половину полосы.
//
// ⚠ ПО УМОЛЧАНИЮ ВЫКЛЮЧЕНО, И ЭТО НАМЕРЕННО FAIL-CLOSED. Сервер, собранный без этого вызова
// (сегодня — любой, потому что app/app.go принадлежит задаче воркера B8), отказывает на
// StartDesignRun и DraftDesignIdea внятными словами вместо того, чтобы завести оплаченный прогон,
// который никто не исполнит.
//
// ЗАЧЕМ СЕТТЕР, А НЕ АРГУМЕНТ admin.New. Флаг живёт в конфигурации воркера, а config/cfg.go и
// app/app.go принадлежат ЕГО задаче; расширение сигнатуры New сломало бы сборку у всех, кто её
// зовёт, до того как та задача сядет.
//
// ⚠ ЗНАЧЕНИЕ ОБЯЗАНО БЫТЬ ТЕМ ЖЕ САМЫМ, ЧТО ГЕЙТИТ РЕГИСТРАЦИЮ ВОРКЕРА, и «тем же самым» здесь
// значит из ОДНОГО выражения, а не из двух чтений одной переменной. В app/app.go это одна строка
// рядом с регистрацией:
//
//	dg := designgen.ConfigFromEnv()       // ← уже есть у воркера
//	adminS.SetDesignGenerationEnabled(dg.Enabled)
//
// Разойдясь, эти два значения дают ровно два состояния, каждое из которых хуже выключенного:
// хендлер открыт без воркера — оплаченные прогоны висят в pending навсегда; воркер поднят при
// закрытом хендлере — работающий воркер над пустой очередью.
//
// Звать ДО начала обслуживания: поле читается запросами и после старта не меняется.
func (s *Server) SetDesignGenerationEnabled(v bool) { s.designGenerationEnabled = v }

// designGenerationGate — единственная проверка флага. Одна на два платных глагола: две копии
// разошлись бы молча, и один из глаголов однажды остался бы включённым на проде.
func (s *Server) designGenerationGate() error {
	if s.designGenerationEnabled {
		return nil
	}
	return designRefusal(codes.FailedPrecondition, designReasonGenerationDisabled,
		designGenerationDisabledMsg, nil)
}

// ─────────────────────────── цена до клика ───────────────────────────

// designPriceEstimate — ОЦЕНКА, А НЕ КОТИРОВКА, и она названа оценкой вслух (34-PLAN §5.4;
// серверной котировки на проводе нет вовсе).
//
// Она делает ровно одно: наполняет `design_run.price_estimate`, то есть РЕЗЕРВ дня. Резерв — это
// потолок, а не счёт: фактическую цену пишет попытка (`FinishAttempt`), а резерв снимается целиком
// на терминальном переходе. Поэтому оценка обязана быть скорее завышенной, чем заниженной:
// заниженная пропускает за дневной потолок больше прогонов, чем владелец согласился оплатить.
//
// ⚠ ЦИФРЫ ЖДУТ ВЛАДЕЛЬЦА (34-PLAN §6). До тех пор это правдоподобная догадка в долларах, и она
// стоит здесь одной таблицей ровно затем, чтобы её было где заменить одной правкой.
var designPriceEstimate = map[string]decimal.Decimal{
	entity.DesignRunKindFlat:      decimal.RequireFromString("0.04"),
	entity.DesignRunKindRender:    decimal.RequireFromString("0.08"),
	entity.DesignRunKindThreed:    decimal.RequireFromString("0.60"),
	entity.DesignRunKindVector:    decimal.RequireFromString("0.04"),
	entity.DesignRunKindDraftIdea: decimal.RequireFromString("0.02"),
}

// designEstimateFor — оценка задания целиком: цена одного выхода на число запрошенных выходов.
func designEstimateFor(kind string, outputs int) decimal.NullDecimal {
	per, ok := designPriceEstimate[kind]
	if !ok {
		return decimal.NullDecimal{}
	}
	if outputs < 1 {
		outputs = 1
	}
	return decimal.NullDecimal{Decimal: per.Mul(decimal.NewFromInt(int64(outputs))), Valid: true}
}

// ─────────────────────────── потолки снимка ───────────────────────────

const (
	// designMaxParamsBytes / designMaxInputsBytes — потолки, объявленные контрактом дословно
	// («Total encoded size is capped at 8 KB», «capped at 64 KB»). Проверяются ПОСЛЕ сборки, по
	// закодированным байтам: считать поля вместо байтов значит проверять другое число.
	designMaxParamsBytes = 8 << 10
	designMaxInputsBytes = 64 << 10
	// designMaxInputRefs / designMaxInputSlots — «refs ≤ 24; slots ≤ 8. A snapshot must fit in a
	// row and in an eye».
	designMaxInputRefs  = 24
	designMaxInputSlots = 8
	// designMaxAskRunes — потолок дельта-фразы. Строка `ask` едет в промпт и в историю; без
	// потолка одно нажатие вставляет туда целую книгу.
	designMaxAskRunes = 4000
)

// Раскладки кадра — словарь DesignRunParams.layout, повторённый здесь потому, что в сторе он
// приватный, а проверять запрос обязан тот, кто его принимает.
const (
	designLayoutOne     = "one"
	designLayoutPerView = "per_view"
)

// designProfileName / designProfileVersion — подпись профиля промпта, замерзающая в строке
// прогона. Профиль пока один; версия растёт, когда меняется СМЫСЛ собираемого промпта, и именно
// по ней история отличает «то же самое» от «другое, просто похоже выглядит».
const (
	designProfileName    = "design-band"
	designProfileVersion = 1
)

// ─────────────────────────── StartDesignRun ───────────────────────────

// StartDesignRun opens a paid picture job.
//
// ПОРЯДОК ПРОВЕРОК — РЕШЕНИЕ, А НЕ СТИЛЬ: сначала то, что не стоит ни одного чтения (флаг, форма
// запроса), потом чтения (карточка, полоса), и только в самом конце стор, который тратит деньги.
// Перевёрнутый порядок платил бы за запрос, который всё равно был бы отвергнут.
func (s *Server) StartDesignRun(ctx context.Context, req *pb_admin.StartDesignRunRequest) (*pb_admin.StartDesignRunResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	clientRequestID := strings.TrimSpace(req.GetClientRequestId())
	if clientRequestID == "" {
		return nil, status.Error(codes.InvalidArgument,
			"client_request_id is required — without it a double click pays twice")
	}
	if err := s.designGenerationGate(); err != nil {
		return nil, err
	}

	kind := strings.TrimSpace(req.GetKind())
	if !entity.IsDesignRunKind(kind) {
		return nil, status.Errorf(codes.InvalidArgument,
			"kind %q is not flat | render | threed | vector", kind)
	}
	// draft_idea ОТКАЗЫВАЕТСЯ ЗДЕСЬ, дословно по контракту. Текстовый прогон исполняется в
	// хендлере синхронно и возвращает свой ответ; заведённый отсюда, он вернул бы строку
	// `pending`, которую никто никогда не опрашивает, — предикат захвата воркера исключает
	// draft_idea намеренно, иначе воркер оплатил бы второй вызов той же модели.
	if kind == entity.DesignRunKindDraftIdea {
		return nil, status.Error(codes.InvalidArgument,
			"a text run has its own verb: call DraftDesignIdea, which executes inline and returns its answer")
	}
	ask := strings.TrimSpace(req.GetAsk())
	if len([]rune(ask)) > designMaxAskRunes {
		return nil, status.Errorf(codes.InvalidArgument,
			"ask is %d characters; the ceiling is %d", len([]rune(ask)), designMaxAskRunes)
	}

	// ─── карточка и полоса ───
	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "design run: cannot load the tech card",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot load the tech card")
	}
	// runLimit = 1: истории здесь не нужно ни строки, нужны верстак, референсы и флаг рендера.
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return nil, designError(ctx, "failed to read the design band before starting a run", err, nil)
	}

	// ─── W-13: 3D ТОЛЬКО ПОСЛЕ FABRIC RENDER ───
	//
	// Флаг считает GetBand по ВСЕЙ карточке (`design_picture.kind = 'render' AND hidden_at IS
	// NULL`), а не по загруженной странице: рендер вполне лежит за потолком страницы, и гейт,
	// посчитанный по ней, закрыл бы 3D ровно там, где оно законно.
	if kind == entity.DesignRunKindThreed && !band.HasFabricRender {
		return nil, designRefusal(codes.FailedPrecondition, "no_fabric_render",
			"3D needs a fabric render first: this card has no render that is not hidden. "+
				"The order is flats → fabric render → 3D",
			map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}

	// ─── реран: параметры и входы приезжают ИЗ БАЗЫ ───
	var parent *entity.DesignRun
	if req.GetRerunOfRunId() > 0 {
		parent, err = s.designRerunParent(ctx, cardID, int(req.GetRerunOfRunId()))
		if err != nil {
			return nil, err
		}
	}

	params, err := designEffectiveParams(req.GetParams(), parent)
	if err != nil {
		return nil, err
	}
	paramsJSON, err := designMarshalJSON(params)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design run: params did not encode",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the run parameters could not be stored")
	}
	if len(paramsJSON) > designMaxParamsBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"params encode to %d bytes; the ceiling is %d", len(paramsJSON), designMaxParamsBytes)
	}

	inputs, fitAtLaunch, err := s.designRunInputs(ctx, designInputSources{
		Kind:   kind,
		Card:   card,
		Refs:   band.References,
		Bench:  band.Bench,
		Params: params,
	}, parent)
	if err != nil {
		return nil, err
	}
	inputsJSON, err := designMarshalJSON(inputs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design run: the input snapshot did not encode",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the input snapshot could not be stored")
	}
	if len(inputsJSON) > designMaxInputsBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"the input snapshot encodes to %d bytes; the ceiling is %d",
			len(inputsJSON), designMaxInputsBytes)
	}

	outputs := designRequestedOutputs(kind, params)
	started, err := s.repo.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId:      cardID,
		ClientRequestId: clientRequestID,
		Kind:            kind,
		Ask:             ask,
		// json.RawMessage, А НЕ entity.RawJSON, И ЭТО НЕ ОПЕЧАТКА. RawJSON существует ради
		// NULL-безопасного ЧТЕНИЯ колонки; здесь тип принадлежит запросной структуре стора, у
		// которой NULL взяться неоткуда. Правило «только RawJSON» — про колонки.
		Params:           json.RawMessage(paramsJSON),
		Inputs:           json.RawMessage(inputsJSON),
		ProfileName:      designProfileName,
		ProfileVersion:   designProfileVersion,
		FitAtLaunch:      fitAtLaunch,
		RequestedOutputs: outputs,
		PriceEstimate:    designEstimateFor(kind, outputs),
		Author:           designActor(ctx),
		RerunOf:          designParentID(parent),
	})
	if err != nil {
		return nil, designError(ctx, "failed to start the design run", err, nil)
	}
	return &pb_admin.StartDesignRunResponse{
		Run:    s.designRunResponse(ctx, started.Run),
		Budget: s.designBudgetResponse(ctx, started.Budget),
	}, nil
}

// designParentID — id родителя рерана либо 0. Отдельной функцией, чтобы «nil значит обычный
// прогон» было написано ровно один раз.
func designParentID(parent *entity.DesignRun) int {
	if parent == nil {
		return 0
	}
	return parent.Id
}

// designRerunParent читает прогон, который повторяют, и отвечает за то, чтобы им нельзя было
// указать на чужую карточку или на текстовый прогон.
//
// СТОР ПРОВЕРЯЕТ ТО ЖЕ САМОЕ ВНУТРИ СВОЕЙ ТРАНЗАКЦИИ, и это не дубликат: там проверка — пояс
// против гонки (родителя могли удалить между чтением и вставкой), здесь — источник ВХОДОВ,
// которые без этого чтения неоткуда взять.
func (s *Server) designRerunParent(ctx context.Context, cardID, parentID int) (*entity.DesignRun, error) {
	parent, err := s.repo.Design().GetRun(ctx, parentID)
	if err != nil {
		return nil, designError(ctx, "failed to read the run being rerun", err, nil)
	}
	if parent.TechCardId != cardID {
		// NotFound, а не PermissionDenied: «этого прогона у этой карточки нет» — правда, и она
		// не рассказывает постороннему, что прогон с таким номером существует у кого-то ещё.
		return nil, designRefusal(codes.NotFound, "not_found",
			fmt.Sprintf("run %d does not belong to tech card %d", parentID, cardID), nil)
	}
	if parent.Kind == entity.DesignRunKindDraftIdea {
		return nil, status.Errorf(codes.InvalidArgument,
			"run %d is a text run: it has no picture inputs to repeat", parentID)
	}
	return parent, nil
}

// designEffectiveParams — ЧТО ПРОСЯТ У МОДЕЛИ.
//
// РАЗДЕЛЕНИЕ, КОТОРОЕ ВАЖНО НЕ ПЕРЕПУТАТЬ. Параметры — это ЗАПРОС («какие виды, какая
// раскладка»), и он принадлежит человеку, который жмёт кнопку. Входы — это ПРОВЕНАНС («что
// именно ушло в модель»), и он принадлежит серверу. Поэтому клиентские параметры рерана
// применяются поверх (контракт: «`ask` and `params` still apply ON TOP»), а входы всегда
// переписываются со строки родителя, и клиент не может прислать их вовсе.
//
// Когда клиент параметров не прислал, наследуются родительские: реран «то же самое ещё раз» —
// самый частый, и заставлять клиента пересобирать снимок значило бы вернуть ему ровно ту
// возможность соврать, ради устранения которой реран отдан серверу.
func designEffectiveParams(in *pb_common.DesignRunParams, parent *entity.DesignRun) (*pb_common.DesignRunParams, error) {
	params := in
	if params == nil && parent != nil && len(parent.Params) > 0 {
		inherited := &pb_common.DesignRunParams{}
		if err := designUnmarshalJSON(parent.Params, inherited); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored parameters do not parse", parent.Id)
		}
		params = inherited
	}
	if params == nil {
		params = &pb_common.DesignRunParams{}
	}
	// КЛОН, А НЕ ЗАПРОС: ниже поля правятся (пустая раскладка становится явной), а мутировать
	// входящее сообщение значит писать в чужую память — перехватчики и логи видят тот же объект.
	params, _ = proto.Clone(params).(*pb_common.DesignRunParams)

	for i, v := range params.GetViews() {
		if !entity.IsDesignGhostView(v) {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.views.%d %q is not a view of the garment", i, v)
		}
	}
	switch params.GetLayout() {
	case "":
		// ПУСТОЕ ЗАПИСЫВАЕТСЯ ЯВНО. Раскладка замерзает в истории и участвует в дивайдере
		// «earlier — inputs have changed»; строка, у которой её нет, сравнивалась бы с строкой,
		// у которой она `one`, и разошлась бы без причины.
		params.Layout = designLayoutOne
	case designLayoutOne, designLayoutPerView:
	default:
		return nil, status.Errorf(codes.InvalidArgument,
			"params.layout %q is not one | per_view", params.GetLayout())
	}
	for i, v := range params.GetFixTargets() {
		if !entity.IsDesignSilhouetteView(v) {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.fix_targets.%d %q is not a silhouette side; a detail is named in fix_slot_ids", i, v)
		}
	}
	if t := params.GetFixTarget(); t != "" && !entity.IsDesignSilhouetteView(t) {
		return nil, status.Errorf(codes.InvalidArgument,
			"params.fix_target %q is not a silhouette side", t)
	}
	for i, id := range params.GetExtraInputMediaIds() {
		if id <= 0 {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.extra_input_media_ids.%d must be a media id", i)
		}
	}
	return params, nil
}

// designRequestedOutputs — сколько кадров задание вправе ждать. Число едет в строку и рисует
// плитки-плейсхолдеры, поэтому «сколько-нибудь» здесь не годится: пустая плитка, которую никто
// не заполнит, читается как потерянный результат.
func designRequestedOutputs(kind string, params *pb_common.DesignRunParams) int {
	switch kind {
	case entity.DesignRunKindThreed, entity.DesignRunKindVector:
		// Одна модель и один SVG. Кадры поворотного стола, если они появятся, приедут одним
		// артефактом, а не отдельными кадрами полосы.
		return 1
	}
	if params.GetLayout() == designLayoutOne {
		// КОМПОЗИТ — ОДНА КАРТИНКА, сколько бы видов на ней ни было. Разрез на N кадров это
		// отдельный, бесплатный акт (SplitDesignPicture), и считать его выходами прогона значило
		// бы обещать плитки, которых генерация не приносит.
		return 1
	}
	if n := len(params.GetViews()); n > 0 {
		return n
	}
	return 1
}

// ─────────────────────────── CancelDesignRun ───────────────────────────

// CancelDesignRun stops a run.
//
// ДВА РАЗНЫХ АКТА ПОД ОДНИМ ИМЕНЕМ, и решает между ними стор: ждущий прогон закрывается сразу и
// возвращает резерв дню, идущий получает только `cancel_requested_at`, потому что он УЖЕ у
// поставщика и выбросить оплаченный результат нельзя. Хендлер здесь ничего не выбирает — он бы
// выбирал по устаревшему чтению.
func (s *Server) CancelDesignRun(ctx context.Context, req *pb_admin.CancelDesignRunRequest) (*pb_admin.CancelDesignRunResponse, error) {
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.repo.Design().CancelRun(ctx, int(req.GetRunId()), designActor(ctx))
	if err != nil {
		return nil, designError(ctx, "failed to cancel the design run", err, nil)
	}
	return &pb_admin.CancelDesignRunResponse{Run: s.designRunResponse(ctx, *run)}, nil
}

// ─────────────────────────── GetDesignRun ───────────────────────────

// GetDesignRun reads ONE run whole — the row a rerun is assembled from and the row the run panel
// draws. Its shape is the history row's shape exactly, so the answer drops into what the client
// already drew instead of forcing it to merge two shapes of one row.
func (s *Server) GetDesignRun(ctx context.Context, req *pb_admin.GetDesignRunRequest) (*pb_admin.GetDesignRunResponse, error) {
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.repo.Design().GetRun(ctx, int(req.GetRunId()))
	if err != nil {
		return nil, designError(ctx, "failed to read the design run", err, nil)
	}
	return &pb_admin.GetDesignRunResponse{Run: s.designRunResponse(ctx, *run)}, nil
}

// ─────────────────────────── DraftDesignIdea ───────────────────────────

// draftIdeaSystemPrompt — роль модели. Она пишет ПРОЗУ ДЛЯ ЧЕЛОВЕКА, а не JSON: ответ ложится в
// `design_run.output_text` и читается глазами, поэтому и просить надо связный текст.
const draftIdeaSystemPrompt = "You are a fashion designer's assistant. " +
	"From the notes of a garment's moodboard you draft ONE short brief of the garment the notes " +
	"are reaching for: silhouette, proportions, construction, the two or three details that carry " +
	"the idea. Write plain prose in English, at most 250 words. " +
	"Never invent a fabric, a colour or a measurement that the notes do not mention — say what is " +
	"missing instead."

// draftIdeaNotConfiguredMsg / draftIdeaModelUnavailableMsg — те же две несводимые настройки, что
// у остальных функций на s.aiOps, и те же слова: одна причина обязана звучать одинаково везде,
// иначе дежурный чинит две разные поломки вместо одной.
const (
	draftIdeaNotConfiguredMsg    = "drafting the idea is not configured (set OPENROUTER_API_KEY)"
	draftIdeaModelUnavailableMsg = "drafting the idea is misconfigured: " + modelUnavailableAdviceMsg
)

// ─────────────────────── ДВА БЕСПЛАТНЫХ ГЛАГОЛА ───────────────────────
//
// ⚠ НИ ОДИН ИЗ НИХ НЕ ПРОХОДИТ designGenerationGate, И ЭТО ПРОВЕРЯЕМОЕ РЕШЕНИЕ, А НЕ ЗАБЫВЧИВОСТЬ.
// Флаг DESIGN_GENERATION_ENABLED стережёт ДЕНЬГИ: он существует затем, чтобы при отсутствующем
// воркере не заводилась оплаченная строка, которую некому забрать. Отметить кадр выбранным и
// подшить уже загруженный SVG не тратят ни цента и не заводят прогона вовсе — навешенный на них
// флаг выключал бы обычную работу с полосой в деплое, где генерация просто не включена.
//
// Оба стоят в этом файле по указанию задачи; по существу они соседи HideDesignPicture и
// SaveDesignEditLayer из design_band.go — там же живут остальные бесплатные глаголы.

// SetDesignPictureSelected marks a picture as CHOSEN, and un-marks it (owner requirement W-12:
// «мы так же можем маркать 3д рендеры как выбранные»).
//
// A VERB OF ITS OWN, DELIBERATELY NOT A FLAG ON HideDesignPicture: hidden says «do not show me
// this», selected says «this is the one». The two are independent — a chosen picture may later be
// hidden — and folding them would make one gesture silently undo the other.
//
// Nothing is exclusive: the owner speaks in the plural, so many pictures may be chosen at once.
func (s *Server) SetDesignPictureSelected(ctx context.Context, req *pb_admin.SetDesignPictureSelectedRequest) (*pb_admin.SetDesignPictureSelectedResponse, error) {
	pic, err := s.repo.Design().SetPictureSelected(ctx,
		int(req.GetPictureId()), req.GetSelected(), designActor(ctx))
	if err != nil {
		return nil, designError(ctx, "failed to set the design picture selection", err, nil)
	}
	return &pb_admin.SetDesignPictureSelectedResponse{Picture: designPictureToPb(*pic)}, nil
}

// ImportDesignVector files an ALREADY-UPLOADED vector file into the band as an edit layer.
//
// IT SPENDS NOTHING, AND THAT IS THE LINE BETWEEN IT AND GENERATION. Vectorising BY MACHINE is a
// paid provider call and goes through StartDesignRun with kind = vector; this verb files a file
// that already exists, which is why the money gate above does not stand here.
//
// THE CLIENT PARSES, THE SERVER RECORDS THE PROVENANCE — the same division of labour
// FlattenDesignEditLayer draws, and for the same reason: there is no SVG parser and no vector
// renderer anywhere in this repository.
func (s *Server) ImportDesignVector(ctx context.Context, req *pb_admin.ImportDesignVectorRequest) (*pb_admin.ImportDesignVectorResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	clientRequestID := strings.TrimSpace(req.GetClientRequestId())
	if clientRequestID == "" {
		return nil, status.Error(codes.InvalidArgument,
			"client_request_id is required — without it a retry files the same SVG twice")
	}
	strokes := req.GetStrokes()
	if len(strokes) > design.MaxStrokesBytes {
		return nil, designError(ctx, "strokes too large",
			fmt.Errorf("%w: %d bytes, the ceiling is %d",
				entity.ErrDesignStrokesTooLarge, len(strokes), design.MaxStrokesBytes), nil)
	}
	// The column VALIDATES JSON, so an opaque payload would come back as a raw 3140 naming a
	// column. Checked here, the person is told what is wrong with what they sent — the same
	// treatment SaveDesignEditLayer gives the same bytes.
	if len(strokes) > 0 && !json.Valid(strokes) {
		return nil, status.Error(codes.InvalidArgument, "strokes must be JSON")
	}
	layer, err := s.repo.Design().ImportVector(ctx, entity.DesignVectorImport{
		TechCardId:      int(req.GetTechCardId()),
		ClientRequestId: clientRequestID,
		SourceMediaId:   int(req.GetSourceMediaId()),
		SourcePictureId: int(req.GetSourcePictureId()),
		Origin:          strings.TrimSpace(req.GetOrigin()),
		BaseMediaId:     int(req.GetBaseMediaId()),
		Strokes:         json.RawMessage(strokes),
		Actor:           designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to import the design vector", err, nil)
	}
	// STROKES ARE ECHOED, exactly as the contract's response comment says — the caller has just
	// sent them, and the layer it must now compare-and-set against is the whole answer.
	return &pb_admin.ImportDesignVectorResponse{Layer: designLayerToPb(*layer, true)}, nil
}

// DraftDesignIdea runs a TEXT model over the moodboard's WORDS and returns its answer inline.
//
// ПОЧЕМУ СИНХРОННО, А НЕ ВОРКЕРОМ, КАК ВСЁ ОСТАЛЬНОЕ. Стор намеренно исключает `draft_idea` из
// предиката захвата (`kind <> 'draft_idea'` в designRunClaimableSQL): воркер, забравший эту
// строку, оплатил бы ВТОРОЙ вызов той же модели. Ответ здесь приходит за секунды и нужен человеку
// на экране немедленно, а не строкой истории, которую он потом опрашивает.
//
// ⚠ КАРТИНКИ ДОСКИ СЮДА НЕ ПОПАДАЮТ. Модель текстовая, промпт собирается из mood_note и из ТЕКСТА
// выносок — ни одного media_id, ни одного url. Это вторая половина W-15: доска даёт слова, и
// только слова.
func (s *Server) DraftDesignIdea(ctx context.Context, req *pb_admin.DraftDesignIdeaRequest) (*pb_admin.DraftDesignIdeaResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	clientRequestID := strings.TrimSpace(req.GetClientRequestId())
	if clientRequestID == "" {
		return nil, status.Error(codes.InvalidArgument,
			"client_request_id is required — without it a double click pays twice")
	}
	// ФЛАГ ЗАКРЫВАЕТ И ЭТУ ДВЕРЬ, ПО ДРУГОЙ ПРИЧИНЕ. Воркер ей не нужен, но это тот же платный
	// вызов в том же денежном регистре, и полоса выкатывается инертной ЦЕЛИКОМ: половина
	// включённой генерации — это состояние, которого никто не проверял.
	if err := s.designGenerationGate(); err != nil {
		return nil, err
	}
	if !s.aiOps.Enabled() {
		return nil, aiRefusal(aiReasonNotConfigured, draftIdeaNotConfiguredMsg, nil)
	}

	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "draft design idea: cannot load the tech card",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot load the tech card")
	}
	// ПУСТАЯ ДОСКА — ОТКАЗ, А НЕ ПЛАТНЫЙ ВЫЗОВ НИ О ЧЁМ.
	//
	// ⚠ ПРОВЕРЯЕТСЯ mood, А НЕ ДЛИНА ПРОМПТА, и это разные вопросы. Промпт несёт ещё и имя
	// изделия с фитом, поэтому у любой названной карточки он непустой ВСЕГДА — сторож по его
	// длине не сработал бы никогда и оплачивал бы «придумай одежду по слову „пальто“».
	mood := designMoodSnapshot(card)
	if mood == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"the moodboard says nothing yet: write the board's note or pin a callout, then draft the idea")
	}
	prompt := designDraftIdeaPrompt(card, mood)

	// СНИМОК ВХОДОВ ТЕКСТОВОГО ПРОГОНА — ЭТО ДОСКА, И ТОЛЬКО ОНА. Ни refs, ни slots: картинок
	// этот прогон не читает вовсе, и пустые списки здесь — утверждение, а не забывчивость.
	inputs := &pb_common.DesignInputSnapshot{Mood: mood, Fit: card.Fit.String}
	inputsJSON, err := designMarshalJSON(inputs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: the input snapshot did not encode",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the input snapshot could not be stored")
	}

	est := designEstimateFor(entity.DesignRunKindDraftIdea, 1)
	started, err := s.repo.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId:       cardID,
		ClientRequestId:  clientRequestID,
		Kind:             entity.DesignRunKindDraftIdea,
		Inputs:           json.RawMessage(inputsJSON),
		ProfileName:      designProfileName,
		ProfileVersion:   designProfileVersion,
		FitAtLaunch:      card.Fit.String,
		RequestedOutputs: 0, // текстовый прогон не рождает ни одного кадра
		PriceEstimate:    est,
		Author:           designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to open the design idea draft", err, nil)
	}
	run := started.Run

	// ─── ПОВТОР ───
	//
	// ЗАКОНЧЕННЫЙ ПРОГОН ОТДАЁТСЯ КАК ЕСТЬ И МОДЕЛЬ НЕ ЗОВЁТСЯ ВТОРОЙ РАЗ: ровно за этим и стоит
	// client_request_id. Незаконченный — тот, чей хендлер умер посреди вызова; его перехват
	// разрешён ТОЛЬКО по истёкшей лизе, потому что живая лиза означает, что вызов идёт прямо
	// сейчас, и второй звонок оплатил бы его второй раз.
	if started.Idempotent && !designRunResumable(run, s.repo.Now().UTC()) {
		return &pb_admin.DraftDesignIdeaResponse{
			Run:    s.designRunResponse(ctx, run),
			Budget: s.designBudgetResponse(ctx, started.Budget),
		}, nil
	}
	if !run.ClaimToken.Valid || run.ClaimToken.String == "" {
		// Строка без захвата не принадлежит никому, и закрыть её нечем: CompleteRun сверяет
		// токен. Это не должно случаться — токен выдаёт StartRun, — поэтому и говорится вслух.
		slog.Default().ErrorContext(ctx, "draft design idea: the run carries no claim token",
			slog.Int("run_id", run.Id))
		return nil, status.Error(codes.Internal, "the idea draft could not be claimed")
	}

	attempt, err := s.repo.Design().StartAttempt(ctx, entity.DesignAttemptStart{
		RunId:      run.Id,
		ClaimToken: run.ClaimToken.String,
		Provider:   "openrouter",
	})
	if err != nil {
		return nil, designError(ctx, "failed to open the idea draft attempt", err, nil)
	}

	text, callErr := s.aiOps.Complete(ctx, draftIdeaSystemPrompt, prompt, false)
	if callErr == nil && strings.TrimSpace(text) == "" {
		callErr = errors.New("the model returned an empty draft")
	}
	if callErr != nil {
		s.designFailDraft(ctx, run, attempt.AttemptNo, callErr)
		return nil, s.designDraftCallError(ctx, cardID, callErr)
	}

	// ЦЕНА ПОПЫТКИ — ОЦЕНКА, И ЭТО НАЗВАНО ВСЛУХ. Чат-эндпоинт OpenRouter возвращает токены, но
	// не деньги, а `spent`, который никогда не растёт, — это дневной потолок, который никогда не
	// исчерпывается: черновики можно было бы жать бесконечно, оставаясь «в бюджете».
	if err := s.repo.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
		RunId: run.Id, AttemptNo: attempt.AttemptNo,
		State: entity.DesignAttemptDelivered, Price: est,
	}); err != nil {
		return nil, designError(ctx, "failed to close the idea draft attempt", err, nil)
	}
	done, err := s.repo.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId:      run.Id,
		ClaimToken: run.ClaimToken.String,
		OutputText: sql.NullString{String: text, Valid: true},
	})
	if err != nil {
		return nil, designError(ctx, "failed to file the idea draft", err, nil)
	}
	budget, err := s.repo.Design().GetBudget(ctx)
	if err != nil {
		return nil, designError(ctx, "failed to read the design budget", err, nil)
	}
	return &pb_admin.DraftDesignIdeaResponse{
		Run:    s.designRunResponse(ctx, *done),
		Budget: s.designBudgetResponse(ctx, budget),
	}, nil
}

// designRunResumable — можно ли доисполнить прогон, чей хендлер не вернулся.
//
// ЛИЗА — ЕДИНСТВЕННЫЙ ПРИЗНАК. Строка `pending` с ЖИВОЙ лизой означает «вызов идёт прямо сейчас,
// в соседнем запросе»; перехватить её значит оплатить ту же модель дважды. Строка с ИСТЁКШЕЙ
// лизой означает, что хендлер умер: у draft_idea нет воркера, который бы её подобрал
// (ClaimRuns исключает этот род), и без перехвата она висела бы с зарезервированными деньгами до
// полуночи.
func designRunResumable(run entity.DesignRun, now time.Time) bool {
	switch run.Status {
	case entity.DesignRunDone, entity.DesignRunFailed, entity.DesignRunCancelled:
		return false
	}
	if !run.ClaimToken.Valid || run.ClaimToken.String == "" {
		// Без токена закрыть строку нечем: CompleteRun сверяет его в WHERE. Перехват без него
		// оставил бы прогон открытым уже после оплаченного вызова.
		return false
	}
	if !run.ClaimExpiresAt.Valid {
		return false
	}
	return run.ClaimExpiresAt.Time.Before(now)
}

// designFailDraft закрывает попытку и прогон после провала вызова. ЛУЧШЕЕ УСИЛИЕ И ГРОМКОЕ:
// ошибка здесь не возвращается человеку — он должен увидеть ту, из-за которой всё началось, — но
// молчание оставило бы прогон висеть с зарезервированными деньгами.
func (s *Server) designFailDraft(ctx context.Context, run entity.DesignRun, attemptNo int, cause error) {
	if err := s.repo.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
		RunId: run.Id, AttemptNo: attemptNo,
		State:     entity.DesignAttemptFailed,
		ErrorCode: "provider_error",
	}); err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: cannot close the failed attempt",
			slog.Int("run_id", run.Id), slog.String("err", err.Error()))
	}
	if _, err := s.repo.Design().FailRun(ctx, entity.DesignRunFail{
		RunId:      run.Id,
		ClaimToken: run.ClaimToken.String,
		ErrorCode:  "provider_error",
		LastError:  cause.Error(),
		// НЕ RETRYABLE: ретраить некому. Воркер эту строку не заберёт по построению, а повтор —
		// это новый клик человека с новым client_request_id.
		Retryable: false,
	}); err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: cannot close the failed run",
			slog.Int("run_id", run.Id), slog.String("err", err.Error()))
	}
}

// designDraftCallError переводит провал вызова модели в отказ, который человек может починить.
func (s *Server) designDraftCallError(ctx context.Context, cardID int, err error) error {
	if errors.Is(err, openrouter.ErrNotConfigured) {
		return aiRefusal(aiReasonNotConfigured, draftIdeaNotConfiguredMsg, nil)
	}
	slog.Default().ErrorContext(ctx, "draft design idea: the model call failed",
		slog.Int("tech_card_id", cardID), slog.String("model", s.aiOps.Model()),
		slog.String("base_url", s.aiOps.BaseURL()), slog.String("err", err.Error()))
	if errors.Is(err, openrouter.ErrModelUnavailable) {
		return aiModelRefusal(draftIdeaModelUnavailableMsg, s.aiOps.Model())
	}
	return status.Errorf(codes.Unavailable, "drafting the idea failed: %v", err)
}

// ─────────────────────────── W-15: СБОРКА ВХОДОВ ───────────────────────────

// designInputSources — ВСЁ, ИЗ ЧЕГО СОБИРАЕТСЯ СНИМОК ВХОДОВ, и ничего сверх.
type designInputSources struct {
	Kind   string
	Card   *entity.TechCard
	Refs   []entity.DesignReference
	Bench  []entity.DesignBenchSlot
	Params *pb_common.DesignRunParams
}

// designAssembleInputs — ГАРАНТИЯ W-15, И ОНА ЖИВЁТ ЗДЕСЬ, А НЕ НА ЭКРАНЕ.
//
// ВЛАДЕЛЕЦ ПРОЦИТИРОВАЛ СТРОКУ ПРОТОТИПА ДОСЛОВНО: «the mood, not the prompt: nothing here is
// sent to generation». Экран это обещает; обещание экрана обходится любым вторым клиентом,
// повтором запроса и просто вкладкой, открытой по ссылке. Гарантия — вот эта функция.
//
// ПРАВИЛО ОДНОЙ ФРАЗОЙ: КАРТИНКИ ВХОДЯТ ТОЛЬКО ИЗ ДВУХ ИМЕНОВАННЫХ ИСТОЧНИКОВ — строк
// `design_reference` (то, что человек ЯВНО перенёс в INPUT — REFERENCES) и
// `params.extra_input_media_ids` (то, что он ЯВНО назвал вторым способом). Плюс плиты верстака,
// которые не картинки доски, а собственные чертежи карточки. `Card.Media` в сборке картинок не
// читается ВООБЩЕ — ни для фильтра, ни для проверки.
//
// ⚠ ФИЛЬТР «ВЫБРОСИТЬ ТО, ЧТО ЛЕЖИТ НА ДОСКЕ» БЫЛ БЫ ОШИБКОЙ, И ЭТО НЕ ТОНКОСТЬ. Владелец
// требует ровно обратного (U-5 §5, A2): щелчок по плитке доски ЗАВОДИТ запись референса с тем же
// media_id — это и есть «явный перенос». Медиа, лежащее и на доске, и в референсах, уходит в
// модель ЗАКОННО, потому что человек его туда положил. Фильтровать по членству в доске значило бы
// молча ломать главный жест полосы; правило про ИСТОЧНИК, а не про картинку.
//
// `mood` У КАРТИНОЧНЫХ ПРОГОНОВ ОСТАЁТСЯ ПУСТЫМ. Не потому что доска бесполезна, а потому что
// снимок — это то, что ушло в модель: заполненный `mood` дал бы воркеру, который его прочитает,
// media_id доски прямо в руки, и W-15 держалась бы на его дисциплине. Единственный прогон, у
// которого `mood` заполнен, — текстовый: он читает СЛОВА доски и не получает ни одной картинки
// (см. DraftDesignIdea).
func designAssembleInputs(src designInputSources) (*pb_common.DesignInputSnapshot, error) {
	out := &pb_common.DesignInputSnapshot{
		Views:  src.Params.GetViews(),
		Layout: src.Params.GetLayout(),
	}
	if src.Card != nil {
		out.Fit = src.Card.Fit.String
		// ОПИСАНИЕ ИЗДЕЛИЯ (W-3) — «пишем общий коммент», который уходит в КАЖДЫЙ прогон.
		// Замораживается КОПИЕЙ, а не джойном: правка описания завтра не имеет права переписать
		// то, что сказали модели вчера, — иначе история перестаёт быть свидетельством.
		//
		// ⚠ ЭТО НЕ MoodNote. Та про ДОСКУ и её читает ровно один текстовый прогон (W-15); эта про
		// ИЗДЕЛИЕ и её читает каждая генерация. Подставленная сюда записка доски отправила бы в
		// модель ровно те слова, которые W-15 запрещает.
		out.GarmentNote = src.Card.GarmentDescription.String
	}

	// ─── refs: design_reference, затем явно названные extra_input_media_ids ───
	//
	// ВЫНОСКИ ПРИКАЛЫВАЮТСЯ К РЕФЕРЕНСУ, А НЕ ПРИХОДЯТ С ДОСКИ, и разница здесь не словесная.
	// Ключом служит media_id УЖЕ ОТОБРАННОГО референса: доска в этот отбор не входит ни при каком
	// входе (см. шапку), поэтому картинка попадает сюда только пройдя через явный перенос
	// человеком — а раз картинка законна, законна и разметка, которую он на ней нарисовал.
	// Card.Media при этом по-прежнему не читается ВООБЩЕ: карта строится по Card.Callouts.
	callouts := designCalloutsByMedia(src.Card)
	seen := make(map[int]struct{}, len(src.Refs))
	for _, r := range src.Refs {
		if r.MediaId <= 0 {
			continue
		}
		if _, dup := seen[r.MediaId]; dup {
			continue
		}
		seen[r.MediaId] = struct{}{}
		out.Refs = append(out.Refs, &pb_common.DesignInputRef{
			MediaId: int32(r.MediaId),
			Role:    r.Role,
			// ЗАПИСКА ЧЕЛОВЕКА ПРО ЭТУ КАРТИНКУ (W-3): «только воротник», «ткань, а не крой».
			// Восемь референсов без записок называют восемь сторон и ни одного намерения.
			Note:     r.Note.String,
			Callouts: callouts[r.MediaId],
		})
	}
	for _, id := range src.Params.GetExtraInputMediaIds() {
		if id <= 0 {
			continue
		}
		if _, dup := seen[int(id)]; dup {
			continue
		}
		seen[int(id)] = struct{}{}
		// РОЛЬ ПУСТА, И ЭТО ПРАВДА: дополнительный вход назван человеком прямо в запросе и о
		// стороне изделия ничего не говорит. Придуманная здесь роль соврала бы модели.
		//
		// ЗАПИСКИ ТОЖЕ НЕТ — её негде взять: записка живёт на строке design_reference, а этого
		// входа в референсах нет. РАЗМЕТКА ЖЕ ЕСТЬ, если она есть: выноска нарисована человеком
		// на этой самой картинке, и молчать о ней значило бы послать в модель меньше, чем он
		// нарисовал, ровно по тому же доводу, по которому геометрия перестала теряться у доски.
		out.Refs = append(out.Refs, &pb_common.DesignInputRef{
			MediaId:  id,
			Callouts: callouts[int(id)],
		})
	}
	if len(out.Refs) > designMaxInputRefs {
		return nil, status.Errorf(codes.InvalidArgument,
			"a run may carry %d reference images; this one has %d", designMaxInputRefs, len(out.Refs))
	}

	// ─── slots: плиты верстака ───
	out.Slots = designInputSlots(src)
	if len(out.Slots) > designMaxInputSlots {
		return nil, status.Errorf(codes.InvalidArgument,
			"a run may carry %d bench plates; this one has %d", designMaxInputSlots, len(out.Slots))
	}
	return out, nil
}

// designInputSlots — КАКИЕ ПЛИТЫ ВЕРСТАКА ЕДУТ В ЭТОТ ПРОГОН.
//
// Ось верстака ДВЕ (вид × род, 0349), и род выбирается по роду прогона: рендер строится из
// ФЛЭТОВ, 3D — из РЕНДЕРОВ. Одноосное чтение брало бы рендер фронта вместо флэта фронта ровно
// там, где оба есть, — то есть на любой карточке, дошедшей до 3D.
//
// ВЫБОРКА `fix` СУЖАЕТ СПИСОК. `fix_targets` + `fix_slot_ids` — ОДНА выборка, а не два режима:
// «выбрать всё в FLAT SLOTS» (W-10) называет три стороны и манжету одним прогоном.
func designInputSlots(src designInputSources) []*pb_common.DesignInputSlot {
	want := entity.DesignPictureKindFlat
	if src.Kind == entity.DesignRunKindThreed {
		want = entity.DesignPictureKindRender
	}
	targets := map[string]struct{}{}
	for _, v := range src.Params.GetFixTargets() {
		targets[v] = struct{}{}
	}
	if t := src.Params.GetFixTarget(); t != "" {
		targets[t] = struct{}{}
	}
	slotIDs := map[int]struct{}{}
	for _, id := range src.Params.GetFixSlotIds() {
		slotIDs[int(id)] = struct{}{}
	}
	selective := len(targets) > 0 || len(slotIDs) > 0

	out := make([]*pb_common.DesignInputSlot, 0, len(src.Bench))
	for _, slot := range src.Bench {
		if entity.DesignKindOrFlat(slot.Kind) != want {
			continue
		}
		if slot.Picture == nil || slot.Picture.MediaId <= 0 {
			continue
		}
		if selective {
			_, byView := targets[slot.ViewKey]
			_, bySlot := slotIDs[slot.Id]
			if !byView && !bySlot {
				continue
			}
		}
		in := &pb_common.DesignInputSlot{
			ViewKey:    slot.ViewKey,
			DetailName: slot.DetailName.String,
			MediaId:    int32(slot.Picture.MediaId),
			LayerRev:   int32(slot.Picture.LayerRev),
		}
		if !entity.IsDesignSilhouetteView(slot.ViewKey) {
			// slot_id ТОЛЬКО У ДЕТАЛЕЙ: вид сам называет каждую из четырёх сторон, а две
			// детали по виду не различить — и без ключа сравнение «вход протух» стало бы
			// невычислимым НАВСЕГДА, потому что снимок замерзает и не чинится задним числом.
			in.SlotId = int32(slot.Id)
		}
		if slot.Picture.Media != nil && slot.Picture.Media.ContentHash.Valid {
			in.ContentHash = slot.Picture.Media.ContentHash.String
		}
		out = append(out, in)
	}
	return out
}

// designRunInputs — снимок ЭТОГО прогона плюс фит, под которым он уходит.
//
// РЕРАН ПЕРЕПИСЫВАЕТ ВХОДЫ СО СТРОКИ РОДИТЕЛЯ ЦЕЛИКОМ, и это ровно то, ради чего реран отдан
// серверу: повторить прогон значит послать модели ТО ЖЕ САМОЕ, а «то же самое» знает только
// история. Из сегодняшнего состояния карточки берётся ноль полей.
//
// ДВА ПОЛЯ ВСЁ-ТАКИ ОБНОВЛЯЮТСЯ, И ОБА — НЕ ПРОВЕНАНС. `views`/`layout` — это КОПИЯ параметров
// внутри снимка (контракт: «the fingerprint that draws the current / earlier divider»), и снимок,
// чей отпечаток не сходится с собственными параметрами строки, врал бы дивайдеру истории.
func (s *Server) designRunInputs(ctx context.Context, src designInputSources, parent *entity.DesignRun) (*pb_common.DesignInputSnapshot, string, error) {
	if parent == nil {
		snap, err := designAssembleInputs(src)
		if err != nil {
			return nil, "", err
		}
		fit := ""
		if src.Card != nil {
			fit = src.Card.Fit.String
		}
		return snap, fit, nil
	}
	snap := &pb_common.DesignInputSnapshot{}
	if len(parent.Inputs) > 0 {
		if err := designUnmarshalJSON(parent.Inputs, snap); err != nil {
			slog.Default().WarnContext(ctx, "design rerun: the parent's input snapshot did not parse",
				slog.Int("run_id", parent.Id), slog.String("err", err.Error()))
			return nil, "", status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored inputs do not parse", parent.Id)
		}
	}
	snap.Views = src.Params.GetViews()
	snap.Layout = src.Params.GetLayout()
	// ФИТ БЕРЁТСЯ ИЗ СНИМКА РОДИТЕЛЯ, А НЕ С КАРТОЧКИ. Модель получит те же слова, что получила в
	// прошлый раз, значит и `fit_at_launch` строки обязан говорить о том же: иначе плита
	// приедет со штампом сегодняшнего фита, а нарисована будет по вчерашнему, и минт сверил бы
	// её не с тем.
	return snap, snap.GetFit(), nil
}

// ─────────────────────────── доска: только слова ───────────────────────────

// designMoodSnapshot — доска в том виде, в каком её ЧИТАЕТ ТЕКСТОВЫЙ ПРОГОН: общая записка плюс
// выноски, приколотые на картинки доски.
//
// ⚠ ЗДЕСЬ ЕДИНСТВЕННОЕ МЕСТО ВО ВСЁМ ФАЙЛЕ, ГДЕ ЧИТАЕТСЯ Card.Media, и зовётся оно ровно из одного
// глагола — DraftDesignIdea. Card.Callouts читает ещё designCalloutsByMedia, и она доски не знает:
// она отдаёт КАРТУ по media_id, а какие ключи из неё спросить, решает уже отобранный список
// референсов. Всё остальное собирается designAssembleInputs, куда доска не приходит ни при каком
// входе.
func designMoodSnapshot(card *entity.TechCard) *pb_common.DesignMoodSnapshot {
	if card == nil {
		return nil
	}
	board := make(map[int]struct{}, len(card.Media))
	for _, m := range card.Media {
		if m.Category == entity.TechCardMediaCategoryMoodboard && m.MediaId > 0 {
			board[m.MediaId] = struct{}{}
		}
	}
	out := &pb_common.DesignMoodSnapshot{Note: card.MoodNote.String}
	for _, c := range card.Callouts {
		if !c.MediaId.Valid {
			continue
		}
		if _, ok := board[int(c.MediaId.Int32)]; !ok {
			// Выноска на техническом эскизе — не доска. Пустить её сюда значило бы, что
			// «мудборд» на экране и «мудборд» в снимке — два разных множества.
			continue
		}
		mc := designFrozenCallout(c)
		if mc == nil {
			continue
		}
		out.Callouts = append(out.Callouts, mc)
	}
	if out.GetNote() == "" && len(out.GetCallouts()) == 0 {
		return nil
	}
	return out
}

// designFrozenCallout — ОДНА ВЫНОСКА В ЗАМОРОЖЕННОМ ВИДЕ: слова плюс геометрия. nil, если слов
// нет — снимок хранит то, ЧТО ПРОЧИТАЛИ, и безымянный маркер прочитать нечем.
//
// ⚠ ОДНА ФУНКЦИЯ НА ДВА ЧИТАТЕЛЯ (доска и референсы), И ЭТО НЕСУЩЕЕ. Контракт говорит прямо, что
// DesignMoodCallout — ЕДИНСТВЕННАЯ форма замороженной выноски и что она переиспользуется дословно
// для референсов; две копии сборки разошлись бы в первый же раз, когда одна из них отрастила поле.
//
// ГЕОМЕТРИЯ СОБИРАЕТСЯ ТЕМ ЖЕ КОДОМ, ЧТО И У ЛИСТА, И НИКАКИМ ДРУГИМ. Самодельный JSON здесь уже
// стоил репозиторию молчаливой потери всей разметки: вид и цвет на проводе — ЭНУМЫ, и объект с
// хранимыми строками разбирается без ошибки в ПУСТОЕ сообщение. Единственный способ не разойтись —
// пройти теми же картами.
func designFrozenCallout(c entity.TechCardCallout) *pb_common.DesignMoodCallout {
	if !c.MediaId.Valid {
		return nil
	}
	text := entity.TechCardCalloutPrintedLine(c)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	mc := &pb_common.DesignMoodCallout{MediaId: c.MediaId.Int32, Text: text}
	if raw, err := dto.TechCardCalloutAnnotationJSON(c); err == nil {
		ann := &pb_common.TechCardAnnotation{}
		if err := designUnmarshalJSON(raw, ann); err == nil {
			mc.Annotation = ann
		}
	}
	return mc
}

// designCalloutsByMedia — РАЗМЕТКА КАРТОЧКИ, РАЗЛОЖЕННАЯ ПО КАРТИНКАМ, для сборки входов.
//
// ⚠ ЭТО КАРТА, А НЕ ОТБОР, и разница здесь и есть граница W-15. Функция не знает ни доски, ни
// референсов и ничего не решает о том, какая картинка законна: она отвечает на вопрос «что
// нарисовано на медиа N», а спрашивают её ТОЛЬКО про те медиа, которые уже прошли отбор
// designAssembleInputs. Фильтр «доска» здесь был бы вторым, расходящимся мнением о том же
// правиле — а правило про ИСТОЧНИК картинки, и живёт оно ровно в одном месте.
func designCalloutsByMedia(card *entity.TechCard) map[int][]*pb_common.DesignMoodCallout {
	if card == nil || len(card.Callouts) == 0 {
		return nil
	}
	out := make(map[int][]*pb_common.DesignMoodCallout, len(card.Callouts))
	for _, c := range card.Callouts {
		mc := designFrozenCallout(c)
		if mc == nil {
			continue
		}
		id := int(mc.GetMediaId())
		out[id] = append(out[id], mc)
	}
	return out
}

// designDraftIdeaPrompt — ЧТО ИМЕННО УХОДИТ В ТЕКСТОВУЮ МОДЕЛЬ.
//
// СЛОВА, И ТОЛЬКО СЛОВА. Ни media_id, ни url, ни имени файла: картинка доски в промпт не попадает
// никак — ни байтами, ни ссылкой, ни номером, по которому её можно было бы достать. Это половина
// W-15, которую видно глазами прямо здесь.
func designDraftIdeaPrompt(card *entity.TechCard, mood *pb_common.DesignMoodSnapshot) string {
	var b strings.Builder
	if card != nil {
		if v := strings.TrimSpace(card.Name); v != "" {
			b.WriteString("Garment: " + v + "\n")
		}
		if v := strings.TrimSpace(card.Fit.String); v != "" {
			b.WriteString("Fit: " + v + "\n")
		}
	}
	if note := strings.TrimSpace(mood.GetNote()); note != "" {
		b.WriteString("\nThe board is about:\n" + note + "\n")
	}
	if len(mood.GetCallouts()) > 0 {
		b.WriteString("\nNotes pinned on the board:\n")
		for _, c := range mood.GetCallouts() {
			// НОМЕР КАРТИНКИ НЕ ПИШЕТСЯ. Он не помог бы модели — она картинки не видит — и
			// был бы ровно тем, чего W-15 не допускает.
			b.WriteString("- " + strings.TrimSpace(c.GetText()) + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// ─────────────────────────── общее ───────────────────────────

// designJSONMarshal — ПИСАТЕЛЬ КОЛОНОК `params` И `inputs`.
//
// ⚠ UseProtoNames: true — НЕСУЩЕЕ, И МЕНЯТЬ ЕГО НЕЛЬЗЯ. Стор ходит в эти колонки SQL-путями
// (`$.slots[*].media_id`, `$.extra_input_media_ids`, `$.colour`), написанными по snake_case.
// Дефолтный protojson написал бы lowerCamelCase, и оба запроса стали бы МОЛЧА пустыми: сторож
// HidePicture перестал бы отказывать, чипы цвета исчезли бы, mixed_input никогда не поднялся бы.
// Ни одной ошибки при этом не появится — пустой результат законен для карточки без прогонов,
// поэтому тест сам по себе этого не ловит.
//
// EmitUnpopulated НЕ ВКЛЮЧЁН: снимок ограничен 64 KB, и заполнение всех пустых полей тратило бы
// потолок на нули.
var designJSONMarshal = protojson.MarshalOptions{UseProtoNames: true}

func designMarshalJSON(m proto.Message) ([]byte, error) { return designJSONMarshal.Marshal(m) }

// designRunResponse — одна дверь из строки в ответ: конверсия плюс редакция денег. Две копии
// этой пары разошлись бы, и один из глаголов однажды отдал бы цены аккаунту без costing:read.
func (s *Server) designRunResponse(ctx context.Context, run entity.DesignRun) *pb_common.DesignRun {
	pb := designRunToPb(ctx, run)
	s.stripDesignCosting(ctx, []*pb_common.DesignRun{pb}, nil)
	return pb
}

// designBudgetResponse — то же для полосы бюджета.
func (s *Server) designBudgetResponse(ctx context.Context, b entity.DesignBudget) *pb_common.DesignBudget {
	pb := designBudgetToPb(b)
	s.stripDesignCosting(ctx, nil, pb)
	return pb
}
