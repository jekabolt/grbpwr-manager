package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Таблица истинности щита количеств на связях шага (0334).
//
// САМЫЕ ВАЖНЫЕ ДВЕ КЛЕТКИ, и они противоположны друг другу:
//
//   - «карточка несёт количества, бандл НЕ осведомлён» — то самое тихое стирание отставшей
//     вкладкой, ради которого щит написан. ОТКАЗ, и данные не тронуты.
//   - «карточка несёт количества, бандл ОСВЕДОМЛЁН, количеств не прислал» — рядовая правка:
//     технолог убрал число. ПРОПУСК. Клетка выписана отдельным тестом, потому что «защита,
//     которая делает поле нестираемым» — классический дефект этой конструкции, и здесь он закрыт
//     решением не заводить парный `*_cleared`.

func bqPayload(aware bool, ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber: "BQ-GATE",
		Name:        "gate",
		BomQtyAware: aware,
		Operations:  ops,
	}
}

// bqOpLinkedNoQty — шаг, который шлёт и сегодняшний бандл, и вчерашний: материал привязан, числа
// нет. Это состояние ВСЕХ живых строк (3 на проде, 14 на бете), и отличить два бандла на нём можно
// ТОЛЬКО по флагу.
func bqOpLinkedNoQty() *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{
		OperationNumber: 10,
		OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:            gateZone(),
		BomLineKeys:     []string{"btn-1"},
	}
}

func bqOpWithQty() *pb_common.TechCardOperation {
	op := bqOpLinkedNoQty()
	op.BomQuantities = []*pb_common.TechCardOperationBomQty{
		{LineKey: "btn-1", QtyPerGarment: &pb_decimal.Decimal{Value: "6"}},
	}
	return op
}

// bqStoredWithQty — сохранённая карточка, у которой на связи ЕСТЬ количество.
func bqStoredWithQty() *entity.TechCard {
	return storedCard(entity.TechCardOperation{
		OperationType: entity.OpTypeMachine,
		BomLineKeys:   []string{"btn-1"},
		BomQuantities: []entity.OperationBomQty{{
			LineKey: "btn-1", QtyPerGarment: decimal.RequireFromString("6"),
		}},
	})
}

// bqStoredLinkedNoQty — сохранённая карточка со связью, но БЕЗ количества: сегодняшнее состояние
// каждой живой строки. На ней щит обязан МОЛЧАТЬ, иначе он заблокирует всю сегодняшнюю админку.
func bqStoredLinkedNoQty() *entity.TechCard {
	return storedCard(entity.TechCardOperation{
		OperationType: entity.OpTypeMachine,
		BomLineKeys:   []string{"btn-1"},
	})
}

func bqRequireRefusal(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: щит пропустил запись — количества были бы стёрты молча", what)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("%s: отказ пришёл кодом %v, ожидался FailedPrecondition — клиент не поймёт, что "+
			"надо обновить вкладку: %v", what, st.Code(), err)
	}
	for _, want := range []string{"update the admin panel", "per-step material quantities"} {
		if !strings.Contains(st.Message(), want) {
			t.Fatalf("%s: отказ не содержит %q — оператор не узнает ни что потеряется, ни что "+
				"делать: %q", what, want, st.Message())
		}
	}
}

