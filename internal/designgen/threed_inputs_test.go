package designgen

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/stretchr/testify/require"
)

// ═══ V-14, СЕРВЕРНАЯ ПОЛОВИНА: В СБОРКУ 3D УЕЗЖАЮТ ЕЁ ПЛИТЫ, А НЕ РЕФЕРЕНСЫ КАРТОЧКИ ═══════════
//
// Пробы ниже держат ПОВЕДЕНИЕ (что именно оказалось в job.References), а не текст исходника, и
// каждая умеет краснеть: мутация «убрать фильтр» валит первые две, мутация «фильтровать всем
// родам» валит контрольную.

// threedRun — прогон 3D с четырьмя плитами верстака И тем, чего у 3D быть не должно: двумя
// референсами карточки, явно названным дополнительным медиа и фотографией ткани из рецепта.
// Ровно такой снимок собирает designAssembleInputs на живой карточке.
func threedRun() entity.DesignRun {
	r := testRun(1, entity.DesignRunKindThreed)
	r.Params = entity.RawJSON(`{
	  "views": ["front","back","side_l","side_r"],
	  "extra_input_media_ids": [77],
	  "colour": {"fabric_media_id": 88},
	  "threed": {"presentation": "model", "body_type": "athletic", "fit_override": "slim"}
	}`)
	r.Inputs = entity.RawJSON(`{
	  "garment_note": "boxy overshirt",
	  "refs": [
	    {"media_id": 90, "role": "silhouette", "note": "the shape"},
	    {"media_id": 91, "role": "detail", "note": "this collar"}
	  ],
	  "slots": [
	    {"view_key": "side_r", "media_id": 4},
	    {"view_key": "back",   "media_id": 2},
	    {"view_key": "front",  "media_id": 1},
	    {"view_key": "side_l", "media_id": 3}
	  ]
	}`)
	return r
}

// TestThreedSendsOnlyItsOwnPlates — САМ ДЕФЕКТ V-14, поставленный числом.
//
// До починки этот снимок давал СЕМЬ картинок: четыре плиты плюс два референса карточки, плюс
// дополнительное медиа, плюс свотч ткани. Провайдер принимает 1..4 (meshy.MaxImages) и отказывает
// локально — то есть каждый прогон 3D на живой карточке умирал у двери, не начавшись.
func TestThreedSendsOnlyItsOwnPlates(t *testing.T) {
	job, err := buildJob(context.Background(), media(1, 2, 3, 4, 77, 88, 90, 91), threedRun(), "medium")
	require.NoError(t, err)

	require.Equal(t, []string{
		"https://cdn.example/m/1.png",
		"https://cdn.example/m/2.png",
		"https://cdn.example/m/3.png",
		"https://cdn.example/m/4.png",
	}, job.References, "3D крутит ВИДЫ ИЗДЕЛИЯ: только плиты своего верстака, front первым")

	for _, id := range []string{"/77.", "/88.", "/90.", "/91."} {
		for _, u := range job.References {
			require.NotContains(t, u, id, "в сборку 3D уехала картинка, которая не вид изделия")
		}
	}
}

// TestThreedFitsTheProvidersCeiling — ТА ЖЕ ПРАВДА, СКАЗАННАЯ ПРОВАЙДЕРОМ, а не нашим equal.
//
// Проба, которая только считает элементы, сторожит наше представление о потолке. Эта зовёт тот же
// локальный отказ, который стоит на пути настоящего прогона, поэтому она краснеет ровно тогда,
// когда краснел бы прод.
func TestThreedFitsTheProvidersCeiling(t *testing.T) {
	job, err := buildJob(context.Background(), media(1, 2, 3, 4, 77, 88, 90, 91), threedRun(), "medium")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(job.References), meshy.MinImages,
		"сборке нужен хотя бы фронт")
	require.LessOrEqual(t, len(job.References), meshy.MaxImages,
		"meshy.Submit отказывает локально выше этого числа — прогон не начнётся вовсе")
}

// TestRenderStillCarriesTheCardsReferences — ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ, и без него первая проба
// зеленела бы и от «выбросить референсы у всех родов».
//
// Фабрик-рендер РИСУЕТ по этим картинкам: референс карточки, дополнительное медиа и фотография
// ткани — его законный вход, и потолка в четыре картинки у него нет (маршрут другой, OpenRouter).
func TestRenderStillCarriesTheCardsReferences(t *testing.T) {
	r := threedRun()
	r.Kind = entity.DesignRunKindRender

	job, err := buildJob(context.Background(), media(1, 2, 3, 4, 77, 88, 90, 91), r, "medium")
	require.NoError(t, err)
	require.Len(t, job.References, 8, "у рендера вход не сужается")
	require.Contains(t, strings.Join(job.References, " "), "/90.",
		"референс карточки обязан доезжать до рендера")
	require.Contains(t, strings.Join(job.References, " "), "/88.",
		"фотография ткани обязана доезжать до рендера")
}

// TestThreedWithNoPlatesSendsNothing — ЧЕСТНАЯ ПУСТОТА.
//
// Прогону 3D, у которого верстак пуст, крутить нечего. Раньше он молча уезжал в сборку с
// референсами настроения вместо видов, закрывался `done` и списывал деньги за модель неизвестно
// чего; теперь провайдер отказывает своим ErrImageCount, и отказ читается человеком.
func TestThreedWithNoPlatesSendsNothing(t *testing.T) {
	r := testRun(1, entity.DesignRunKindThreed)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":90},{"media_id":91}]}`)

	job, err := buildJob(context.Background(), media(90, 91), r, "medium")
	require.NoError(t, err)
	require.Empty(t, job.References)

	// НАСТОЯЩИЙ ОТКАЗ ПРОВАЙДЕРА, а не наше представление о нём: клиент включён (ключ задан), и
	// счётчик картинок проверяется ЛОКАЛЬНО, до всякой сети — поэтому проба не ходит наружу.
	client := meshy.New(meshy.Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"})
	_, subErr := client.Submit(context.Background(), meshy.Request{ImageURLs: job.References})
	require.ErrorIs(t, subErr, meshy.ErrImageCount,
		"провайдер обязан отказать пустой сборке, а не собрать что попало")
}

// TestThreedPromptNamesTheBody — V-15 на проводе.
//
// `model_id` в промпт не едет и ехать ему некуда (у снимка нет поля под имя модели), поэтому
// телосложение — ЕДИНСТВЕННОЕ, что этот прогон говорит про тело словами. Без этой строки выбор
// телосложения был бы органом без действия.
func TestThreedPromptNamesTheBody(t *testing.T) {
	job, err := buildJob(context.Background(), media(1, 2, 3, 4), threedRun(), "medium")
	require.NoError(t, err)
	require.Contains(t, job.Prompt, "body athletic")
	require.Contains(t, job.Prompt, "presentation model")
	require.Contains(t, job.Prompt, "fit slim")
}

// TestThreedPromptSaysNothingAboutAnUnstatedBody — пустое поле МОЛЧИТ, а не выдумывает.
func TestThreedPromptSaysNothingAboutAnUnstatedBody(t *testing.T) {
	r := threedRun()
	r.Params = entity.RawJSON(`{"threed": {"presentation": "air"}}`)

	job, err := buildJob(context.Background(), media(1, 2, 3, 4), r, "medium")
	require.NoError(t, err)
	require.NotContains(t, job.Prompt, "body ")
	require.Contains(t, job.Prompt, "presentation air")
}
