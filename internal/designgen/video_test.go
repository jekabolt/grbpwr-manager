package designgen

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/runblob"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ THE VIDEO ROUTE (B-32) — the stand is a recording fake at the adapter's seam ═════════════════
//
// The runblob adapter's host is a constant by design (an editable base url is a place a key can be
// sent), so the route is driven through videoTransport: the fake records every submit body and answers
// every status read as told. The WIRE — the path, the Bearer, the body marshalling, the two 404s — is
// pinned by the adapter's own goldens (runblob_test.go); what this file measures is what the route hands
// the adapter and what it does with the answers: the money, the artifact, the ledger.

const videoGenID = "3f2b1c4e-8a7d-4e21-9b0c-5d6e7f8a9b10"

// mp4Bytes — the smallest payload the route (and the bucket's sniff) reads as an mp4 container.
var mp4Bytes = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'c', 'l', 'i', 'p'}

type fakeVideoTransport struct {
	mu       sync.Mutex
	enabled  bool
	submits  []map[string]any
	families []string
	statusOf []string
	// submitReply answers every Submit; nil = accepted at $0.29.
	submitReply func(body map[string]any) (*runblob.Submission, error)
	// statusReply answers every Status; nil = completed with a clip url.
	statusReply func(id string) (*runblob.Generation, error)
}

func (f *fakeVideoTransport) Enabled() bool { return f.enabled }

func (f *fakeVideoTransport) Submit(_ context.Context, family string, body map[string]any) (*runblob.Submission, error) {
	f.mu.Lock()
	f.families = append(f.families, family)
	f.submits = append(f.submits, body)
	f.mu.Unlock()
	if f.submitReply != nil {
		return f.submitReply(body)
	}
	return &runblob.Submission{ID: videoGenID, Status: "pending",
		PriceUSD: decimal.NewNullDecimal(decimal.RequireFromString("0.29")), Engaged: true}, nil
}

func (f *fakeVideoTransport) Status(_ context.Context, _ string, id string) (*runblob.Generation, error) {
	f.mu.Lock()
	f.statusOf = append(f.statusOf, id)
	f.mu.Unlock()
	if f.statusReply != nil {
		return f.statusReply(id)
	}
	return &runblob.Generation{Status: "completed", VideoURL: "https://cdn.runblob.io/v/abc.mp4", Model: "kling_2.5_turbo"}, nil
}

// videoRouteWith — the route over a fake transport, its clip download stubbed to `clip` (nil = mp4Bytes).
func videoRouteWith(tr *fakeVideoTransport, route func() VideoRoute, clip func(string) ([]byte, error)) *videoProvider {
	if clip == nil {
		clip = func(string) ([]byte, error) { return mp4Bytes, nil }
	}
	return &videoProvider{c: tr, route: route,
		fetch: func(_ context.Context, u string) ([]byte, error) { return clip(u) }}
}

// videoRun — a frozen video run: the ask, params.video, the one source picture 41 (1000×1500: portrait).
func videoRun(id int) entity.DesignRun {
	r := testRun(id, entity.DesignRunKindVideo)
	r.Ask = nullString("the coat sways in a slow breeze")
	r.Params = entity.RawJSON(`{"video": {"source_media_id": 41, "duration": 5, "model": "kling_2.5_turbo"}}`)
	r.Inputs = entity.RawJSON(`{"refs": [{"media_id": 41, "note": "the picture to animate"}]}`)
	r.PriceEstimate = decimal.NewNullDecimal(decimal.RequireFromString("1.5"))
	return r
}

func videoMedia() fakeMedia {
	m := media(41)
	item := m.byID[41]
	item.FullSizeWidth, item.FullSizeHeight = 1000, 1500
	m.byID[41] = item
	return m
}

// acceptedVideoRun — the same run after a submit: the attempt row holds the locator with the price.
func acceptedVideoRun(id int, locator string) entity.DesignRun {
	r := videoRun(id)
	r.Attempts = []entity.DesignRunAttempt{{
		RunId: id, AttemptNo: 1, Provider: entity.AIProviderRunblob,
		State:             entity.DesignAttemptAccepted,
		ProviderRequestId: nullString(locator),
	}}
	return r
}

