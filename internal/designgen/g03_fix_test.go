package designgen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ═══ G-03 fix pass — the worker side of Codex 1–6, 9, 10 and Fable m-1 ═══

func asyncFill(out *Outcome, err error) *fakeAsyncProvider {
	return &fakeAsyncProvider{
		fakeProvider: fakeProvider{name: providerNameFalFill, produces: []string{ContentTypePNG}, out: out, err: err},
		collectOut:   okOutcome(1, 0.1),
	}
}

func failedCodes(st *fakeStore) []string {
	out := []string{}
	for _, f := range st.failed {
		out = append(out, fmt.Sprintf("%s retry=%v", f.ErrorCode, f.Retryable))
	}
	return out
}

// TestAFailedAcceptedWriteFAILS_THE_PASS_CLOSED — Codex 1 (a). fal accepted the submit and the row
// that would carry its id refused the write: the pass must not carry on with the id in memory (a
// collect that then times out would leave the next pickup free to BUY the job again). Terminal,
// `submit_unconfirmed`, the id in last_error, no collect. MUTATION (measured red): ignore the
// recordAttempt error in execute → the collect runs and the run is filed.
func TestAFailedAcceptedWriteFAILS_THE_PASS_CLOSED(t *testing.T) {
	st := &fakeStore{finishErr: errBoom, finishErrOn: entity.DesignAttemptAccepted}
	prov := asyncFill(&Outcome{RequestID: "fal-ai/flux-pro/v1/fill#req-9", Pending: true}, nil)
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: prov})

	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))
	require.Len(t, prov.calls, 1, "the one submit")
	require.Empty(t, prov.collectFor, "no collect on an id nobody wrote down")
	require.Empty(t, st.completed)
	require.Equal(t, []string{CodeSubmitUnconfirmed + " retry=false"}, failedCodes(st))
	require.Contains(t, st.failed[0].LastError, "req-9", "the id a person reconciles by")
}

// TestAnOpenSubmitAtPickupIsNOT_BOUGHT_AGAIN — Codex 1 (a), the other half: whatever killed the
// previous pass between StartAttempt and the closing write, the next pickup sees a `dispatching`
// attempt with no accepted id — and refuses, closing that row `unknown`, instead of submitting.
// Positive control: a submit that CLOSED as a plain failure is not ambiguous and is retried.
// MUTATION (measured red): drop the unresolvedSubmit guard → a second submit.
func TestAnOpenSubmitAtPickupIsNOT_BOUGHT_AGAIN(t *testing.T) {
	open := entity.DesignRunAttempt{RunId: 4, AttemptNo: 1, Provider: providerNameFalFill,
		State: entity.DesignAttemptDispatching, StartedAt: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)}
	st := &fakeStore{getRun: &entity.DesignRun{Id: 4, Attempts: []entity.DesignRunAttempt{open}}}
	prov := asyncFill(&Outcome{RequestID: "x#y", Pending: true}, nil)
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: prov})
	// Past the settle grace: the owner of the open row cannot still be inside its call.
	w.now = func() time.Time { return open.StartedAt.Add(2 * time.Hour) }

	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))
	require.Empty(t, prov.calls, "NO second submit")
	require.Empty(t, st.started, "no attempt opened")
	require.Equal(t, []string{CodeSubmitUnconfirmed + " retry=false"}, failedCodes(st))
	require.Len(t, st.finished, 1, "the open row is closed for the history")
	require.Equal(t, 1, st.finished[0].AttemptNo)
	require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)

	closed := open
	closed.State, closed.FinishedAt = entity.DesignAttemptFailed, sql.NullTime{Time: open.StartedAt, Valid: true}
	st2 := &fakeStore{getRun: &entity.DesignRun{Id: 4, Attempts: []entity.DesignRunAttempt{closed}}}
	prov2 := asyncFill(&Outcome{RequestID: "x#y", Pending: true}, nil)
	w2 := testWorker(st2, nil, newFakeSink(ContentTypePNG), Providers{Threed: prov2})
	w2.now = w.now
	require.NoError(t, w2.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))
	require.Len(t, prov2.calls, 1, "a submit that closed as a refusal is retried as before")
}

