package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/minio/minio-go/v7"
)

// Ф8 — ЧТЕНИЕ ПРИВАТНОГО ОБЪЕКТА БИБЛИОТЕКИ.
//
// До сих пор в bucket не было НИ ОДНОГО чтения объекта: только Upload*/Presign*/Delete*. Чтение
// появляется ради одного случая — текст заметки едет клиенту по RPC, а не подписанной ссылкой
// (text/markdown не в inline-allowlist, а fetch подписанного URL из JS упирается в CORS бакета —
// грабли выкроек).
//
// Гарды, без которых метод превращается в оракул по бакету:
//   - ключ обязан лежать под files-library/ (isManagedKeyInSegment, тот же гард, что у
//     PresignLibraryObject) — чужой префикс это ОТКАЗ;
//   - читается не больше потолка заметки с запасом — превышение это тоже отказ, а не усечение:
//     половина текста хуже отсутствия текста;
//   - ключ приходит ТОЛЬКО из строки БД. Это свойство вызывающего, а не метода, поэтому его
//     обязаны держать оба легитимных пути (хендлер заметки и публичный маршрут /api/f/{token}).

const (
	// libraryReadHeadroom is the slack above the note cap. Потолок заметки считается по тексту,
	// который прислал редактор, а с диска приезжает объект — тот же текст, но, например, с BOM или
	// с хвостом перевода строки. Запас в 64 KiB отделяет «человек написал больше, чем можно» от
	// «объект на байт длиннее, чем ожидал вызывающий», и второе не должно выглядеть как первое.
	libraryReadHeadroom = 64 * 1024
	// maxLibraryReadBytes — сколько метод согласен вытащить в память ЗА ОДИН вызов. Это не свойство
	// заметки, а свойство чтения: библиотека принимает файлы в десятки мегабайт, и метод, читающий
	// «что дадут», превратил бы карточку любого такого файла в способ выесть память процесса.
	maxLibraryReadBytes = entity.MaxLibraryNoteBytes + libraryReadHeadroom
)

var (
	// ErrLibraryObjectKeyNotManaged — ключ не из библиотеки. Отдельная ошибка, а не просто текст:
	// вызывающему по ней видно, что запрос был отклонён ГАРДОМ, а не бакетом, и это единственный
	// случай, когда ответ не зависит от того, что лежит в хранилище.
	ErrLibraryObjectKeyNotManaged = errors.New("bucket: object key is not a managed files-library key")
	// ErrLibraryObjectTooLarge — объект длиннее потолка чтения. Отказ, а не обрезка.
	ErrLibraryObjectTooLarge = errors.New("bucket: library object is larger than the note read limit")
)

// GetLibraryObject reads a PRIVATE library object into memory.
//
// Range-запрос стоит ровно на потолке (SetRange даёт максимум maxLibraryReadBytes+1 байт), поэтому
// «слишком большой объект» невозможно СКАЧАТЬ даже случайно: лишний байт приезжает только затем,
// чтобы отличить «ровно потолок» от «больше потолка».
func (b *Bucket) GetLibraryObject(ctx context.Context, objectKey string) ([]byte, error) {
	key := strings.Trim(objectKey, "/")
	// Гард ПЕРВЫМ и до всякого обращения к клиенту: метод не должен уметь сходить в бакет за
	// произвольным ключом, даже чтобы получить оттуда отказ.
	if !isManagedKeyInSegment(key, libraryFolder) {
		return nil, fmt.Errorf("%w: %q", ErrLibraryObjectKeyNotManaged, objectKey)
	}

	opts := minio.GetObjectOptions{}
	// Диапазон включающий: [0, max] — это max+1 байт.
	if err := opts.SetRange(0, maxLibraryReadBytes); err != nil {
		return nil, fmt.Errorf("set range for library object %q: %w", key, err)
	}
	obj, err := b.Client.GetObject(ctx, b.S3BucketName, key, opts)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't open library object",
			slog.String("key", key), slog.String("err", err.Error()))
		return nil, fmt.Errorf("get library object %q: %w", key, err)
	}
	defer obj.Close()

	// minio отдаёт объект лениво: отсутствующий ключ и любой отказ бакета приезжают ПЕРВЫМ Read,
	// а не из GetObject — поэтому единственное место, где их видно, здесь.
	data, err := readWithinLimit(obj, maxLibraryReadBytes)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't read library object",
			slog.String("key", key), slog.String("err", err.Error()))
		return nil, fmt.Errorf("read library object %q: %w", key, err)
	}
	return data, nil
}

// readWithinLimit reads at most limit bytes and REFUSES when there is more.
//
// Отдельная функция, потому что это единственная часть чтения, которую можно доказать без бакета:
// граница «ровно потолок / потолок плюс байт» проверяется прямо на ней.
func readWithinLimit(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: over %d bytes", ErrLibraryObjectTooLarge, limit)
	}
	return data, nil
}
