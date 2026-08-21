package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ------------------------------------------------------------------------------------------
// Ф2 плана PHASE-STOP-LOSS: транспорт admin-гейтвея обязан быть ГРОМКИМ.
//
// Что здесь доказывается и почему именно так:
//   * ЦИТАТА — незнакомое ПОЛЕ и незнакомое ИМЯ ЧЛЕНА ENUM в теле POST /api/admin/tech-card/update
//     дают 400, тело называет виновника, а RPC не вызывается вовсе.
//   * МУТАЦИЯ — TestAdminGatewaySilentDropIsOneBitAway поднимает ТОТ ЖЕ мукс и ТЕ ЖЕ тела, меняя
//     ровно один бит опций (DiscardUnknown: true, дефолт grpc-gateway v2.21.0), и показывает, что
//     обе пробы становятся зелёными 200 с ПОТЕРЯННЫМ значением. Без этой половины «400» мог бы
//     означать что угодно — не тот маршрут, кривой JSON, отсутствующий хендлер; с ней доказано,
//     что тела валидны, маршрут тот, и красноту цитаты держит именно бит.
//   * РЕГРЕСС — тело в форме, которую admin-клиент получает с провода (EmitUnpopulated: явные
//     null у незаполненных сообщений), проходит как раньше; и ответ гейтвея эти null всё ещё
//     печатает — второй бит контракта не тронут.
//
// Мукс тесты берут из newAdminServeMux() — из прода, а не копией опций: удаление
// WithMarshalerOption из http.go красит этот файл целиком.
// ------------------------------------------------------------------------------------------

// techCardStubServer ловит вызов RPC. Ключевой факт — СЧЁТЧИК: разбор тела происходит ДО RPC, и
// «сервер не звали» отличает отказ транспорта от отказа бизнес-правила.
type techCardStubServer struct {
	pb_admin.UnimplementedAdminServiceServer
	updateCalls int
	lastUpdate  *pb_admin.UpdateTechCardRequest
}

func (s *techCardStubServer) UpdateTechCard(_ context.Context, req *pb_admin.UpdateTechCardRequest) (*pb_admin.UpdateTechCardResponse, error) {
	s.updateCalls++
	s.lastUpdate = req
	return &pb_admin.UpdateTechCardResponse{}, nil
}

func (s *techCardStubServer) GetTechCard(_ context.Context, _ *pb_admin.GetTechCardRequest) (*pb_admin.GetTechCardResponse, error) {
	// Намеренно ПУСТОЙ ответ: с EmitUnpopulated он печатается как {"techCard":null,…},
	// без него — как {}. Это и есть проба второго бита.
	return &pb_admin.GetTechCardResponse{}, nil
}