// TestAnUnconfirmedSubmitIsTERMINAL_NOT_WEATHER — Codex 1 (b): the transport's ErrSubmitUnconfirmed
// (a timeout after the request was written, a 5xx, a 2xx with no id) closes the attempt `unknown` and
// the run terminally — the default would have been «retryable», i.e. a second purchase.
// MUTATION (measured red): drop the fal.ErrSubmitUnconfirmed case in classify.
func TestAnUnconfirmedSubmitIsTERMINAL_NOT_WEATHER(t *testing.T) {
	cause := fmt.Errorf("%w: fal: POST /fal-ai/flux-pro/v1/fill: %w", fal.ErrSubmitUnconfirmed, context.DeadlineExceeded)
	v := classify(cause)
	require.False(t, v.Retryable)
	require.Equal(t, CodeSubmitUnconfirmed, v.Code)
	require.Equal(t, entity.DesignAttemptUnknown, v.State, "the money may be gone")

	st := &fakeStore{}
	prov := asyncFill(nil, cause)
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: prov})
	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))
	require.Equal(t, []string{CodeSubmitUnconfirmed + " retry=false"}, failedCodes(st))
	require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)

	// The accepted-but-idless 2xx wraps both sentinels; the unconfirmed reading wins.
	lost := fmt.Errorf("%w: %w: submit returned no request id", fal.ErrSubmitUnconfirmed, fal.ErrUnexpectedResponse)
	require.Equal(t, CodeSubmitUnconfirmed, classify(lost).Code)
}

// TestAPaidJobWAITS_FOR_ITS_ROUTE — Codex 2 (second half): FAL_KEY removed while a job is ACCEPTED.
// The collect is free and the job is bought, so the pre-flight's refusal must not be terminal (it
// would release the reserve over a paid result); an unpaid run is still refused terminally.
// MUTATION (measured red): drop the accepted-id branch in execute's pre-flight → terminal.
func TestAPaidJobWAITS_FOR_ITS_ROUTE(t *testing.T) {
	off := asyncFill(nil, nil)
	off.off = true
	paid := &entity.DesignRun{Id: 4, Attempts: []entity.DesignRunAttempt{{RunId: 4, AttemptNo: 1,
		State: entity.DesignAttemptAccepted, ProviderRequestId: sql.NullString{String: "fal-ai/flux-pro/v1/fill#r", Valid: true}}}}
	st := &fakeStore{getRun: paid}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: off})
	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))
	require.Equal(t, []string{CodePaidCollectWaiting + " retry=true"}, failedCodes(st),
		"G-03 r2, Codex 4: the store reads this word as a wait that spends no round of the ceiling")
	require.Empty(t, off.calls)
	require.Empty(t, off.collectFor)

	st2 := &fakeStore{getRun: &entity.DesignRun{Id: 4}}
	w2 := testWorker(st2, nil, newFakeSink(ContentTypePNG), Providers{Threed: off})
	require.NoError(t, w2.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))
	require.Equal(t, []string{CodeKindNotAvailable + " retry=false"}, failedCodes(st2), "nothing paid: refused as before")

	st3 := &fakeStore{getRunErr: errBoom}
	w3 := testWorker(st3, nil, newFakeSink(ContentTypePNG), Providers{Threed: off})
	require.Error(t, w3.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"),
		"history unreadable: the pass is abandoned, neither refused nor submitted")
	require.Empty(t, st3.failed)
}

