package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ОТКАЗЫ ОЧЕРЕДИ И ДЕНЕГ — НЕ ПОЛОМКА СЕРВЕРА.
//
// ЧТО БЫЛО. Трёх sentinel-ов генеративной половины (budget_exceeded, claim_lost, run_terminal) в
// таблице designRefusals не было вовсе, а всё, чего в ней нет, уходит на провод как
// `codes.Internal, "failed to …"` ПЛЮС строка ERROR в лог. То есть штатное состояние дня —
// «дневной потолок исчерпан» — человек читал как «сервер сломался», дежурный получал ошибку на
// ровном месте, а машинного токена, который комментарий у самого sentinel-а обещает клиенту, на
// проводе не появлялось ни разу.
//
// ЭТИ ПРОБЫ ХОДЯТ ЧЕРЕЗ ЖИВЫЕ ХЕНДЛЕРЫ, а не зовут designError напрямую: таблица бесполезна, если
// путь до неё где-то заворачивает ошибку в свою прозу и errors.Is перестаёт срабатывать.

// errorReason разбирает отказ полосы на ДВЕ половины, по которым ветвится клиент: gRPC-код и
// машинные подробности из errdetails.ErrorInfo (включая ключ `reason`). Переехала сюда из
// design_sheet_mint_test.go, снесённого вместе с подсистемой минта; читателей у неё почти два
// десятка по всем пробам полосы.
//
// ⚠ ОТСУТСТВИЕ ErrorInfo — НЕ ПРОВАЛ, А ЗАКОННЫЙ ИСХОД, и поэтому здесь нет require на деталь.
// Отказы у ДВЕРИ (designEffectiveParams и соседи) — это обычный status.Errorf без деталей: они
// не проходят через designError вовсе. Уронив пробу на пустых деталях, хелпер запретил бы
// проверять код отказа ровно там, где проверяется только код.
func errorReason(t *testing.T, err error) (codes.Code, map[string]string) {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "не gRPC-статус: %v", err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return st.Code(), info.GetMetadata()
		}
	}
	return st.Code(), nil
}

// ⚠ ЗДЕСЬ ЖИЛА TestStartDesignRunAnswersTheDailyCapAsAPrecondition, И ОНА СНЯТА ВМЕСТЕ СО СВОИМ
// ПРЕДМЕТОМ (0358, L-8). Она удостоверяла, что исчерпанный дневной потолок доезжает до клиента
// машинной причиной `budget_exceeded`; потолка больше нет ни в одной форме, sentinel удалён, и
// проба на его перевод проверяла бы состояние, которого сервер не производит.
//
// ЧТО ЗАНЯЛО ЕЁ МЕСТО: TestStartDesignRunIsNeverRefusedForMoney ниже — утверждение ровно обратного
// и уже про живой код. Стереть пробу молча было нельзя: следующий читатель обязан узнать, что
// отказ по деньгам не «забыли протестировать», а сняли.

// НИ ОДИН ПРОГОН НЕ ОТКАЗЫВАЕТСЯ ПО ДЕНЬГАМ — НА ЯРУСЕ ПЕРЕВОДА ОТКАЗОВ.
//
// Слова владельца: «у нас в принципе не должно быть потолка похуй чем он съеден убери потолок».
// Проба держит ту половину, которую видно с провода: в словаре отказов больше нет строки, дающей
// денежную причину. Токен `budget_exceeded` недостижим, и клиентская ветка, которая его ждёт,
// ждала бы вечно.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть в designRefusals строку с денежным sentinel-ом.
func TestStartDesignRunIsNeverRefusedForMoney(t *testing.T) {
	for _, r := range designRefusals {
		require.NotEqual(t, "budget_exceeded", r.reason,
			"денежный отказ снят вместе с потолком: клиент не должен получать причину, "+
				"по которой сервер больше не отказывает")
	}
}

// ЗАХВАТ ПОТЕРЯН НА ПУТИ РЕЗЮМА — Aborted, В ОДНОМ РЯДУ С slot_rev_mismatch И bench_moved.
//
// Это тот же класс: оптимистичный захват не подтвердился, состояние уехало, правильное действие —
// перечитать и повторить. Internal сказал бы «сломались мы», и клиент, который умеет откатывать
// Aborted, откатывать бы не стал.
//
// МУТАЦИЯ: убрать строку {ErrDesignClaimLost, …} — Aborted превращается в Internal.
func TestDraftDesignIdeaAnswersALostClaimAsAborted(t *testing.T) {
	client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an idea"}}]}`))
	})
	rig := newDraftIdeaRig(t, client)

	const tok = "rotated-token"
	run := designResumedRun(tok)
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Return(&entity.DesignRunStarted{Run: run, Idempotent: true, Resumed: true}, nil).Once()
	rig.design.EXPECT().StartAttempt(mock.Anything, mock.AnythingOfType("entity.DesignAttemptStart")).
		Return(&entity.DesignRunAttempt{RunId: run.Id, AttemptNo: 2}, nil).Once()
	rig.design.EXPECT().FinishAttempt(mock.Anything, mock.AnythingOfType("entity.DesignAttemptFinish")).
		Return(nil).Once()
	rig.design.EXPECT().CompleteRun(mock.Anything, mock.AnythingOfType("entity.DesignRunComplete")).
		Return(nil, fmt.Errorf("%w: design run %d changed hands while its result was being filed",
			entity.ErrDesignClaimLost, run.Id)).Once()

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "55555555-5555-5555-5555-555555555555",
	})
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.Aborted, code)
	require.Equal(t, "claim_lost", md["reason"])
}

// ЗАКОНЧЕННЫЙ ПРОГОН — FailedPrecondition, отдельной причиной: «ты опоздал» и «строка кончилась»
// разные новости, и повторять вторую бессмысленно, в отличие от первой.
func TestDraftDesignIdeaAnswersATerminalRunAsAPrecondition(t *testing.T) {
	client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an idea"}}]}`))
	})
	rig := newDraftIdeaRig(t, client)

	const tok = "rotated-token"
	run := designResumedRun(tok)
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Return(&entity.DesignRunStarted{Run: run, Idempotent: true, Resumed: true}, nil).Once()
	rig.design.EXPECT().StartAttempt(mock.Anything, mock.AnythingOfType("entity.DesignAttemptStart")).
		Return(nil, fmt.Errorf("%w: design run %d is already done", entity.ErrDesignRunTerminal, run.Id)).Once()

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "55555555-5555-5555-5555-555555555555",
	})
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "run_terminal", md["reason"])
}

// newDesignRunRigWithoutStartRun — стенд, у которого StartRun НЕ заглушён: пробы отказов сами
// решают, чем он ответит.
func newDesignRunRigWithoutStartRun(t *testing.T) *designRunRig {
	t.Helper()
	rig := &designRunRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	designStubNoDisplayOnly(rig.design)
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).
		Return(designMoodCard(), nil).Maybe()
	rig.design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.Anything).
		Return(designBandWith(true), nil).Maybe()
	rig.srv = &Server{repo: rig.repo, designGenerationEnabled: true}
	return rig
}

// designResumedRun — строка, которую стор ОТДАЛ этому вызывающему: свежий токен и живая лиза.
func designResumedRun(token string) entity.DesignRun {
	return entity.DesignRun{
		Id: 900, TechCardId: designRunCardID, Status: entity.DesignRunPending,
		ClaimToken:     sql.NullString{String: token, Valid: true},
		ClaimExpiresAt: sql.NullTime{Time: time.Now().Add(5 * time.Minute), Valid: true},
	}
}
