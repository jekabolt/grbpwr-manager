package bucket

import (
	"context"
	"errors"
)

// КАРКАС Ф8 — ЧТЕНИЕ ПРИВАТНОГО ОБЪЕКТА БИБЛИОТЕКИ. ЗДЕСЬ ТОЛЬКО ПОДПИСЬ.
//
// Тело пишет T-8.2 (в library.go, рядом с загрузкой, или здесь — на выбор исполнителя), вместе с
// тестами гардов. До сих пор в bucket не было НИ ОДНОГО чтения объекта: только Upload*/Presign*/
// Delete*. Чтение появляется ради одного случая — текст заметки едет клиенту по RPC, а не
// подписанной ссылкой (text/markdown не в inline-allowlist, а fetch подписанного URL из JS
// упирается в CORS бакета — грабли выкроек).
//
// Гарды, без которых метод превращается в оракул по бакету:
//   - ключ обязан лежать под files-library/ (isManagedKeyInSegment, тот же гард, что у
//     PresignLibraryObject) — чужой префикс это ОТКАЗ;
//   - читается не больше потолка заметки (entity.MaxLibraryNoteBytes + запас) — превышение это
//     тоже отказ, а не усечение: половина текста хуже отсутствия текста;
//   - ключ приходит ТОЛЬКО из строки БД. Это свойство вызывающего, а не метода, поэтому его
//     обязаны держать оба легитимных пути (хендлер заметки и публичный маршрут /api/f/{token}).
//
// Заглушка отказывает намеренно: пустые байты выглядели бы как пустая заметка, то есть как
// стёртый человеком текст.
var errLibraryReadNotImplemented = errors.New("bucket: library object read is not implemented yet (T-8.2)")

func (b *Bucket) GetLibraryObject(ctx context.Context, objectKey string) ([]byte, error) {
	return nil, errLibraryReadNotImplemented
}