// TestTheCollectPollsTHE_SUBMITTED_SLUG — Codex 2: the accepted row carries "<slug>#<id>", and the
// collect polls THAT namespace after FAL_MODEL_OUTPAINT moved to bria. The collect's own row reports
// the same locator (one charge, one key). A bare id (a row from before) is read against today's slug.
// MUTATION (measured red): collectRouteFile polling c.ModelFor(route) → bria's namespace is asked.
func TestTheCollectPollsTHE_SUBMITTED_SLUG(t *testing.T) {
	stand := &outpaintStand{canvas: solidPNG(t, 64, 64, color.NRGBA{R: 1, A: 255}), units: "1"}
	srv := httptest.NewServer(stand.handler(t))
	defer srv.Close()
	moved := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond,
		PollTimeout: time.Second, ModelOutpaint: "fal-ai/bria/expand"})
	locator := fal.DefaultModelOutpaint + "#out-1"
	out, err := NewFalOutpaintProvider(moved).(Collector).Collect(context.Background(), Job{RunID: 3}, locator)
	require.NoError(t, err)
	require.Equal(t, locator, out.RequestID)
	require.Contains(t, stand.paths, "GET /fal-ai/flux-2-pro/requests/out-1/status")
	for _, p := range stand.paths {
		require.NotContains(t, p, "/fal-ai/bria/", "the queue of today's slug is never asked for this request")
	}

	model, id := splitFalLocator("out-1")
	require.Equal(t, "", model)
	require.Equal(t, "out-1", id)
	model, id = splitFalLocator(fal.DefaultModelFill + "#abc")
	require.Equal(t, fal.DefaultModelFill, model)
	require.Equal(t, "abc", id)

	// A slug too long to be remembered in VARCHAR(128) is refused before the submit, for free.
	long := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, ModelOutpaint: "fal-ai/" + strings.Repeat("x", 90)})
	r, _ := entity.DesignExtendRatioValue("21:9")
	plan, _ := planExtend(200, 300, r)
	_, err = NewFalOutpaintProvider(long).Execute(context.Background(),
		Job{References: []string{"https://cdn.example/m/11.png"}, Extend: &plan})
	require.ErrorIs(t, err, fal.ErrBadOption)
}

