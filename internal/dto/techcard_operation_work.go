package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ОСЬ «РАБОТА» НА ШАГЕ (0330) — РАЗБОР И ПРАВИЛА КОГЕРЕНТНОСТИ.
//
// СВОЙ ФАЙЛ, А НЕ ХВОСТ techcard_operation_kinds.go, НАМЕРЕННО: тот файл параллельно правит
// соседняя фаза («перестать терять данные»), и дописка в конец общего файла — ровно тот шов, на
// котором ловятся дефекты параллельных агентов. Правила здесь ни с одним правилом волны 0324 не
// пересекаются: они смотрят на КАТАЛОГ, а не на блоки.
//
// ЧТО ТАКОЕ РАБОТА. Третья ось шага рядом с глаголом («что делаем») и машинкой («на чём»):
// КАКАЯ ЭТО РАБОТА, тем словом, которым технолог называет её у машины. До 0330 вид операции
// нигде не хранился — экран выводил его заново из пары (глагол, машинка), — и оттого сто
// прод-строк из ста двадцати шести (перепись 2026-08-21) неразличимы между собой.
//
// ЧЕТЫРЕ ПРАВИЛА, И ВСЕ ЧЕТЫРЕ — ИМЕНОВАННЫЙ FieldViolation НА `operations[N].work`:
//
//  1. КАТАЛОГ НЕ ЗАГРУЖЕН (`catalog_unavailable`). Не «пропустить», а ОТКАЗАТЬ: правило, которое
//     иногда не работает, не защищает ничего, а незнакомый токен всё равно упёрся бы во внешний
//     ключ — но уже голым 1452 без имени поля и без слов.
//  2. ТОКЕНА НЕТ В КАТАЛОГЕ (`unknown_work`). Тот же ответ, что даст FK, но по-человечески и до
//     транзакции.
//  3. ГЛАГОЛ ШАГА НЕ РАВЕН ГЛАГОЛУ РАБОТЫ (`work_verb_mismatch`). Работа НЕСЁТ глагол — «стачать»
//     это машинная строчка, и никакой другой, — и два ответа на один вопрос на одной строке
//     означают, что печатный лист и рельс сборки скажут разное.
//  4. МАШИНКА НЕ ИЗ СПИСКА РАБОТЫ (`work_machine_mismatch`), И ТОЛЬКО ПРИ machine_mode = ask.
//     Три режима отвечают на «на чём» по-разному: fixed — машинка СЛЕДУЕТ из работы и вопроса нет;
//     none — ось «на чём» у этого глагола не машинная вовсе; ask — работа законно живёт на
//     нескольких машинках, и вот там список закрыт. Проверять машинку при fixed/none значило бы
//     задавать вопрос, которого работа не задаёт.
//
// ⚠️ НЕРЕТРОАКТИВНОСТЬ — НЕ ОБЕЩАНИЕ, А УСТРОЙСТВО. Все правила висят на НЕПУСТОМ work; колонка
// рождается NULL на каждой из 126 строк прода, значит ни одна сохранённая строка новых правил
// нарушить не может. Отдельно проверено на сиде 0329: шесть работ глагола hardware_set объявлены
// с machine_mode = none, хотя 0328 сделала машинку законной на этом глаголе, — правило 4 их не
// касается вовсе, и ретроактивного отказа не даёт даже там.
//
// ⚠️ ПУСТАЯ СТРОКА — НЕ ОТКАЗ, А ЖЕСТ. `work = ""` значит «вид не назначен» и пишется как NULL.
// На ОСВЕДОМЛЁННОЙ записи это человеческое «снять вид», и оно обязано исполняться буквально;
// поэтому щит совместимости стоит на ЯВНОМ флаге operation_work_aware, а не на присутствии поля
// (см. internal/apisrv/admin/techcard_operation_work_gate.go — там же довод целиком).
//
// ЧЕГО ЗДЕСЬ НЕТ. Правила «снятую (retired) работу нельзя выбрать заново» здесь нет и быть не
// может: оно требует ХРАНИМОЙ карточки («если строка уже несёт этот токен — принимается»), а
// разбор payload'а её не видит. Оно живёт в apisrv рядом со щитом, где карточка на руках.

// workUnavailableFix / workCatalogSource — тексты, которые читает человек. Вынесены, чтобы отказ
// и тест называли одно и то же одними словами.
const workCatalogSource = "the work catalog (migration 0329)"

// parseOperationWork разбирает ось «работа» одного шага.
//
// machineType — ЯВНЫЙ тип машинки шага, каким его вернула канонизация глагола (легаси-глагол,
// назвавший машинку, тоже явный: он материализуется в ту же колонку). Резолв через
// machine_profile_key сюда не доходит и правилом 4 не засчитывается — ровно та же граница, что у
// machineNotApplicable волны 0324: иначе законность поля зависела бы от парка карточки, который
// можно вычистить, не открыв ни одного шага.
func parseOperationWork(o *pb_common.TechCardOperation, opType entity.TechCardOperationType,
	machineType sql.NullString, step string,
) (sql.NullString, error) {
	token := strings.TrimSpace(o.GetWork())
	if token == "" {
		// «Вид не назначен». Ни одного правила, ни одного обращения к каталогу: это состояние
		// каждой существующей строки обеих баз, и оно обязано стоить ноль.
		return sql.NullString{}, nil
	}

	catalog := entity.OperationWorkCatalogSnapshot()
	if catalog == nil {
		return sql.NullString{}, entity.NewFieldViolation(step+".work", "catalog_unavailable", token,
			"this server has not loaded "+workCatalogSource+", so it cannot tell one work from another — save the step without a work, or report this: the catalog is read once at startup")
	}

	work, ok := catalog.Lookup(token)
	if !ok {
		return sql.NullString{}, entity.NewFieldViolation(step+".work", "unknown_work", token,
			"no such work in "+workCatalogSource+" — pick one from the list; the catalog grows by migration, so a work this admin panel knows but this server does not means the server is behind")
	}

	// Правило 3. Глагол — ЧАСТЬ ИДЕНТИЧНОСТИ работы (он заморожен хешем в guard-тесте сида), и
	// поэтому сравнивается строкой с токеном глагола шага, а не «приводится» к нему.
	if string(opType) != work.Verb {
		return sql.NullString{}, entity.NewFieldViolation(step+".work", "work_verb_mismatch", string(opType),
			fmt.Sprintf("the work %q is a %q step, and this step is %q — pick the work that matches the step, or change the step type",
				token, work.Verb, string(opType)))
	}

	// Правило 4. ТОЛЬКО при ask — см. довод в шапке.
	if work.MachineMode == entity.OperationWorkMachineModeAsk && !work.AllowsMachine(machineType.String) {
		got := machineType.String
		if !machineType.Valid || got == "" {
			got = "(none named on the step)"
		}
		return sql.NullString{}, entity.NewFieldViolation(step+".work", "work_machine_mismatch", got,
			fmt.Sprintf("the work %q runs on %s, and this step names %s — pick a machine from that list, or pick a work that runs on this one",
				token, strings.Join(work.Machines, " / "), got))
	}

	return sql.NullString{String: token, Valid: true}, nil
}
