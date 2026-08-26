package bucket

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	// puts counts every object that ever ARRIVED, so an assertion on an empty store can tell
	// "it was taken back" from "it was never sent" — the two look identical in `objects`.
	puts int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.objects[key] = body
		f.puts++
		f.mu.Unlock()
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	case http.MethodDelete:
		// DeleteObjects is what compensation runs on, so the store has to actually forget.
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
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

// ─────────────────────────────────────────────────────────────────────────────
// ВЕРБАТИМНЫЙ ПУТЬ — ЧТО ИМЕННО ОН ОБЯЗАН ДОКАЗАТЬ.
//
// Дедуп импорта тех-карты сравнивает sha из архива с media.content_hash. Архив несёт sha
// ОБЪЕКТА, который экспорт скачал из бакета; UploadContentImage кладёт в бакет ПЕРЕКОДИРОВАННЫЙ
// WebP и снимает хеш с него. Значит для растра эти два числа не совпадут никогда — колонка
// заполнена, всё зелено, совпадений нет. UploadContentImageVerbatim закрывает ровно этот разрыв,
// поэтому проба ниже гоняет НАСТОЯЩИЕ байты картинки и требует РАВЕНСТВА sha входа и sha того,
// что доехало до PutObject под full-size ключом.
//
// Каждое такое утверждение идёт в паре с положительным контролем: те же байты через
// UploadContentImage дают ДРУГОЙ хеш. Без этой второй половины тест был бы зелёным и в мире, где
// перекодирования нет вовсе, — то есть не различал бы почин и его отсутствие.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("files", name))
	require.NoError(t, err)
	return b
}

// TestVerbatimUploadRoundTripsTheContentHash is the round-trip proof: sha(bytes handed in) ==
// sha(bytes stored as full-size) == media.content_hash. Run over a real JPEG, a real PNG and a
// WebP — the last one is the case the archive actually exercises, because a picture uploaded the
// ordinary way lies in the bucket as WebP and travels into the archive as such.
func TestVerbatimUploadRoundTripsTheContentHash(t *testing.T) {
	webpBytes := func(t *testing.T) []byte {
		t.Helper()
		var buf bytes.Buffer
		img, err := png.Decode(bytes.NewReader(makePNG(t, 40, 30)))
		require.NoError(t, err)
		require.NoError(t, encodeWEBP(&buf, img, 100))
		return buf.Bytes()
	}

	for _, tc := range []struct {
		name string
		raw  func(*testing.T) []byte
		ext  string
	}{
		{"jpeg", func(t *testing.T) []byte { return readFixture(t, "test.jpg") }, ".jpg"},
		{"png", func(t *testing.T) []byte { return readFixture(t, "test.png") }, ".png"},
		{"webp", webpBytes, ".webp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			b, store, ms := newProbeBucket(t)

			var row *entity.MediaItem
			captureAddMedia(ms, &row)

			raw := tc.raw(t)
			full, err := b.UploadContentImageVerbatim(ctx, raw, "content", "verbatim")
			require.NoError(t, err)
			require.NotNil(t, row)

			stored := store.storedUnder(t, "verbatim-og")
			require.Equal(t, raw, stored,
				"the full-size object must be the payload itself — anything else and the hash below is a hash of bytes the archive has never seen")
			require.True(t, row.ContentHash.Valid)
			require.Equal(t, sha256Hex(raw), row.ContentHash.String,
				"content_hash must equal the sha of the INPUT, which is what an archive carries")
			require.Equal(t, sha256Hex(stored), row.ContentHash.String,
				"and it must still equal the sha of the stored object — the invariant is unchanged, only which bytes lie there")

			// The object keeps its own extension and content type: a verbatim JPEG served as
			// .webp would be a lie to every cache in front of it.
			require.True(t, strings.HasSuffix(full.GetMedia().GetFullSize().GetMediaUrl(), tc.ext),
				"full-size url = %q, want the payload's own %q", full.GetMedia().GetFullSize().GetMediaUrl(), tc.ext)

			// The smaller variants are still re-encoded WebP, and they are separate objects —
			// verbatim is a statement about the full-size object only.
			require.True(t, strings.HasSuffix(row.CompressedMediaURL, ".webp"))
			require.True(t, strings.HasSuffix(row.ThumbnailMediaURL, ".webp"))
			require.NotEqual(t, row.FullSizeMediaURL, row.CompressedMediaURL)

			// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ. The same bytes down the ordinary path produce a DIFFERENT
			// hash — that difference IS the defect this method exists to remove, and this
			// assertion is what stops the test from passing in a world where the two paths are
			// secretly the same.
			b2, _, ms2 := newProbeBucket(t)
			var reencoded *entity.MediaItem
			captureAddMedia(ms2, &reencoded)
			_, err = b2.UploadContentImage(ctx, dataURL("image/"+strings.TrimPrefix(tc.ext, "."), raw), "content", "reencoded")
			require.NoError(t, err)
			require.NotEqual(t, sha256Hex(raw), reencoded.ContentHash.String,
				"the re-encoding path must NOT match the payload's sha; if it did, this whole method would be pointless and the probe would be proving nothing")
		})
	}
}

