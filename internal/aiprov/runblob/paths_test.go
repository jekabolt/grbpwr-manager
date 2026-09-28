package runblob

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
)

// ═══ B-31 — the adapter grows: family PATHS, two id fields, three result fields ═══
//
// FROM THE DOCS (runblob-specs/kling.json; the Nano Banana page, both read 2026-09-28): the photo
// endpoints /v1/kling/o1-photo and /v1/kling/o3-photo answer {generation_id, status, price} and read
// back {generation_id, status, prompt, image_url, model}; Nano Banana /v1/gemini answers {task_uuid,
// status:"pending", price:"0.0210"} and reads back {task_uuid, status, prompt, result_image_url | null,
// message | null}. UNVERIFIED (G-06): every VALUE below (the description words, the cdn paths, the
// model labels) — the fields are the docs', the values are made up for the stand.

const taskUUID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"

// TestSubmitPathsAndIdFields pins the submit per family PATH — the o1-photo / o3-photo sub-segments
// go on the wire as given, the gemini answer's id is task_uuid — and that the id is read from
// whichever field the family answers with.
//
// MUTATION (measured red → green): Submit reading sr.GenerationID alone (the task_uuid fallback
// dropped) → the gemini row answers empty_answer → red; the path built as "/v1/"+family's first
// segment → the o1-photo row's recorded path red.
func TestSubmitPathsAndIdFields(t *testing.T) {
	cases := []struct {
		path, body, wantID, wantPrice string
	}{
		{PathGemini, `{"task_uuid":"` + taskUUID + `","status":"pending","price":"0.0210"}`, taskUUID, "0.021"},
		{PathKlingO1Photo, `{"generation_id":"` + genID + `","status":"pending","price":"0.0290"}`, genID, "0.029"},
		{PathKlingO3Photo, `{"generation_id":"` + genID + `","status":"pending","price":"0.0500"}`, genID, "0.05"},
		// both present (UNVERIFIED (G-06): no family documents both): generation_id wins, as the first named.
		{FamilyKling, `{"generation_id":"` + genID + `","task_uuid":"` + taskUUID + `","status":"pending","price":"0.2900"}`, genID, "0.29"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(http.StatusCreated, tc.body))
			sub, err := newAt(srv.URL, 2*time.Second).Submit(context.Background(), tc.path,
				map[string]any{"prompt": "a coat"})
			require.NoError(t, err)
			require.Equal(t, "/v1/"+tc.path+"/generate", rec.paths[0])
			require.Equal(t, tc.wantID, sub.ID)
			require.True(t, sub.PriceUSD.Valid)
			require.True(t, sub.PriceUSD.Decimal.Equal(decimal.RequireFromString(tc.wantPrice)), sub.PriceUSD.Decimal.String())
			require.True(t, sub.Engaged)
		})
	}

	t.Run("neither id field: engaged, empty_answer, the sentence names both", func(t *testing.T) {
		rec := &recorder{}
		srv := rec.server(t, answer(http.StatusCreated, `{"status":"pending","price":"0.0210"}`))
		sub, err := newAt(srv.URL, 2*time.Second).Submit(context.Background(), PathGemini, map[string]any{"prompt": "p"})
		ce := callErr(t, err)
		require.Equal(t, aiprov.CodeEmptyAnswer, ce.Code)
		require.True(t, ce.Engaged)
		require.Equal(t, "runblob: the gemini submit was accepted (HTTP 201) with no generation_id / task_uuid", err.Error())
		require.NotNil(t, sub)
		require.True(t, sub.PriceUSD.Valid, "the price was readable and travels with the partial")
	})
}

