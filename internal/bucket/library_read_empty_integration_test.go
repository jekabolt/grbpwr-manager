package bucket

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
)

// ПУСТАЯ ЗАМЕТКА — ЭТО НОРМА, А НЕ КРАЙНИЙ СЛУЧАЙ.
//
// Заметка заводится именем: имя спрашивают сразу, текст набирают потом. Значит объект нулевой
// длины лежит в бакете у КАЖДОЙ только что созданной заметки, и чтение обязано его вернуть.
//
// Прежняя реализация ставила Range-запрос `bytes=0-N` на потолок чтения. На объекте нулевой
// длины любой диапазон по стандарту неудовлетворим, и хранилище отвечает 416 — то есть каждая
// новая заметка встречала человека словами «текст заметки не прочитался», и правка тоже.
//
// ДОКАЗАТЬ ЭТО МОЖНО ТОЛЬКО НАСТОЯЩИМ ХРАНИЛИЩЕМ: 416 — поведение S3, а не нашего кода, и
// подставной ридер его не воспроизводит. Поэтому тест идёт против живого MinIO и включается
// переменной окружения; без неё он пропускается и обычный прогон не трогает.
//
//	docker run -d --name grbpwr-minio -p 39000:9000 \
//	  -e MINIO_ROOT_USER=probe -e MINIO_ROOT_PASSWORD=probe12345 minio/minio server /data
//	BUCKET_PROBE_ENDPOINT=127.0.0.1:39000 go test ./internal/bucket/ -run TestGetLibraryObject
func TestGetLibraryObjectReadsAnEmptyObject(t *testing.T) {
	endpoint := os.Getenv("BUCKET_PROBE_ENDPOINT")
	if endpoint == "" {
		t.Skip("BUCKET_PROBE_ENDPOINT не задан — тесту нужен живой S3")
	}
	ctx := context.Background()

	cli, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(
			envOr("BUCKET_PROBE_KEY", "probe"),
			envOr("BUCKET_PROBE_SECRET", "probe12345"), ""),
		Secure: false,
	})
	require.NoError(t, err)

	const bucketName = "grbpwr-probe"
	exists, err := cli.BucketExists(ctx, bucketName)
	require.NoError(t, err)
	if !exists {
		require.NoError(t, cli.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{}))
	}

	b := &Bucket{Client: cli, Config: &Config{S3BucketName: bucketName}}

	// Ключ обязан лежать под files-library/, иначе гард откажет ещё до обращения к хранилищу —
	// и тест доказывал бы работу гарда, а не чтения.
	cases := []struct {
		name string
		key  string
		body string
	}{
		{"пустая заметка — ноль байт", "files-library/2026/august/note-empty.md", ""},
		{"КОНТРОЛЬ: заметка с текстом", "files-library/2026/august/note-text.md", "# план\nсвет жёсткий"},
		{"КОНТРОЛЬ: один байт", "files-library/2026/august/note-one.md", "x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := b.Client.PutObject(ctx, bucketName, c.key,
				strings.NewReader(c.body), int64(len(c.body)),
				minio.PutObjectOptions{ContentType: "text/markdown"})
			require.NoError(t, err)

			data, err := b.GetLibraryObject(ctx, c.key)
			require.NoError(t, err, "чтение обязано пройти: объект существует, ключ управляемый")
			require.Equal(t, c.body, string(data))
		})
	}

	t.Run("КОНТРОЛЬ: потолок чтения по-прежнему сторожит", func(t *testing.T) {
		// Range убран, и единственное, что теперь держит потолок, — readWithinLimit. Проверяем
		// это на настоящем объекте, а не на ридере: иначе «потолок цел» было бы утверждением
		// про функцию, а не про метод.
		key := "files-library/2026/august/note-huge.md"
		huge := bytes.Repeat([]byte("a"), maxLibraryReadBytes+1024)
		_, err := b.Client.PutObject(ctx, bucketName, key,
			bytes.NewReader(huge), int64(len(huge)),
			minio.PutObjectOptions{ContentType: "text/markdown"})
		require.NoError(t, err)

		_, err = b.GetLibraryObject(ctx, key)
		require.ErrorIs(t, err, ErrLibraryObjectTooLarge)
	})

	t.Run("КОНТРОЛЬ: чужой ключ отвергается гардом, а не хранилищем", func(t *testing.T) {
		_, err := b.GetLibraryObject(ctx, "media/2026/secret.png")
		require.ErrorIs(t, err, ErrLibraryObjectKeyNotManaged)
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
