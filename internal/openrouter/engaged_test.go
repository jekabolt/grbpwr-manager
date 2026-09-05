package openrouter

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ─────────── ГРАНИЦА «КУПЛЕНО / НЕ КУПЛЕНО», ЗАМЕРЕННАЯ НА ПРОВОДЕ ───────────
//
// Всё, что здесь проверяется, — ОДИН вопрос про деньги: успел ли запрос доехать до поставщика до
// того, как всё сломалось. Ответ на него — единственное, что отличает «регистр честно пишет ноль»
// от «регистр молча прячет потраченное»; см. ProviderEngaged и design_run.go: designFailDraft.
//
// ⚠ ПРОБЫ НЕ СМОТРЯТ НА ТЕКСТ ОШИБКИ ВООБЩЕ, И ЭТО ЧАСТЬ УТВЕРЖДЕНИЯ. Ровно тем, что смотрит на
// текст, такая починка и гниёт: строки net/http меняются между релизами Go, а промах в любую
// сторону — это либо выдуманные деньги, либо снова спрятанные.

// deadAddr — адрес, на котором ГАРАНТИРОВАННО никто не слушает: слушатель поднят, чтобы у ядра
// спросить свободный порт, и тут же закрыт. Литерал вроде 127.0.0.1:1 на чужой машине однажды
// окажется занят, и «соединение отказано» тихо превратится в другой исход.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}

// hangingProvider — поставщик, который ПОЛУЧАЕТ запрос целиком и не отвечает.
//
// ⚠ ТЕЛО ЗАПРОСА ЧИТАЕТСЯ ЯВНО, И ЭТО НЕ ГИГИЕНА, А УСЛОВИЕ ЗАМЕРА. Во-первых, дочитанное тело и
// есть «поставщик получил запрос» — то самое, что проба утверждает. Во-вторых, net/http отменяет
// r.Context() по разрыву соединения только после того, как тело прочитано: обработчик, ждущий
// отмены с непрочитанным телом, вешает httptest.Server.Close навсегда (первая версия этой пробы
// повесила прогон ровно так).
//
// Освобождается ЯВНЫМ каналом до Close, чтобы проба не зависела от того, как быстро сервер заметит
// ушедшего клиента.
func hangingProvider(t *testing.T) (url string, reached <-chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	got := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(got) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv.URL, got
}

// ЗАПРОС, КОТОРЫЙ НЕ УШЁЛ, НЕ КУПЛЕН.
//
// Соединение отказано — поставщик не получил ни байта, платить не за что, и NULL в регистре есть
// правда о нём. ЭТО РАЗЛИЧАЮЩАЯ ПРОБА: без неё починка «списывать при обрыве» выродилась бы в
// «списывать всегда», то есть в выдуманные деньги на каждом отказе соединения.
//
// МУТАЦИЯ: вернуть `engaged(failed)` безусловно (убрать проверку wrote.Load()) → краснеет.
func TestARequestThatNeverLeftIsNotCharged(t *testing.T) {
	c := New(Config{APIKey: "k", BaseURL: deadAddr(t), HTTPTimeout: 2 * time.Second})
	_, _, _, err := c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.Error(t, err)
	require4(t, err)
	require.False(t, ProviderEngaged(err),
		"соединение отказано: поставщик не получил запроса, и списывать нечего")
}

// ЗАПРОС, КОТОРЫЙ УШЁЛ И НЕ ДОЖДАЛСЯ ОТВЕТА, КУПЛЕН.
//
// Это и есть дыра, ради которой всё заводилось: POST дописан, поставщик считает, срок вышел. Из
// Do приезжает ровно такой же *url.Error, как у отказанного соединения, — различает их только
// флаг записи, поднятый httptrace.
//
// МУТАЦИЯ: убрать `engaged(...)` у ветки Do → краснеет.
func TestARequestCutAfterItReachedTheProviderIsCharged(t *testing.T) {
	url, reached := hangingProvider(t)
	c := New(Config{APIKey: "k", BaseURL: url, HTTPTimeout: 200 * time.Millisecond})
	start := time.Now()
	_, _, _, err := c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "проба обязана упереться в срок, а не в чужой")
	select {
	case <-reached:
	default:
		t.Fatal("поставщик не получил запроса — проба измеряет не то, что утверждает")
	}
	require.True(t, ProviderEngaged(err),
		"POST дописан в соединение: поставщик считает, и регистр обязан это знать")
}