// submittedVideoRow — the submit's ledger row as Execute writes it: `accepted`, priced at $0.29 provider.
func submittedVideoRow(job Job, locator string) {
	h := job.beginCall(context.Background(), entity.AIProviderRunblob, "kling_2.5_turbo", 1)
	end := acceptedEnd(locator)
	end.CostUSD, end.CostSource = decimal.NewNullDecimal(decimal.RequireFromString("0.29")), entity.AICostProvider
	job.finishCall(context.Background(), h, end)
}

// TestAVideoRunSubmitsThePictureAndFiveSeconds — ACCEPTANCE: «a video run → one submit with the
// picture's URL and duration:"5"». The worker's fresh pass: one Submit on the kling family with the
// source picture's public url as image_url, the ask as the prompt, the frozen slug, "5" as a STRING
// (the spec's spelling for every model but Kling 3), and the ratio nearest the picture's shape; the
// attempt closes `accepted` with the locator `kling#<id>#0.29` and NO price (the collect books it).
//
// MUTATION (measured red → green): videoDurationWire returning the int 5 for kling_2.5_turbo → the
// body's duration is a float64 5, the string assertion red.
func TestAVideoRunSubmitsThePictureAndFiveSeconds(t *testing.T) {
	tr := &fakeVideoTransport{enabled: true,
		statusReply: func(string) (*runblob.Generation, error) { return &runblob.Generation{Status: "processing"}, nil }}
	st := &fakeStore{}
	w := testWorker(st, videoMedia(), newFakeSink(ContentTypeMP4), Providers{Video: videoRouteWith(tr, nil, nil)})

	require.NoError(t, w.execute(context.Background(), videoRun(3), "tok"))

	require.Equal(t, []string{"kling"}, tr.families)
	require.Len(t, tr.submits, 1, "one clip, one submit")
	body := tr.submits[0]
	require.Equal(t, "https://cdn.example/m/41.png", body["image_url"], "the source picture's public url")
	require.Equal(t, "the coat sways in a slow breeze", body["prompt"], "the ask, verbatim")
	require.Equal(t, "kling_2.5_turbo", body["model"], "the frozen slug")
	require.Equal(t, "5", body["duration"], `duration travels as the STRING "5" on the 2.x models`)
	require.Equal(t, "9:16", body["aspect_ratio"], "a 1000×1500 picture is nearest 9:16")

	require.Equal(t, []string{"the coat sways in a slow breeze"}, st.recordedPrompts,
		"the history column holds exactly what Kling was sent — the ask, no craft paragraph")
	require.Len(t, st.started, 2, "the submit's attempt, then the free collect's")
	require.Equal(t, entity.AIProviderRunblob, st.started[0].Provider, "the attempt names the account the money went to")
	sub := st.finished[0]
	require.Equal(t, entity.DesignAttemptAccepted, sub.State)
	require.Equal(t, "kling#"+videoGenID+"#0.29", sub.ProviderRequestId, "the locator carries the price the job was bought at")
	require.False(t, sub.Price.Valid, "an accepted submit books no price — the collect does")
	// Still processing: the collect's row closes retryable and the run comes back for a free look.
	require.Equal(t, []string{videoGenID}, tr.statusOf)
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable, "processing is a wait, not a failure")
	require.Equal(t, CodeProviderTimeout, st.failed[0].ErrorCode)
}

