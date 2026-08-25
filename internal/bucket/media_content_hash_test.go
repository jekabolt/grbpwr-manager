package bucket

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ЧТО ЗДЕСЬ ДОКАЗЫВАЕТСЯ И ПОЧЕМУ ИМЕННО ТАК.
//
// media.content_hash существует ради одного вопроса импорта: «этот файл у нас уже есть?».
// Ответ верен ТОЛЬКО если хеш взят с тех же байтов, что скачает экспорт, — то есть с
// ОБЪЕКТА В БАКЕТЕ. Путь картинок перекодирует присланное в WebP, поэтому «посчитать sha
// от того, что прислал клиент» — правдоподобная, тихая и полностью ломающая дедупликацию
// ошибка: колонка заполнена, всё зелено, совпадений не будет никогда.
//
// Поэтому проба не спрашивает «хеш непустой». Она поднимает подставное хранилище, ЗАПОМИНАЕТ
// БАЙТЫ, КОТОРЫЕ ДО НЕГО ДОЕХАЛИ, и сравнивает content_hash из AddMedia с sha256 ровно этих
// байтов под full-size ключом. Отдельным утверждением по пути картинок хеш сверяется на
// НЕРАВЕНСТВО sha от присланного PNG — это положительный контроль: если однажды кто-то
// «упростит» код до хеша входа, тест обязан покраснеть, а не согласиться.

// fakeS3 — минимальное хранилище: принимает PUT и запоминает тело. Ключ здесь = путь без
// имени бакета (minio ходит path-style, потому что хост — IP).
type fakeS3 struct {
	bucket  string
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	}
	w.WriteHeader(http.StatusOK)
}

// storedUnder returns the single object whose key ends with the given suffix. Failing when
// the count is not exactly one is the point: an assertion against "the full-size object"
// must not quietly pick one of several when the key scheme changes.
func (f *fakeS3) storedUnder(t *testing.T, suffix string) []byte {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var hits []string
	for k := range f.objects {
		if strings.Contains(k, suffix) {
			hits = append(hits, k)
		}
	}
	sort.Strings(hits)
	require.Lenf(t, hits, 1, "expected exactly one stored object matching %q, got %v", suffix, hits)
	return f.objects[hits[0]]
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newProbeBucket wires a real *Bucket at a fake S3. TLS is used deliberately: over a plain
// connection minio-go signs the body with the streaming (aws-chunked) scheme, and the server
// would then record framing bytes rather than the object — the probe would compare the hash
// against something that is not the object and fail for a reason that has nothing to do with
// the code under test.
func newProbeBucket(t *testing.T) (*Bucket, *fakeS3, *mocks.MockMedia) {
	t.Helper()
	const bucketName = "probe-bucket"

	store := &fakeS3{bucket: bucketName, objects: map[string][]byte{}}
	srv := httptest.NewTLSServer(store)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	cli, err := minio.New(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4("probe", "probe12345", ""),
		Secure:    true,
		Transport: srv.Client().Transport,
		// Pinning the region keeps minio-go from issuing a GetBucketLocation round-trip
		// the fake would have to answer with XML.
		Region: "us-east-1",
	})
	require.NoError(t, err)

	ms := mocks.NewMockMedia(t)
	b := &Bucket{
		Client: cli,
		Config: &Config{
			S3BucketName:      bucketName,
			S3Endpoint:        u.Host,
			BaseFolder:        "probe",
			SubdomainEndpoint: "cdn.probe.test",
		},
		ms: ms,
	}
	return b, store, ms
}

// captureAddMedia makes the media store record the row instead of writing it.
func captureAddMedia(ms *mocks.MockMedia, out **entity.MediaItem) {
	ms.EXPECT().AddMedia(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, m *entity.MediaItem) (int, error) {
			*out = m
			return 7, nil
		}).Once()
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 11), B: 0x40, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// TestContentHashIsSHAOfStoredFullSizeImage covers the WebP path — the one where the stored
// bytes are NOT the posted bytes.
func TestContentHashIsSHAOfStoredFullSizeImage(t *testing.T) {
	ctx := context.Background()
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	posted := makePNG(t, 24, 18)
	_, err := b.UploadContentImage(ctx, dataURL("image/png", posted), "content", "probe-img")
	require.NoError(t, err)
	require.NotNil(t, row)

	stored := store.storedUnder(t, "probe-img-og")
	require.True(t, row.ContentHash.Valid, "content_hash must be written on the image path")
	require.Equal(t, sha256Hex(stored), row.ContentHash.String,
		"content_hash must be the sha of the full-size object as stored")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: присланные байты и сохранённые — разные, поэтому утверждение
	// выше действительно различает «хеш объекта» и «хеш входа». Без этой строки тест был бы
	// зелёным и в мире, где перекодирования нет вовсе.
	require.NotEqual(t, sha256Hex(posted), row.ContentHash.String,
		"the probe must be able to tell a hash of the stored object from a hash of the payload")
}

// TestContentHashOnRawGIFPath covers the pass-through path: full-size and compressed are the
// same object, so one hash describes both.
func TestContentHashOnRawGIFPath(t *testing.T) {
	ctx := context.Background()
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	raw := makeAnimatedGIF(t, 12, 9)
	_, err := b.UploadContentImage(ctx, dataURL("image/gif", raw), "content", "probe-gif")
	require.NoError(t, err)
	require.NotNil(t, row)

	stored := store.storedUnder(t, "probe-gif-og")
	require.Equal(t, raw, stored, "the GIF path must store the payload verbatim")
	require.True(t, row.ContentHash.Valid, "content_hash must be written on the raw GIF path")
	require.Equal(t, sha256Hex(stored), row.ContentHash.String)
	require.Equal(t, row.FullSizeMediaURL, row.CompressedMediaURL,
		"one object serves both urls here, so one hash is the whole answer")
}

// TestContentHashOnVideoPath covers the third and last writer of a media row.
func TestContentHashOnVideoPath(t *testing.T) {
	ctx := context.Background()
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	// Minimal ISO-BMFF header: the video path never decodes, it only sniffs bytes 4:8.
	raw := append([]byte{0, 0, 0, 0x18}, []byte("ftypisom")...)
	raw = append(raw, []byte("probe payload bytes")...)

	_, err := b.UploadContentVideo(ctx, raw, "content", "probe-vid", string(contentTypeMP4))
	require.NoError(t, err)
	require.NotNil(t, row)

	stored := store.storedUnder(t, "probe-vid")
	require.Equal(t, raw, stored, "video is stored verbatim")
	require.True(t, row.ContentHash.Valid, "content_hash must be written on the video path")
	require.Equal(t, sha256Hex(stored), row.ContentHash.String)
}
