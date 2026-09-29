package admin

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ───────────── ВТОРЫЕ ВОРОТА: РОД, КОТОРЫЙ ВСЁ РАВНО НЕ ДОЕДЕТ ─────────────
//
// ЧТО ЗДЕСЬ ЗАЩИЩАЕТСЯ. StartDesignRun РЕЗЕРВИРУЕТ ДЕНЬГИ: строка заводится с price_estimate, и
// резерв дня держится до полуночи либо до терминального перехода. Пока приёмник не умел хранить
// GLB, каждый прогон вида threed принимался этой дверью, резервировал деньги и через
// тик воркера ГАРАНТИРОВАННО падал — по разу на клик. Отказ обязан стоять до резерва.
//
// ЧЕГО ЗДЕСЬ НЕТ И БЫТЬ НЕ ДОЛЖНО: списка родов. Ответ приходит функцией из воркера и считается
// из возможностей маршрута и приёмника; этот ярус только переводит его в код gRPC.

// stubGateRefusal — отказ ворот в той же форме, в какой его отдаёт воркер: с машинной причиной.
type stubGateRefusal struct{ reason, msg string }

func (e *stubGateRefusal) Error() string         { return e.msg }
func (e *stubGateRefusal) RefusalReason() string { return e.reason }

// TestStartDesignRunRefusesAKindThatCannotBeStoredBeforeAnyReserve.
//
// Репозиторий здесь БЕЗ ЕДИНОГО ожидания: любое обращение к нему — чтение карточки, полосы, а тем
// более StartRun — роняет пробу по имени. Это и есть измерение «до денег»: не отсутствие проверки,
// а доказанное отсутствие вызова.
func TestStartDesignRunRefusesAKindThatCannotBeStoredBeforeAnyReserve(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	srv := &Server{repo: repo, designGenerationEnabled: true}
	srv.SetDesignKindGate(func(kind string) error {
		return &stubGateRefusal{
			reason: "output_not_storable",
			msg:    "designgen: this route's output has nowhere to be stored: the route returns image/svg+xml",
		}
	})

	_, err := srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindFlat))
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code,
		"запрос правильный — не готова система; это не InvalidArgument")
	require.Equal(t, "output_not_storable", md["reason"],
		"машинная причина обязана доехать до клиента: по ней он отличает «у нас выключено» от «модель отказала»")
	require.Equal(t, entity.DesignRunKindFlat, md["kind"])
	require.Contains(t, err.Error(), "image/svg+xml",
		"человеку нужно назвать ТИП, из-за которого отказано, иначе чинить нечего")
	require.Contains(t, err.Error(), "Nothing was reserved")
}

// TestTheKindGateAppliesToEveryKindItRefuses — ворота спрашивают ПРО РОД, и род приезжает в них
// тот самый, который просили. Без этого гейт мог бы отказывать всем одинаково.
func TestTheKindGateAppliesToEveryKindItRefuses(t *testing.T) {
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender,
		entity.DesignRunKindThreed,
	} {
		t.Run(kind, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			srv := &Server{repo: repo, designGenerationEnabled: true}
			var asked []string
			srv.SetDesignKindGate(func(k string) error {
				asked = append(asked, k)
				return &stubGateRefusal{reason: "kind_not_available", msg: "no route is wired"}
			})
			_, err := srv.StartDesignRun(designRunCtx(), designStartRequest(kind))
			require.Error(t, err)
			require.Equal(t, []string{kind}, asked, "ворота обязаны спрашивать про запрошенный род")
			_, md := errorReason(t, err)
			require.Equal(t, "kind_not_available", md["reason"])
		})
	}
}

// TestAKindTheGateAllowsStillReachesTheStore — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ.
//
// Без него все пробы выше зеленели бы и на двери, которая отказывает всегда: «отказ доказан»
// ничего не стоит, если разрешение не доказано рядом.
func TestAKindTheGateAllowsStillReachesTheStore(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	var asked []string
	rig.srv.SetDesignKindGate(func(k string) error { asked = append(asked, k); return nil })

	resp, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindFlat))
	require.NoError(t, err)
	require.NotNil(t, resp.GetRun())
	require.Equal(t, []string{entity.DesignRunKindFlat}, asked)
	require.NotNil(t, rig.sent, "разрешённый род обязан дойти до стора")
	require.Equal(t, entity.DesignRunKindFlat, rig.sent.Kind)
}

// TestAnUnwiredKindGateRefusesNothing. Сервер без ворот — это сервер с выключенным флагом денег
// (app.go вешает обе вещи из одного `if enabled`), поэтому «не повесили» не должно означать
// «сломано»: платный глагол в такой сборке закрыт строчкой выше.
func TestAnUnwiredKindGateRefusesNothing(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	require.Nil(t, rig.srv.designKindGate, "стенд собран без ворот — это и есть проверяемый случай")
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindFlat))
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
}

// TestAGateErrorWithoutAReasonStillRefuses. Ошибка, не назвавшая машинной причины, — всё ещё
// отказ: тихо пропустить её значило бы зарезервировать деньги под прогон, который не доедет.
func TestAGateErrorWithoutAReasonStillRefuses(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	srv := &Server{repo: repo, designGenerationEnabled: true}
	srv.SetDesignKindGate(func(string) error { return errors.New("the sink is not wired at all") })

	_, err := srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, designReasonKindUnavailable, md["reason"])
}

// TestTheMoneyFlagIsCheckedBeforeTheKindGate — порядок ворот. Выключенная генерация обязана
// отвечать «включите DESIGN_GENERATION_ENABLED», а не рассказывать про типы файлов: дежурному
// нужна та причина, которую он может устранить.
func TestTheMoneyFlagIsCheckedBeforeTheKindGate(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	srv := &Server{repo: repo} // флаг выключен
	called := false
	srv.SetDesignKindGate(func(string) error { called = true; return nil })

	_, err := srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindFlat))
	require.Error(t, err)
	_, md := errorReason(t, err)
	require.Equal(t, designReasonGenerationDisabled, md["reason"])
	require.False(t, called, "выключенный флаг закрывает дверь раньше и дешевле")
}

// TestTheRetiredVectorKindIsRefusedAtTheDoor — vector generation was removed on 2026-09-29 (Recraft
// out). A client that still sends kind=vector gets InvalidArgument before any read, reserve or gate.
func TestTheRetiredVectorKindIsRefusedAtTheDoor(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	srv := &Server{repo: repo, designGenerationEnabled: true}
	gateAsked := false
	srv.SetDesignKindGate(func(string) error { gateAsked = true; return nil })

	_, err := srv.StartDesignRun(designRunCtx(), designStartRequest("vector"))
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.False(t, gateAsked, "an unknown kind never reaches the gate")
}
