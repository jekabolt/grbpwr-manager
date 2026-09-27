package meshy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ═══ B-09: THE PER-RUN 3D OPTIONS ON THE DIRECT MESHY ROUTE ══════════════════════════════════════
//
// Field names verified 2026-09-27 against https://docs.meshy.ai/en/api/multi-image-to-3d:
// should_texture (default true), enable_pbr (default false), geometry_resolution (standard | 2k,
// «2k requires meshy-7.1 or latest»), texture_resolution (2k | 4k | 8k, default 2k), ai_model
// (default `latest`), texture_prompt.

func rawMeshyStand(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "task-b09"})
	}))
	t.Cleanup(srv.Close)
	return New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: 2 * time.Second}), &bodies
}

func benchMeshyRequest() Request {
	return Request{
		ImageURLs:     []string{"https://cdn.example/front.png", "https://cdn.example/back.png"},
		TexturePrompt: "colourway BLK; matte heavy jersey",
	}
}

// TestAnUnstatedOptionSetSendsTODAYS_EXACT_BYTES — the golden string is the body this client sent
// at 03985d5 (verified by running the original transport on the same request). MUTATIONS: default
// texture flipped; omitempty dropped from geometry_resolution / texture_resolution; ai_model pinned.
func TestAnUnstatedOptionSetSendsTODAYS_EXACT_BYTES(t *testing.T) {
	c, bodies := rawMeshyStand(t)
	_, err := c.Submit(context.Background(), benchMeshyRequest())
	require.NoError(t, err)
	require.Equal(t,
		`{"image_urls":["https://cdn.example/front.png","https://cdn.example/back.png"],`+
			`"target_formats":["glb"],"should_texture":true,"enable_pbr":false,`+
			`"texture_prompt":"colourway BLK; matte heavy jersey"}`,
		(*bodies)[0])

	req := benchMeshyRequest()
	req.Texture, req.PBR, req.Quality = OptionOn, OptionOff, QualityStandard
	_, err = c.Submit(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, (*bodies)[0], (*bodies)[1])
}

// TestTheOptionsReachTheMeshyBody. MUTATIONS: ShouldTexture / EnablePBR hard-coded; detailed not
// setting geometry_resolution / texture_resolution; ai_model pinned to meshy-7.1 (→ red, doc.go);
// the 4k texture kept on a PBR task (→ red: the 64 MiB charged-failure guard).
func TestTheOptionsReachTheMeshyBody(t *testing.T) {
	c, bodies := rawMeshyStand(t)
	send := func(texture, pbr, quality string) map[string]any {
		req := benchMeshyRequest()
		req.Texture, req.PBR, req.Quality = texture, pbr, quality
		_, err := c.Submit(context.Background(), req)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte((*bodies)[len(*bodies)-1]), &m))
		return m
	}

	off := send(OptionOff, "", "")
	require.Equal(t, false, off["should_texture"])
	require.NotContains(t, off, "texture_prompt")

	pbr := send("", OptionOn, "")
	require.Equal(t, true, pbr["enable_pbr"])

	detailed := send("", "", QualityDetailed)
	require.Equal(t, "2k", detailed["geometry_resolution"])
	require.Equal(t, "4k", detailed["texture_resolution"])
	require.NotContains(t, detailed, "ai_model", "the provider default `latest` serves 2k; no slug is baked in")

	detailedPBR := send("", OptionOn, QualityDetailed)
	require.Equal(t, "2k", detailedPBR["geometry_resolution"])
	require.NotContains(t, detailedPBR, "texture_resolution",
		"four 4k PBR maps risk the 64 MiB cap, which refuses the GLB AFTER the charge")

	detailedBare := send(OptionOff, "", QualityDetailed)
	require.Equal(t, "2k", detailedBare["geometry_resolution"])
	require.NotContains(t, detailedBare, "texture_resolution", "no texture, no texture resolution")
}

// TestAnUnsendableOptionIsREFUSED_LOCALLY — before the payment, classified non-retryable.
func TestAnUnsendableOptionIsREFUSED_LOCALLY(t *testing.T) {
	c, bodies := rawMeshyStand(t)
	for _, tc := range []struct{ texture, pbr, quality string }{
		{"yes", "", ""}, {"", "maybe", ""}, {"", "", "ultra"}, {OptionOff, OptionOn, ""},
	} {
		req := benchMeshyRequest()
		req.Texture, req.PBR, req.Quality = tc.texture, tc.pbr, tc.quality
		_, err := c.Submit(context.Background(), req)
		require.Errorf(t, err, "%+v", tc)
		require.True(t, errors.Is(err, ErrBadOption), "%+v: %v", tc, err)
		require.True(t, errors.Is(err, ErrBadRequest))
	}
	require.Empty(t, *bodies)
}
