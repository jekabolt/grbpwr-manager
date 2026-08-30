package meshy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ПРОБЫ «ДЕНЬГИ СПИСАНЫ, МОДЕЛИ НЕТ».
//
// ЧТО ЗДЕСЬ ДОКАЗЫВАЕТСЯ. Провайдер называет `consumed_credits` на конверте задачи, и до
// ChargedError это число терял КАЖДЫЙ терминальный провал: успешная задача без glb-ссылки, модель
// за пределом размера, оборвавшаяся закачка. Во всех трёх модель построена, кредиты сгорели, а
// попытка закрывалась с NULL-ценой — денег нет ни в журнале трат, ни в дневном потолке.
//
// ⚠ ПРОБЫ ГОНЯТ НАСТОЯЩИЙ HTTP-ОБМЕН, а не зовут chargedWith напрямую: предмет проверки — то, что
// число ДОЕЗЖАЕТ из конверта провайдера до вызывающего, а хелпер, считающий правильно там, где его
// никто не зовёт, зелен под любой мутацией места вызова.

const chargedCredits = 30

// chargedStand — провайдер, который отвечает на retrieve тем, что ему велели, и умеет отдавать
// модель нужного размера.
type chargedStand struct {
	srv  *httptest.Server
	task map[string]any
	// modelBody — тело, которым отвечает /assets/. Раздувается, когда пробе нужен предел размера.
	modelBody []byte
}

func newChargedStand(t *testing.T, task map[string]any) *chargedStand {
	t.Helper()
	st := &chargedStand{task: task, modelBody: []byte(fakeModelBody)}
	mux := http.NewServeMux()
	mux.HandleFunc(multiImagePath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"result": fakeTaskID})
	})
	mux.HandleFunc(multiImagePath+"/", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{}
		for k, v := range st.task {
			out[k] = v
		}
		if urls, ok := out["model_urls"].(map[string]string); ok && urls["glb"] == "SELF" {
			out["model_urls"] = map[string]string{"glb": st.srv.URL + "/assets/model.glb"}
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(st.modelBody)
	})
	st.srv = httptest.NewServer(mux)
	t.Cleanup(st.srv.Close)
	return st
}

func (st *chargedStand) client() *Client {
	return New(Config{
		APIKey: "test-key", BaseURL: st.srv.URL,
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond,
		PollTimeout: 2 * time.Second, DownloadTimeout: 2 * time.Second,
	})
}

// УСПЕШНАЯ ЗАДАЧА БЕЗ GLB-ССЫЛКИ — САМАЯ ДОРОГАЯ СТРОКА ПАКЕТА.
//
// Задача SUCCEEDED, значит модель построена и кредиты сгорели; ссылки, которой её забрать, просто
// нет. До этой починки наружу уходил голый ErrNoGLB, и прогон закрывался с NULL-ценой.
func TestNoGLBCarriesTheChargeOut(t *testing.T) {
	st := newChargedStand(t, map[string]any{
		"id":               fakeTaskID,
		"status":           string(StatusSucceeded),
		"progress":         100,
		"consumed_credits": chargedCredits,
		// model_urls отсутствует вовсе — ровно то, о чём говорит ErrNoGLB.
	})
	var model bytes.Buffer
	_, err := st.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model})

	if !errors.Is(err, ErrNoGLB) {
		t.Fatalf("err = %v, ждали ErrNoGLB (обёртка обязана оставлять сентинел читаемым)", err)
	}
	credits, ok := Charge(err)
	if !ok {
		t.Fatal("провал ушёл без цены: провайдер сумму НАЗВАЛ, а журнал трат её не увидит")
	}
	if credits != chargedCredits {
		t.Errorf("credits = %d, ждали %d", credits, chargedCredits)
	}
	if !strings.Contains(err.Error(), "charged") {
		t.Errorf("текст ошибки молчит о списании: %q", err.Error())
	}
}

