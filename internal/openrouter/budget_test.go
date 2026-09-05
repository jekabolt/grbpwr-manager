package openrouter

// БЮДЖЕТ ВЫЗОВА ВЫВЕДЕН ИЗ ПОТОЛКА ТОКЕНОВ, А НЕ ЗАПИСАН РЯДОМ С НИМ.
//
// ⚠ ЧТО ЧИНИТСЯ. defaultTimeout был потолком ВСЕГО вызова и стоял в http.Client, то есть был одним
// числом на все запросы, поставленным до того, как стало известно, сколько токенов у запроса
// попрошено. Связь с потолком ответа держалась просьбой в комментарии — «в этом порядке, и ни одно
// без другого» (analysisReasoningEffort). Просьба не удержала: designConstructionMaxTokens подняли
// 3000 → 8000 в одиночку, и разрешённый ответ перестал успевать приехать за 60 s (133 ток/с против
// замеренных ~60). Обрыв приходил ТРАНСПОРТНОЙ ошибкой, а вызывающий закрывал попытку ценой NULL.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ФАЙЛ ПРОВЕРЕН (каждая прогнана, покраснела и откачена):
//  1. `return base` в начале CompletionBudget (игнорировать max_tokens) → краснеет
//     TestTheAnswerCeilingBuysItsOwnTime и TestCompletionBudgetGrowsWithTheCeiling;
//  2. убрать context.WithTimeout из postChatCompletion → краснеет
//     TestTheCallWithoutACeilingStillHasADeadline (срок вообще перестаёт действовать);
//  3. вернуть http.Client{Timeout: base} и снять срок с запроса → краснеет
//     TestTheAnswerCeilingBuysItsOwnTime (потолок снова не покупает времени).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// slowCompletions отвечает валидной парой «контент + usage», но НЕ РАНЬШЕ чем через delay.
func slowCompletions(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return // клиент ушёл: отвечать некому
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"completion_tokens": 1},
		})
	}))
}

// TestTheAnswerCeilingBuysItsOwnTime — ЗАПРОС С ПОТОЛКОМ ЖИВЁТ ДОЛЬШЕ, ЧЕМ БАЗА, И ЭТО ВИДНО НА
// ПРОВОДЕ, а не только в арифметике.
//
// Две половины одной пробы, обе на ОДНОМ И ТОМ ЖЕ медленном сервере и ОДНОЙ И ТОЙ ЖЕ крошечной
// базе. Разница между ними ровно одна — попрошенный потолок:
//
//   - без потолка бюджет равен базе, сервер в неё не укладывается → вызов обязан оборваться;
//   - с потолком база та же, но печать 8000 токенов при minCompletionTokPerSec добавляет минуты →
//     тот же самый сервер обязан успеть.
//
// ⚠ БАЗА ВЗЯТА МИКРОСКОПИЧЕСКОЙ НАРОЧНО. Проба, ждущая настоящие 60 s, не запускается никем;
// проба, у которой обе половины успевают, не отличает починку от её отсутствия.
func TestTheAnswerCeilingBuysItsOwnTime(t *testing.T) {
	srv := slowCompletions(t, 300*time.Millisecond)
	defer srv.Close()

	newClient := func() *Client {
		return New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: 40 * time.Millisecond})
	}

	t.Run("без потолка бюджет равен базе и обрывается", func(t *testing.T) {
		_, _, _, err := newClient().CompleteWithMeta(context.Background(), "sys", "usr", false, 0)
		if err == nil {
			t.Fatal("вызов без потолка обязан упереться в базу: 40 мс против 300 мс ответа")
		}
	})

	t.Run("с потолком тот же сервер успевает", func(t *testing.T) {
		// 8000 — потолок структурного черновика (designConstructionMaxTokens). Печать при
		// minCompletionTokPerSec = 30 добавляет к базе ~266 s, поэтому 300 мс уже не стена.
		text, _, _, err := newClient().CompleteWithMeta(context.Background(), "sys", "usr", false, 8000)
		if err != nil {
			t.Fatalf("потолок обязан покупать своё время, а вызов оборвался: %v", err)
		}
		if text != "ok" {
			t.Fatalf("ответ = %q, хотел %q", text, "ok")
		}
	})
}

