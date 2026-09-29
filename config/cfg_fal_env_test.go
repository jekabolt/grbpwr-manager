package config

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFalConfigFromEnv proves the BINDINGS, one variable at a time.
//
// viper.AutomaticEnv is deliberately off in this package, so a key reaches the process ONLY through
// an explicit viper.BindEnv line. A forgotten line does not fail, does not log, and looks exactly
// like a correctly-unset optional override: the value stays at its default while whoever set it in
// the DigitalOcean dashboard believes it took effect. The only way to tell those two apart is to
// set every variable to a value nothing else would produce and insist it arrives.
//
// The variables are set in the DO DASHBOARD, never in .do/app.yaml: pushing the spec deploys prod
// and overwrites live SECRET values with the empty ones in the file.
func TestFalConfigFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	t.Setenv("FAL_KEY", "fal-test-key")
	t.Setenv("FAL_BASE_URL", "https://fal.example.test")
	t.Setenv("FAL_MODEL_3D", "vendor/model/v9/multi-view-to-3d")
	t.Setenv("FAL_HTTP_TIMEOUT", "45s")
	t.Setenv("FAL_POLL_INTERVAL", "7s")
	t.Setenv("FAL_POLL_TIMEOUT", "20m")
	t.Setenv("FAL_DOWNLOAD_TIMEOUT", "9m")
	t.Setenv("FAL_UNIT_USD", "0.75")
	t.Setenv("FAL_MODEL_CUTOUT", "vendor/matting/v9")
	t.Setenv("FAL_UNIT_USD_CUTOUT", "0.045")
	t.Setenv("FAL_UNITS_CEILING_3D", "3")
	t.Setenv("FAL_MODEL_IMAGE", "vendor/pic/v9")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "fal-test-key", cfg.Fal.APIKey,
		"FAL_KEY must reach the config: it is the whole switch, and unbound it reads as "+
			"'3D is not configured' on a deployment where the key WAS set")
	assert.Equal(t, "https://fal.example.test", cfg.Fal.BaseURL)
	assert.Equal(t, "vendor/model/v9/multi-view-to-3d", cfg.Fal.Model3D,
		"the slug must be overridable without a deploy: a retired one is a 404 on every press")
	assert.Equal(t, 45*time.Second, cfg.Fal.HTTPTimeout)
	assert.Equal(t, 7*time.Second, cfg.Fal.PollInterval)
	assert.Equal(t, 20*time.Minute, cfg.Fal.PollTimeout)
	assert.Equal(t, 9*time.Minute, cfg.Fal.DownloadTimeout,
		"the download budget is separate from the poll ceiling on purpose — a fetch cut by the "+
			"wait loses an artifact that is already paid for and whose link expires")
	assert.InDelta(t, 0.75, cfg.Fal.UnitUSD, 1e-9)
	assert.InDelta(t, 3, cfg.Fal.UnitsCeiling3D, 1e-9,
		"FAL_UNITS_CEILING_3D sizes the 3D reservation under a tariff; unbound, a tariff closes the 3D door")

	// ─── ВТОРОЙ МАРШРУТ ТОГО ЖЕ ТРАНСПОРТА: СВОЙ СЛАГ И СВОЙ ТАРИФ ───
	assert.Equal(t, "vendor/matting/v9", cfg.Fal.ModelCutout,
		"аварийный руль на случай снятого слага: без него единственный способ съехать с мёртвой "+
			"модели — деплой")
	assert.InDelta(t, 0.045, cfg.Fal.UnitUSDCutout, 1e-9,
		"у выреза СВОЙ тариф: единицы двух маршрутов отличаются на два порядка, и одно число "+
			"оценило бы двухцентовую операцию в доллар")

	// THE VALUES MUST SURVIVE THE CONSTRUCTOR, not merely land in the struct: a default applied
	// over a configured value is the same silent failure one layer down.
	assert.Equal(t, "vendor/pic/v9", cfg.Fal.ModelImage,
		"FAL_MODEL_IMAGE (H3) — the image transport's default slug; unbound, the override is silently ignored")
	c := fal.New(cfg.Fal)
	assert.True(t, c.Enabled())
	assert.Equal(t, "vendor/pic/v9", c.ModelImage())
	assert.Equal(t, 7*time.Second, c.PollInterval())
	assert.Equal(t, 20*time.Minute, c.PollTimeout())
	assert.Equal(t, "vendor/model/v9/multi-view-to-3d", c.Model())
	assert.Equal(t, "1.5", c.CostUSD(2).String(),
		"the configured unit rate must be the one that prices a build")

	// ⚠ И ОБА ЧИСЛА ПРОВЕРЯЮТСЯ НА ОДНОМ КЛИЕНТЕ, ПОТОМУ ЧТО ПУТАНИЦА МЕЖДУ НИМИ — ЭТО И ЕСТЬ
	// ДЕФЕКТ, РАДИ КОТОРОГО ЗАВЕДЕНА ВТОРАЯ ПЕРЕМЕННАЯ. Ставки нарочно разные (0.75 против 0.045),
	// так что вычисление выреза по ставке 3D видно числом: 0.09, а не 1.5.
	assert.Equal(t, "vendor/matting/v9", c.ModelCutout())
	assert.Equal(t, "0.09", c.CostCutoutUSD(2).String(),
		"вырез считается по FAL_UNIT_USD_CUTOUT, а не по FAL_UNIT_USD")
}

