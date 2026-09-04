package admin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ═══ E-13: СВОЯ 3D-МОДЕЛЬ НА КАРТОЧКЕ — ОДНА НЕДОСТАЮЩАЯ ДВЕРЬ ══════════════════════════════════
//
// Владелец (круг 16): «в 3D в 3D MODELS OF THIS CARD добавь возможность загрузить свою 3d модель».
// Всё остальное уже стояло: RegisterDesignUpload принимает `kind = threed` (и это отдельно
// проверено в design_band_kind_test.go), design_picture.run_id обнуляемая, а запрос выходов полосы
// уже пускает кадр без прогона («p.run_id IS NULL AND p.kind IN ('render','threed','pattern')») и
// возвращает его со штампом run_kind = "" / run_rrev = 0. Бакет умеет model/gltf-binary с
// векторной волны. Не было ровно ДВЕРИ, через которую байты модели попадают в медиа.

// TestUploadContentModelGoesToTheMediaShelfAsAGLB — несущая проверка ТИПА, зафиксированного
// ГЛАГОЛОМ.
//
// МУТАЦИЯ, КОТОРУЮ ОНА ЛОВИТ, И ЭТО САМАЯ ВЕРОЯТНАЯ ЗДЕСЬ: дверь написана копией соседней
// (UploadContentVector) и литерал типа остался «image/svg+xml». Тогда .glb уезжает в SVG-ветку
// bucket.UploadContentNonRaster, там его встречает recraft.InspectSVG, и человек получает отказ
// «не XML» на совершенно исправный файл. Ожидание мока стоит ровно на "model/gltf-binary", поэтому
// и копия литерала, и «хендлер стал читать тип из запроса» валят тест.
//
// Папка тоже утверждается: модель живёт на ТОЙ ЖЕ полке, что картинка и видео (медиа-хранилище), а
// не в библиотеке файлов — иначе GetMediaUsage её не увидит, и «где используется» соврёт.
func TestUploadContentModelGoesToTheMediaShelfAsAGLB(t *testing.T) {
	glb := []byte("glTF\x02\x00\x00\x00\x0c\x00\x00\x00")
	minted := &pb_common.MediaFull{Id: 77, ContentHash: "beef"}

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, glb, "model/gltf-binary", "grbpwr-com", mock.Anything).
		Return(minted, nil)

	s := &Server{bucket: fs}
	resp, err := s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{Raw: glb})

	require.NoError(t, err)
	require.Same(t, minted, resp.GetMedia())
}

// TestTheTwoNonRasterDoorsDoNotShareAContentType — ПАРА, А НЕ ДВА ОТДЕЛЬНЫХ УТВЕРЖДЕНИЯ.
//
// Смысл разделения на два глагола записан на самом контракте: «this door stores image/svg+xml and
// nothing else, so a client cannot talk its way into the GLB branch». Проверка каждой двери по
// отдельности проходит и в том мире, где обе двери зовут ОДИН тип, — а это ровно та поломка,
// которой разделение и посвящено, и стоит она в обе стороны: SVG, уехавший GLB-веткой, минует
// recraft.InspectSVG, то есть границу безопасности, а не формальность.
//
// Поэтому обе двери едут по ОДНОМУ моку, и он записывает, какой тип назвала каждая.
func TestTheTwoNonRasterDoorsDoNotShareAContentType(t *testing.T) {
	var seen []string

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ []byte, contentType, _, _ string) {
			seen = append(seen, contentType)
		}).
		Return(&pb_common.MediaFull{Id: 1}, nil)

	s := &Server{bucket: fs}
	_, err := s.UploadContentVector(context.Background(), &pb_admin.UploadContentVectorRequest{
		Raw: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
	})
	require.NoError(t, err)
	_, err = s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{
		Raw: []byte("glTF\x02\x00\x00\x00\x0c\x00\x00\x00"),
	})
	require.NoError(t, err)

	require.Len(t, seen, 2)
	require.NotEqual(t, seen[0], seen[1],
		"один тип на две двери означает, что одна из них разбирает чужие байты: SVG, уехавший "+
			"GLB-веткой, минует recraft.InspectSVG — границу безопасности, а не формальность")
	require.Equal(t, []string{"image/svg+xml", "model/gltf-binary"}, seen,
		"каждый глагол называет СВОЙ тип, и тип не приезжает с провода")
}

// TestUploadContentModelRefusalIsTheClientsFault — отказ гейта формы (пустые байты, не-GLB,
// оборванный контейнер, файл за потолком 64 МиБ) обязан приехать клиенту InvalidArgument СО
// СЛОВАМИ проверяющего: это единственная подсказка, по которой человек чинит свой файл. Тот же
// разрез, что у UploadContentVector и UploadPattern.
func TestUploadContentModelRefusalIsTheClientsFault(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, mock.Anything, "model/gltf-binary", "grbpwr-com", mock.Anything).
		Return(nil, fmt.Errorf("%w: not a glTF binary: bad magic", bucket.ErrInvalidNonRaster))

	s := &Server{bucket: fs}
	_, err := s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{
		Raw: []byte("not a model at all"),
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "glTF")
}

// TestUploadContentModelStorageFailureIsInternal — обратная сторона того же разреза: сломанный S3
// не вина клиента, и назвать его InvalidArgument значило бы отправить человека чинить исправный
// файл.
func TestUploadContentModelStorageFailureIsInternal(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, mock.Anything, "model/gltf-binary", "grbpwr-com", mock.Anything).
		Return(nil, errors.New("s3: connection reset"))

	s := &Server{bucket: fs}
	_, err := s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{
		Raw: []byte("glTF\x02\x00\x00\x00\x0c\x00\x00\x00"),
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Internal, st.Code())
}
