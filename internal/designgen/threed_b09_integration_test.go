package designgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ═══ B-09 × B-core: ТО ЖЕ, ЧЕРЕЗ НАСТОЯЩИЙ buildJob (живёт вместе с патчем интеграции) ══════════

// TestAReferenceRunBuildsFromItsNamedPictures — buildJob отдаёт маршруту названные картинки в порядке
// человека с видами по позиции, а опции — полями задания. МУТАЦИЯ: snapshot.go зовёт
// threedPictures вместо threedPicturesOf — уезжают плиты 1/2.
func TestAReferenceRunBuildsFromItsNamedPictures(t *testing.T) {
	job, err := buildJob(context.Background(), media(1, 2, 55, 56, 57, 77, 90), nil, referenceRun(5), "medium")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://cdn.example/m/55.png", "https://cdn.example/m/56.png", "https://cdn.example/m/57.png",
	}, job.References)
	require.Equal(t, []string{"front", "back", "side_l"}, job.ReferenceViews)
	require.Equal(t, "off", job.ThreedTexture)
	require.Equal(t, "detailed", job.ThreedQuality)
	require.Empty(t, job.ThreedPBR)
}

// TestAnUntexturedReferenceRunSendsAndRecordsNoWords — через воркера: тело без texture_prompt,
// история — пустая строка, то есть ровно отправленное.
func TestAnUntexturedReferenceRunSendsAndRecordsNoWords(t *testing.T) {
	stand := newFalSubmitStand(t)
	st := &fakeStore{}
	w := testWorker(st, media(55, 56, 57), newFakeSink(ContentTypeGLB, ContentTypePNG),
		Providers{Threed: falRoute(t, stand.srv.URL, "meshy/v7/multi-image-to-3d")})
	_ = w.execute(context.Background(), referenceRun(6), "tok")
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(<-stand.body), &body))
	require.Equal(t, false, body["should_texture"])
	require.Equal(t, "2k", body["geometry_resolution"])
	require.NotContains(t, body, "texture_prompt")
	require.Len(t, st.recordedPrompts, 1)
	require.Equal(t, "", st.recordedPrompts[0])
}

// TestADetailedBuildIsBookedAtItsTier — сбор fal без тарифа пишет $1.40 за detailed. МУТАЦИЯ:
// Collect, зовущий CostUSDFor без тира — $1.2, красно.
func TestADetailedBuildIsBookedAtItsTier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/model.glb"):
			_, _ = w.Write([]byte("glTF-bytes"))
			return
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
			return
		}
		w.Header().Set("x-fal-billable-units", "1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_glb": map[string]any{"url": "http://" + r.Host + "/model.glb"},
		})
	}))
	t.Cleanup(srv.Close)
	coll := falRoute(t, srv.URL, "meshy/v7/multi-image-to-3d").(Collector)

	out, err := coll.Collect(context.Background(), Job{RunID: 7, ThreedQuality: "detailed"}, "req-d")
	require.NoError(t, err)
	require.Equal(t, "1.4", out.Price.Decimal.String())
	out, err = coll.Collect(context.Background(), Job{RunID: 8}, "req-s")
	require.NoError(t, err)
	require.Equal(t, "1.2", out.Price.Decimal.String())
}