// TestTheCutoutSlugAndTariffFALL_BACK_TO_THE_CODE_DEFAULTS — вторая половина той же проводки.
//
// ⚠ УМОЛЧАНИЕ СЛАГА — ЛИЦЕНЗИОННОЕ РЕШЕНИЕ, А НЕ ВКУСОВОЕ, и проверяется здесь именно поэтому:
// соседние по качеству веса (BRIA RMBG-2.0) живут под CC BY-NC, и попасть на них проще всего НЕ
// выбрав ничего. А умолчание тарифа — про деньги в другую сторону: без него неоценённый вырез
// записался бы нулём, то есть «бесплатным», и дневная книга не увидела бы траты.
func TestTheCutoutSlugAndTariffFALL_BACK_TO_THE_CODE_DEFAULTS(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("FAL_KEY", "fal-test-key")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Empty(t, cfg.Fal.ModelCutout, "ничего не задано — значит в конфиге пусто")
	assert.Zero(t, cfg.Fal.UnitUSDCutout)

	c := fal.New(cfg.Fal)
	assert.Equal(t, "fal-ai/birefnet/v2", c.ModelCutout(), "MIT-модель, а не CC BY-NC")
	assert.Equal(t, fal.EstimatedCutoutUSD().String(), c.CostCutoutUSD(3).String(),
		"без тарифа умножать не на что: отвечаем оценкой за ЗАПРОС, а не нулём и не выдумкой")
}

// TestFalUnsetIsAnHonestLock is the other half. With no key the client is disabled — and that must
// stay a CLOSED BUTTON that NAMES THE VARIABLE, not a queued run: a 3D run submitted to a provider
// nobody can call would sit in `pending` until a sweeper eventually called it abandoned, with the
// reason nowhere on the screen.
func TestFalUnsetIsAnHonestLock(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("FAL_KEY", "") // explicit: an empty variable is the same as an absent one

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Empty(t, cfg.Fal.APIKey)
	assert.False(t, fal.New(cfg.Fal).Enabled())
	assert.Contains(t, fal.ErrNotConfigured.Error(), "FAL_KEY",
		"the refusal a person reads has to name the setting they can act on")
}

// TestTheThreedRouteIsChosenByAWORD_AND_DEFAULTS_TO_FAL.
//
// ⚠ THIS SETTING DECIDES WHO GETS PAID for a turntable, so it is an explicit word rather than an
// inference from which key happens to be present — a rule like «use fal if FAL_KEY is set» would
// move the owner's money between two vendors as a side effect of typing a key into a dashboard.
//
// The default is `fal` because the owner named that provider. An unknown word normalises to the
// default rather than refusing the boot (a typo in a route name must not take the backend down) and
// app.go logs the effective route on every start.
func TestTheThreedRouteIsChosenByAWORD_AND_DEFAULTS_TO_FAL(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderFal, effectiveThreedProvider(cfg.DesignGen),
		"unset must mean the provider the owner asked for by name")

	t.Setenv("DESIGN_THREED_PROVIDER", "meshy")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderMeshy, effectiveThreedProvider(cfg.DesignGen))

	t.Setenv("DESIGN_THREED_PROVIDER", "MESHY")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderMeshy, effectiveThreedProvider(cfg.DesignGen),
		"an operator typing the word in capitals meant the same vendor")

	t.Setenv("DESIGN_THREED_PROVIDER", "notaprovider")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderFal, effectiveThreedProvider(cfg.DesignGen),
		"a typo falls back to the default; it must not take the boot down and must not be silent — "+
			"app.go logs the route it wired")
}

