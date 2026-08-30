package designgen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/meshy"
)

// ПРОБА ПОДСКАЗКИ О ПОВЕРХНОСТИ (`texture_prompt`).
//
// ЧТО ОНА ДОКАЗЫВАЕТ И ПОЧЕМУ ИМЕННО ТАК. Она смотрит на ИСХОДЯЩИЙ ЗАПРОС, а не на возврат
// textureSteer: потолок стоит у поставщика, и единственное утверждение, которое здесь чего-то
// стоит, — «то, что реально ушло в сеть, поставщик принимает». Проба, читающая хелпер, зеленела бы
// и в том мире, где место вызова хелпер игнорирует, — а именно так этот дефект и выглядел: полный
// промпт прогона уходил в поле, у которого локальный потолок 600 рун, и КАЖДЫЙ реалистичный
// 3D-прогон умирал терминальным provider_bad_request, не доехав до сети.

// threedSteerStand — поставщик, который ЗАПОМИНАЕТ тело сабмита. Отвечает так же, как настоящий:
// {"result": "<task id>"}.
type threedSteerStand struct {
	srv  *httptest.Server
	body chan string
}

func newThreedSteerStand(t *testing.T) *threedSteerStand {
	t.Helper()
	st := &threedSteerStand{body: make(chan string, 4)}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		st.body <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"task-777"}`))
	}))
	t.Cleanup(st.srv.Close)
	return st
}

// sentPrompt достаёт `texture_prompt` из перехваченного тела. Разбирается JSON, а не ищется
// подстрока: подстрока сошлась бы и с полем, которого в запросе нет.
func (st *threedSteerStand) sentPrompt(t *testing.T) string {
	t.Helper()
	select {
	case raw := <-st.body:
		var body struct {
			TexturePrompt string `json:"texture_prompt"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("тело сабмита не разобралось: %v (%s)", err, raw)
		}
		return body.TexturePrompt
	default:
		t.Fatal("сабмита не было вовсе: запрос до поставщика не доехал")
		return ""
	}
}

func newThreedSteerProvider(t *testing.T, baseURL string) Provider {
	t.Helper()
	return NewThreedProvider(meshy.New(meshy.Config{APIKey: "k", BaseURL: baseURL}))
}

// longRunPrompt — промпт ПРОГОНА, а не строка-заполнитель: те же разделы и тот же разделитель
// («\n\n»), которыми их склеивает composePrompt. Длина заведомо за потолком поставщика.
func longRunPrompt() string {
	var b strings.Builder
	b.WriteString("make the shoulder softer and the hem straight")
	for i := 0; i < 40; i++ {
		b.WriteString("\n\nreferences:\n- front — the collar stands, the placket is hidden, the cuff is doubled")
	}
	return b.String()
}

// ПОЛНЫЙ ПРОМПТ ПРОГОНА НЕ УБИВАЕТ 3D-МАРШРУТ.
//
// meshy.Submit отказывает выше meshy.MaxTexturePrompt ЛОКАЛЬНО, до сети, а classify читает этот
// отказ как терминальный provider_bad_request. Значит без урезания подсказки ни один прогон с
// живым промптом не доезжает до поставщика вовсе.
func TestThreedSteerFitsTheProviderCeiling(t *testing.T) {
	st := newThreedSteerStand(t)
	p := newThreedSteerProvider(t, st.srv.URL)

	prompt := longRunPrompt()
	if len([]rune(prompt)) <= meshy.MaxTexturePrompt {
		t.Fatalf("проба бессмысленна: промпт %d рун, потолок %d", len([]rune(prompt)), meshy.MaxTexturePrompt)
	}
	out, err := p.Execute(context.Background(), Job{
		RunID:      42,
		Kind:       "threed",
		Prompt:     prompt,
		References: []string{"https://example.com/front.png"},
	})
	if err != nil {
		t.Fatalf("сабмит отказан: %v", err)
	}
	if out == nil || out.RequestID != "task-777" {
		t.Fatalf("задание не принято поставщиком: %+v", out)
	}

	sent := st.sentPrompt(t)
	if n := len([]rune(sent)); n > meshy.MaxTexturePrompt {
		t.Errorf("в сеть ушло %d рун подсказки при потолке %d", n, meshy.MaxTexturePrompt)
	}
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: пустая подсказка тоже «влезает в потолок», и без этой строки проба
	// зеленела бы на реализации, которая просто выбрасывает поле целиком.
	if strings.TrimSpace(sent) == "" {
		t.Error("подсказка ушла пустой: поверхность перестала управляться вовсе")
	}
	if !strings.HasPrefix(prompt, sent) {
		t.Errorf("ушло не начало промпта, а что-то своё: %q", sent)
	}
	// РЕЗ ПО ГРАНИЦЕ РАЗДЕЛА: обрезанная посреди фразы подсказка велела бы поставщику половину
	// предложения. Разделы склеены «\n\n», значит и рез обязан лечь туда.
	if strings.HasSuffix(sent, "\n") || strings.Contains(sent[len(sent)-1:], " ") {
		t.Errorf("рез лёг посреди фразы: %q", sent[max(0, len(sent)-40):])
	}
}

// КОРОТКАЯ ПОДСКАЗКА ДОЕЗЖАЕТ ДОСЛОВНО.
//
// Это вторая половина той же пробы и она обязательна: без неё «влезает в потолок» доказывалось бы
// реализацией, которая режет ВСЕГДА — и тогда прогон, чья подсказка и так коротка, всё равно терял
// бы свои слова.
func TestThreedShortSteerTravelsWhole(t *testing.T) {
	st := newThreedSteerStand(t)
	p := newThreedSteerProvider(t, st.srv.URL)

	const prompt = "matte black technical nylon, no logos"
	if _, err := p.Execute(context.Background(), Job{
		RunID: 43, Kind: "threed", Prompt: prompt,
		References: []string{"https://example.com/front.png"},
	}); err != nil {
		t.Fatalf("сабмит отказан: %v", err)
	}
	if sent := st.sentPrompt(t); sent != prompt {
		t.Errorf("подсказка приехала изменённой: %q, ждали %q", sent, prompt)
	}
}

// ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ САМОГО ПОТОЛКА: без урезания поставщик отказывает ЛОКАЛЬНО, и отказ
// терминальный. Проба держит сам довод починки — то, что дефект был не косметическим.
func TestMeshyRefusesAnUncutRunPromptLocally(t *testing.T) {
	st := newThreedSteerStand(t)
	c := meshy.New(meshy.Config{APIKey: "k", BaseURL: st.srv.URL})

	_, err := c.Submit(context.Background(), meshy.Request{
		ImageURLs:     []string{"https://example.com/front.png"},
		TexturePrompt: longRunPrompt(),
	})
	if !errors.Is(err, meshy.ErrPromptTooLong) {
		t.Fatalf("err = %v, ждали meshy.ErrPromptTooLong", err)
	}
	if v := classify(err); v.Retryable {
		t.Errorf("отказ классифицирован как погода (%s): пять оплаченных попыток на неисправимую длину", v.Code)
	}
	select {
	case raw := <-st.body:
		t.Errorf("запрос всё-таки ушёл в сеть: %s", raw)
	default:
	}
}