// TestTheCollectStoresTheClipAndBooksThePrice — ACCEPTANCE: «the collect downloads the mp4 into the
// sink under video/mp4 and closes the run delivered». A resume of an accepted run: one free status
// read, the download, the sink's Put with video/mp4, CompleteRun with one `video` picture, the collect
// attempt `delivered` at the submit's price ($0.29, off the locator), no second submit.
//
// MUTATION (measured red → green): Collect returning the Outcome without `Price: price` → the
// delivered attempt's price invalid, red.
func TestTheCollectStoresTheClipAndBooksThePrice(t *testing.T) {
	tr := &fakeVideoTransport{enabled: true}
	var asked string
	route := videoRouteWith(tr, nil, func(u string) ([]byte, error) { asked = u; return mp4Bytes, nil })
	locator := "kling#" + videoGenID + "#0.2900"
	prior := acceptedVideoRun(4, locator)
	st := &fakeStore{getRun: &prior}
	sink := newFakeSink(ContentTypeMP4)
	w := testWorker(st, videoMedia(), sink, Providers{Video: route})

	require.NoError(t, w.execute(context.Background(), acceptedVideoRun(4, locator), "tok"))

	require.Empty(t, tr.submits, "a paid job is collected, never bought again")
	require.Equal(t, []string{videoGenID}, tr.statusOf)
	require.Equal(t, "https://cdn.runblob.io/v/abc.mp4", asked, "the clip is fetched from the result url, at once")
	require.Equal(t, []string{ContentTypeMP4}, sink.putTypes, "the sink's VIDEO door")
	require.Equal(t, mp4Bytes, sink.putBytes[0])
	require.Len(t, st.completed, 1)
	require.Len(t, st.completed[0].Outputs, 1)
	require.Equal(t, entity.DesignPictureKindVideo, st.completed[0].Outputs[0].Kind, "filed as a clip, never as a flat")
	require.Len(t, st.finished, 1, "the collect's own attempt row")
	fin := st.finished[0]
	require.Equal(t, entity.DesignAttemptDelivered, fin.State)
	require.True(t, fin.Price.Valid && fin.Price.Decimal.Equal(decimal.RequireFromString("0.29")),
		"the submit's price is booked on the delivering collect: got %v", fin.Price)
	require.Equal(t, locator, fin.ProviderRequestId, "one charge, one locator")
	require.Empty(t, st.failed)
}

// TestAFailedGenerationIsRefundedAndFree — ACCEPTANCE: «a failed status → failed, free». runblob's
// `failed` refunds: the collect attempt closes `failed` with NO price, the run closes terminal, the
// ledger's `accepted` row becomes `failed` with a KNOWN zero (cost_source provider — the refund is the
// provider's own statement), and the reason travels in the sentence.
//
// MUTATION (measured red → green): videoCollectEnd writing `unknown`/none for errVideoFailed → the
// ledger row's status and the zero cost both red.
func TestAFailedGenerationIsRefundedAndFree(t *testing.T) {
	tr := &fakeVideoTransport{enabled: true, statusReply: func(string) (*runblob.Generation, error) {
		return &runblob.Generation{Status: "failed", Error: "CONTENT_POLICY_VIOLATION"}, nil
	}}
	route := videoRouteWith(tr, nil, nil)
	job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindVideo}, entity.AIPurposeVideoGenerate)
	submittedVideoRow(job, "kling#"+videoGenID+"#0.29")

	out, err := route.Collect(context.Background(), job, "kling#"+videoGenID+"#0.29")
	require.Nil(t, out, "a refunded failure books nothing on the attempt")
	require.ErrorIs(t, err, errVideoFailed)
	require.Contains(t, err.Error(), "CONTENT_POLICY_VIOLATION", "the provider's reason reaches the sentence")
	v := classify(err)
	require.False(t, v.Retryable, "terminal: the provider ended it")
	require.Equal(t, entity.DesignAttemptFailed, v.State, "failed, not unknown — the money came back")
	require.Equal(t, CodeTaskFailed, v.Code)

	rows := ai.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AICallFailed, rows[0].Status)
	require.True(t, rows[0].End.CostUSD.Valid && rows[0].End.CostUSD.Decimal.IsZero(),
		"a refund is a KNOWN zero, never NULL: %v", rows[0].End.CostUSD)
	require.Equal(t, entity.AICostProvider, rows[0].End.CostSource)
}

