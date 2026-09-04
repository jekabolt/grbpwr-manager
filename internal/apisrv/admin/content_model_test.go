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

// TestUploadContentModelGoesToTheMediaShelfAsAGLB — несущая проверка ГЛАГОЛА бакета, а не типа.
//
// С круга 18 (D-29) тип не называется в хендлере вовсе: дверь зовёт bucket.UploadContentModel,
// который хранит model/gltf-binary по построению. МУТАЦИЯ, КОТОРУЮ ЛОВИТ ЭТА ПРОБА: хендлер
// написан копией соседней двери (UploadContentVector) и зовёт UploadContentNonRaster с литералом
// типа — тогда превью теряется по дороге в бакет (у того глагола нет второго аргумента), и модель
// снова встаёт на карточку битой плиткой. Строгий мок без ожидания на UploadContentNonRaster роняет
// такую копию по имени.
//
// Папка тоже утверждается: модель живёт на ТОЙ ЖЕ полке, что картинка и видео (медиа-хранилище), а
// не в библиотеке файлов — иначе GetMediaUsage её не увидит, и «где используется» соврёт.
func TestUploadContentModelGoesToTheMediaShelfAsAGLB(t *testing.T) {
	glb := []byte("glTF\x02\x00\x00\x00\x0c\x00\x00\x00")
	minted := &pb_common.MediaFull{Id: 77, ContentHash: "beef"}

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentModel(mock.Anything, glb, []byte(nil), "grbpwr-com", mock.Anything).
		Return(minted, nil)

	s := &Server{bucket: fs}
	resp, err := s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{Raw: glb})

	require.NoError(t, err)
	require.Same(t, minted, resp.GetMedia())
}

// TestUploadContentModelHandsThePreviewToTheBucketInTheSameCall — ОДИН ВЫЗОВ, ДВА ФАЙЛА (D-29).
//
// Владелец: «загружали glb и + миниатюру фото превью … и это все как один объект». Превью едет в
// ТОМ ЖЕ вызове бакета, что и модель, байт в байт: второй вызов означал бы полу-объект при обрыве
// между ними — ровно то, ради чего поле живёт на этом запросе, а не на своём глаголе.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНИТ: хендлер перестал передавать req.GetPreview() (в бакет уезжает nil);
// хендлер зовёт бакет дважды (второй вызов незаявлен — строгий мок роняет по имени).
func TestUploadContentModelHandsThePreviewToTheBucketInTheSameCall(t *testing.T) {
	glb := []byte("glTF\x02\x00\x00\x00\x0c\x00\x00\x00")
	preview := []byte("\x89PNG\r\n\x1a\n-not-really-decoded-here")
	minted := &pb_common.MediaFull{Id: 78, ContentHash: "cafe"}

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentModel(mock.Anything, glb, preview, "grbpwr-com", mock.Anything).
		Return(minted, nil).Once()

	s := &Server{bucket: fs}
	resp, err := s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{
		Raw: glb, Preview: preview,
	})

	require.NoError(t, err)
	require.Same(t, minted, resp.GetMedia())
}

// TestTheTwoNonRasterDoorsDoNotShareAVerb — ПАРА, А НЕ ДВА ОТДЕЛЬНЫХ УТВЕРЖДЕНИЯ.
//
// Смысл разделения на два глагола записан на самом контракте: «this door stores image/svg+xml and
// nothing else, so a client cannot talk its way into the GLB branch». До D-29 обе двери звали ОДИН
// глагол бакета с разными литералами типа, и проба сверяла литералы. Теперь у модели свой глагол
// бакета (UploadContentModel), у вектора — прежний с фиксированным «image/svg+xml»; общего
// параметра типа между ними больше нет, и разделение держится ИМЕНАМИ ВЫЗОВОВ. Поэтому обе двери
// едут по ОДНОМУ строгому моку, у которого заявлено ровно по одному вызову на дверь: вектор,
// уехавший в UploadContentModel, или модель, уехавшая в UploadContentNonRaster, падают на
// незаявленном вызове по имени — в обе стороны, как и раньше.
func TestTheTwoNonRasterDoorsDoNotShareAVerb(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, mock.Anything, "image/svg+xml", "grbpwr-com", mock.Anything).
		Return(&pb_common.MediaFull{Id: 1}, nil).Once()
	fs.EXPECT().UploadContentModel(mock.Anything, mock.Anything, mock.Anything, "grbpwr-com", mock.Anything).
		Return(&pb_common.MediaFull{Id: 2}, nil).Once()

	s := &Server{bucket: fs}
	_, err := s.UploadContentVector(context.Background(), &pb_admin.UploadContentVectorRequest{
		Raw: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
	})
	require.NoError(t, err)
	_, err = s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{
		Raw: []byte("glTF\x02\x00\x00\x00\x0c\x00\x00\x00"),
	})
	require.NoError(t, err)
}

// TestUploadContentModelRefusalIsTheClientsFault — отказ гейта формы (пустые байты, не-GLB,
// оборванный контейнер, файл за потолком 64 МиБ, И превью, которое не растр) обязан приехать
// клиенту InvalidArgument СО СЛОВАМИ проверяющего: это единственная подсказка, по которой человек
// чинит свой файл. Тот же разрез, что у UploadContentVector и UploadPattern.
func TestUploadContentModelRefusalIsTheClientsFault(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentModel(mock.Anything, mock.Anything, mock.Anything, "grbpwr-com", mock.Anything).
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
	fs.EXPECT().UploadContentModel(mock.Anything, mock.Anything, mock.Anything, "grbpwr-com", mock.Anything).
		Return(nil, errors.New("s3: connection reset"))

	s := &Server{bucket: fs}
	_, err := s.UploadContentModel(context.Background(), &pb_admin.UploadContentModelRequest{
		Raw: []byte("glTF\x02\x00\x00\x00\x0c\x00\x00\x00"),
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Internal, st.Code())
}
