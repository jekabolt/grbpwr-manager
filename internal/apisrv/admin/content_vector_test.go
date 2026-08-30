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

// TestUploadContentVectorGoesToTheMediaShelf держит сам поворот владельца: вектор уезжает В МЕДИА
// (той же дорогой, что видео, — bucket.UploadContentNonRaster), а не в библиотеку файлов. Несущая
// проверка — ТИП, зафиксированный глаголом: ожидание мока стоит ровно на "image/svg+xml" и на
// базовой папке медиа, поэтому и «хендлер стал читать тип из запроса», и «дверь заговорила по
// GLB-ветке» валят тест, а mockery в строгом режиме валит его и на любом другом методе бакета —
// уехать в UploadLibraryObject (старый адрес) отсюда недостижимо.
func TestUploadContentVectorGoesToTheMediaShelf(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`)
	minted := &pb_common.MediaFull{Id: 42, ContentHash: "abcd"}

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, svg, "image/svg+xml", "grbpwr-com", mock.Anything).
		Return(minted, nil)

	s := &Server{bucket: fs}
	resp, err := s.UploadContentVector(context.Background(), &pb_admin.UploadContentVectorRequest{Raw: svg})

	require.NoError(t, err)
	require.Same(t, minted, resp.GetMedia())
}

// TestUploadContentVectorRefusalIsTheClientsFault — отказ гейта (пустые байты, не-SVG, активное
// содержимое) обязан приехать клиенту InvalidArgument СО СЛОВАМИ инспектора, а не безликим
// Internal: это единственная подсказка, по которой человек чинит свой файл. Тот же разрез, что у
// UploadPattern с ErrInvalidPattern.
func TestUploadContentVectorRefusalIsTheClientsFault(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, mock.Anything, "image/svg+xml", "grbpwr-com", mock.Anything).
		Return(nil, fmt.Errorf("%w: svg contains a <script> element", bucket.ErrInvalidNonRaster))

	s := &Server{bucket: fs}
	_, err := s.UploadContentVector(context.Background(), &pb_admin.UploadContentVectorRequest{
		Raw: []byte("<svg><script>alert(1)</script></svg>"),
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "script")
}

// TestUploadContentVectorStorageFailureIsInternal — обратная сторона того же разреза: сломанный
// S3 не вина клиента, и назвать его InvalidArgument значило бы отправить человека чинить
// исправный файл.
func TestUploadContentVectorStorageFailureIsInternal(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	fs.EXPECT().UploadContentNonRaster(mock.Anything, mock.Anything, "image/svg+xml", "grbpwr-com", mock.Anything).
		Return(nil, errors.New("s3: connection reset"))

	s := &Server{bucket: fs}
	_, err := s.UploadContentVector(context.Background(), &pb_admin.UploadContentVectorRequest{
		Raw: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Internal, st.Code())
}