// TestBomQtyGateTruthTable проходит таблицу истинности целиком — шесть клеток, выписанных в шапке
// techcard_bom_qty_gate.go. По одной клетке за строку: клетка, забытая в таблице, и есть тот
// сценарий, в котором щит промолчит в проде.
func TestBomQtyGateTruthTable(t *testing.T) {
	cases := []struct {
		name       string
		payload    *pb_common.TechCardInsert
		stored     *entity.TechCard
		wantRefuse bool
	}{
		{
			name:    "stored нет | payload нет | aware нет → сохранить (сегодняшний путь)",
			payload: bqPayload(false, bqOpLinkedNoQty()),
			stored:  nil,
		},
		{
			name:       "stored нет | эхо количеств | aware нет → отказ: бандл эхоит, чего не знает",
			payload:    bqPayload(false, bqOpWithQty()),
			stored:     nil,
			wantRefuse: true,
		},
		{
			name:    "stored нет | количества | aware есть → сохранить",
			payload: bqPayload(true, bqOpWithQty()),
			stored:  nil,
		},
		{
			name:       "stored ЕСТЬ количества | payload пуст | aware нет → отказ: устаревшая вкладка",
			payload:    bqPayload(false, bqOpLinkedNoQty()),
			stored:     bqStoredWithQty(),
			wantRefuse: true,
		},
		{
			name:    "stored СВЯЗЬ БЕЗ числа | payload пуст | aware нет → сохранить (стирать нечего)",
			payload: bqPayload(false, bqOpLinkedNoQty()),
			stored:  bqStoredLinkedNoQty(),
		},
		{
			name:    "stored ЕСТЬ количества | количества | aware есть → сохранить",
			payload: bqPayload(true, bqOpWithQty()),
			stored:  bqStoredWithQty(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := bomQtyWireGate(c.payload)
			if err == nil {
				err = bomQtyStoredGate(c.payload, c.stored)
			}
			if c.wantRefuse {
				bqRequireRefusal(t, err, c.name)
				return
			}
			if err != nil {
				t.Fatalf("%s: щит отказал там, где обязан молчать: %v", c.name, err)
			}
		})
	}
}

// TestBomQtyUnawareWriteAgainstStoredQuantitiesIsRefused — ТЕСТ 5, названный отдельно, потому что
// это единственная клетка, ради которой щит существует.
//
// ДАННЫЕ НЕ ТРОНУТЫ — и это проверяется здесь же: гейт вызывается ДО конверсии и до любого похода в
// стор, поэтому отказ означает, что сохранение не началось вовсе. Сохранённая карточка после
// отказа обязана нести ровно то же количество.
func TestBomQtyUnawareWriteAgainstStoredQuantitiesIsRefused(t *testing.T) {
	stored := bqStoredWithQty()
	before := stored.Operations[0].BomQuantities[0].QtyPerGarment.String()

	err := bomQtyStoredGate(bqPayload(false, bqOpLinkedNoQty()), stored)
	bqRequireRefusal(t, err, "устаревшая вкладка против карточки с количествами")

	after := stored.Operations[0].BomQuantities[0].QtyPerGarment.String()
	if before != after {
		t.Fatalf("щит тронул сохранённые данные: было %q, стало %q — он обязан только отказывать",
			before, after)
	}
}

// TestBomQtyUnawareWriteAgainstCardWithoutQuantitiesPasses — ТЕСТ 6.
//
// Предикат считает КОЛИЧЕСТВА, а не связи. Проверка вида «у шага есть привязанный материал»
// объявила бы фактом волны каждую сегодняшнюю карточку и заблокировала бы всю админку разом —
// ровно тот отказ, от которого щит обязан воздержаться.
func TestBomQtyUnawareWriteAgainstCardWithoutQuantitiesPasses(t *testing.T) {
	if err := bomQtyStoredGate(bqPayload(false, bqOpLinkedNoQty()), bqStoredLinkedNoQty()); err != nil {
		t.Fatalf("щит отказал карточке со связью, но БЕЗ количеств: %v — так он блокирует каждую "+
			"живую карточку с привязанным материалом", err)
	}
	if err := bomQtyStoredGate(bqPayload(false), storedCard()); err != nil {
		t.Fatalf("щит отказал пустой карточке: %v", err)
	}
}

