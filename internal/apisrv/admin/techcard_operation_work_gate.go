package admin

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- ЩИТ ОСИ «РАБОТА» (0330) --------------------------------------------------------------------
//
// ПЯТЫЙ ЩИТ ТОЙ ЖЕ ПОРОДЫ, что machine_fields_aware (0306), медиа, узлы сборки (0307) и виды
// операций (0324): устройство зеркалит их дословно, включая разделение на две функции. Правило 1
// читается С ПРОВОДА и потому срабатывает до конверсии; правило 2 требует ЗАГРУЖЕННОЙ карточки и
// раньше её появления сказать нечего. Слить их значило бы опустить и первое правило до поздней
// точки, где отставшая вкладка прочла бы отказ о поле, которого не рисует.
//
// ⚠️ ФЛАГ ЯВНЫЙ, А НЕ ВЫВЕДЕННЫЙ ИЗ ПРИСУТСТВИЯ ПОЛЯ, И ЭТО РЕШЕНИЕ, КОТОРОЕ НЕЛЬЗЯ ПЕРЕИГРАТЬ.
// Четвёртый щит различает бандлы presence-детекцией (payloadSpeaksOperationKinds: присланный блок и
// есть факт), и здесь этот приём непригоден ПРИНЦИПИАЛЬНО. `work` — СТРОКА, а у строки нет разницы
// между «поля не было» и «поле пришло пустым»: оба состояния приезжают в сервер одинаковой пустотой.
// Значит «владелец СНЯЛ работу с единственной размеченной строки» было бы неотличимо от «сохраняет
// старый бандл», и одно из двух пришлось бы запретить — а запрещённым оказался бы человеческий жест,
// то есть работа стала бы НЕСНИМАЕМОЙ. Явный operation_work_aware разводит их навсегда: снятие
// работы это aware = true и пустая строка.
//
// ПОЧЕМУ ОТКАЗ, А НЕ СЛИЯНИЕ. Операции пишутся ПОЛНОЙ ЗАМЕНОЙ и у строки шага НЕТ СТАБИЛЬНОГО
// КЛЮЧА — «донести хранимое», как доносится разметка детали, физически невозможно: сервер не знает,
// какая присланная строка соответствует какой сохранённой. Честны ровно два исхода: значение
// доезжает целиком либо сохранение отказывает целиком. ПОРЯДКОМ ВЫКАТКИ ЭТО НЕ ЗАКРЫВАЕТСЯ —
// открытая вкладка ест данные и после деплоя клиента.
//
// ЩИТ НЕ ФИЛЬТРУЕТ ПОЛЯ: разбор `work` в dto идёт ВСЕГДА, независимо от флага. «Игнорировать при
// aware = false» выглядит защитой, а на деле открывает дыру — CloneStyleForSeason строит payload
// сам, и клон размеченной карточки вернулся бы пустым без единой ошибки. Поэтому серверные пути
// ставят флаг ЯВНО (style.go, betaseed), а не обходят щит молчанием.
//
// ПАРНОГО `*_cleared` У НЕГО НЕТ — как у 110 и 115. «Работа снята» это рядовая правка одной строки,
// а не жест «снять разметку целиком»; бекстоп «осведомлённая пустота против размеченной карточки»
// объявил бы такую правку аварией и сделал бы работу неснимаемой — тем самым дефектом, ради обхода
// которого флаг и заведён явным.
//
//	stored нет | work нет | aware нет  → сохранить (сегодняшний путь: все 126 строк прода)
//	stored нет | work ЕСТЬ| aware нет  → отказ: старый бандл такого поля не знает, значит это эхо
//	stored нет | любой    | aware есть → сохранить
//	stored ЕСТЬ| —        | aware нет  → FailedPrecondition: устаревшая вкладка
//	stored ЕСТЬ| work ЕСТЬ| aware есть → сохранить (обычное редактирование)
//	stored ЕСТЬ| work пуст| aware есть → СОХРАНИТЬ И СНЯТЬ: жест «работа не назначена», см. выше

const outdatedOperationWorkClientFix = "this version of the admin panel cannot name the work on a step, and its save replaces the whole step list — update the admin panel (hard-refresh) and try again"

func outdatedOperationWorkClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedOperationWorkClientFix)
}

// operationWorkWireGate — правило 1, читается с провода до конверсии.
func operationWorkWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationWorkAware() {
		return nil
	}
	if payloadCarriesOperationWork(pb) {
		// Наблюдаемость: без счётчика отказов никто не узнает, бьётся ли отставший бандл о щит на
		// проде — а это единственный признак, что клиент где-то не обновился.
		slog.Default().Warn("operation work gate refused an unaware payload that carries step works",
			slog.String("gate", "wire"), slog.String("cell", "stored:any/payload:work/aware:no"))
		return outdatedOperationWorkClient("the payload names the work on a step without declaring support for it")
	}
	return nil
}

