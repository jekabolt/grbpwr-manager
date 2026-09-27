package designgen

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ═══ G-02 r2 (Codex 1 + 2): THE ROUTE IS ASKED AGAIN AT THE PICKUP ═══
//
// The door checks the run's options against the route configured WHEN THE RUN WAS CREATED. A run can
// sit pending across a redeploy that moves FAL_MODEL_3D to the hitem3d override or turns
// DESIGN_THREED_PBR off; the worker must then refuse the fresh submit — free, terminal, named — rather
// than pay for a build whose options the new route drops.

const hitemSlug = "hitem3d/hi3d/v3.0/multi-view-to-3d"

// routeRun — steerRun (a real frozen snapshot through the real buildJob) with its threed block
// replaced.
func routeRun(id int, threed string) entity.DesignRun {
	r := steerRun(id)
	old := `"threed": {"presentation": "model", "body_type": "athletic", "fit_override": "slim"}`
	params := string(r.Params)
	if !strings.Contains(params, old) {
		panic("steerRun's threed block moved; update routeRun")
	}
	r.Params = entity.RawJSON(strings.Replace(params, old, `"threed": `+threed, 1))
	return r
}

func routeWorker(t *testing.T, st *fakeStore, prov Provider, pbr bool) *Worker {
	t.Helper()
	w := steerWorker(t, st, prov)
	w.c.ThreedPBR = pbr
	return w
}

// TestTheWorkerREFUSES_AN_OPTION_THE_ROUTE_NO_LONGER_READS — every row is a run the door accepted
// under one route and the worker picks up under another. MUTATIONS (each measured red): the
// threedUnreadAtSubmit call removed from execute (every refusal row submits and pays);
// ThreedRouteOf ignoring its pbr argument (the pbr row); threedRouteOptions listing surface_hint
// regardless of readsText (the hitem3d hint row); ThreedUnread without the untextured-hint clause
// (both texture-off hint rows).
func TestTheWorkerREFUSES_AN_OPTION_THE_ROUTE_NO_LONGER_READS(t *testing.T) {
	for _, c := range []struct {
		name   string
		model  string // fal slug; "" = direct Meshy
		pbr    bool
		threed string
		field  string
	}{
		{"detailed, now on the hitem3d override", hitemSlug, true, `{"quality": "detailed"}`, ThreedOptionQuality},
		{"untextured, now on the hitem3d override", hitemSlug, true, `{"texture": "off"}`, ThreedOptionTexture},
		{"pbr, now with DESIGN_THREED_PBR off", "meshy/v7/multi-image-to-3d", false, `{"pbr": "on"}`, ThreedOptionPBR},
		{"surface words, now on the hitem3d override", hitemSlug, true, `{"surface_hint": "matte red cotton"}`, ThreedOptionSurfaceHint},
		{"surface words on an untextured fal meshy build", "meshy/v7/multi-image-to-3d", true,
			`{"texture": "off", "surface_hint": "matte red cotton"}`, ThreedOptionSurfaceHint},
		{"surface words on an untextured direct Meshy build", "", true,
			`{"texture": "off", "surface_hint": "matte red cotton"}`, ThreedOptionSurfaceHint},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := &fakeStore{}
			var body chan string
			var prov Provider
			if c.model == "" {
				stand := newThreedSteerStand(t)
				body, prov = stand.body, newThreedSteerProvider(t, stand.srv.URL)
			} else {
				stand := newFalSubmitStand(t)
				body, prov = stand.body, falRoute(t, stand.srv.URL, c.model)
			}
			w := routeWorker(t, st, prov, c.pbr)

			require.NoError(t, w.execute(context.Background(), routeRun(40, c.threed), "tok"))

			select {
			case raw := <-body:
				t.Fatalf("the build was submitted — and paid — with an option the route drops: %s", raw)
			default:
			}
			require.Empty(t, st.started, "no attempt row: nothing may be spent")
			require.Empty(t, st.recordedPrompts, "nothing was sent, so no prompt is recorded")
			require.Len(t, st.failed, 1, "the run must be failed in words, not left to its lease")
			require.Equal(t, CodeOptionNotRead, st.failed[0].ErrorCode)
			require.Equal(t, entity.DesignErrorCodeOptionNotRead, st.failed[0].ErrorCode,
				"the worker says the door's word for the same fact")
			require.False(t, st.failed[0].Retryable,
				"terminal: the frozen params and the configured route answer the same on every pass")
			require.Contains(t, st.failed[0].LastError, "params.threed."+c.field)
		})
	}
}