// TestStatusReadsEveryResultField pins the three result fields and `message`: video_url stays
// VideoURL; image_url (Kling photo) and result_image_url (Nano Banana) both land in ImageURL;
// ResultURL answers the one that is set; message (a failed Nano Banana job's error code) is read as a
// string or null; Failure prefers `error` and falls back to `message`.
//
// MUTATION (measured red → green): ResultImageURL's tag misspelled `json:"result_image"` → the gemini
// completed row red; Message not read (the field dropped) → the gemini failed row red.
func TestStatusReadsEveryResultField(t *testing.T) {
	cases := []struct {
		name, path, body string
		want             Generation
		result, failure  string
	}{
		{"kling video", FamilyKling, okStatusBody,
			Generation{Status: "completed", VideoURL: "https://cdn.runblob.io/v/abc.mp4", Model: "kling_2.5_turbo"},
			"https://cdn.runblob.io/v/abc.mp4", ""},
		{"kling o1-photo completed", PathKlingO1Photo,
			`{"generation_id":"` + genID + `","status":"completed","prompt":"p","image_url":"https://cdn.runblob.io/i/out.png","model":"kling-o1-photo"}`,
			Generation{Status: "completed", ImageURL: "https://cdn.runblob.io/i/out.png", Model: "kling-o1-photo"},
			"https://cdn.runblob.io/i/out.png", ""},
		{"gemini completed", PathGemini,
			`{"task_uuid":"` + taskUUID + `","status":"completed","prompt":"p","result_image_url":"https://cdn.runblob.io/g/out.png","message":null}`,
			Generation{Status: "completed", ImageURL: "https://cdn.runblob.io/g/out.png"},
			"https://cdn.runblob.io/g/out.png", ""},
		{"gemini processing", PathGemini,
			`{"task_uuid":"` + taskUUID + `","status":"processing","prompt":"p","result_image_url":null,"message":null}`,
			Generation{Status: "processing"}, "", ""},
		{"gemini failed: the code is in message", PathGemini,
			`{"task_uuid":"` + taskUUID + `","status":"failed","prompt":"p","result_image_url":null,"message":"CONTENT_POLICY_VIOLATION"}`,
			Generation{Status: "failed", Message: "CONTENT_POLICY_VIOLATION"}, "", "CONTENT_POLICY_VIOLATION"},
		{"kling photo failed: error wins over message", PathKlingO3Photo,
			`{"generation_id":"` + genID + `","status":"failed","image_url":null,"model":"kling-o3-photo-4k","message":"TASK_FAILED","error":"upstream down"}`,
			Generation{Status: "failed", Model: "kling-o3-photo-4k", Message: "TASK_FAILED", Error: "upstream down"},
			"", "upstream down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(http.StatusOK, tc.body))
			g, err := newAt(srv.URL, 2*time.Second).Status(context.Background(), tc.path, genID)
			require.NoError(t, err)
			require.Equal(t, &tc.want, g)
			require.Equal(t, "/v1/"+tc.path+"/generations/"+genID, rec.paths[0])
			require.Equal(t, tc.result, g.ResultURL())
			require.Equal(t, tc.failure, g.Failure())
		})
	}
	var none *Generation
	require.Empty(t, none.ResultURL())
	require.Empty(t, none.Failure())
}

// TestPathShapeIsClosed — the new paths pass, and everything that is not exactly one of the five is
// refused before the wire: a second sub-segment, a known family with a stray sub-path, a dot, a query.
//
// MUTATION (measured red → green): pathShape's sub-segment group made `(/[^/]+)?` → "kling/../x" is
// no longer refused by shape and reaches the closed-list check with the same answer, but
// "kling/o1-photo?x=1" … the query row red.
func TestPathShapeIsClosed(t *testing.T) {
	for _, p := range knownPaths {
		require.NoError(t, validPath(p), p)
	}
	require.Equal(t, []string{"gemini", "kling", "kling/o1-photo", "kling/o3-photo", "veo"}, knownPaths)
	rec := &recorder{}
	srv := rec.server(t, answer(http.StatusCreated, okSubmitBody))
	c := newAt(srv.URL, 2*time.Second)
	for _, p := range []string{"kling/o1-photo/x", "kling/o1", "gemini/", "/gemini", "kling/o1-photo?x=1",
		"kling/o1.photo", "kling//o1-photo", "Kling/o1-photo", "kling/o1-photo/../generate"} {
		_, err := c.Submit(context.Background(), p, map[string]any{"prompt": "p"})
		ce := callErr(t, err)
		require.Equal(t, aiprov.CodeBadRequest, ce.Code, p)
		require.False(t, ce.Engaged, p)
		_, err = c.Status(context.Background(), p, genID)
		require.Equal(t, aiprov.CodeBadRequest, callErr(t, err).Code, p)
	}
	require.Zero(t, rec.count(), "a refused path reached the provider")
}
