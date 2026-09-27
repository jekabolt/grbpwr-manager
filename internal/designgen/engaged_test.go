package designgen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
)

// ═══ B-14 — THE WORKER READS THE TRANSPORT'S MONEY FACT, NOT ITS SENTINEL ═══
//
// TestTheWriteIsTheMoneyBoundaryON_EVERY_DESIGN_TRANSPORT runs each paid call of the design band —
// orimages Generate, recraft direct imageToImage, the fal 3D submit, the Meshy create-task — through
// its REAL client and its real route against an httptest stand, and reads the two answers the worker
// writes from it: classify's verdict (does the queue try again, and in which state does the attempt
// close) and the ledger row. The rows it pins are the ones that were wrong before B-14:
//
//   - a deadline AFTER the request was written — the orimages / Meshy / recraft default retried it,
//     a second payment for one picture — is now `unknown`, NOT retryable, and booked `unknown`;
//   - a refused dial BEFORE the write is `failed` and retryable, booked `free` (it was `unknown`);
//   - a 5xx is `failed` and retryable, booked `free` (it was `unknown` twice over) — except fal's
//     submit and Meshy's create call (B-13/A1), where every 5xx but a bare 503 may have been
//     enqueued: `unknown`, final;
//   - 401 / 402 / 404 / 429 keep their codes and are booked `free`;
//   - a real 408 answering one of these paid POSTs is ENGAGED (B-13/A3): `unknown`, never retried,
//     booked `unknown` — the server may have taken the whole body and billed it before giving up.
//     (It used to be retryable weather for the worker while the ledger booked it `unknown`: the row
//     said «money may have moved» and the next pass bought again.)
//
// MUTATIONS (each measured red→green): classify's CallError override removed (classifyBySentinel
// returned as is) → the post-write rows turn retryable and the 5xx / pre-write rows `unknown`;
// imageCallEnd's `spoke && !ce.Engaged` arm removed → the orimages refusals and pre-write rows book
// `unknown`; falSubmitEnd reading `!spoke && …` only (the CallError's Engaged ignored) → fal's
// post-write and 500/502/504 rows book `free`; meshySubmitEnd's `spoke && !ce.Engaged` arm removed →
// the Meshy refusals book `unknown`.
//
// B-13/A3 MUTATIONS (each measured red→green): each transport's 408 arm reverted to «not engaged,
// retryable» in turn (orimages / recraft direct: the `status == http.StatusRequestTimeout` branch
// disabled; fal / Meshy: `|| status == http.StatusRequestTimeout` dropped from the submit arm) → that
// transport's «a real 408» row goes red here and in TestA408IsNEVER_BOOKED_FREE, the other three stay
// green.

// designTransport is one paid call of the band, driven through its real client and route.
type designTransport struct {
	name    string
	billing string
	// call runs ONE paid call against baseURL with the given client timeout and returns the error and
	// the ledger row it wrote.
	call func(t *testing.T, baseURL string, timeout time.Duration) (error, aiprovtest.Row)
	// code is classify's word for a refusal with this status — «codes as today».
	code map[int]string
	// unconfirmed5xx — fal, Meshy (B-13/A1): a 5xx other than a bare 503 on a submit may have been
	// enqueued.
	unconfirmed5xx bool
}

func oneRow(t *testing.T, ai *aiprovtest.Store) aiprovtest.Row {
	t.Helper()
	rows := ai.Rows()
	require.Len(t, rows, 1, "one paid call, one ledger row")
	return rows[0]
}