// TestTheCallWithoutACeilingStillHasADeadline — СРОК ЕСТЬ ВСЕГДА, а не только когда попрошен
// потолок. http.Client.Timeout снят (см. New), и если бы срок ставился только на путь с потолком,
// прозаический черновик — а он идёт БЕЗ потолка — висел бы на молчащем поставщике бесконечно.
func TestTheCallWithoutACeilingStillHasADeadline(t *testing.T) {
	srv := slowCompletions(t, 2*time.Second)
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: 50 * time.Millisecond})
	start := time.Now()
	if _, _, _, err := c.CompleteWithMeta(context.Background(), "sys", "usr", false, 0); err == nil {
		t.Fatal("вызов без потолка обязан иметь срок")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("срок не сработал: вызов занял %s при базе 50 мс", elapsed)
	}
}

// TestCallerDeadlineStillWins — БЮДЖЕТ УДЛИНЯЕТ СВОЙ СРОК, А НЕ ЧУЖОЙ. context.WithTimeout не
// умеет продлевать дедлайн, который уже стоит у вызывающего, и это ровно то, что нужно: дверь,
// решившая ждать меньше, обязана дождаться своего.
func TestCallerDeadlineStillWins(t *testing.T) {
	srv := slowCompletions(t, 2*time.Second)
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	start := time.Now()
	// Потолок огромный — бюджет транспорта измеряется минутами, но срок вызывающего короче.
	if _, _, _, err := c.CompleteWithMeta(ctx, "sys", "usr", false, 8000); err == nil {
		t.Fatal("срок вызывающего обязан оставаться сильнее")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("срок вызывающего проигнорирован: вызов занял %s при 60 мс", elapsed)
	}
}

// TestCompletionBudgetGrowsWithTheCeiling — АРИФМЕТИКА СВЯЗИ, отдельно от провода.
func TestCompletionBudgetGrowsWithTheCeiling(t *testing.T) {
	base := 60 * time.Second

	if got := CompletionBudget(base, 0); got != base {
		t.Fatalf("без потолка бюджет = %s, хотел базу %s", got, base)
	}
	if got := CompletionBudget(base, -100); got != base {
		t.Fatalf("отрицательный потолок читается как «нет потолка»: %s", got)
	}
	if got := CompletionBudget(0, 0); got != defaultTimeout {
		t.Fatalf("незаданная база = %s, хотел defaultTimeout %s", got, defaultTimeout)
	}
	if got := DefaultCompletionBudget(0); got != defaultTimeout {
		t.Fatalf("DefaultCompletionBudget(0) = %s, хотел %s", got, defaultTimeout)
	}

	// ПЕЧАТЬ СЧИТАЕТСЯ ПО ЗАЯВЛЕННОЙ СКОРОСТИ, и число здесь выписано своей копией: сверка формулы
	// с собою зеленела бы и на версии, где minCompletionTokPerSec молча стал тысячей.
	const wantTokPerSec = 30
	for _, tokens := range []int{1000, 2500, 8000, 100000} {
		got := CompletionBudget(base, tokens)
		wantPrinting := time.Duration(tokens) * time.Second / wantTokPerSec
		if got != base+wantPrinting {
			t.Fatalf("бюджет(%d) = %s, хотел %s", tokens, got, base+wantPrinting)
		}
		// И ГЛАВНОЕ СВОЙСТВО: за отведённое время потолок обязан успеть напечататься на скорости
		// НЕ ВЫШЕ заявленной.
		if rate := float64(tokens) / got.Seconds(); rate > wantTokPerSec {
			t.Fatalf("потолок %d требует %.1f ток/с при заявленных %d", tokens, rate, wantTokPerSec)
		}
	}

	// МОНОТОННОСТЬ: больший потолок не может стоить меньше времени.
	if CompletionBudget(base, 8000) <= CompletionBudget(base, 3000) {
		t.Fatal("бюджет обязан расти вместе с потолком")
	}
}