// TestBomQtyAwareEmptyWriteStillClearsTheQuantities — ТЕСТ 7 и САМАЯ ВАЖНАЯ КЛЕТКА ПОСЛЕ ОТКАЗА.
//
// Осведомлённая запись БЕЗ количеств против карточки С количествами обязана ПРОЙТИ: «количество
// стёрли» — рядовая правка (число оказалось неверным, материал со связи ушёл), а не авария.
// Парного `*_cleared` у щита нет ИМЕННО поэтому: бекстоп объявил бы такую правку ошибкой и сделал
// бы количество НЕСТИРАЕМЫМ — классический дефект такой защиты.
func TestBomQtyAwareEmptyWriteStillClearsTheQuantities(t *testing.T) {
	aware := bqPayload(true, bqOpLinkedNoQty())
	if err := bomQtyWireGate(aware); err != nil {
		t.Fatalf("проводной щит отказал осведомлённой записи без количеств: %v", err)
	}
	if err := bomQtyStoredGate(aware, bqStoredWithQty()); err != nil {
		t.Fatalf("щит запретил СТЕРЕТЬ количество осведомлённой записью: %v — количество стало "+
			"нестираемым, а это рядовая правка", err)
	}
}

// TestBomQtyGateReadsThePayloadNotTheStoredCard — правило 1 обязано работать БЕЗ загруженной
// карточки: на создании карточки ещё нет, а эхо старого бандла услышать надо.
func TestBomQtyGateReadsThePayloadNotTheStoredCard(t *testing.T) {
	if err := bomQtyWireGate(bqPayload(false, bqOpWithQty())); err == nil {
		t.Fatal("проводной щит пропустил неосведомлённый payload, который ВЕЗЁТ количества: " +
			"старый бандл такого поля не знает, значит это эхо")
	}
	if err := bomQtyWireGate(bqPayload(true, bqOpWithQty())); err != nil {
		t.Fatalf("проводной щит отказал осведомлённому payload'у: %v", err)
	}
	if err := bomQtyWireGate(nil); err != nil {
		t.Fatalf("проводной щит упал на nil-payload: %v", err)
	}
}

// TestBomQtyGatesAreActuallyCalled — ЩИТ, КОТОРОГО НИКТО НЕ ЗОВЁТ, ЗЕЛЕНЕЕТ ВЕЧНО.
//
// Все тесты выше зовут bomQtyWireGate/bomQtyStoredGate НАПРЯМУЮ, поэтому ни один из них не
// заметит, если вызов исчезнет из CreateTechCard/UpdateTechCard: функции останутся правильными, а
// защиты не будет. Это выяснено мутацией (вызовы убирались — все тесты остались зелёными), и
// закрывается единственным способом, не требующим поднимать сервер с базой: чтением исходника.
//
// ТРИ ВЫЗОВА, А НЕ «ХОТЬ ОДИН»: проводной на создании, проводной на обновлении и сторовый на
// обновлении. Сторового на создании нет намеренно — карточки ещё нет, стирать нечего.
func TestBomQtyGatesAreActuallyCalled(t *testing.T) {
	body, err := os.ReadFile("techcard.go")
	if err != nil {
		t.Fatalf("не читается techcard.go: %v — тест обязан упасть, а не «не найти вызовов»", err)
	}
	src := string(body)
	// Положительный контроль: если разбор смотрит не туда, соседний щит тоже «не найдётся», и
	// пустой результат перестаёт читаться как «вызова нет».
	if strings.Count(src, "operationWorkWireGate(req.TechCard)") != 2 {
		t.Fatalf("в techcard.go не нашлось двух вызовов operationWorkWireGate — извлекатель смотрит " +
			"не туда, а сломанный извлекатель зеленит проверку ниже на любой ошибке")
	}
	if got := strings.Count(src, "bomQtyWireGate(req.TechCard)"); got != 2 {
		t.Errorf("bomQtyWireGate зовётся %d раз(а), ожидалось 2 (создание и обновление) — "+
			"неосведомлённое эхо количеств пройдёт насквозь", got)
	}
	if got := strings.Count(src, "bomQtyStoredGate(req.TechCard, stored)"); got != 1 {
		t.Errorf("bomQtyStoredGate зовётся %d раз(а), ожидался 1 (обновление) — отставшая вкладка "+
			"сотрёт количества молча", got)
	}
}