// serveAdmin поднимает переданный мукс с застабленным AdminService.
func serveAdmin(t *testing.T, mux *runtime.ServeMux, stub *techCardStubServer) *httptest.Server {
	t.Helper()
	if err := pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub); err != nil {
		t.Fatalf("register admin handler: %v", err)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func postTechCardUpdate(t *testing.T, ts *httptest.Server, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/admin/tech-card/update", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /tech-card/update: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(rb)
}

// Тела проб. Все три отличаются от валидного РОВНО одним местом, чтобы разница в ответе
// объяснялась только им.
const (
	// Форма, в которой admin-клиент возвращает прочитанное: незаполненные вложенные сообщения —
	// явным null (EmitUnpopulated), незаполненные enum'ы — своим нулевым именем, повторяющиеся —
	// пустым списком.
	payloadRoundTrip = `{"id":42,"techCard":{"operations":[{"operationNumber":10,` +
		`"operationType":"TECH_CARD_OPERATION_TYPE_PRESS","zone":"TECH_CARD_GARMENT_ZONE_OTHER",` +
		`"pieceLineKeys":[],"bomLineKeys":[],"smv":null,"topstitch":null,"seamAllowanceMm":null,` +
		`"attachmentSizeMm":null,"machineType":"TECH_CARD_MACHINE_TYPE_UNKNOWN",` +
		`"press":{"action":"TECH_CARD_PRESS_ACTION_STEAM","toward":"TECH_CARD_PRESS_TOWARD_UNKNOWN"},` +
		`"stitching":null,"fastening":null,"media":[]}]},"expectedLockVersion":3}`

	// (а) Незнакомое ПОЛЕ. Имя выбрано как в инциденте: клиент, который шлёт под-глагол ВТО полем
	// шага, а не блоком press.
	payloadUnknownField = `{"id":42,"techCard":{"operations":[{"operationNumber":10,` +
		`"operationType":"TECH_CARD_OPERATION_TYPE_PRESS","zone":"TECH_CARD_GARMENT_ZONE_OTHER",` +
		`"pressAction2":"TECH_CARD_PRESS_ACTION_STEAM"}]},"expectedLockVersion":3}`

	// (б) Незнакомое ИМЯ ЧЛЕНА enum — член, которого не было никогда.
	payloadUnknownEnumMember = `{"id":42,"techCard":{"operations":[{"operationNumber":10,` +
		`"operationType":"TECH_CARD_OPERATION_TYPE_PRESS","zone":"TECH_CARD_GARMENT_ZONE_OTHER",` +
		`"press":{"action":"TECH_CARD_PRESS_ACTION_NO_SUCH"}}]},"expectedLockVersion":3}`

	// (в) Тот же класс, но реальная граница совместимости: член, СНЯТЫЙ волной 0327 и стоящий
	// сегодня в `reserved` (techcard.proto:2053-2054). Бандл старше снятия шлёт именно это.
	payloadRetiredEnumMember = `{"id":42,"techCard":{"operations":[{"operationNumber":10,` +
		`"operationType":"TECH_CARD_OPERATION_TYPE_PRESS","zone":"TECH_CARD_GARMENT_ZONE_OTHER",` +
		`"press":{"action":"TECH_CARD_PRESS_ACTION_OPEN"}}]},"expectedLockVersion":3}`
)

// TestAdminGatewayRejectsUnknownField — цитата (а).
func TestAdminGatewayRejectsUnknownField(t *testing.T) {
	stub := &techCardStubServer{}
	ts := serveAdmin(t, newAdminServeMux(), stub)

	code, body := postTechCardUpdate(t, ts, payloadUnknownField)
	t.Logf("ответ гейтвея на незнакомое поле: %d %s", code, body)

	if code != http.StatusBadRequest {
		t.Fatalf("незнакомое поле дало %d, ожидался 400; тело: %s", code, body)
	}
	if !strings.Contains(body, "pressAction2") {
		t.Fatalf("тело отказа не называет виновника %q: %s", "pressAction2", body)
	}
	if !strings.Contains(body, "unknown field") {
		t.Fatalf("тело отказа не говорит про unknown field (форма нужна клиенту для баннера Ф5): %s", body)
	}
	if stub.updateCalls != 0 {
		t.Fatalf("RPC вызвана %d раз(а) при отказе транспорта — значит тело разобралось", stub.updateCalls)
	}
}

// TestAdminGatewayRejectsUnknownEnumMember — цитата (б) и (в).
func TestAdminGatewayRejectsUnknownEnumMember(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		culprit string
	}{
		{"члена не существовало никогда", payloadUnknownEnumMember, "TECH_CARD_PRESS_ACTION_NO_SUCH"},
		{"член снят волной 0327 и стоит в reserved", payloadRetiredEnumMember, "TECH_CARD_PRESS_ACTION_OPEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &techCardStubServer{}
			ts := serveAdmin(t, newAdminServeMux(), stub)

			code, body := postTechCardUpdate(t, ts, tc.body)
			t.Logf("ответ гейтвея на %s: %d %s", tc.culprit, code, body)

			if code != http.StatusBadRequest {
				t.Fatalf("незнакомый член enum дал %d, ожидался 400; тело: %s", code, body)
			}
			if !strings.Contains(body, tc.culprit) {
				t.Fatalf("тело отказа не называет виновника %q: %s", tc.culprit, body)
			}
			if !strings.Contains(body, "action") {
				t.Fatalf("тело отказа не называет поле: %s", body)
			}
			if stub.updateCalls != 0 {
				t.Fatalf("RPC вызвана %d раз(а) при отказе транспорта", stub.updateCalls)
			}
		})
	}
}

// TestAdminGatewayKeepsRoundTripPayloadWorking — регресс: строгость не задела форму, в которой
// клиент реально возит прочитанное.
func TestAdminGatewayKeepsRoundTripPayloadWorking(t *testing.T) {
	stub := &techCardStubServer{}
	ts := serveAdmin(t, newAdminServeMux(), stub)

	code, body := postTechCardUpdate(t, ts, payloadRoundTrip)
	if code != http.StatusOK {
		t.Fatalf("валидное тело дало %d: %s", code, body)
	}
	if stub.updateCalls != 1 {
		t.Fatalf("RPC вызвана %d раз(а), ожидался 1", stub.updateCalls)
	}
	got := stub.lastUpdate
	if got.GetId() != 42 || got.GetExpectedLockVersion() != 3 {
		t.Fatalf("id/lock доехали как %d/%d, ожидались 42/3", got.GetId(), got.GetExpectedLockVersion())
	}
	ops := got.GetTechCard().GetOperations()
	if len(ops) != 1 {
		t.Fatalf("операций доехало %d, ожидалась 1", len(ops))
	}
	if act := ops[0].GetPress().GetAction(); act != pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_STEAM {
		t.Fatalf("press.action доехал как %v, ожидался STEAM", act)
	}
	if ops[0].GetTopstitch() != nil || ops[0].GetSmv() != nil {
		t.Fatalf("явный null доехал не как «не задано»: topstitch=%v smv=%v", ops[0].GetTopstitch(), ops[0].GetSmv())
	}
}