// МОДЕЛЬ БОЛЬШЕ ПОТОЛКА — ТОЖЕ ОПЛАЧЕНА.
//
// Второй терминальный провал после метра: закачка отказывает по maxModelBytes, но задача уже
// построена и оплачена. Байты потеряны, деньги — нет.
func TestAnOversizeModelStillReportsItsCharge(t *testing.T) {
	st := newChargedStand(t, map[string]any{
		"id":               fakeTaskID,
		"status":           string(StatusSucceeded),
		"progress":         100,
		"consumed_credits": chargedCredits,
		"model_urls":       map[string]string{"glb": "SELF"},
	})
	st.modelBody = bytes.Repeat([]byte("x"), maxModelBytes+1)

	var model bytes.Buffer
	_, err := st.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, ждали ErrTooLarge", err)
	}
	credits, ok := Charge(err)
	if !ok || credits != chargedCredits {
		t.Errorf("Charge = (%d, %v), ждали (%d, true)", credits, ok, chargedCredits)
	}
}

// ЗАДАЧА, ЗА КОТОРУЮ НЕ ВЗЯЛИ ДЕНЕГ, НЕ ПРИТВОРЯЕТСЯ ОПЛАЧЕННОЙ.
//
// ⚠ БЕЗ ЭТОЙ ПРОБЫ ДВЕ ПРЕДЫДУЩИЕ НИЧЕГО НЕ СТОЯТ: реализация, вешающая ChargedError на ВСЁ,
// прошла бы их обе — и записала бы в журнал трат ноль там, где честный ответ «никто не сказал».
// Ноль и «неизвестно» — разные утверждения об одном прогоне.
func TestAnUnbilledFailureCarriesNoCharge(t *testing.T) {
	st := newChargedStand(t, map[string]any{
		"id":               fakeTaskID,
		"status":           string(StatusSucceeded),
		"progress":         100,
		"consumed_credits": 0, // провайдер суммы не назвал
	})
	var model bytes.Buffer
	_, err := st.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model})
	if !errors.Is(err, ErrNoGLB) {
		t.Fatalf("err = %v, ждали ErrNoGLB", err)
	}
	if credits, ok := Charge(err); ok {
		t.Errorf("Charge = (%d, true) на неоплаченном провале: «неизвестно» превратилось в число", credits)
	}
}

// НЕДОДЕЛАННАЯ ЗАДАЧА НЕ НЕСЁТ ЦЕНЫ.
//
// Await крутится на ErrNotReady, а не заканчивается на нём; цена, привешенная сюда, читалась бы
// заново на каждом опросе одной и той же незавершённой задачи.
func TestAnUnfinishedTaskCarriesNoCharge(t *testing.T) {
	st := newChargedStand(t, map[string]any{
		"id":               fakeTaskID,
		"status":           string(StatusInProgress),
		"progress":         40,
		"consumed_credits": chargedCredits,
	})
	var model bytes.Buffer
	_, err := st.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, ждали ErrNotReady", err)
	}
	if _, ok := Charge(err); ok {
		t.Error("идущая задача объявлена оплаченной: она ещё ничего не завершила")
	}
}

// ЦЕНА ПЕРЕЖИВАЕТ ОЖИДАНИЕ, А НЕ ТОЛЬКО ОДИН ПРОСМОТР.
//
// Воркер зовёт Await, а не Collect. Проба держит именно тот путь: обёртка обязана доехать через
// цикл опроса, иначе починка зелена в Collect и мертва там, где ею пользуются.
func TestTheChargeSurvivesAwait(t *testing.T) {
	st := newChargedStand(t, map[string]any{
		"id":               fakeTaskID,
		"status":           string(StatusSucceeded),
		"progress":         100,
		"consumed_credits": chargedCredits,
	})
	var model bytes.Buffer
	_, err := st.client().Await(context.Background(), fakeTaskID, Sink{Model: &model})
	if !errors.Is(err, ErrNoGLB) {
		t.Fatalf("err = %v, ждали ErrNoGLB", err)
	}
	if credits, ok := Charge(err); !ok || credits != chargedCredits {
		t.Errorf("Charge = (%d, %v), ждали (%d, true)", credits, ok, chargedCredits)
	}
}