// effectiveThreedProvider runs the config through the SAME normalisation app.go runs before it
// picks a route — where the word is lower-cased and an unknown one falls back. Asking the raw
// struct would test viper rather than the behaviour, and would have missed the very defect this
// helper was written after: app.go once compared the RAW value, so `MESHY` wired fal in silence.
func effectiveThreedProvider(c designgen.Config) string {
	designgen.Normalize(&c)
	return c.ThreedProvider
}

// TestTheGenericFalRoutesAreBOUND_ONE_VARIABLE_AT_A_TIME — PLAYGROUND phase 3 (B-12). Six new
// variables, each set to a value nothing else produces, each asserted on the struct AND through the
// constructor: an unbound variable reads as the default without a word, and the owner who typed a
// tariff into the dashboard would believe the reserve was sized by it.
func TestTheGenericFalRoutesAreBOUND_ONE_VARIABLE_AT_A_TIME(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("FAL_KEY", "fal-test-key")
	t.Setenv("FAL_MODEL_OUTPAINT", "fal-ai/bria/expand")
	t.Setenv("FAL_MODEL_FILL", "vendor/fill/v9")
	t.Setenv("FAL_UNIT_USD_OUTPAINT", "0.011")
	t.Setenv("FAL_UNITS_CEILING_OUTPAINT", "7")
	t.Setenv("FAL_UNIT_USD_FILL", "0.013")
	t.Setenv("FAL_UNITS_CEILING_FILL", "5")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, "fal-ai/bria/expand", cfg.Fal.ModelOutpaint)
	assert.Equal(t, "vendor/fill/v9", cfg.Fal.ModelFill)
	assert.InDelta(t, 0.011, cfg.Fal.UnitUSDOutpaint, 1e-9)
	assert.InDelta(t, 7, cfg.Fal.UnitsCeilingOutpaint, 1e-9)
	assert.InDelta(t, 0.013, cfg.Fal.UnitUSDFill, 1e-9)
	assert.InDelta(t, 5, cfg.Fal.UnitsCeilingFill, 1e-9)

	c := fal.New(cfg.Fal)
	assert.Equal(t, "fal-ai/bria/expand", c.ModelFor(fal.RouteOutpaint))
	assert.Equal(t, "vendor/fill/v9", c.ModelFor(fal.RouteFill))
	out, ok := c.RouteCeilingUSD(fal.RouteOutpaint)
	assert.True(t, ok)
	assert.Equal(t, "0.077", out.String(), "outpaint reserve = its own tariff × its own ceiling")
	fill, ok := c.RouteCeilingUSD(fal.RouteFill)
	assert.True(t, ok)
	assert.Equal(t, "0.065", fill.String(), "fill reserve = its own tariff × its own ceiling")
	assert.Equal(t, "0.022", c.CostRouteUSD(fal.RouteOutpaint, 2).String())
	assert.Equal(t, "0.026", c.CostRouteUSD(fal.RouteFill, 2).String())
}

// TestTheGenericFalRoutesFALL_BACK_TO_THE_CODE_DEFAULTS — nothing set: the code slugs and the code
// ceilings, and both reserves are bounded (the shape beta ships with).
func TestTheGenericFalRoutesFALL_BACK_TO_THE_CODE_DEFAULTS(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("FAL_KEY", "fal-test-key")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	c := fal.New(cfg.Fal)
	assert.Equal(t, fal.DefaultModelOutpaint, c.ModelFor(fal.RouteOutpaint))
	assert.Equal(t, fal.DefaultModelFill, c.ModelFor(fal.RouteFill))
	for _, r := range []fal.Route{fal.RouteOutpaint, fal.RouteFill} {
		got, ok := c.RouteCeilingUSD(r)
		assert.True(t, ok, "no tariff → the code ceiling, bounded")
		assert.Equal(t, fal.EstimatedRouteUSD(r).String(), got.String())
	}
}
