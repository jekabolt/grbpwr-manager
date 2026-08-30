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
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ КЛАССА «ОБРЕЗКА, ПРИНЯТАЯ ЗА ЦЕЛОЕ».
//
// ЧТО БЫЛО. Снимок прогона допускает до 24 референсов; картиночная модель берёт 16, Meshy — 4. Обе
// дороги МОЛЧА ОТРЕЗАЛИ ХВОСТ — растровая с warn-строкой, которой автор прогона не видит никогда,
// трёхмерная вообще без единого слова — и закрывали прогон как `done`. Замороженный снимок при
// этом продолжал утверждать, что модели ушли все 24 картинки. Какие именно уехали, не записано
// нигде и уже не будет: снимок не чинится задним числом.
//
// ЧТО СТАЛО. Обе дороги ОТКАЗЫВАЮТ, и отказывает тот, кто знает потолок, — сам клиент поставщика,
// ЛОКАЛЬНО, до сетевого вызова. Отсюда три свойства сразу: денег не потрачено, потолок живёт в
// одном месте (копия числа рядом с вызывающим — это второе число), и у отказа есть sentinel,
// который классификатор знает как «не погода».

// РАСТРОВАЯ ДОРОГА: 17 картинок — отказ, и ни одного платного вызова.
//
// МУТАЦИЯ: вернуть `refs = refs[:16]` в imageProvider.Execute — вызов уходит, стоит денег, и
// прогон закрывается `done` с картинкой, собранной не из того, что записано в снимке.
func TestImageRouteRefusesMoreReferencesThanTheModelTakes(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++ }))
	defer srv.Close()

	p := NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: srv.URL}))
	refs := make([]string, orimages.MaxInputReferences+1)
	for i := range refs {
		refs[i] = "https://media.grbpwr.com/x.png"
	}

	_, err := p.Execute(context.Background(), Job{
		RunID: 1, Kind: entity.DesignRunKindFlat, Prompt: "draw it", References: refs,
	})
	require.Error(t, err, "обрезка молча закрывала бы прогон done с картинкой не из того состава")
	require.ErrorIs(t, err, orimages.ErrBadRequest)
	require.Zero(t, called, "отказ обязан стоить ноль: он вынесен ДО сетевого вызова")

	// ⚠ ВТОРАЯ ПОЛОВИНА, БЕЗ КОТОРОЙ ПЕРВАЯ ВЫПОЛНИМА ЗАГЛУШКОЙ «всегда отказывать»: ровно на
	// потолке запрос уходит.
	require.Equal(t, verdict{Retryable: false, Code: CodeBadRequest, State: entity.DesignAttemptFailed},
		classify(err), "повтор пошлёт ТОТ ЖЕ список: пять оплаченных кругов ни за что")
}

// ТРЁХМЕРНАЯ ДОРОГА: 5 картинок — отказ, и ни одной отправки.
//
// МУТАЦИЯ: вернуть `refs = refs[:meshy.MaxImages]` в threedProvider.Execute. Отправка — это и есть
// платёж (Submit платный, Collect бесплатный), так что молчаливая обрезка здесь покупала модель,
// собранную из четырёх картинок из девяти, и записывала в историю девять.
func TestThreedRouteRefusesMoreReferencesThanMeshyTakes(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++ }))
	defer srv.Close()

	p := NewThreedProvider(meshy.New(meshy.Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second}))
	refs := make([]string, meshy.MaxImages+1)
	for i := range refs {
		refs[i] = "https://media.grbpwr.com/x.png"
	}

	_, err := p.Execute(context.Background(), Job{
		RunID: 2, Kind: entity.DesignRunKindThreed, Prompt: "matte black nylon", References: refs,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, meshy.ErrImageCount)
	require.Zero(t, called, "отправка — это платёж; отказ обязан случиться до неё")
	require.False(t, classify(err).Retryable)
}

// НИ ОДИН ИЗ ДВУХ ОТКАЗОВ НЕ ЛОЖНЫЙ: ровно на потолке обе дороги работают. Без этой пробы обе
// верхние выполнимы одной строкой «всегда возвращать ошибку».
func TestBothRoutesAcceptExactlyTheCeiling(t *testing.T) {
	t.Run("image", func(t *testing.T) {
		called := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called++
			w.Header().Set("Content-Type", "application/json")
			// Пустой список картинок при 200 — известный отказ клиента; здесь важно, что запрос
			// ДОШЁЛ, а не чем он кончился.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"images":[]}}]}`))
		}))
		defer srv.Close()

		p := NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: srv.URL}))
		refs := make([]string, orimages.MaxInputReferences)
		for i := range refs {
			refs[i] = "https://media.grbpwr.com/x.png"
		}
		_, err := p.Execute(context.Background(), Job{
			RunID: 3, Kind: entity.DesignRunKindFlat, Prompt: "draw it", References: refs,
		})
		require.Error(t, err)
		require.False(t, errors.Is(err, orimages.ErrBadRequest),
			"на самом потолке запрос законен: отказ здесь означал бы, что мы отрезали не хвост, а дверь")
		require.Equal(t, 1, called, "запрос обязан ДОЙТИ до поставщика")
	})

	t.Run("threed", func(t *testing.T) {
		called := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"task-1"}`))
		}))
		defer srv.Close()

		p := NewThreedProvider(meshy.New(meshy.Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second}))
		refs := make([]string, meshy.MaxImages)
		for i := range refs {
			refs[i] = "https://media.grbpwr.com/x.png"
		}
		out, err := p.Execute(context.Background(), Job{
			RunID: 4, Kind: entity.DesignRunKindThreed, Prompt: "matte black nylon", References: refs,
		})
		require.NoError(t, err)
		require.Equal(t, "task-1", out.RequestID)
		require.Equal(t, 1, called)
	})
}
