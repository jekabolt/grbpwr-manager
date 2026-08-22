package admin

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ТАБЛИЦА ИСТИННОСТИ ЩИТА ОСИ «РАБОТА» (0330).
//
// САМЫЕ ВАЖНЫЕ ДВЕ КЛЕТКИ, И ОНИ ПРОТИВОПОЛОЖНЫ ДРУГ ДРУГУ:
//
//   - «карточка размечена, бандл НЕ осведомлён» — то самое тихое стирание отставшей вкладкой, ради
//     которого щит написан. ОТКАЗ.
//   - «карточка размечена, бандл ОСВЕДОМЛЁН, работу прислал пустой» — человеческий жест «снять
//     работу». ПРОПУСК. Именно ради этой клетки флаг сделан ЯВНЫМ: presence-детекция обе клетки
//     видит одинаково (пустая строка неотличима от отсутствующей), и одну из них пришлось бы
//     запретить — запрещённым оказался бы жест, то есть работа стала бы неснимаемой.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ЭТОТ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены):
//
//  1. Из operationWorkStoredGate убран ранний выход по флагу (`if pb.GetOperationWorkAware()`), то
//     есть щит вернулся к presence-детекции: осведомлённая запись стала отличаться от неосведомлённой
//     только тем, что несёт work. TestOperationWorkAwareClearIsAGesture ПОКРАСНЕЛ — жест «снять
//     работу» стал отказом. Вернул — зелёный.
//  2. В techCardOperationsQuery (internal/store/techcard/production.go) убрана колонка `o.work`:
//     покраснели TestOperationColumnsAllSelected и TestOperationWorkDigestSymmetry в internal/dto.
//     Вернул — зелёные.
//  3. Хвост дайджеста "work" переставлен ПЕРЕД "fastening": покраснели
//     TestWorkTailStandsTwelfthAfterFastening, TestWorkTailSitsRightAfterFasteningInTheTuple и
//     TestTechCardConstructionWorkDigestHexFrozen. ⚠️ С ПЕРВОГО РАЗА hex НЕ ПОКРАСНЕЛ: карточка
//     эталона состояла из одного шага с единственным хвостом, а у единственного хвоста нет соседа,
//     относительно которого он мог бы переставиться. Эталон усилен вторым шагом (fastening + work,
//     см. opWorkNextToFastening) — после чего мутация покраснела. Слепое пятно нашла мутация, а не
//     ревью, и это и есть довод за требование обеих половин гейта.
//  4. Хвост "work" сделан БЕЗУСЛОВНЫМ (эмитится и при NULL): покраснел
//     TestTechCardConstructionDigestHexFrozen — то есть отпечаток ВСЕХ существующих строк, включая
//     126 прод-строк, — плюс кортежный голден и восемь тестов формы. Вернул — зелёные.

func workPayload(aware bool, works ...string) *pb_common.TechCardInsert {
	ops := make([]*pb_common.TechCardOperation, 0, len(works))
	for i, w := range works {
		ops = append(ops, &pb_common.TechCardOperation{
			OperationNumber: int32((i + 1) * 10),
			OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:            gateZone(),
			MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Work:            w,
		})
	}
	return &pb_common.TechCardInsert{
		StyleNumber:        "WORK-GATE",
		Name:               "gate",
		OperationWorkAware: aware,
		Operations:         ops,
	}
}

// storedWorkCard — карточка, у которой шаг НАЗЫВАЕТ работу. Сегодня таких нет ни одной: колонка
// рождается NULL на всех 126 строках прода, и щит молчит до первой ручной разметки владельцем.
func storedWorkCard(token string) *entity.TechCard {
	return storedCard(entity.TechCardOperation{
		OperationType: entity.OpTypeMachine,
		MachineType:   sql.NullString{String: "lockstitch", Valid: true},
		Work:          sql.NullString{String: token, Valid: true},
	})
}

func wantFailedPrecondition(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: щит пропустил запись — разметка была бы стёрта молча, полной заменой", what)
	}
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("%s: код отказа %s, ожидался FailedPrecondition — клиент различает «обнови вкладку» "+
			"и «поправь поле» именно по коду", what, got)
	}
}

// --- ПРАВИЛО 2: сохранённая карточка размечена, а запись осведомлённости не объявляет -------------

func TestOperationWorkStoredGateRefusesUnawareOverStoredWork(t *testing.T) {
	// Payload НЕВИННЫЙ: отставший бандл поля не знает и не шлёт. Отличить его от осведомлённого,
	// который работу снял, можно ТОЛЬКО по флагу — в этом весь смысл щита.
	err := operationWorkStoredGate(workPayload(false), storedWorkCard("topstitch"))
	wantFailedPrecondition(t, err, "хранимая работа против неосведомлённой записи")
}

// TestOperationWorkAwareClearIsAGesture — ПРОТИВОПОЛОЖНАЯ КЛЕТКА, и она важнее первой.
//
// Осведомлённая запись с пустой работой против размеченной карточки — это «владелец снял работу».
// Пропуск обязателен: бекстоп здесь объявил бы рядовую правку аварией и сделал бы работу
// НЕСНИМАЕМОЙ. Именно этот тест краснеет, если щит вернуть к presence-детекции.
func TestOperationWorkAwareClearIsAGesture(t *testing.T) {
	if err := operationWorkStoredGate(workPayload(true, ""), storedWorkCard("topstitch")); err != nil {
		t.Fatalf("осведомлённая запись со снятой работой отвергнута (%v) — жест «снять работу» стал "+
			"невыполнимым, а это и есть классический дефект такой защиты", err)
	}
}

