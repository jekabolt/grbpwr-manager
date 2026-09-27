package designgen

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// providerQualityWords — ЧЕТЫРЕ СЛОВА, КОТОРЫЕ ПРИНИМАЕТ ЭНДПОИНТ, и список тут стоит РОВНО затем,
// чтобы опечатка в константе или в переменной среды падала здесь, а не платным 400 на проде.
// Источник — orimages.Request.Quality («the same four on every GPT Image slug, measured
// 2026-08-30»); это НЕ вторая копия дила, это проверка того, что дил произносит существующее слово.
var providerQualityWords = map[string]bool{"auto": true, "low": true, "medium": true, "high": true}

// TestFlatAsksForTheTopOfTheDial — M-3, дословно: «генерить флеты в максимально доступном
// разрешении».
//
// Меряется НЕ поле конфига, а то, что реально доехало до провайдера: правка, которая переименует
// поле или забудет протащить его через buildJob, обязана краснеть.
func TestFlatAsksForTheTopOfTheDial(t *testing.T) {
	for _, c := range []struct {
		kind string
		want string
	}{
		{entity.DesignRunKindFlat, ImageQualityMax},
		// СОСЕДИ ОСТАЛИСЬ НА СВОЁМ ПОЛОЖЕНИИ. Без этой половины «подняли всем» и «подняли флэту»
		// неразличимы, а разница между ними — счёт за каждый рендер, перекрас и паттерн.
		{entity.DesignRunKindRender, DefaultConfig().ImageQuality},
		{entity.DesignRunKindRecolor, DefaultConfig().ImageQuality},
		{entity.DesignRunKindPattern, DefaultConfig().ImageQuality},
	} {
		t.Run(c.kind, func(t *testing.T) {
			img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
			w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

			require.NoError(t, w.execute(context.Background(), testRun(1, c.kind), "tok"))
			require.Len(t, img.calls, 1)
			require.Equal(t, c.want, img.calls[0].Quality)
		})
	}
}

// TestEveryKindAsksForAWordTheProviderAccepts — сторож от опечатки в самом слове.
func TestEveryKindAsksForAWordTheProviderAccepts(t *testing.T) {
	c := DefaultConfig()
	applyDefaults(&c)
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindRecolor,
		entity.DesignRunKindPattern, entity.DesignRunKindVector, entity.DesignRunKindThreed,
	} {
		q := c.QualityFor(kind)
		require.Truef(t, providerQualityWords[q], "%s asks for %q, which the endpoint does not accept", kind, q)
	}
}

// TestFlatQualityStaysAKnob — потолок дила стоит умолчанием, а не законом: счёт за `high` реальный,
// и владелец обязан иметь возможность отвести его без деплоя.
func TestFlatQualityStaysAKnob(t *testing.T) {
	t.Setenv(EnvImageQualityFlat, "medium")
	c := ConfigFromEnv()
	require.Equal(t, "medium", c.QualityFor(entity.DesignRunKindFlat))
	require.Equal(t, "medium", c.QualityFor(entity.DesignRunKindRender))

	t.Setenv(EnvImageQualityFlat, "")
	require.Equal(t, ImageQualityMax, ConfigFromEnv().QualityFor(entity.DesignRunKindFlat))
}

// TestGlobalDialStillMovesTheOtherKinds — вторая половина того же: общий дил не перестал работать.
func TestGlobalDialStillMovesTheOtherKinds(t *testing.T) {
	t.Setenv(EnvImageQuality, "low")
	c := ConfigFromEnv()
	require.Equal(t, "low", c.QualityFor(entity.DesignRunKindRender))
	require.Equal(t, ImageQualityMax, c.QualityFor(entity.DesignRunKindFlat),
		"общий дил вниз не обязан утаскивать флэт: у флэта свой")
}

// TestTheFlatsTopQualityDoesNotLEAK_INTO_A_RESOLUTION_ENGINE — B-16: the flat asks for the top of the
// QUALITY dial (above), and a Gemini flat must carry its tier's resolution instead, with no quality
// word at all (12-PROVIDERS §E: the catalogue lists no `quality` on the slug). MUTATION (measured red):
// `job.Quality, job.Resolution = "", t.Value` → `job.Resolution = t.Value` in applyImageOptions.
func TestTheFlatsTopQualityDoesNotLEAK_INTO_A_RESOLUTION_ENGINE(t *testing.T) {
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{Image: img})
	w.c.EngineGemini = true // the flag is on at the pickup (off refuses the run: G-03, Codex 6)
	r := testRun(1, entity.DesignRunKindFlat)
	r.Params = entity.RawJSON(`{"image":{"model":"google/gemini-3-pro-image","quality":"medium"}}`)

	require.NoError(t, w.execute(context.Background(), r, "tok"))
	require.Len(t, img.calls, 1)
	require.Equal(t, "2K", img.calls[0].Resolution)
	require.Empty(t, img.calls[0].Quality, "the flat's %q is a GPT word", ImageQualityMax)
}