// TestTheWorkerSUBMITS_WHAT_THE_ROUTE_READS — the positive controls: without them the table above
// would prove only that the worker refuses every 3D run.
func TestTheWorkerSUBMITS_WHAT_THE_ROUTE_READS(t *testing.T) {
	for _, c := range []struct {
		name   string
		model  string
		pbr    bool
		threed string
	}{
		{"detailed + words on fal meshy", "meshy/v7/multi-image-to-3d", false,
			`{"quality": "detailed", "surface_hint": "matte red cotton"}`},
		{"pbr with DESIGN_THREED_PBR on", "meshy/v7/multi-image-to-3d", true, `{"pbr": "on"}`},
		{"today's constants stated on the hitem3d override", hitemSlug, false,
			`{"texture": "on", "pbr": "off", "quality": "standard", "surface_hint": "  "}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			stand := newFalSubmitStand(t)
			st := &fakeStore{}
			w := routeWorker(t, st, falRoute(t, stand.srv.URL, c.model), c.pbr)

			_ = w.execute(context.Background(), routeRun(41, c.threed), "tok")

			sentBody(t, stand.body) // fails the test when nothing was submitted
			for _, f := range st.failed {
				require.NotEqual(t, CodeOptionNotRead, f.ErrorCode)
			}
		})
	}
}

// TestAnAcceptedBuildIsCOLLECTED_NOT_REFUSED — a build submitted (and paid) under the old route is
// collected for free even when the new route would not read its options: refusing it would throw
// away money already spent. MUTATION (measured red): the route check hoisted above the resume lookup.
func TestAnAcceptedBuildIsCOLLECTED_NOT_REFUSED(t *testing.T) {
	stand := newFalSubmitStand(t)
	st := &fakeStore{}
	run := routeRun(42, `{"quality": "detailed"}`)
	full := run
	full.Attempts = []entity.DesignRunAttempt{{
		AttemptNo: 1, State: entity.DesignAttemptAccepted,
		ProviderRequestId: sql.NullString{String: "req-old", Valid: true},
	}}
	st.getRun = &full
	w := routeWorker(t, st, falRoute(t, stand.srv.URL, hitemSlug), false)

	_ = w.execute(context.Background(), run, "tok")

	require.Len(t, st.started, 1, "the free collect opens its own attempt")
	for _, f := range st.failed {
		require.NotEqual(t, CodeOptionNotRead, f.ErrorCode, "an already-paid build must not be refused")
	}
}

// TestThreedUnreadNAMES_THE_DROPPED_OPTION — the shared expression, unit by unit (the door asks the
// same function; see admin's TestThePlaygroundDoor… rows for the wiring there).
func TestThreedUnreadNAMES_THE_DROPPED_OPTION(t *testing.T) {
	fr := func(model string, pbr bool) *ThreedRoute {
		r := FalThreedRoute(fal.New(fal.Config{APIKey: "k", Model3D: model}), pbr)
		return &r
	}
	mr := MeshyThreedRoute(meshy.New(meshy.Config{APIKey: "k"}), false)
	for _, c := range []struct {
		name                         string
		r                            *ThreedRoute
		texture, pbr, quality, words string
		want                         string
	}{
		{"nothing stated", nil, "", "", "", "", ""},
		{"today's constants, no route", nil, "on", "off", "standard", " ", ""},
		{"words, no route", nil, "", "", "", "silk", ThreedOptionSurfaceHint},
		{"words, hitem3d", fr(hitemSlug, true), "", "", "", "silk", ThreedOptionSurfaceHint},
		{"words, fal meshy", fr("meshy/v7/multi-image-to-3d", false), "", "", "", "silk", ""},
		{"words, direct meshy", &mr, "on", "", "", "silk", ""},
		{"words untextured, direct meshy", &mr, "off", "", "", "silk", ThreedOptionSurfaceHint},
		{"pbr, direct meshy without the flag", &mr, "", "on", "", "", ThreedOptionPBR},
		{"detailed, hitem3d", fr(hitemSlug, true), "", "", "detailed", "", ThreedOptionQuality},
	} {
		got, why := ThreedUnread(c.r, c.texture, c.pbr, c.quality, c.words)
		require.Equal(t, c.want, got, c.name)
		require.Equal(t, got == "", why == "", "%s: a refusal carries its reason", c.name)
	}
	require.Nil(t, ThreedRouteOf(&fakeProvider{}, true), "an unknown provider is no known route")
}

// ═══ G-02 r2 (Codex 5): THE CEILING DIAGNOSTIC SAYS ONLY WHAT FOLLOWS ═══

// captureSlog swaps the default logger for the duration of the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestTheCeilingBreachCLAIMS_UNDER_RESERVATION_ONLY_WHEN_IT_WAS — Codex's scenario: FAL_UNIT_USD
// 0.01, ceiling 1, reported units 100. The booking is $1.00 and the door reserved max($1.20 static,
// $0.01 ceiling) = $1.20, so the ceiling is wrong but the reservation was not short. MUTATION
// (measured red): the old unconditional ERROR «the run's reservation was below its booking».
func TestTheCeilingBreachCLAIMS_UNDER_RESERVATION_ONLY_WHEN_IT_WAS(t *testing.T) {
	c := fal.New(fal.Config{APIKey: "k", UnitUSD: 0.01, UnitsCeiling3D: 1})
	booked := decimal.NullDecimal{Decimal: c.CostUSDForQuality("", 100, ""), Valid: true}
	require.Equal(t, "1", booked.Decimal.String())
	reserved := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	for _, tc := range []struct {
		name     string
		reserved decimal.NullDecimal
		units    float64
		level    string // "" = nothing logged
		short    bool
	}{
		{"covered by the static floor", reserved("1.2"), 100, "WARN", false},
		{"no reservation recorded", decimal.NullDecimal{}, 100, "WARN", false},
		{"really short", reserved("0.5"), 100, "ERROR", true},
		{"within the ceiling", reserved("1.2"), 1, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureSlog(t)
			logThreedCeilingBreach(context.Background(), c, Job{RunID: 9, ThreedReservedUSD: tc.reserved},
				"req-9", tc.units, booked)
			out := buf.String()
			if tc.level == "" {
				require.Empty(t, out)
				return
			}
			require.Contains(t, out, "level="+tc.level)
			require.Contains(t, out, "FAL_UNITS_CEILING_3D")
			require.Equal(t, tc.short, strings.Contains(out, "reservation was below its booking"), out)
		})
	}
}

// TestTheJobCarriesTheReservationPerBuild — buildJob hands the collect what the door reserved for ONE
// build (price_estimate over requested_outputs). MUTATION (measured red): not dividing by outputs.
func TestTheJobCarriesTheReservationPerBuild(t *testing.T) {
	run := steerRun(43)
	run.RequestedOutputs = 2
	run.PriceEstimate = decimal.NullDecimal{Decimal: decimal.RequireFromString("2.4"), Valid: true}
	job, err := buildJob(context.Background(), media(21, 22), nil, run, "medium")
	require.NoError(t, err)
	require.True(t, job.ThreedReservedUSD.Valid)
	require.Equal(t, "1.2", job.ThreedReservedUSD.Decimal.String())

	run.PriceEstimate = decimal.NullDecimal{}
	job, err = buildJob(context.Background(), media(21, 22), nil, run, "medium")
	require.NoError(t, err)
	require.False(t, job.ThreedReservedUSD.Valid, "no estimate, no claim")
}