// TestAMissingKeyClosesTheVideoDoorForFree — ACCEPTANCE: «a missing key → the door refuses before any
// run row». The pre-flight the door calls (PreflightKind) refuses a keyless runblob as
// kind_not_available with the sentence that names where the key goes — admin → AI providers, and that
// runblob has no environment variable; and the worker's own pass refuses the same way before any
// attempt row exists.
//
// MUTATION (measured red → green): videoProvider.Enabled returning true whatever the transport says →
// the pre-flight passes, the pass opens an attempt, red on both counts.
func TestAMissingKeyClosesTheVideoDoorForFree(t *testing.T) {
	st := &fakeStore{}
	w := testWorker(st, videoMedia(), newFakeSink(ContentTypeMP4),
		Providers{Video: NewVideoProvider(runblob.New(runblob.Config{}), nil)})

	err := w.PreflightKind(entity.DesignRunKindVideo)
	require.Error(t, err)
	var kr *KindRefusal
	require.True(t, errors.As(err, &kr))
	require.Equal(t, CodeKindNotAvailable, kr.Reason)
	require.Contains(t, err.Error(), "admin → AI providers")
	require.Contains(t, err.Error(), "no environment variable")

	require.NoError(t, w.execute(context.Background(), videoRun(5), "tok"))
	require.Empty(t, st.started, "no attempt row: nothing could have been reserved or charged")
	require.Len(t, st.failed, 1)
	require.False(t, st.failed[0].Retryable)
	require.Equal(t, CodeKindNotAvailable, st.failed[0].ErrorCode)
}

// TestAnUnansweredSubmitIsUnknownAndNeverResubmitted — D-16 for video: a submit that LEFT (the
// adapter says engaged) closes the attempt `unknown` / submit_unconfirmed, the run terminal, the
// ledger row `unknown` — and the next pass does not buy again (Retryable false). A 502 on the submit
// is read the same way (a gateway can lose the queue's answer after the enqueue); a bare 503 stays free.
//
// MUTATION (measured red → green): videoSubmitError returning the CallError unwrapped → classify's
// default branch closes the attempt `failed` instead of `unknown`, red.
func TestAnUnansweredSubmitIsUnknownAndNeverResubmitted(t *testing.T) {
	for _, c := range []struct {
		name   string
		reply  *aiprov.CallError
		state  string
		ledger string
	}{
		{"engaged post-write break", &aiprov.CallError{Provider: "runblob", Code: aiprov.CodeTimeout, Engaged: true,
			Err: errors.New("runblob: request failed: deadline")}, entity.DesignAttemptUnknown, entity.AICallUnknown},
		{"502 from the gateway", &aiprov.CallError{Provider: "runblob", Code: aiprov.CodeProviderError, HTTPStatus: 502,
			Retryable: true, Err: errors.New("runblob: API error (HTTP 502): bad gateway")}, entity.DesignAttemptUnknown, entity.AICallUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			reply := c.reply
			tr := &fakeVideoTransport{enabled: true,
				submitReply: func(map[string]any) (*runblob.Submission, error) { return nil, reply }}
			st := &fakeStore{}
			w := testWorker(st, videoMedia(), newFakeSink(ContentTypeMP4), Providers{Video: videoRouteWith(tr, nil, nil)})

			require.NoError(t, w.execute(context.Background(), videoRun(6), "tok"))
			require.Len(t, st.finished, 1)
			require.Equal(t, c.state, st.finished[0].State)
			require.Equal(t, CodeSubmitUnconfirmed, st.finished[0].ErrorCode)
			require.Len(t, st.failed, 1)
			require.False(t, st.failed[0].Retryable, "money may have moved: never buy again")
		})
	}
	t.Run("a bare 503 is a refusal at the gate: free, retryable", func(t *testing.T) {
		tr := &fakeVideoTransport{enabled: true, submitReply: func(map[string]any) (*runblob.Submission, error) {
			return nil, &aiprov.CallError{Provider: "runblob", Code: aiprov.CodeProviderError, HTTPStatus: http.StatusServiceUnavailable,
				Retryable: true, Err: errors.New("runblob: API error (HTTP 503): busy")}
		}}
		st := &fakeStore{}
		w := testWorker(st, videoMedia(), newFakeSink(ContentTypeMP4), Providers{Video: videoRouteWith(tr, nil, nil)})
		require.NoError(t, w.execute(context.Background(), videoRun(7), "tok"))
		require.Equal(t, entity.DesignAttemptFailed, st.finished[0].State)
		require.True(t, st.failed[0].Retryable)
	})
}