func designTransports() []designTransport {
	ref := []string{"https://cdn.example/f.png"}
	return []designTransport{
		{
			name: "openrouter images", billing: entity.AIProviderOpenRouter,
			call: func(t *testing.T, baseURL string, timeout time.Duration) (error, aiprovtest.Row) {
				job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindFlat, Prompt: "a flat", Layout: "one"},
					entity.AIPurposeImageGenerate)
				_, err := NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: baseURL, HTTPTimeout: timeout})).
					Execute(context.Background(), job)
				return err, oneRow(t, ai)
			},
			code: map[int]string{401: CodeUnauthorized, 402: CodeOutOfCredit, 404: CodeModelRetired, 429: CodeRateLimited,
				408: CodeProviderUnavailable},
		},
		{
			name: "recraft direct", billing: entity.AIProviderRecraft,
			call: func(t *testing.T, baseURL string, timeout time.Duration) (error, aiprovtest.Row) {
				job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindVector, Prompt: "a flat", References: ref},
					entity.AIPurposeVector)
				_, err := NewVectorProvider(recraft.New(recraft.Config{Route: string(recraft.RouteDirect),
					Direct: recraft.DirectConfig{APIKey: "k", BaseURL: baseURL, HTTPTimeout: timeout}}, nil)).
					Execute(context.Background(), job)
				return err, oneRow(t, ai)
			},
			code: map[int]string{401: CodeUnauthorized, 402: CodeOutOfCredit, 404: CodeModelRetired, 429: CodeRateLimited,
				408: CodeBadRequest},
		},
		{
			name: "fal 3D submit", billing: entity.AIProviderFal, unconfirmed5xx: true,
			call: func(t *testing.T, baseURL string, timeout time.Duration) (error, aiprovtest.Row) {
				job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindThreed, References: ref,
					ReferenceViews: []string{entity.DesignViewFront}}, entity.AIPurposeThreed)
				_, err := NewFalThreedProvider(fal.New(fal.Config{APIKey: "k", BaseURL: baseURL, Model3D: falMeshySlug,
					HTTPTimeout: timeout})).Execute(context.Background(), job)
				return err, oneRow(t, ai)
			},
			code: map[int]string{401: CodeUnauthorized, 402: CodeOutOfCredit, 404: CodeModelRetired, 429: CodeRateLimited,
				408: CodeSubmitUnconfirmed},
		},
		{
			name: "meshy submit", billing: entity.AIProviderMeshy, unconfirmed5xx: true,
			call: func(t *testing.T, baseURL string, timeout time.Duration) (error, aiprovtest.Row) {
				job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindThreed, References: ref}, entity.AIPurposeThreed)
				_, err := NewThreedProvider(meshy.New(meshy.Config{APIKey: "k", BaseURL: baseURL, HTTPTimeout: timeout})).
					Execute(context.Background(), job)
				return err, oneRow(t, ai)
			},
			// A 404 answering Meshy's POST is its generic 4xx (ErrBadRequest), as it was before B-14.
			code: map[int]string{401: CodeUnauthorized, 402: CodeOutOfCredit, 404: CodeBadRequest, 429: CodeRateLimited,
				408: CodeSubmitUnconfirmed},
		},
	}
}

