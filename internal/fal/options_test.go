package fal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// ═══ B-09: THE PER-RUN 3D OPTIONS ON THE fal ROUTE ═══════════════════════════════════════════════
//
// Field names verified 2026-09-27 against fal's own OpenAPI for the endpoint
// (https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=meshy/v7/multi-image-to-3d):
// should_texture (default true), enable_pbr (default false, «Requires should_texture to be true»),
// geometry_resolution (standard | 2k), texture_prompt («Requires should_texture to be true»). The
// schema has NO texture_resolution and NO ai_model — both are asserted ABSENT below.

// rawSubmitStand records the RAW body of every submit, so a probe can compare bytes, not a map.
func rawSubmitStand(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "req-b09"})
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func benchRequest() Request3D {
	return Request3D{
		FrontURL:      "https://cdn.example/front.png",
		BackURL:       "https://cdn.example/back.png",
		TexturePrompt: "colourway BLK; matte heavy jersey",
	}
}

// TestAnUnstatedOptionSetSendsTODAYS_EXACT_BYTES — the bench-plate route (every run frozen before
// the fields, every run of STEP 5) must not move by one byte.
//
// ⚠ THE GOLDEN STRING IS THE BODY THIS TRANSPORT SENT AT 03985d5, written out by hand, not captured
// from the new code. MUTATIONS THAT TURN IT RED: default `texture: true` flipped; `omitempty` removed
// from geometry_resolution; a texture_resolution / ai_model key added to the meshy body; the steer
// dropped for a textured build.
func TestAnUnstatedOptionSetSendsTODAYS_EXACT_BYTES(t *testing.T) {
	srv, bodies := rawSubmitStand(t)
	c := newTestClient(t, srv.URL)
	_, err := c.Submit(context.Background(), benchRequest())
	require.NoError(t, err)
	require.Len(t, *bodies, 1)
	require.Equal(t,
		`{"image_urls":["https://cdn.example/front.png","https://cdn.example/back.png"],`+
			`"should_texture":true,"enable_pbr":false,"enable_safety_checker":true,`+
			`"texture_prompt":"colourway BLK; matte heavy jersey"}`,
		(*bodies)[0])

	// «standard» and «off»/«on» at their default are the same statement as silence.
	req := benchRequest()
	req.Texture, req.PBR, req.Quality = OptionOn, OptionOff, QualityStandard
	_, err = c.Submit(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, (*bodies)[0], (*bodies)[1], "the defaults spelled out must send the default bytes")
}

func decodeBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

// TestTheOptionsReachTheMeshyBody — each option changes exactly its own key.
//
// MUTATIONS: ShouldTexture hard-coded true (texture off → red); EnablePBR hard-coded false (pbr on →
// red); GeometryResolution never set (detailed → red); the untextured build keeping its
// texture_prompt (→ red: the provider documents it as «Requires should_texture»).
func TestTheOptionsReachTheMeshyBody(t *testing.T) {
	srv, bodies := rawSubmitStand(t)
	c := newTestClient(t, srv.URL)

	send := func(texture, pbr, quality string) map[string]any {
		req := benchRequest()
		req.Texture, req.PBR, req.Quality = texture, pbr, quality
		_, err := c.Submit(context.Background(), req)
		require.NoError(t, err)
		return decodeBody(t, (*bodies)[len(*bodies)-1])
	}

	off := send(OptionOff, "", "")
	require.Equal(t, false, off["should_texture"])
	require.Equal(t, false, off["enable_pbr"])
	require.NotContains(t, off, "texture_prompt", "an untextured build has no texturing stage to steer")

	pbr := send("", OptionOn, "")
	require.Equal(t, true, pbr["should_texture"])
	require.Equal(t, true, pbr["enable_pbr"])
	require.Equal(t, "colourway BLK; matte heavy jersey", pbr["texture_prompt"])

	detailed := send("", "", QualityDetailed)
	require.Equal(t, "2k", detailed["geometry_resolution"])
	require.Equal(t, true, detailed["should_texture"])
	// NOT IN fal's SCHEMA — a key the validator does not know is a 422 that reads like our bug.
	require.NotContains(t, detailed, "texture_resolution")
	require.NotContains(t, detailed, "ai_model")

	standard := send("", "", QualityStandard)
	require.NotContains(t, standard, "geometry_resolution", "standard is the provider's own default")
}