// operationWorkStoredGate — правило 2, требует загруженной карточки. Именно оно и срабатывает на
// практике: payload отставшей вкладки поля не несёт вовсе и выглядит невинно, и только хранилище
// знает, что полная замена собирается стереть разметку.
func operationWorkStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationWorkAware() {
		return nil
	}
	if storedNamesOperationWork(stored) {
		slog.Default().Warn("operation work gate refused an outdated bundle against a card whose steps name their work",
			slog.String("gate", "stored"), slog.String("cell", "stored:work/aware:no"),
			slog.Int("tech_card_id", storedCardID(stored)))
		return outdatedOperationWorkClient("this tech card names the work on its steps")
	}
	return nil
}

// payloadCarriesOperationWork — предикат правила 1.
//
// ПО НЕПУСТОМУ ЗНАЧЕНИЮ, А НЕ ПО «ПОЛЕ ПРИСУТСТВУЕТ»: у строки в proto3 присутствия нет вовсе, и
// спрашивать его не у чего. Поэтому предикат отвечает на единственный доступный вопрос — везёт ли
// payload хоть одну НЕПУСТУЮ работу. Ровно это и есть эхо: старый бандл токена не знает и написать
// его не мог.
func payloadCarriesOperationWork(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, o := range pb.GetOperations() {
		if o == nil {
			continue
		}
		if strings.TrimSpace(o.GetWork()) != "" {
			return true
		}
	}
	return false
}

// storedNamesOperationWork — предикат правила 2: несёт ли СОХРАНЁННАЯ карточка хоть одну работу.
//
// Сегодня — не несёт ни одна: колонка рождается NULL на каждой из 126 строк прода, и щит молчит на
// каждой карточке обеих баз. Он оживает ровно в тот день, когда владелец разметит первую строку.
func storedNamesOperationWork(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if w := stored.Operations[i].Work; w.Valid && strings.TrimSpace(w.String) != "" {
			return true
		}
	}
	return false
}

// --- СНЯТАЯ РАБОТА: НЕ ПРЕДЛАГАТЬ, НО И НЕ ОТНИМАТЬ -----------------------------------------------
//
// ПОЧЕМУ ЭТО ПРАВИЛО ЖИВЁТ ЗДЕСЬ, А НЕ В dto. Три правила когерентности (незнакомый токен, глагол,
// машинка) смотрят только на payload и каталог — их место в разборе. Это четвёртое требует
// СОХРАНЁННОЙ КАРТОЧКИ: снятая работа обязана отказывать НОВОЙ разметке и обязана ПРИНИМАТЬСЯ там,
// где строка уже её несёт. Разбор payload'а карточки не видит, и опустить правило до него значило
// бы либо запереть сохранение размеченной когда-то строки навсегда («сломаться можно, исчезнуть
// нельзя» — а тут исчезло бы право сохранить), либо не иметь правила вовсе.
//
// СРАВНЕНИЕ ПО КАРТОЧКЕ, А НЕ ПО СТРОКЕ, И ЭТО ВЫНУЖДЕНО ТЕМ ЖЕ, ЧЕМ ОТКАЗ ВМЕСТО СЛИЯНИЯ: у шага
// нет стабильного ключа, полная замена не даёт сопоставить присланную строку с сохранённой. Значит
// вопрос звучит «несла ли эта карточка такую снятую работу до записи», и ответ «да» пропускает
// запись. Цена известна и принята: технолог может перенести снятую работу с одной строки карточки
// на другую. Это правка ЧЕЛОВЕКА внутри уже размеченной карточки, а не появление снятого пункта в
// пикере, и она безвредна.
//
// КАТАЛОГ НЕ ЗАГРУЖЕН — ПРАВИЛО МОЛЧИТ, И ЭТО НЕ ДЫРА. Непустую работу при пустом снимке уже
// отверг разбор (parseOperationWork, `catalog_unavailable`), то есть до сюда такая запись не
// доезжает вовсе.
func operationWorkRetiredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	catalog := entity.OperationWorkCatalogSnapshot()
	if catalog == nil || !payloadCarriesOperationWork(pb) {
		return nil
	}
	storedTokens := make(map[string]bool)
	if stored != nil {
		for i := range stored.Operations {
			if w := stored.Operations[i].Work; w.Valid {
				storedTokens[strings.TrimSpace(w.String)] = true
			}
		}
	}
	for i, o := range pb.GetOperations() {
		if o == nil {
			continue
		}
		token := strings.TrimSpace(o.GetWork())
		if token == "" || storedTokens[token] {
			continue
		}
		work, ok := catalog.Lookup(token)
		if !ok || !work.Retired() {
			// Незнакомый токен — не забота этого правила: его уже назвал разбор. Повторять здесь
			// значило бы завести второй ответ на один вопрос.
			continue
		}
		return apierr.Invalid(entity.NewFieldViolation(
			fmt.Sprintf("operations[%d].work", i), "work_retired", token,
			fmt.Sprintf("the work %q has been withdrawn and is no longer offered — pick a current work; steps that already carry it keep it and still save",
				work.Label)))
	}
	return nil
}