func statusStand(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"said no"},"message":"said no","detail":"said no"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTheWriteIsTheMoneyBoundaryON_EVERY_DESIGN_TRANSPORT(t *testing.T) {
	for _, tr := range designTransports() {
		t.Run(tr.name+": a deadline AFTER the write is unknown and never retried", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
			}))
			t.Cleanup(srv.Close)
			err, row := tr.call(t, srv.URL, 150*time.Millisecond)

			ce, ok := aiprov.AsCallError(err)
			require.True(t, ok, "%v", err)
			require.Equal(t, tr.billing, ce.Provider)
			require.True(t, ce.Engaged, "the provider has the whole request")

			v := classify(err)
			require.False(t, v.Retryable, "retrying a written request pays twice")
			require.Equal(t, entity.DesignAttemptUnknown, v.State)

			require.Equal(t, entity.AICallUnknown, row.Status)
			require.NotNil(t, row.End.Engaged)
			require.True(t, *row.End.Engaged)
			require.False(t, row.End.CostUSD.Valid, "unknown is NULL, never 0")
		})

		t.Run(tr.name+": a refused dial BEFORE the write is free and retried", func(t *testing.T) {
			dead := httptest.NewServer(http.NotFoundHandler())
			deadURL := dead.URL
			dead.Close()
			err, row := tr.call(t, deadURL, time.Second)

			ce, ok := aiprov.AsCallError(err)
			require.True(t, ok, "%v", err)
			require.False(t, ce.Engaged)
			v := classify(err)
			require.True(t, v.Retryable)
			require.Equal(t, entity.DesignAttemptFailed, v.State)
			require.Equal(t, CodeProviderUnavailable, v.Code)
			require.Equal(t, entity.AICallFree, row.Status)
		})

		for _, status := range []int{500, 502, 503, 504} {
			t.Run(fmt.Sprintf("%s: HTTP %d", tr.name, status), func(t *testing.T) {
				err, row := tr.call(t, statusStand(t, status), time.Second)
				ce, ok := aiprov.AsCallError(err)
				require.True(t, ok, "%v", err)
				require.Equal(t, status, ce.HTTPStatus)
				require.Equal(t, intp(status), row.End.HTTPStatus)
				v := classify(err)

				if tr.unconfirmed5xx && status != http.StatusServiceUnavailable {
					// fal's own truth (G-03 r3), Meshy's since B-13/A1: the gateway may have lost the
					// queue's answer after the enqueue. Engaged, final, and booked as possibly spent.
					require.True(t, ce.Engaged)
					require.False(t, v.Retryable)
					require.Equal(t, CodeSubmitUnconfirmed, v.Code)
					require.Equal(t, entity.DesignAttemptUnknown, v.State)
					require.Equal(t, entity.AICallUnknown, row.Status)
					return
				}
				require.False(t, ce.Engaged, "a refusal at the gate moved no money")
				require.True(t, v.Retryable, "and the same request may succeed later")
				require.Equal(t, entity.DesignAttemptFailed, v.State)
				require.Equal(t, entity.AICallFree, row.Status)
				require.True(t, row.End.CostUSD.Valid)
				require.True(t, row.End.CostUSD.Decimal.IsZero())
			})
		}

		for _, status := range []int{401, 402, 404, 429} {
			t.Run(fmt.Sprintf("%s: HTTP %d", tr.name, status), func(t *testing.T) {
				err, row := tr.call(t, statusStand(t, status), time.Second)
				ce, ok := aiprov.AsCallError(err)
				require.True(t, ok, "%v", err)
				require.False(t, ce.Engaged)
				require.Equal(t, status, ce.HTTPStatus)

				v := classify(err)
				require.Equal(t, tr.code[status], v.Code, "the code a person reads, as before B-14")
				require.Equal(t, status == http.StatusTooManyRequests, v.Retryable)
				require.Equal(t, entity.DesignAttemptFailed, v.State)

				require.Equal(t, entity.AICallFree, row.Status)
				require.Equal(t, intp(status), row.End.HTTPStatus)
				require.Equal(t, v.Code, row.End.ErrorCode)
			})
		}

		t.Run(tr.name+": a real 408 is engaged — unknown for the worker AND the ledger, never retried (B-13/A3)", func(t *testing.T) {
			err, row := tr.call(t, statusStand(t, http.StatusRequestTimeout), time.Second)
			ce, ok := aiprov.AsCallError(err)
			require.True(t, ok, "%v", err)
			require.True(t, ce.Engaged, "the server may have taken the whole body before it gave up")
			require.False(t, ce.Retryable)
			require.Equal(t, http.StatusRequestTimeout, ce.HTTPStatus)

			v := classify(err)
			require.Equal(t, tr.code[http.StatusRequestTimeout], v.Code, "the sentinel still names the code")
			require.False(t, v.Retryable, "a retry would buy a second answer beside a first nobody sees")
			require.Equal(t, entity.DesignAttemptUnknown, v.State)

			require.Equal(t, entity.AICallUnknown, row.Status, "a 408 proves nothing about the bill")
			require.NotNil(t, row.End.Engaged)
			require.True(t, *row.End.Engaged, "the transport says so now; the ledger no longer has to guess")
			require.False(t, row.End.CostUSD.Valid, "unknown is NULL, never 0")
			require.Equal(t, intp(http.StatusRequestTimeout), row.End.HTTPStatus)
		})
	}
}