// TestTheCutoutRemembersITS_SLUG_TOO — the same locator on the pre-existing cut-out route (the fix is
// shared): a FAL_MODEL_CUTOUT move between the submit and the resume polls the submitted namespace.
func TestTheCutoutRemembersITS_SLUG_TOO(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/cut.png":
			_, _ = w.Write(pngWithAlpha(t, 16, 16))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case strings.Contains(r.URL.Path, "/requests/"):
			_, _ = w.Write([]byte(`{"image":{"url":"http://` + r.Host + `/cut.png"}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	moved := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond,
		PollTimeout: time.Second, ModelCutout: "fal-ai/other/matting"})
	out, err := NewFalCutoutProvider(moved).(Collector).Collect(context.Background(), Job{}, fal.DefaultModelCutout+"#c-1")
	require.NoError(t, err)
	require.Equal(t, fal.DefaultModelCutout+"#c-1", out.RequestID)
	require.Contains(t, paths, "GET /"+queuePathOf(fal.DefaultModelCutout)+"/requests/c-1/status")
}

func queuePathOf(slug string) string {
	parts := strings.SplitN(slug, "/", 3)
	return parts[0] + "/" + parts[1]
}

// TestAnAssumedChargeIsBOOKED_AT_THE_CEILING — Codex 3: a 2xx result whose body is broken and which
// named no units rides the error with one ASSUMED unit; under a tariff that would book tariff × 1,
// under what a multi-megapixel job costs. The collect books the route's bounded ceiling instead; with
// named units it books tariff × units as before. MUTATION (measured red): drop the Assumed branch in
// chargedRouteOutcome → 0.01.
func TestAnAssumedChargeIsBOOKED_AT_THE_CEILING(t *testing.T) {
	units := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case strings.Contains(r.URL.Path, "/requests/"):
			if units != "" {
				w.Header().Set("x-fal-billable-units", units)
			}
			_, _ = w.Write([]byte(`{not json`))
		}
	}))
	defer srv.Close()
	c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second,
		UnitUSDFill: 0.01, UnitsCeilingFill: 10})
	col := NewFalFillProvider(c).(Collector)

	out, err := col.Collect(context.Background(), Job{}, fal.DefaultModelFill+"#f-1")
	require.Error(t, err)
	require.NotNil(t, out, "the charge reaches the ledger")
	require.Equal(t, "0.1", out.Price.Decimal.String(), "one assumed unit is booked at the ceiling (0.01 × 10)")
	require.Equal(t, fal.DefaultModelFill+"#f-1", out.RequestID)

	units = "3"
	out, err = col.Collect(context.Background(), Job{}, fal.DefaultModelFill+"#f-1")
	require.Error(t, err)
	require.Equal(t, "0.03", out.Price.Decimal.String(), "named units: tariff × units")
}

// TestAJPEGSourceKeepsITS_OWN_DECODED_PIXELS — Codex 4 = Fable m-5, the owner's fidelity: an opaque
// JPEG source extended or retouched comes back as a lossless PNG whose kept pixels are EXACTLY the
// source's decoded pixels (before: a q92 re-encode of the whole picture). MUTATION (measured red):
// encodeComposite always JPEG → the content type and the pixel equality both fail.
func TestAJPEGSourceKeepsITS_OWN_DECODED_PIXELS(t *testing.T) {
	_, grad := gradientPNG(t, 200, 300)
	var jb bytes.Buffer
	require.NoError(t, jpeg.Encode(&jb, grad, &jpeg.Options{Quality: 80}))
	decoded, err := jpeg.Decode(bytes.NewReader(jb.Bytes()))
	require.NoError(t, err)
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": jb.Bytes()}}

	t.Run("extend", func(t *testing.T) {
		answer := solidPNG(t, 533, 300, color.NRGBA{R: 250, A: 255})
		prov := &fakeProvider{name: providerNameFalOutpaint, out: &Outcome{Artifacts: []Artifact{{Bytes: answer, ContentType: ContentTypePNG}}}}
		sink := newFakeSink(ContentTypePNG, ContentTypeJPEG)
		w := testWorker(&fakeStore{}, media(11), sink, Providers{Outpaint: prov})
		w.objects = objs
		require.NoError(t, w.execute(context.Background(),
			extendRun(`{"extra_input_media_ids":[11],"extend":{"aspect_ratio":"16:9"}}`), "tok"))
		require.Equal(t, ContentTypePNG, sink.putTypes[0])
		got, err := png.Decode(bytes.NewReader(sink.putBytes[0]))
		require.NoError(t, err)
		off := prov.calls[0].Extend.Offset
		for y := 0; y < 300; y++ {
			for x := 0; x < 200; x++ {
				require.Equal(t, color.NRGBAModel.Convert(decoded.At(x, y)), color.NRGBAModel.Convert(got.At(x+off.X, y+off.Y)),
					"(%d,%d)", x, y)
			}
		}
	})
	t.Run("inpaint", func(t *testing.T) {
		mobjs := &fakeObjects{byKey: map[string][]byte{"m/11.png": jb.Bytes(), "m/12.png": grayMask(t, 200, 300, image.Rect(50, 60, 90, 100))}}
		answer := solidPNG(t, 200, 300, color.NRGBA{G: 255, A: 255})
		prov := &fakeProvider{name: providerNameFalFill, out: &Outcome{Artifacts: []Artifact{{Bytes: answer, ContentType: ContentTypePNG}}}}
		sink := newFakeSink(ContentTypePNG, ContentTypeJPEG)
		w := testWorker(&fakeStore{}, media(11, 12), sink, Providers{Fill: prov})
		w.objects = mobjs
		require.NoError(t, w.execute(context.Background(), inpaintRun("a brass button"), "tok"))
		require.True(t, strings.HasPrefix(prov.calls[0].References[0], "data:image/png;base64,"), "the crop is lossless too")
		require.Equal(t, ContentTypePNG, sink.putTypes[0])
		got, err := png.Decode(bytes.NewReader(sink.putBytes[0]))
		require.NoError(t, err)
		paint := image.Rect(50, 60, 90, 100)
		for y := 0; y < 300; y++ {
			for x := 0; x < 200; x++ {
				if image.Pt(x, y).In(paint) {
					continue
				}
				require.Equal(t, color.NRGBAModel.Convert(decoded.At(x, y)), color.NRGBAModel.Convert(got.At(x, y)), "(%d,%d)", x, y)
			}
		}
	})
	t.Run("too large for a PNG in the bucket: an opaque picture becomes a JPEG that fits", func(t *testing.T) {
		keep := compositeMaxPNGBytes
		var jq bytes.Buffer
		require.NoError(t, jpeg.Encode(&jq, decoded, &jpeg.Options{Quality: 92}))
		compositeMaxPNGBytes = jq.Len() // the q92 JPEG fits exactly; the lossless PNG does not
		defer func() { compositeMaxPNGBytes = keep }()
		art, err := encodeComposite(decoded, false)
		require.NoError(t, err)
		require.Equal(t, ContentTypeJPEG, art.ContentType)
		require.LessOrEqual(t, len(art.Bytes), compositeMaxPNGBytes)
		// A transparent one never does (G-03 r2, Codex 5): TestAnOversizedCompositeNEVER_DROPS_ALPHA_AND_NEVER_OVERSHOOTS.
		_, err = encodeComposite(noiseImage(200, 300, 0x80), false)
		require.ErrorIs(t, err, errCompositeTooLarge)
	})
}

// TestAnInpaintAnswerOfAnotherSizeIsNAMED_NOT_HIDDEN — Codex 5: the answer is expected at the size the
// crop travelled at; a rounded side (≤ max(16 px, 2 %)) is fitted, a canvas of another size is kept
// as delivered with inpaint_not_composited (it was silently scaled into the crop before).
// MUTATION (measured red): drop the drift check in compositeInpaint → the 128×64 answer is fitted.
func TestAnInpaintAnswerOfAnotherSizeIsNAMED_NOT_HIDDEN(t *testing.T) {
	srcBytes, _ := gradientPNG(t, 64, 64)
	w := testWorker(&fakeStore{}, media(11, 12), newFakeSink(ContentTypePNG), Providers{})
	w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": srcBytes, "m/12.png": grayMask(t, 64, 64, image.Rect(10, 10, 20, 20))}}
	plan := InpaintPlan{SourceURL: "https://cdn.example/m/11.png", MaskURL: "https://cdn.example/m/12.png",
		Bounds: image.Rect(0, 0, 64, 64), Rect: image.Rect(0, 0, 64, 64), Scale: 1, Crop: image.Pt(64, 64)}

	wide := solidPNG(t, 128, 64, color.NRGBA{R: 255, A: 255})
	out := &Outcome{Artifacts: []Artifact{{Bytes: wide, ContentType: ContentTypePNG}}}
	err := w.postProcess(context.Background(), Job{Inpaint: &plan}, out)
	require.ErrorIs(t, err, errInpaintNotComposited)
	require.Contains(t, err.Error(), "128×64")
	require.Equal(t, wide, out.Artifacts[0].Bytes, "the paid answer is kept as delivered")

	rounded := solidPNG(t, 72, 64, color.NRGBA{R: 255, A: 255}) // one side rounded up by 8 px
	out = &Outcome{Artifacts: []Artifact{{Bytes: rounded, ContentType: ContentTypePNG}}}
	require.NoError(t, w.postProcess(context.Background(), Job{Inpaint: &plan}, out))
}

// TestTheChargeLogSAYS_ONLY_WHAT_FOLLOWS — Codex 10 (the G-02 r2 wording): units over the ceiling
// under a reserve that still covers the booking is a WARN about the ceiling; «the reservation was
// below its booking» is an ERROR only when the booked money exceeds what the run reserved.
// MUTATION (measured red): log the ERROR on units > ceiling alone → the first case sees an ERROR.
func TestTheChargeLogSAYS_ONLY_WHAT_FOLLOWS(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	c := fal.New(fal.Config{APIKey: "k", UnitUSDOutpaint: 0.01, UnitsCeilingOutpaint: 1})
	booked := decimal.NullDecimal{Decimal: decimal.RequireFromString("0.02"), Valid: true}
	covered := Job{RunID: 1, RouteReservedUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.12"), Valid: true}}
	logRouteChargeOverReserve(context.Background(), c, fal.RouteOutpaint, covered, "x#1", 2, booked)
	require.Contains(t, buf.String(), "level=WARN")
	require.NotContains(t, buf.String(), "level=ERROR", "0.02 under a 0.12 reserve is not «short»")
	require.NotContains(t, buf.String(), "below its booking")

	buf.Reset()
	short := Job{RunID: 1, RouteReservedUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.01"), Valid: true}}
	logRouteChargeOverReserve(context.Background(), c, fal.RouteOutpaint, short, "x#1", 2, booked)
	require.Contains(t, buf.String(), "level=ERROR")
	require.Contains(t, buf.String(), "below its booking")

	// A drawn-bigger canvas with no tariff at all still says so when the booking passes the reserve.
	buf.Reset()
	logRouteChargeOverReserve(context.Background(), fal.New(fal.Config{APIKey: "k"}), fal.RouteFill, short, "x#1", 4,
		decimal.NullDecimal{Decimal: decimal.RequireFromString("0.15"), Valid: true})
	require.Contains(t, buf.String(), "level=ERROR")
}

// TestAFlagTurnedOffSTOPS_QUEUED_SPEND — Codex 6: a Gemini run the door froze while
// DESIGN_ENGINE_GEMINI was on reaches a worker whose flag is off: refused before RecordRunPrompt and
// StartAttempt (free), terminal, `unknown_image_model`. The same run with the flag on is sent.
// MUTATION (measured red): drop the engineOffAtSubmit call → the provider is called.
func TestAFlagTurnedOffSTOPS_QUEUED_SPEND(t *testing.T) {
	for _, slug := range []string{EngineGemini3Pro, EngineSeedream5Pro} {
		r := testRun(1, entity.DesignRunKindFlat)
		r.Params = entity.RawJSON(`{"image":{"model":"` + slug + `","quality":"high"}}`)
		img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
		st := &fakeStore{}
		w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})
		require.NoError(t, w.execute(context.Background(), r, "tok"))
		require.Empty(t, img.calls, slug)
		require.Empty(t, st.started, "nothing opened, nothing spent")
		require.Empty(t, st.recordedPrompts)
		require.Equal(t, []string{CodeUnknownImageModel + " retry=false"}, failedCodes(st))
	}
	// A GPT row is never refused here, flags or no flags.
	r := testRun(1, entity.DesignRunKindFlat)
	r.Params = entity.RawJSON(`{"image":{"model":"openai/gpt-image-2","quality":"high"}}`)
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{Image: img})
	require.NoError(t, w.execute(context.Background(), r, "tok"))
	require.Len(t, img.calls, 1)
}

// TestABigExtendSHRINKS_TO_FIT_THE_INLINE_CAP — Codex 9: the scaled source travels as a lossless PNG,
// and an incompressible one over the inline cap is planned SMALLER until it fits, rather than refused
// after the reservation. What still cannot fit is refused in buildJob — before StartAttempt, free.
// MUTATION (measured red): extendShrinkTries = 1 → errFreeformJobTooLarge on the first row.
func TestABigExtendSHRINKS_TO_FIT_THE_INLINE_CAP(t *testing.T) {
	keep := extendInlineCap
	defer func() { extendInlineCap = keep }()
	extendInlineCap = 1 << 20

	big := noisePNG(t, 1024, 1536)
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": big}}
	p := parseParams(entity.RawJSON(`{"extra_input_media_ids":[11],"extend":{"aspect_ratio":"21:9"}}`))
	job := Job{References: []string{"https://cdn.example/m/11.png"}}
	require.NoError(t, deriveExtendPlan(context.Background(), objs, p, &job))
	require.LessOrEqual(t, len(job.References[0]), extendInlineCap)
	first, _ := planExtend(1024, 1536, 21.0/9)
	require.Less(t, job.Extend.Scale, first.Scale, "planned smaller than the 3 MP plan")

	// The composite pastes EXACTLY the pixels that travelled.
	src, _, err := fetchStoredPicture(context.Background(), objs, job.Extend.SourceURL)
	require.NoError(t, err)
	sent, err := png.Decode(bytes.NewReader(decodeDataURI(t, job.References[0])))
	require.NoError(t, err)
	art, err := compositeExtend(src, *job.Extend, solidPNG(t, job.Extend.Canvas.Dx(), job.Extend.Canvas.Dy(), color.NRGBA{A: 255}))
	require.NoError(t, err)
	got, err := png.Decode(bytes.NewReader(art.Bytes))
	require.NoError(t, err)
	region := image.NewNRGBA(job.Extend.Source)
	draw.Draw(region, region.Bounds(), got, job.Extend.Offset, draw.Src)
	sentN := image.NewNRGBA(sent.Bounds())
	draw.Draw(sentN, sentN.Bounds(), sent, image.Point{}, draw.Src)
	require.Equal(t, sentN.Pix, region.Pix, "pasted == submitted, pixel for pixel")

	// Nothing fits: refused while the job is built — before StartAttempt, so nothing is spent.
	extendInlineCap = 100
	prov := &fakeProvider{name: providerNameFalOutpaint}
	st := &fakeStore{}
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG, ContentTypeJPEG), Providers{Outpaint: prov})
	w.objects = objs
	require.NoError(t, w.execute(context.Background(),
		extendRun(`{"extra_input_media_ids":[11],"extend":{"aspect_ratio":"21:9"}}`), "tok"))
	require.Empty(t, prov.calls)
	require.Empty(t, st.started, "no attempt: free")
	require.Equal(t, []string{CodeJobTooLarge + " retry=false"}, failedCodes(st))
}

func decodeDataURI(t *testing.T, uri string) []byte {
	t.Helper()
	i := strings.Index(uri, ",")
	require.Positive(t, i)
	raw, err := base64.StdEncoding.DecodeString(uri[i+1:])
	require.NoError(t, err)
	return raw
}

// TestAForeignSlugIsCLOSED_NOT_ADVERTISED — Fable m-1: FAL_MODEL_OUTPAINT / FAL_MODEL_FILL naming a
// slug no body was written for is not a bounded route (the band hides the tile, the door refuses
// kind_not_available), and the worker's second lock refuses it before the submit.
// MUTATION (measured red): drop falRouteSlugSupported in FalRouteOf → Bounded.
func TestAForeignSlugIsCLOSED_NOT_ADVERTISED(t *testing.T) {
	for _, tc := range []struct {
		cfg  fal.Config
		kind string
		env  string
	}{
		{fal.Config{APIKey: "k", ModelOutpaint: "fal-ai/ideogram/v3/reframe"}, entity.DesignRunKindExtend, "FAL_MODEL_OUTPAINT"},
		{fal.Config{APIKey: "k", ModelFill: "fal-ai/flux-lora/inpainting"}, entity.DesignRunKindInpaint, "FAL_MODEL_FILL"},
	} {
		r, ok := FalRouteOf(fal.New(tc.cfg), tc.kind)
		require.True(t, ok)
		require.False(t, r.Bounded, tc.kind)
		require.True(t, r.Unsupported, tc.kind)
		require.Equal(t, tc.env, r.Flag)
		require.Contains(t, r.Unbounded, tc.env)
	}
	for _, slug := range []string{fal.DefaultModelOutpaint, "fal-ai/bria/expand"} {
		r, _ := FalRouteOf(fal.New(fal.Config{APIKey: "k", ModelOutpaint: slug}), entity.DesignRunKindExtend)
		require.True(t, r.Bounded, slug)
		require.False(t, r.Unsupported, slug)
	}
	_, err := fillBody("fal-ai/flux-lora/inpainting", Job{References: []string{"x"}})
	require.ErrorIs(t, err, fal.ErrBadOption)
	require.True(t, errors.Is(err, fal.ErrBadRequest), "terminal and free for the classifier")
}
