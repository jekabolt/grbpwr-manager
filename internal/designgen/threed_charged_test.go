package designgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/stretchr/testify/require"
)

// ПРОБА ДЕНЕГ ПРОВАЛЬНОГО 3D-ПРОГОНА.
//
// ЧТО БЫЛО. Провайдер называет `consumed_credits` на конверте задачи; на терминальных провалах —
// успешная задача без glb-ссылки, модель за пределом размера — это число терялось вместе с
// ошибкой, и попытка закрывалась с NULL-ценой. Деньги списаны, в журнале трат их нет, дневной
// потолок их не видит, и владелец никогда не узнает, сколько стоили провалы.
//
// ⚠ ПРОБА ГОНИТ ЦЕЛЫЙ ПРОХОД ВОРКЕРА НАД НАСТОЯЩИМ ПРОВАЙДЕРОМ, а не смотрит на возврат
// threed.Collect. Предмет проверки — то, что доедет ДО СТОРА (entity.DesignAttemptFinish.Price):
// именно оно и есть журнал трат. Проба над возвратом функции зеленела бы и в том мире, где
// settle() эту цену выбрасывает.

// chargedTaskStand — Meshy, который отвечает УСПЕХОМ и НАЗЫВАЕТ СУММУ, но не даёт ссылки на модель.
// Это ровно тот случай, где деньги сгорели, а отдать нечего.
func chargedTaskStand(t *testing.T, credits int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task-42","status":"SUCCEEDED","progress":100,` +
			`"consumed_credits":` + itoa(credits) + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// acceptedThreedRun — прогон, чья ОПЛАЧЕННАЯ отправка уже состоялась: попытка закрыта как
// `accepted` и несёт id задачи. Воркер по такой строке идёт СРАЗУ в сбор, не покупая модель второй
// раз, — и именно сбор здесь узнаёт цену.
func acceptedThreedRun() entity.DesignRun {
	run := testRun(8, entity.DesignRunKindThreed)
	run.Attempts = []entity.DesignRunAttempt{{
		RunId: 8, AttemptNo: 1, Provider: "meshy",
		State:             entity.DesignAttemptAccepted,
		ProviderRequestId: nullString("task-42"),
	}}
	return run
}

func chargedThreedWorker(t *testing.T, st *fakeStore, credits int) *Worker {
	t.Helper()
	stand := chargedTaskStand(t, credits)
	c := meshy.New(meshy.Config{
		APIKey: "k", BaseURL: stand.URL,
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond,
		PollTimeout: 2 * time.Second, DownloadTimeout: 2 * time.Second,
	})
	return testWorker(st, nil, newFakeSink(ContentTypeGLB, ContentTypePNG),
		Providers{Threed: NewThreedProvider(c)})
}

// ЦЕНА ПРОВАЛА ДОЕЗЖАЕТ ДО ЖУРНАЛА ТРАТ.
func TestAPaidThreedFailureStillReachesTheLedger(t *testing.T) {
	prior := acceptedThreedRun()
	st := &fakeStore{getRun: &prior}
	w := chargedThreedWorker(t, st, 30)

	require.NoError(t, w.execute(context.Background(), acceptedThreedRun(), "tok"))

	require.Len(t, st.finished, 1, "у сбора своя строка попытки: именно на ней становится известна цена")
	fin := st.finished[0]
	require.True(t, fin.Price.Valid,
		"цена провала обязана быть записана: провайдер сумму НАЗВАЛ, деньги списаны")
	// 30 кредитов по умолчанию meshy (~$0.02 за кредит) = $0.60. Число сказано пробой своими
	// словами намеренно: сверять цену с той же функцией, что её считает, значит не сверять ничего.
	require.Equal(t, "0.6", fin.Price.Decimal.String())
	require.Equal(t, "task-42", fin.ProviderRequestId,
		"строка журнала обязана называть задачу, в которую ушли деньги")
	// «Деньги списаны, модели нет» — это именно `unknown`, а не `failed`: провал, который стоил.
	require.Equal(t, entity.DesignAttemptUnknown, fin.State)
	require.Equal(t, CodeEmptyResponse, fin.ErrorCode)

	require.Len(t, st.failed, 1, "прогон обязан закрыться провалом: отдавать нечего")
	require.False(t, st.failed[0].Retryable,
		"повтор купил бы вторую модель: задача уже успешна и уже оплачена")
	require.Empty(t, st.completed)
}

// ПРОВАЛ, ЗА КОТОРЫЙ НЕ ВЗЯЛИ ДЕНЕГ, ПИШЕТ NULL, А НЕ НОЛЬ.
//
// ⚠ БЕЗ ЭТОЙ ПОЛОВИНЫ ПРОБА ВЫШЕ ДОКАЗЫВАЛА БЫ МЕНЬШЕ, ЧЕМ КАЖЕТСЯ: реализация, пишущая цену
// ВСЕГДА, прошла бы её — и записала бы в журнал ноль там, где честный ответ «никто не сказал».
// Ноль и «неизвестно» — разные утверждения, и дневной потолок читает их по-разному.
func TestAnUnbilledThreedFailureWritesNoPrice(t *testing.T) {
	prior := acceptedThreedRun()
	st := &fakeStore{getRun: &prior}
	w := chargedThreedWorker(t, st, 0)

	require.NoError(t, w.execute(context.Background(), acceptedThreedRun(), "tok"))

	require.Len(t, st.finished, 1)
	require.False(t, st.finished[0].Price.Valid,
		"провайдер суммы не назвал: в журнал обязан уйти NULL, а не ноль")
	require.Len(t, st.failed, 1)
}

// СЕНТИНЕЛ ПЕРЕЖИВАЕТ ОБЁРТКУ.
//
// Цена привешена к ошибке, а не подменяет её: классификатор читает провал теми же errors.Is, и
// обёртка, съевшая сентинел, молча превратила бы неисправимый провал в «погоду» — пять оплаченных
// попыток над задачей, которая уже успешна.
func TestTheChargeDoesNotHideTheFault(t *testing.T) {
	stand := chargedTaskStand(t, 30)
	p := NewThreedProvider(meshy.New(meshy.Config{
		APIKey: "k", BaseURL: stand.URL,
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond,
		PollTimeout: 2 * time.Second, DownloadTimeout: 2 * time.Second,
	}))
	collector, ok := p.(Collector)
	require.True(t, ok, "3D-маршрут обязан уметь собирать: сбор и есть бесплатная половина")

	out, err := collector.Collect(context.Background(), Job{RunID: 8}, "task-42")
	require.Error(t, err)
	require.True(t, errors.Is(err, meshy.ErrNoGLB), "сентинел обязан остаться читаемым сквозь обёртку")
	require.NotNil(t, out, "исход обязан приехать ВМЕСТЕ с ошибкой: деньги реальны")
	require.Empty(t, out.Artifacts, "отдавать нечего — и это то, из-за чего прогон провалится")
	require.True(t, out.Price.Valid)

	v := classify(err)
	require.False(t, v.Retryable, "оплаченный успех не чинится повтором")
	require.Equal(t, entity.DesignAttemptUnknown, v.State)
}