// ОТМЕНЁННЫЙ ВЫЗОВ — ТО ЖЕ САМОЕ. Закрытая вкладка приходит сюда отменой контекста, а не
// таймаутом, и обе двери обязаны вести в один и тот же ответ.
func TestACancelledCallAfterTheRequestLeftIsCharged(t *testing.T) {
	url, reached := hangingProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-reached
		cancel()
	}()
	c := New(Config{APIKey: "k", BaseURL: url, HTTPTimeout: 10 * time.Second})
	_, _, _, err := c.CompleteWithImages(ctx, "sys", "user", nil, false, 0)
	require.Error(t, err)
	require.True(t, ProviderEngaged(err), "отмена ПОСЛЕ отправки не делает вызов бесплатным")
}

// ВОРОТА ПОСТАВЩИКА НЕ ТАРИФИЦИРУЮТСЯ.
//
// 404 (протухший слуг модели), 401 (не тот ключ), 429 (частим) — поставщик ОТКАЗАЛ, а отказ не
// счёт. Это единственное место, где граница выбрана в сторону нуля, и выбрана она нарочно:
// протухший слуг отвечает 404 на КАЖДОЕ нажатие, и пометить это тратой значило бы выдумать
// деньги в тот день, когда фича мертва целиком.
//
// МУТАЦИЯ: обернуть ветку non-2xx в engaged(...) → краснеет.
func TestAProviderRefusalAtTheGateIsNotCharged(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusTooManyRequests} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"no"}}`))
		}))
		c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: 2 * time.Second})
		_, _, _, err := c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
		require.Error(t, err)
		require.False(t, ProviderEngaged(err), "HTTP %d — это ворота, а не счётчик", status)
		srv.Close()
	}
}

// ОТВЕТ 2xx, КОТОРЫЙ НЕЛЬЗЯ ИСПОЛЬЗОВАТЬ, ВСЁ РАВНО ОПЛАЧЕН.
//
// Поставщик принял запрос и отработал его; то, что конверт пуст или не разобрался, — наша беда.
//
// МУТАЦИЯ: снять engaged(...) с ветки «no choices» → краснеет.
func TestAnUnusable2xxIsStillCharged(t *testing.T) {
	for name, body := range map[string]string{
		"no choices":     `{"choices":[]}`,
		"broken":         `{"choices":`,
		"error envelope": `{"error":{"message":"upstream died"}}`,
		"empty message":  `{"choices":[{"message":{"content":"  "},"finish_reason":"stop"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: 2 * time.Second})
			_, _, _, err := c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
			require.Error(t, err)
			require.True(t, ProviderEngaged(err), "2xx означает, что запрос приняли и посчитали")
		})
	}
}

// НЕНАСТРОЕННЫЙ КЛИЕНТ НЕ ПОКУПАЕТ НИЧЕГО, и nil-ошибка тем более.
func TestNothingBeforeTheWireIsCharged(t *testing.T) {
	var off *Client
	_, _, _, err := off.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.ErrorIs(t, err, ErrNotConfigured)
	require.False(t, ProviderEngaged(err))
	require.False(t, ProviderEngaged(nil), "у отсутствующей ошибки нет цены")
}

// ПОМЕТКА НЕ РВЁТ ЦЕПОЧКУ И НЕ ТРОГАЕТ ТЕКСТ.
//
// Обёртка стоит НАД сентинелом, и все прежние читатели (errors.Is у designDraftCallError,
// у ветки ErrBudgetExhausted) обязаны продолжать видеть своё. Текст важен отдельно: эта ошибка
// доезжает до человека целиком, и errors.Join склеил бы её через перевод строки.
func TestTheEngagedMarkKeepsTheChainAndTheSentence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}],` +
			`"usage":{"completion_tokens":8000}}`))
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: 2 * time.Second})
	_, _, _, err := c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBudgetExhausted, "сентинел обязан читаться сквозь пометку")
	require.True(t, ProviderEngaged(err))
	require.NotContains(t, err.Error(), "\n", "текст ошибки едет человеку одной фразой")
	require.Contains(t, err.Error(), "8000")
}

// require4 — маленький сторож самой пробы: у «мёртвого адреса» вызов обязан УПАСТЬ БЫСТРО, а не
// упереться в срок. Медленный отказ означал бы, что мы измеряем не то, что думаем.
func require4(t *testing.T, err error) {
	t.Helper()
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"мёртвый адрес обязан отказать соединением, а не сроком: иначе проба меряет таймаут")
}