// TestTheSubmitLedgerRowCarriesRunblobsPrice — the ledger's `accepted` row is priced AT SUBMIT with
// the provider's own number (cost_source provider), under purpose video.generate and provider runblob;
// a completed collect turns it `ok` at the same number. «Their number» from the first moment.
//
// MUTATION (measured red → green): Execute finishing the row with the bare acceptedEnd (no CostUSD) →
// the accepted row's cost NULL, red.
func TestTheSubmitLedgerRowCarriesRunblobsPrice(t *testing.T) {
	tr := &fakeVideoTransport{enabled: true}
	route := videoRouteWith(tr, func() VideoRoute { return VideoRoute{Model: "kling_2.1"} }, nil)
	job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindVideo, Prompt: "sway",
		References: []string{"https://cdn.example/m/41.png"}}, entity.AIPurposeVideoGenerate)

	out, err := route.Execute(context.Background(), job)
	require.NoError(t, err)
	require.True(t, out.Pending)
	require.Equal(t, "kling_2.1", tr.submits[0]["model"], "no frozen slug: the route row's")
	require.Equal(t, "kling_2.1", out.Model)
	require.False(t, out.Price.Valid, "the OUTCOME carries no price at submit (the store's accepted rule)")
	rows := ai.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AIProviderRunblob, rows[0].Start.ProviderKey)
	require.Equal(t, entity.AIPurposeVideoGenerate, rows[0].Start.Purpose)
	require.Equal(t, "kling_2.1", rows[0].Start.Model)
	require.Equal(t, entity.AICallAccepted, rows[0].Status)
	require.True(t, rows[0].End.CostUSD.Valid && rows[0].End.CostUSD.Decimal.Equal(decimal.RequireFromString("0.29")))
	require.Equal(t, entity.AICostProvider, rows[0].End.CostSource)

	_, err = route.Collect(context.Background(), job, out.RequestID)
	require.NoError(t, err)
	rows = ai.Rows()
	require.Equal(t, entity.AICallOK, rows[0].Status)
	require.True(t, rows[0].End.CostUSD.Decimal.Equal(decimal.RequireFromString("0.29")))
	require.Equal(t, entity.AICostProvider, rows[0].End.CostSource)
}

