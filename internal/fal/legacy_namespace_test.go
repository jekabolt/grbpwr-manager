package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyQueue knows ONE request id under ONE namespace and answers 404 elsewhere, or 503 for the
// namespaces listed in busy (a candidate that cannot be asked).
func legacyQueue(t *testing.T, knownNS, id string, busy ...string) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		for _, b := range busy {
			if strings.HasPrefix(r.URL.Path, "/"+b+"/") {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		switch {
		case r.URL.Path == "/cut.png":
			_, _ = w.Write([]byte("PNG"))
		case !strings.HasPrefix(r.URL.Path, "/"+knownNS+"/requests/"+id):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
		default:
			w.Header().Set(billableUnitsHeader, "1")
			_ = json.NewEncoder(w).Encode(map[string]any{"image": map[string]any{
				"url": "http://" + r.Host + "/cut.png", "content_type": "image/png"}})
		}
	}))
	return srv, &paths
}

// TestABareCutoutIdIsFOUND_UNDER_THE_DEFAULT — G-03 r2, Codex 4 (b). A cut-out row written before the
// locator holds a bare id bought under the default slug; FAL_MODEL_CUTOUT has since moved. Today's
// namespace 404s past the grace, the default's is asked, the paid picture is collected. An id no
// namespace knows keeps the terminal 404; a candidate that could not be asked is NOT a denial.
// MUTATION (measured red): CollectCutoutAt with an empty model back to `return c.CollectCutout(...)`
// → ErrRequestNotFound on the first case.
func TestABareCutoutIdIsFOUND_UNDER_THE_DEFAULT(t *testing.T) {
	srv, _ := legacyQueue(t, queuePath(DefaultModelCutout), "old-1")
	defer srv.Close()
	c := newGenericClient(srv.URL, Config{ModelCutout: "acme/matte/v2"})
	require.Equal(t, []string{"acme/matte/v2", DefaultModelCutout}, c.CutoutLegacyModels())

	var buf bytes.Buffer
	res, err := c.CollectCutoutAt(context.Background(), "", "old-1", &buf)
	require.NoError(t, err)
	require.Equal(t, "PNG", buf.String())
	require.Equal(t, DefaultModelCutout, res.Model, "collected where it was bought")

	_, err = c.CollectCutoutAt(context.Background(), "", "nowhere", &bytes.Buffer{})
	require.ErrorIs(t, err, ErrRequestNotFound, "every namespace denied it: the id really buys nothing")

	busy, _ := legacyQueue(t, "nobody/knows", "old-1", queuePath(DefaultModelCutout))
	defer busy.Close()
	c2 := newGenericClient(busy.URL, Config{ModelCutout: "acme/matte/v2"})
	_, err = c2.CollectCutoutAt(context.Background(), "", "old-1", &bytes.Buffer{})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRequestNotFound, "an unfinished search is weather, not a write-off")
}

// TestALocatedCutoutIsPOLLED_AT_ITS_SLUG_ONLY — the positive control for the new rows: a locator's slug
// is polled directly; the legacy search is not needed and today's slug is never asked.
func TestALocatedCutoutIsPOLLED_AT_ITS_SLUG_ONLY(t *testing.T) {
	srv, paths := legacyQueue(t, queuePath(DefaultModelCutout), "new-1")
	defer srv.Close()
	c := newGenericClient(srv.URL, Config{ModelCutout: "acme/matte/v2"})
	_, err := c.CollectCutoutAt(context.Background(), DefaultModelCutout, "new-1", &bytes.Buffer{})
	require.NoError(t, err)
	for _, p := range *paths {
		require.NotContains(t, p, "acme/matte")
	}
}
