package designgen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/stretchr/testify/require"
)

// TestTheOutgoingRequestNeverCarriesTransparency guards the WIRE, not the helper.
//
// WHY THIS FILE EXISTS. There is already a test asserting that backgroundFor() does not return
// "transparent". An adversarial review then hardcoded Background: "transparent" at the CALL SITE
// (images.go, where the orimages.Request is built) — reproducing the production defect exactly —
// and every one of the package's tests stayed green. The guard sat one door away from the thing it
// was supposed to guard: it pinned what a helper returns, and nothing pinned what is actually sent.
//
// The default image model lists background as auto|opaque. A request carrying "transparent" is a
// 400 on every flat run, and it would be invisible here because the package's other tests use a
// stub that never inspects the request.
//
// So this test reads the JSON that leaves the process.
func TestTheOutgoingRequestNeverCarriesTransparency(t *testing.T) {
	for _, kind := range []string{entity.DesignRunKindFlat, entity.DesignRunKindRender} {
		t.Run(kind, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"data":[{"b64_json":"aGk=","media_type":"image/png"}],"usage":{"cost":0.01}}`)
			}))
			defer srv.Close()

			p := NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: srv.URL}))
			_, _ = p.Execute(context.Background(), Job{
				RunID: 1, TechCardID: 1, Kind: kind,
				Prompt: "a flat", Views: []string{entity.DesignViewFront}, Layout: "one",
			})

			require.NotNil(t, body, "the provider never sent a request — the seam this test guards was not exercised")
			got, _ := body["background"].(string)
			require.NotEqual(t, "transparent", got,
				"the model's catalogue lists background as auto|opaque; sending transparency is a 400 on every run of this kind")
		})
	}
}

// TestTheOutgoingRequestStatesTheBackgroundForAFlat is the other half. Asserting only «not
// transparent» is satisfied by dropping the parameter entirely, which leaves the choice to the
// provider's own default — the same silence that hid the original bug. A flat states its ground.
func TestTheOutgoingRequestStatesTheBackgroundForAFlat(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"b64_json":"aGk=","media_type":"image/png"}],"usage":{"cost":0.01}}`)
	}))
	defer srv.Close()

	p := NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: srv.URL}))
	_, _ = p.Execute(context.Background(), Job{
		RunID: 1, TechCardID: 1, Kind: entity.DesignRunKindFlat,
		Prompt: "a flat", Views: []string{entity.DesignViewFront}, Layout: "one",
	})

	require.NotNil(t, body, "the provider never sent a request")
	require.Contains(t, []string{"auto", "opaque"}, body["background"],
		"a flat must name a background the model knows, and name it rather than leave it to the default")
}