// TestAnUnsendableOptionIsREFUSED_LOCALLY — before the submit, which is the payment, and with a
// sentinel the worker classifies as non-retryable (it wraps ErrBadRequest).
//
// MUTATION: resolveOptions accepting an unknown word (→ a request is sent, red); dropping the
// pbr-without-texture check (→ red).
func TestAnUnsendableOptionIsREFUSED_LOCALLY(t *testing.T) {
	srv, bodies := rawSubmitStand(t)
	c := newTestClient(t, srv.URL)
	for _, tc := range []struct{ texture, pbr, quality string }{
		{"yes", "", ""},
		{"", "maybe", ""},
		{"", "", "ultra"},
		{OptionOff, OptionOn, ""},
	} {
		req := benchRequest()
		req.Texture, req.PBR, req.Quality = tc.texture, tc.pbr, tc.quality
		_, err := c.Submit(context.Background(), req)
		require.Errorf(t, err, "%+v", tc)
		require.True(t, errors.Is(err, ErrBadOption), "%+v: %v", tc, err)
		require.True(t, errors.Is(err, ErrBadRequest), "the classifier reads ErrBadRequest as non-retryable")
	}
	require.Empty(t, *bodies, "nothing may leave for a request this route cannot send")
}

// TestHitem3dKeepsItsConstantsWhateverTheRunAsks — the retired named-slot family gets no mapping
// (its tiers are unmeasured); the options are logged, never half-applied.
func TestHitem3dKeepsItsConstantsWhateverTheRunAsks(t *testing.T) {
	srv, bodies := rawSubmitStand(t)
	c := newTestClient(t, srv.URL)
	req := benchRequest()
	req.Model = hitem3dModel
	req.Texture, req.PBR, req.Quality = OptionOn, OptionOn, QualityDetailed
	_, err := c.Submit(context.Background(), req)
	require.NoError(t, err)
	body := decodeBody(t, (*bodies)[0])
	require.Equal(t, true, body["enable_texture"])
	require.Equal(t, false, body["enable_pbr"])
	require.NotContains(t, body, "resolution")
	require.NotContains(t, body, "geometry_resolution")
}

// TestADetailedBuildIsPRICED_AS_ULTRA — the collect books a detailed build at fal's «ultra mode»
// price when no tariff is configured, and the door reserves the same expression.
//
// MUTATION: EstimatedRequestUSDForQuality ignoring quality (→ 1.2, red); CostUSDForQuality passing
// "" down (→ red); the surcharge applied on top of a configured tariff (→ red on the last line).
func TestADetailedBuildIsPRICED_AS_ULTRA(t *testing.T) {
	require.Equal(t, "1.2", EstimatedRequestUSDForQuality("", "").String())
	require.Equal(t, "1.2", EstimatedRequestUSDForQuality("", QualityStandard).String())
	require.Equal(t, "1.4", EstimatedRequestUSDForQuality("", QualityDetailed).String())
	require.Equal(t, EstimatedRequestUSD().String(), EstimatedRequestUSDForQuality("", "").String(),
		"the default tier IS today's estimate — the door's regression probe rests on it")
	// A retired slug was never offered a tier.
	require.Equal(t, "0.6", EstimatedRequestUSDForQuality("hitem3d/hi3d/v3.0/multi-view-to-3d", QualityDetailed).String())

	noTariff := New(Config{APIKey: "k"})
	require.Equal(t, "1.4", noTariff.CostUSDForQuality("", 1, QualityDetailed).String())
	require.Equal(t, "1.4", noTariff.CostUSDForQuality("", 100, QualityDetailed).String(),
		"still flat without a tariff — the run-17 fix holds at every tier")
	require.Equal(t, "1.2", noTariff.CostUSDFor("", 1).String())

	withTariff := New(Config{APIKey: "k", UnitUSD: 0.01})
	require.Equal(t, "1", withTariff.CostUSDForQuality("", 100, QualityDetailed).String(),
		"a configured tariff prices the provider's own units; the tier is not counted twice")
}