// TestAdminGatewayStillEmitsUnpopulated — второй бит контракта: незаполненное вложенное сообщение
// уезжает клиенту ЯВНЫМ null (память «null с провода vs zod»). Если бы правка Ф2 задела
// MarshalOptions, тело было бы «{}».
func TestAdminGatewayStillEmitsUnpopulated(t *testing.T) {
	stub := &techCardStubServer{}
	ts := serveAdmin(t, newAdminServeMux(), stub)

	resp, err := http.Get(ts.URL + "/api/admin/tech-card/7")
	if err != nil {
		t.Fatalf("GET /tech-card/7: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(resp.Body)
	body := string(rb)
	t.Logf("ответ на пустой GetTechCardResponse: %s", body)
	if !strings.Contains(body, `"techCard":null`) {
		t.Fatalf("EmitUnpopulated потерян: незаполненное сообщение не пришло явным null: %s", body)
	}
	if !strings.Contains(body, `"patternViewerToken":""`) {
		t.Fatalf("EmitUnpopulated потерян: незаполненная строка не пришла пустой: %s", body)
	}
}

// TestAdminGatewaySilentDropIsOneBitAway — МУТАЦИЯ, вшитая в набор.
//
// Тот же мукс, те же тела, единственная разница — DiscardUnknown: true (дефолт grpc-gateway
// v2.21.0, то есть состояние ДО этой задачи). Обе цитаты выше становятся зелёными 200, а
// присланное значение исчезает без следа: press-блок приходит пустым, лишнее поле не приходит
// вовсе. Тест зафиксирован НАВСЕГДА, потому что он и есть механика инцидента Ж7 — и потому что
// без него краснота цитат ничего не доказывала бы про причину.
func TestAdminGatewaySilentDropIsOneBitAway(t *testing.T) {
	laxMarshaler := newAdminJSONMarshaler()
	jsonpb, ok := laxMarshaler.Marshaler.(*runtime.JSONPb)
	if !ok {
		t.Fatalf("маршалер admin-гейтвея больше не *runtime.JSONPb: %T", laxMarshaler.Marshaler)
	}
	if jsonpb.UnmarshalOptions.DiscardUnknown {
		t.Fatalf("прод-маршалер уже глотает незнакомое: DiscardUnknown=true")
	}
	jsonpb.UnmarshalOptions.DiscardUnknown = true // ← ровно один бит, только в этой копии опций

	laxMux := runtime.NewServeMux(runtime.WithMarshalerOption(runtime.MIMEWildcard, laxMarshaler))
	stub := &techCardStubServer{}
	ts := serveAdmin(t, laxMux, stub)

	// (а) незнакомое поле: 200, RPC вызвана, значения нет.
	code, body := postTechCardUpdate(t, ts, payloadUnknownField)
	if code != http.StatusOK {
		t.Fatalf("с DiscardUnknown=true незнакомое поле дало %d (%s) — тело пробы невалидно по другой причине, "+
			"и краснота строгих тестов не доказывает работу бита", code, body)
	}
	if stub.updateCalls != 1 {
		t.Fatalf("лакс-мукс не дошёл до RPC (%d вызовов) — маршрут пробы неверен", stub.updateCalls)
	}
	if n := len(stub.lastUpdate.GetTechCard().GetOperations()); n != 1 {
		t.Fatalf("операций доехало %d, ожидалась 1", n)
	}
	if act := stub.lastUpdate.GetTechCard().GetOperations()[0].GetPress().GetAction(); act != pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_UNKNOWN {
		t.Fatalf("незнакомое поле почему-то доехало как %v", act)
	}

	// (б) незнакомый член enum: 200, RPC вызвана, press-блок ПУСТ — это и есть тихая потеря.
	stub.updateCalls, stub.lastUpdate = 0, nil
	code, body = postTechCardUpdate(t, ts, payloadUnknownEnumMember)
	if code != http.StatusOK {
		t.Fatalf("с DiscardUnknown=true незнакомый член enum дал %d (%s)", code, body)
	}
	if stub.updateCalls != 1 {
		t.Fatalf("лакс-мукс не дошёл до RPC (%d вызовов)", stub.updateCalls)
	}
	press := stub.lastUpdate.GetTechCard().GetOperations()[0].GetPress()
	if press.GetAction() != pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_UNKNOWN {
		t.Fatalf("незнакомый член enum доехал как %v — тихой потери нет, проба бессмысленна", press.GetAction())
	}
}