// TestACompletedJobWithNoClipIsChargedAndUnknown — «paid, and nothing came of it»: completed with an
// empty video_url, or a download that is not an mp4, closes `unknown` WITH the submit's price on the
// outcome (no refund is documented for a completed job); a download that broke on the wire is
// retryable and writes nothing.
func TestACompletedJobWithNoClipIsChargedAndUnknown(t *testing.T) {
	locator := "kling#" + videoGenID + "#0.29"
	t.Run("no video_url", func(t *testing.T) {
		tr := &fakeVideoTransport{enabled: true, statusReply: func(string) (*runblob.Generation, error) {
			return &runblob.Generation{Status: "completed"}, nil
		}}
		job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindVideo}, entity.AIPurposeVideoGenerate)
		submittedVideoRow(job, locator)
		out, err := videoRouteWith(tr, nil, nil).Collect(context.Background(), job, locator)
		require.ErrorIs(t, err, errVideoNoResult)
		require.NotNil(t, out, "the price rides beside the error")
		require.Equal(t, "0.29", out.Price.Decimal.String())
		require.Equal(t, entity.DesignAttemptUnknown, classify(err).State)
		row := ai.Rows()[0]
		require.Equal(t, entity.AICallChargedFailed, row.Status)
		require.True(t, row.End.CostUSD.Valid && row.End.CostUSD.Decimal.Equal(decimal.RequireFromString("0.29")),
			"charged: the submit's number stays on the row: %v", row.End.CostUSD)
		require.Equal(t, entity.AICostProvider, row.End.CostSource)
	})
	t.Run("not an mp4", func(t *testing.T) {
		tr := &fakeVideoTransport{enabled: true}
		route := videoRouteWith(tr, nil, func(string) ([]byte, error) { return []byte("<html>expired</html>"), nil })
		out, err := route.Collect(context.Background(), Job{RunID: 70}, locator)
		require.ErrorIs(t, err, errVideoNoResult)
		require.NotNil(t, out)
		require.Equal(t, "0.29", out.Price.Decimal.String())
	})
	t.Run("the wire broke: retryable, nothing written", func(t *testing.T) {
		tr := &fakeVideoTransport{enabled: true}
		route := videoRouteWith(tr, nil, func(string) ([]byte, error) {
			return nil, fmt.Errorf("%w: reset", errVideoFetchFailed)
		})
		job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindVideo}, entity.AIPurposeVideoGenerate)
		out, err := route.Collect(context.Background(), job, locator)
		require.Nil(t, out)
		require.ErrorIs(t, err, errVideoFetchFailed)
		require.True(t, classify(err).Retryable)
		require.Empty(t, ai.Rows(), "nothing was written: the next collect decides")
	})
}

// TestVideoLocatorAndAspectAndDuration — the small pure rules: the locator round-trips id and price
// (and a bare uuid still collects); the nearest Kling ratio; the two spellings of the duration.
func TestVideoLocatorAndAspectAndDuration(t *testing.T) {
	price := decimal.NewNullDecimal(decimal.RequireFromString("0.29"))
	loc := videoLocator(videoGenID, price)
	require.Equal(t, "kling#"+videoGenID+"#0.29", loc)
	require.LessOrEqual(t, len(loc), providerRequestIDMax, "fits design_run_attempt.provider_request_id")
	id, p := splitVideoLocator(loc)
	require.Equal(t, videoGenID, id)
	require.True(t, p.Valid && p.Decimal.Equal(price.Decimal))
	id, p = splitVideoLocator("kling#" + videoGenID)
	require.Equal(t, videoGenID, id)
	require.False(t, p.Valid, "«calculating» at submit: no price to book")
	id, _ = splitVideoLocator(videoGenID)
	require.Equal(t, videoGenID, id, "a bare uuid is the id")

	for _, c := range []struct {
		w, h int
		want string
	}{{1920, 1080, "16:9"}, {1080, 1920, "9:16"}, {1000, 1000, "1:1"}, {1000, 1500, "9:16"}, {1500, 1000, "16:9"},
		{1200, 1000, "1:1"}, {0, 0, ""}, {-1, 5, ""}} {
		require.Equalf(t, c.want, nearestVideoAspect(c.w, c.h), "%d×%d", c.w, c.h)
	}
	require.Equal(t, "5", videoDurationWire("kling_2.5_turbo", 5))
	require.Equal(t, 5, videoDurationWire("kling_3_pro", 5))

	// A prompt past Kling's ceiling and a data-uri source are refused before the wire, free and terminal.
	route := videoRouteWith(&fakeVideoTransport{enabled: true}, nil, nil)
	_, err := route.Execute(context.Background(), Job{Prompt: strings.Repeat("x", VideoMaxPromptRunes+1),
		References: []string{"https://cdn.example/a.png"}})
	require.Error(t, err)
	require.Equal(t, CodeBadRequest, classify(err).Code)
	require.False(t, classify(err).Retryable)
	_, err = route.Execute(context.Background(), Job{Prompt: "sway", References: []string{"data:image/png;base64,AAAA"}})
	require.Equal(t, CodeBadRequest, classify(err).Code)
}