// craftPNGHeader builds a PNG whose IHDR DECLARES w×h while the file itself stays a few dozen
// bytes — a decompression bomb in miniature. png.DecodeConfig verifies the chunk CRC, so it is
// recomputed; nothing here ever allocates a raster.
func craftPNGHeader(t *testing.T, w, h int) []byte {
	t.Helper()
	raw := makePNG(t, 1, 1)
	out := append([]byte(nil), raw...)
	// 8 signature + 4 length + 4 "IHDR" = 16: width at 16, height at 20, chunk CRC over [12:29].
	binary.BigEndian.PutUint32(out[16:20], uint32(w))
	binary.BigEndian.PutUint32(out[20:24], uint32(h))
	binary.BigEndian.PutUint32(out[29:33], crc32.ChecksumIEEE(out[12:29]))

	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err, "the crafted header must be readable, otherwise the test below refuses it for the wrong reason")
	require.Equal(t, w, cfg.Width)
	require.Equal(t, h, cfg.Height)
	return out
}

// TestVerbatimUploadEnforcesTheHeaderBudgets proves verbatimness did not buy a way past the
// decompression-bomb guards. Both ceilings are refused from the HEADER: nothing is decoded, and —
// the assertion that matters — nothing reaches the bucket and no media row is minted.
func TestVerbatimUploadEnforcesTheHeaderBudgets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		w, h          int
		wantErrSubstr string
	}{
		// 20000 px wide but only 20 MP: over the dimension ceiling, UNDER the pixel one, so it
		// can only be refused by the dimension check.
		{"dimension", 20000, 1000, "exceed maximum allowed"},
		// 64 MP with both sides under 12000: the mirror case, refusable only by the pixel budget.
		{"pixels", 8000, 8000, "pixel limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, store, _ := newProbeBucket(t)

			_, err := b.UploadContentImageVerbatim(context.Background(), craftPNGHeader(t, tc.w, tc.h), "content", "bomb")
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErrSubstr)

			store.mu.Lock()
			defer store.mu.Unlock()
			require.Empty(t, store.objects,
				"a refused payload must not leave anything in the bucket — the budget is checked before the upload, not after")
			// The strict MockMedia was given no AddMedia expectation, so a row minted here would
			// fail the test on its own.
		})
	}
}

// TestVerbatimUploadRefusesWhatItCannotServe guards the format gate: HEIC decodes but no browser
// serves it, and an unrecognised payload names no container at all. Both are refusals, not
// silent re-encodes — a caller that wants a re-encode has UploadContentImage.
func TestVerbatimUploadRefusesWhatItCannotServe(t *testing.T) {
	b, store, _ := newProbeBucket(t)

	_, err := b.UploadContentImageVerbatim(context.Background(), readFixture(t, "test.heic"), "content", "heic")
	require.Error(t, err)
	require.Contains(t, err.Error(), "verbatim upload")

	_, err = b.UploadContentImageVerbatim(context.Background(), []byte("not a picture at all"), "content", "junk")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unrecognized image format")

	_, err = b.UploadContentImageVerbatim(context.Background(), nil, "content", "empty")
	require.Error(t, err)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Empty(t, store.objects)
}

// TestVerbatimUploadKeepsTheAnimatedGIFPath proves the generalisation did not cost the GIF its
// property: the payload still lands untouched AND still backs both the full-size and the
// compressed url, which is what keeps campaign emails animated.
func TestVerbatimUploadKeepsTheAnimatedGIFPath(t *testing.T) {
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	raw := makeAnimatedGIF(t, 12, 9)
	_, err := b.UploadContentImageVerbatim(context.Background(), raw, "content", "anim")
	require.NoError(t, err)

	require.Equal(t, raw, store.storedUnder(t, "anim-og"))
	require.Equal(t, sha256Hex(raw), row.ContentHash.String)
	require.Equal(t, row.FullSizeMediaURL, row.CompressedMediaURL,
		"one object under both urls: a WebP compressed variant here would be a still picture")
}

// TestVideoUploadRemovesItsObjectWhenTheRowCannotBeWritten covers the video path's half of the
// same bargain the image path already keeps: PutObject succeeded, AddMedia did not, and the caller
// is handed nil. Nil carries no urls, so the caller cannot put the object into a compensation plan
// and NOBODY would ever be able to delete it — the object would sit in the bucket for good. The
// upload therefore takes it back itself.
//
// `puts` is the half that makes the assertion mean something: an empty store proves a removal only
// if something was stored in the first place.
func TestVideoUploadRemovesItsObjectWhenTheRowCannotBeWritten(t *testing.T) {
	b, store, ms := newProbeBucket(t)
	ms.EXPECT().AddMedia(mock.Anything, mock.Anything).Return(0, errors.New("the media table is unavailable")).Once()

	raw := append([]byte{0, 0, 0, 0x18}, []byte("ftypisom")...)
	raw = append(raw, []byte("probe payload bytes")...)

	_, err := b.UploadContentVideo(context.Background(), raw, "content", "orphan-vid", string(contentTypeMP4))
	require.Error(t, err)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, 1, store.puts, "the object must have reached the bucket, otherwise the check below proves nothing")
	require.Empty(t, store.objects,
		"an object no media row references and no caller can name is an orphan forever; the upload has to take it back")
}