func TestOperationWorkStoredGateSilentOnUnmarkedCard(t *testing.T) {
	// СЕГОДНЯШНЕЕ СОСТОЯНИЕ ОБЕИХ БАЗ: ни одной названной работы. Щит обязан молчать, иначе выкатка
	// заперла бы админку целиком.
	plain := storedCard(entity.TechCardOperation{
		OperationType: entity.OpTypeMachine,
		MachineType:   sql.NullString{String: "lockstitch", Valid: true},
	})
	if err := operationWorkStoredGate(workPayload(false), plain); err != nil {
		t.Fatalf("щит отказал неразмеченной карточке (%v) — это все 126 строк прода", err)
	}
	// И пустая строка в хранимой колонке — не разметка: NULL и "" читаются одинаково.
	blank := storedCard(entity.TechCardOperation{Work: sql.NullString{String: "  ", Valid: true}})
	if err := operationWorkStoredGate(workPayload(false), blank); err != nil {
		t.Fatalf("щит счёл разметкой пустую строку в колонке: %v", err)
	}
}

// --- ПРАВИЛО 1: payload ВЕЗЁТ работу, не объявляя осведомлённости --------------------------------

func TestOperationWorkWireGateRefusesUnawareEcho(t *testing.T) {
	wantFailedPrecondition(t, operationWorkWireGate(workPayload(false, "topstitch")),
		"неосведомлённый payload с непустой работой")

	if err := operationWorkWireGate(workPayload(true, "topstitch")); err != nil {
		t.Fatalf("осведомлённый payload с работой отвергнут: %v", err)
	}
	if err := operationWorkWireGate(workPayload(false, "", "   ")); err != nil {
		t.Fatalf("неосведомлённый payload БЕЗ работ отвергнут (%v) — так шлёт сегодняшний бандл "+
			"каждую карточку обеих баз", err)
	}
}

// --- СНЯТАЯ РАБОТА: не предлагать, но и не отнимать ----------------------------------------------

func withGateWorkCatalog(t *testing.T, retired ...string) {
	t.Helper()
	isRetired := map[string]bool{}
	for _, r := range retired {
		isRetired[r] = true
	}
	works := []entity.OperationWork{
		{Token: "topstitch", Verb: "machine", Label: "Topstitch", MachineMode: entity.OperationWorkMachineModeAsk},
		{Token: "join_lockstitch", Verb: "machine", Label: "Join — lockstitch", MachineMode: entity.OperationWorkMachineModeFixed},
	}
	for i := range works {
		if isRetired[works[i].Token] {
			works[i].RetiredAt = sql.NullTime{Time: time.Now().UTC(), Valid: true}
		}
	}
	entity.SetOperationWorkCatalog(works)
	t.Cleanup(func() { entity.SetOperationWorkCatalog(nil) })
}

func TestOperationWorkRetiredGate(t *testing.T) {
	withGateWorkCatalog(t, "topstitch")

	// НОВАЯ разметка снятой работой — отказ, и отказ ИМЕНОВАННЫЙ: человек должен увидеть его у
	// строки, а не получить «что-то пошло не так».
	err := operationWorkRetiredGate(workPayload(true, "topstitch"), nil)
	if err == nil {
		t.Fatal("снятая работа принята на создаваемой карточке — снятый пункт вернулся в оборот")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("код отказа %s, ожидался InvalidArgument: это поправимая ошибка поля, а не "+
			"устаревшая вкладка", got)
	}

	// А ТАМ, ГДЕ КАРТОЧКА УЖЕ НЕСЁТ ЭТОТ ТОКЕН, запись обязана пройти: «сломаться можно, исчезнуть
	// нельзя» — иначе размеченная когда-то строка стала бы несохраняемой навсегда, и владелец
	// потерял бы право редактировать карточку целиком.
	if err := operationWorkRetiredGate(workPayload(true, "topstitch"), storedWorkCard("topstitch")); err != nil {
		t.Fatalf("карточка, уже несущая снятую работу, стала несохраняемой: %v", err)
	}

	// Живая работа — молча.
	if err := operationWorkRetiredGate(workPayload(true, "join_lockstitch"), nil); err != nil {
		t.Fatalf("живая работа отвергнута правилом снятия: %v", err)
	}
}

// TestOperationWorkRetiredGateSilentWithoutCatalog — незагруженный каталог не превращает правило в
// пропуск с последствиями: непустую работу при пустом снимке уже отверг разбор (parseOperationWork,
// `catalog_unavailable`), и до щита такая запись не доезжает.
func TestOperationWorkRetiredGateSilentWithoutCatalog(t *testing.T) {
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога опубликован — предыдущий тест его не вернул")
	}
	if err := operationWorkRetiredGate(workPayload(true, "topstitch"), nil); err != nil {
		t.Fatalf("правило снятия заговорило без каталога: %v", err)
	}
}
