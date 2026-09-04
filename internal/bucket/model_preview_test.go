package bucket

import (
	"bytes"
	"context"
	"image"
	"image/gif"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ D-29: МОДЕЛЬ И ЕЁ ПРЕВЬЮ — ОДНА СТРОКА MEDIA ═══════════════════════════════════════════════
//
// Владелец (круг 18): «когда загружаешь свою 3д модель надо что бы мы загружали glb и + миниатюру
// фото превью … и это все как один объект». Здесь доказывается форма строки и строгость проверки
// превью — через настоящий путь загрузки к подставному S3, как и у соседних проб nonraster_test.go.

// TestAModelWithAPreviewIsOneRowWithARealThumbnail — САМА ФОРМА: full_size — .glb (0×0, хеш
// модели), compressed и thumbnail — растровые варианты превью с настоящими размерами и blurhash.
// Три объекта, одна строка.
func TestAModelWithAPreviewIsOneRowWithARealThumbnail(t *testing.T) {
	ctx := context.Background()
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	raw := makeGLB(t)
	preview := makePNG(t, 64, 48)
	got, err := b.UploadContentModel(ctx, raw, preview, "design", "probe-mdlp")
	require.NoError(t, err)
	require.NotNil(t, row)

	// Модель — как и без превью: вербатим, свой тип, своё расширение, хеш объекта как он лежит.
	require.Equal(t, raw, store.storedUnder(t, "probe-mdlp-og"))
	require.Equal(t, "model/gltf-binary", store.storedTypeUnder(t, "probe-mdlp-og"))
	require.True(t, strings.HasSuffix(row.FullSizeMediaURL, ".glb"), row.FullSizeMediaURL)
	require.Zero(t, row.FullSizeWidth, "у модели нет пиксельных размеров")
	require.Zero(t, row.FullSizeHeight)
	require.Equal(t, sha256Hex(raw), row.ContentHash.String, "content_hash описывает МОДЕЛЬ")

	// Превью — два растровых варианта, как у обычной картинки: сжатый и миниатюра.
	require.Equal(t, 3, store.puts, "модель, сжатое превью, миниатюра — и ничего сверх")
	require.Equal(t, "image/webp", store.storedTypeUnder(t, "probe-mdlp-compressed"))
	require.Equal(t, "image/webp", store.storedTypeUnder(t, "probe-mdlp-thumb"))
	require.NotEqual(t, row.FullSizeMediaURL, row.ThumbnailMediaURL,
		"миниатюра больше не сам .glb — ровно тот битый кадр, который человек читал как «сломался сервер»")
	require.NotEqual(t, row.FullSizeMediaURL, row.CompressedMediaURL)
	require.Equal(t, 64, row.ThumbnailWidth)
	require.Equal(t, 48, row.ThumbnailHeight)
	require.Equal(t, 64, row.CompressedWidth)
	require.Equal(t, 48, row.CompressedHeight)
	require.True(t, row.BlurHash.Valid, "у превью есть растр — есть и blurhash")

	// И на проводе та же форма: клиент узнаёт модель по .glb в full_size и рисует thumbnail.
	require.True(t, strings.HasSuffix(got.GetMedia().GetFullSize().GetMediaUrl(), ".glb"))
	require.Equal(t, int32(64), got.GetMedia().GetThumbnail().GetWidth())
	require.Equal(t, sha256Hex(raw), got.GetContentHash())
}

// TestAModelWithoutAPreviewKeepsTheOldShape — старые модели живут дальше: пустое превью — прежняя
// дверь, все три слота указывают на один .glb, один объект в бакете.
func TestAModelWithoutAPreviewKeepsTheOldShape(t *testing.T) {
	ctx := context.Background()
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	raw := makeGLB(t)
	_, err := b.UploadContentModel(ctx, raw, nil, "design", "probe-mdl0")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, 1, store.puts)
	require.Equal(t, row.FullSizeMediaURL, row.ThumbnailMediaURL)
	require.Equal(t, row.FullSizeMediaURL, row.CompressedMediaURL)
	require.False(t, row.BlurHash.Valid)
}

// TestABadPreviewStoresNothingNotEvenTheModel — превью проверяется ПО БАЙТАМ так же строго, как
// GLB, и ДО того, как хоть один байт уехал в бакет: отказ по превью не оставляет полу-объекта
// (модели без превью, которое с ней прислали). Строгий мок медиа-стора без ожидания на AddMedia
// роняет пробу, если строка всё же пишется.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНИТ: проверять превью после загрузки модели (puts станет 1); принимать
// превью по расширению/метке вместо magic (GLB-байты пройдут); пустить GIF (кейс «animated gif»).
func TestABadPreviewStoresNothingNotEvenTheModel(t *testing.T) {
	var animated bytes.Buffer
	require.NoError(t, gif.Encode(&animated, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil))

	cases := map[string][]byte{
		"a model as the preview":    makeGLB(t),
		"a vector as the preview":   []byte(cleanSVG),
		"a gif as the preview":      animated.Bytes(),
		"garbage":                   []byte("this is not a picture"),
		"a truncated png":           makePNG(t, 8, 8)[:12],
		"a jpeg header and no more": {0xFF, 0xD8, 0xFF, 0xE0},
	}
	for name, preview := range cases {
		t.Run(name, func(t *testing.T) {
			b, store, _ := newProbeBucket(t)
			_, err := b.UploadContentModel(context.Background(), makeGLB(t), preview, "design", "probe-bad")
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidNonRaster, "вина клиента, а не хранилища: хендлер отдаст InvalidArgument")
			require.Zero(t, store.puts, "ни модель, ни превью не должны были уехать в бакет")
		})
	}
}

// TestABrokenModelWithAGoodPreviewStoresNothingEither — та же дверь с другой стороны: исправное
// превью не протаскивает битую модель.
func TestABrokenModelWithAGoodPreviewStoresNothingEither(t *testing.T) {
	b, store, _ := newProbeBucket(t)
	_, err := b.UploadContentModel(context.Background(), []byte("glTF but not really"), makePNG(t, 8, 8), "design", "probe-bad2")
	require.ErrorIs(t, err, ErrInvalidNonRaster)
	require.Zero(t, store.puts)
}
