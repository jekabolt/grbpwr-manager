package bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/minio/minio-go/v7"
)

// Ф1.1 — ВСЯ НОВАЯ I/O-ПОВЕРХНОСТЬ БАКЕТА ДЛЯ ЭКСПОРТА/ИМПОРТА ТЕХ-КАРТЫ ZIP-АРХИВОМ.
//
// Здесь четыре разных обращения к хранилищу, и у каждого свой гард, потому что у каждого свой
// вызывающий:
//   - GetManagedObject — ЧТЕНИЕ чужого объекта (медиа/выкройка) ради упаковки его в архив;
//   - UploadArchiveObject / UploadImportObject — ЗАПИСЬ приватного объекта (экспорт и принятый
//     на импорт файл);
//   - PresignArchiveObject — короткоживущая ссылка на выгруженный архив;
//   - ListObjectsOlderThan — отбор просроченного для фоновой чистки (Ф5.1).
//
// Общее правило, которое здесь нигде не проверяется и проверено быть не может: КЛЮЧ ПРИХОДИТ
// ТОЛЬКО ИЗ СТРОКИ БД (media.url через managedObjectKeyFromURL, tech_card_import.object_key) и
// НИКОГДА из запроса. Сегментные гарды ниже сужают достижимое до наших папок, но своё от чужого
// внутри папки они не отличают — это свойство вызывающего. Ровно та же формулировка стоит у
// PresignLibraryObject и GetLibraryObject, и по той же причине.
const (
	// ArchiveSegment — папка выгруженных архивов тех-карт. Экспортируется, потому что его имя
	// нужно вызывающим: Ф5.1 передаёт его в ListObjectsOlderThan.
	ArchiveSegment = "techcard-archives"
	// ImportSegment — папка принятых на импорт архивов.
	ImportSegment = "techcard-imports"

	// MaxArchiveObjectBytes — потолок принимаемого архива, 256 МиБ (решение владельца B-5). Он же
	// стоит на HTTP-маршруте загрузки (Ф2.5, свой MaxBytesReader), и он же продублирован здесь:
	// http-потолок защищает маршрут, а этот — метод. Второй нужен потому, что PutObject со
	// стримом «сколько дадут» не имеет собственной границы вовсе, а GetImportObjectReaderAt
	// материализует объект на диск.
	MaxArchiveObjectBytes = 256 * 1024 * 1024

	// ArchiveLinkTTL — срок presigned-ссылки на архив, 10 минут (решение владельца B-5). Окно
	// «нажал скачать и скачал», после которого ссылка мертва. НЕ мемоизируется и НЕ округляется
	// по окну (в отличие от PresignPatternObject): стабильность строки нужна <object>-эмбедам
	// панели, а у ссылки на скачивание такого потребителя нет.
	ArchiveLinkTTL = 10 * time.Minute
	// maxArchivePresignTTL — верхняя граница того, что этот метод согласен подписать. Гард, а не
	// настройка: архив тех-карты это выгрузка всей карты со всеми файлами, и ссылка на него не
	// должна уметь жить часами по опечатке вызывающего.
	maxArchivePresignTTL = time.Hour

	// ArchiveRetention — сколько объект архива живёт в бакете, 7 дней (решение владельца B-5).
	// Живёт здесь, рядом с сегментами, чтобы у Ф5.1 не появилось второе определение срока.
	ArchiveRetention = 7 * 24 * time.Hour

	// archiveUploadPartSize повторяет libraryUploadPartSize: размер стрима заранее неизвестен
	// (архив пишется в io.Pipe и читается PutObject'ом на лету), поэтому minio буферизует по
	// одной части, а не весь объект.
	archiveUploadPartSize = 16 * 1024 * 1024

	// maxArchiveNameLen ограничивает basename объекта. Имя строится из style_number, то есть из
	// пользовательских данных.
	maxArchiveNameLen = 120

	// archiveNameExt — единственное расширение, под которым архив кладётся в бакет.
	archiveNameExt = ".zip"

	// contentTypeZIP — тип объекта архива. Объявлен здесь, а не в utils.go, потому что в
	// mimeTypeToFileExtension ему делать нечего: расширение архива фиксировано и не выводится
	// из типа.
	contentTypeZIP ContentType = "application/zip"
)

var (
	// ErrInvalidArchiveUpload — отказ ДО обращения к хранилищу: пустой поток, негодное имя,
	// негодный import id. Отдельная ошибка, чтобы API-слой отвечал InvalidArgument, а не
	// Internal (последнее — это отказ S3).
	ErrInvalidArchiveUpload = errors.New("invalid tech card archive upload")
	// ErrManagedKeyNotAllowed — ключ не лежит ни в одном сегменте, который данному методу
	// разрешено читать/подписывать. По ней вызывающему видно, что запрос отклонил ГАРД, а не
	// бакет: это единственный отказ, не зависящий от содержимого хранилища.
	ErrManagedKeyNotAllowed = errors.New("bucket: object key is not in a segment this method may touch")
	// ErrArchiveObjectTooLarge — объект/поток длиннее MaxArchiveObjectBytes. Отказ, а не обрезка:
	// усечённый zip это «архив повреждён», и отличить его от настоящей порчи было бы нечем.
	ErrArchiveObjectTooLarge = errors.New("bucket: tech card archive exceeds the accepted size")
)

// managedReadSegments — сегменты, из которых GetManagedObject согласен читать.
//
// Медиа названо БАЗОВОЙ ПАПКОЙ, потому что другого имени у него нет: constructFullPath кладёт
// media/video в <base>/<folder>/…, а folder для них — сама же базовая папка (см. content.go,
// GetBaseFolder() передаётся как folder). Отсюда следствие, которое надо назвать вслух: разрешив
// базовую папку, мы разрешили и всё, что под ней — выкройки, ярлыки, превью. Сузить это до «только
// медиа» нечем, кроме имени папки, которого не существует.
//
// files-library ЯВНО ИСКЛЮЧЕНА, и это не косметика. У библиотеки есть СВОЙ читатель
// (GetLibraryObject) со своим потолком в размер заметки, и весь смысл того потолка в том, что
// карточку файла на десятки мегабайт нельзя превратить в способ выесть память. Второй читатель,
// который берёт тот же объект без потолка, отменил бы это молча. Архиву библиотека не нужна вовсе —
// в архив едут только медиа карты и выкройки, — так что запрет ничего не стоит и держит два пути
// непересекающимися по построению, а не по дисциплине (тем же приёмом разведены Presign*Object).
func (b *Bucket) managedReadSegments() []string {
	segments := make([]string, 0, 4)
	if base := strings.Trim(strings.TrimSpace(b.BaseFolder), "/"); base != "" {
		segments = append(segments, base)
	}
	return append(segments, patternFolder, ArchiveSegment, ImportSegment)
}

// isAllowedManagedReadKey — гард чтения. Свободная функция с явным списком сегментов: её можно
// доказать без бакета и без сети, что и делает archive_test.go.
func isAllowedManagedReadKey(key string, segments []string) bool {
	// Явный запрет побеждает разрешение: files-library лежит ПОД базовой папкой, поэтому без
	// этой строки её открыл бы сегмент медиа.
	if isManagedKeyInSegment(key, libraryFolder) {
		return false
	}
	for _, segment := range segments {
		if isManagedKeyInSegment(key, segment) {
			return true
		}
	}
	return false
}

// GetManagedObject открывает объект бакета по ключу для потокового чтения и возвращает его размер.
//
// Нужен экспорту (Ф1.3): медиа карты и файлы выкроек едут в архив байтами, а не ссылками, потому
// что ссылка на чужом хосте — это не выгрузка. Поток, а не []byte: одно видео легально весит
// десятки мегабайт, и «прочитать в память всё, что дадут» здесь было бы тем же дефектом, от
// которого защищается GetLibraryObject.
//
// Гард стоит ПЕРВЫМ и до всякого обращения к клиенту: метод не должен уметь сходить в бакет за
// произвольным ключом даже затем, чтобы получить оттуда отказ. Размер снимается Stat'ом —
// отдельным HEAD-запросом, тело он не трогает, поэтому последующее чтение начинается с нуля.
// Stat здесь ещё и единственное место, где виден отсутствующий ключ: minio отдаёт объект лениво,
// и без Stat 404 приехал бы вызывающему первым Read'ом.
func (b *Bucket) GetManagedObject(ctx context.Context, objectKey string) (io.ReadCloser, int64, error) {
	key := strings.Trim(objectKey, "/")
	if !isAllowedManagedReadKey(key, b.managedReadSegments()) {
		return nil, 0, fmt.Errorf("%w: %q", ErrManagedKeyNotAllowed, objectKey)
	}

	obj, err := b.Client.GetObject(ctx, b.S3BucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("get managed object %q: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		slog.Default().ErrorContext(ctx, "can't stat managed object",
			slog.String("key", key), slog.String("err", err.Error()))
		return nil, 0, fmt.Errorf("stat managed object %q: %w", key, err)
	}
	return obj, info.Size, nil
}

// archiveObjectName приводит имя файла архива к тому, что можно положить в ключ S3 и потом отдать
// в Content-Disposition. Имя строится вызывающим из style_number, то есть из данных, которые
// набирал человек: без этой нормализации в ключ уехали бы и слэши (объект оказался бы в другой
// папке, и presign-гард отказал бы уже постфактум), и управляющие символы.
func archiveObjectName(name string) (string, error) {
	name = path.Base(strings.TrimSpace(strings.ReplaceAll(name, "\\", "/")))
	name = strings.TrimSuffix(name, archiveNameExt)
	var b strings.Builder
	meaningful := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			meaningful = true
		case r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= maxArchiveNameLen {
			break
		}
	}
	if !meaningful {
		return "", fmt.Errorf("%w: object name %q has no usable characters", ErrInvalidArchiveUpload, name)
	}
	return b.String() + archiveNameExt, nil
}

// archiveKeyEntropy — 128 бит в ОТДЕЛЬНОМ сегменте ключа, а не в имени файла.
//
// Зачем энтропия: два экспорта одной карты в одну минуту дают одинаковое имя (штамп в имени —
// до минуты, FORMAT.md), и без неё второй молча затёр бы первый, оставив выданную ссылку
// указывающей на чужой объект. Плюс — то же, ради чего энтропию носят ключи выкроек и библиотеки:
// обладание одним ключом не должно подсказывать другой.
//
// Зачем ОТДЕЛЬНЫМ сегментом: basename ключа — это имя, под которым файл ляжет на диск скачавшему
// (Content-Disposition строится из path.Base), и FORMAT.md определяет его как
// techcard-<style>-<ts>.zip. Хвост из 32 hex-символов в имени файла сломал бы ровно это.
func archiveKeyEntropy() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("can't generate archive object key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// UploadArchiveObject стримит собранный zip в ПРИВАТНЫЙ объект и возвращает его ключ.
//
// Приватность — это ОТСУТСТВИЕ метаданных `x-amz-acl: public-read`, которые ставят пути картинок,
// видео, выкроек и ярлыков. Здесь эта асимметрия и есть суть: архив содержит всю тех-карту
// целиком — конструкцию, рецепты, файлы, — и публично адресуемым он быть не может ни на минуту.
// Достать его можно только короткоживущей подписанной ссылкой (PresignArchiveObject).
//
// Размер заранее неизвестен: Ф1.5 пишет архив в io.Pipe и читает его отсюда на лету, поэтому
// PutObject получает −1 и фиксированный PartSize — minio буферизует одну часть, а не весь архив.
func (b *Bucket) UploadArchiveObject(ctx context.Context, r io.Reader, name string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("%w: no payload", ErrInvalidArchiveUpload)
	}
	fileName, err := archiveObjectName(name)
	if err != nil {
		return "", err
	}
	entropy, err := archiveKeyEntropy()
	if err != nil {
		return "", err
	}
	key := ArchiveSegment + "/" + entropy + "/" + fileName

	if _, err := b.Client.PutObject(ctx, b.S3BucketName, key, r, -1,
		minio.PutObjectOptions{
			ContentType: string(contentTypeZIP),
			// Короткий приватный кэш: объект живёт 7 дней и достижим только подписанной ссылкой
			// на 10 минут, поэтому годовой immutable-кэш публичных путей здесь бессмыслен.
			CacheControl: "private, max-age=600",
			PartSize:     archiveUploadPartSize,
		}); err != nil {
		slog.Default().ErrorContext(ctx, "can't upload tech card archive",
			slog.String("key", key), slog.String("err", err.Error()))
		return "", fmt.Errorf("upload tech card archive %q: %w", key, err)
	}
	return key, nil
}

// importIDPattern — что мы согласны принять как идентификатор импорта. Ф2.5 передаёт ULID; правило
// шире ULID намеренно (генератор может смениться), но уже любой строки: id уходит В КЛЮЧ, поэтому
// ни слэша, ни точки, ни пустоты в нём быть не может.
func isValidImportID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// UploadImportObject стримит принятый на импорт архив в приватный объект techcard-imports/<id>.zip
// и возвращает его ключ (Ф2.5 кладёт его в tech_card_import.object_key).
//
// Ключ детерминированный, в отличие от экспортного: строка импорта ссылается на объект, объект
// один на строку, и восстановить одно из другого должно быть можно без второго чтения. Энтропию
// сюда приносит сам id (ULID).
//
// Потолок 256 МиБ проверяется ЗДЕСЬ, а не только на HTTP-маршруте: PutObject со стримом неизвестной
// длины принял бы сколько угодно, а объект в бакете — это уже занятое место и уже возможный вход в
// zip-читалку. Превышение обрывает загрузку ошибкой; minio не публикует незавершённый multipart,
// поэтому объекта после отказа не остаётся.
func (b *Bucket) UploadImportObject(ctx context.Context, r io.Reader, importID string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("%w: no payload", ErrInvalidArchiveUpload)
	}
	if !isValidImportID(importID) {
		return "", fmt.Errorf("%w: import id %q is not a key-safe identifier", ErrInvalidArchiveUpload, importID)
	}
	key := ImportSegment + "/" + importID + archiveNameExt

	if _, err := b.Client.PutObject(ctx, b.S3BucketName, key, newCapReader(r, MaxArchiveObjectBytes), -1,
		minio.PutObjectOptions{
			ContentType:  string(contentTypeZIP),
			CacheControl: "private, max-age=600",
			PartSize:     archiveUploadPartSize,
		}); err != nil {
		slog.Default().ErrorContext(ctx, "can't upload tech card import archive",
			slog.String("key", key), slog.String("err", err.Error()))
		return "", fmt.Errorf("upload tech card import archive %q: %w", key, err)
	}
	return key, nil
}

// capReader отказывает, как только через него прошло больше limit байт. Не io.LimitReader:
// тот молча выдаёт EOF на границе, а молча усечённый архив неотличим от повреждённого.
type capReader struct {
	r     io.Reader
	left  int64
	limit int64
	err   error
}

func newCapReader(r io.Reader, limit int64) *capReader {
	return &capReader{r: r, left: limit, limit: limit}
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		c.err = fmt.Errorf("%w: over %d bytes", ErrArchiveObjectTooLarge, c.limit)
		return 0, c.err
	}
	return n, err
}

// PresignArchiveObject возвращает подписанный GET на объект архива и момент его истечения.
//
// Подписывается ТОЛЬКО сегмент techcard-archives: метод отдаёт наружу ссылку, обходящую всякую
// авторизацию, и список того, что он умеет отдать, обязан быть уже одной папки. Тот же гард и тот
// же санитайзер имени вложения, что у выкроек и библиотеки — managedPresignInput один на всех
// намеренно, вторая копия разошлась бы с первой молча.
//
// Ни окна, ни мемоизации: обе заведены ради <object>-эмбедов панели, которым нужна СТАБИЛЬНАЯ
// строка. Здесь потребитель другой — ссылку открывают один раз, — а цена стабильности (url живёт
// до 12 часов и не отзывается ничем) для выгрузки всей карты неприемлема.
//
// download=true всегда: zip не на что смотреть в браузере, его скачивают. Имя вложения — basename
// ключа, то есть техкарточное имя из FORMAT.md; из запроса оно не приходит НИКОГДА (заголовок
// ответа).
func (b *Bucket) PresignArchiveObject(ctx context.Context, objectKey string, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 || ttl > maxArchivePresignTTL {
		return "", time.Time{}, fmt.Errorf("%w: presign ttl %s is outside (0, %s]",
			ErrInvalidArchiveUpload, ttl, maxArchivePresignTTL)
	}
	key, _, reqParams, err := managedPresignInput(objectKey, ArchiveSegment, true, "")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %w", ErrManagedKeyNotAllowed, err)
	}
	expiresAt := time.Now().UTC().Add(ttl)
	u, err := b.Client.PresignedGetObject(ctx, b.S3BucketName, key, ttl, reqParams)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presign tech card archive %q: %w", key, err)
	}
	return u.String(), expiresAt, nil
}

// isCleanableSegment — что вообще разрешено перечислять на удаление. Гард, который делает «не
// чистить никакие другие сегменты бакета» (запрет Ф5.1) верным ПО ПОСТРОЕНИЮ, а не по дисциплине
// воркера: перепутанный аргумент — это отказ, а не выборка медиатеки на снос.
func isCleanableSegment(segment string) bool {
	return segment == ArchiveSegment || segment == ImportSegment
}

// ListObjectsOlderThan возвращает ключи объектов сегмента, изменённых раньше чем age назад.
// Пара к уже существующему RemoveObjectsByKeys: перечисление и удаление разведены, чтобы отбор
// можно было проверить, не удаляя (Ф5.1).
//
// Объект без даты изменения НЕ попадает в выборку: «возраст неизвестен» это не «старый», а
// удаление здесь необратимо.
func (b *Bucket) ListObjectsOlderThan(ctx context.Context, segment string, age time.Duration) ([]string, error) {
	if !isCleanableSegment(segment) {
		return nil, fmt.Errorf("%w: %q is not a cleanable segment", ErrManagedKeyNotAllowed, segment)
	}
	if age <= 0 {
		return nil, fmt.Errorf("%w: age must be positive, got %s", ErrInvalidArchiveUpload, age)
	}
	cutoff := time.Now().UTC().Add(-age)

	// Собственный отменяемый контекст: minio отдаёт листинг горутиной, которая пишет в канал и
	// слушает ctx.Done. Выйти из цикла по ошибке, не отменив её, — оставить горутину висеть.
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var keys []string
	for obj := range b.Client.ListObjects(listCtx, b.S3BucketName, minio.ListObjectsOptions{
		Prefix:    segment + "/",
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("list %q objects: %w", segment, obj.Err)
		}
		if obj.Key == "" || strings.HasSuffix(obj.Key, "/") {
			continue
		}
		if obj.LastModified.IsZero() || !obj.LastModified.UTC().Before(cutoff) {
			continue
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

// tempFileReaderAt — скачанный на диск объект импорта под интерфейсом, которого требует
// zip.NewReader. Файл РАЗЫМЕНОВАН сразу после создания: он жив, пока жив дескриптор, и исчезает
// вместе с процессом даже при панике или kill'е, так что 256 МиБ временного файла не могут
// пережить сбой.
type tempFileReaderAt struct {
	f *os.File
}

func (t *tempFileReaderAt) ReadAt(p []byte, off int64) (int, error) { return t.f.ReadAt(p, off) }

func (t *tempFileReaderAt) Close() error {
	name := t.f.Name()
	err := t.f.Close()
	// Подстраховка на случай платформы, где разыменование при открытом дескрипторе не сработало.
	if rmErr := os.Remove(name); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) && err == nil {
		err = rmErr
	}
	return err
}

// GetImportObjectReaderAt отдаёт залитый архив импорта в виде io.ReaderAt для zip.NewReader.
//
// ФАКТ, ПРОВЕРЕННЫЙ ПО ИСХОДНИКУ: *minio.Object РЕАЛИЗУЕТ io.ReaderAt
// (minio-go v7.0.62, api-get-object.go:439) и делает это честно — под мьютексом и с readFull,
// то есть контракт «ReadAt заполняет буфер целиком или возвращает ошибку» соблюдён.
//
// И ВСЁ РАВНО ОН ЗДЕСЬ НЕ ГОДИТСЯ. Каждый вызов ReadAt — это ОТДЕЛЬНЫЙ ranged GET по сети
// (api-get-object.go:187-196: при смене смещения предыдущий httpReader закрывается и открывается
// новый). А zip читает мелко: центральный каталог — через bufio на 4 КиБ, тело записи — через
// io.Copy на 32 КиБ или через flate с буфером на 4 КиБ. Архив в 256 МиБ это порядка десятков тысяч
// HTTPS-запросов к Spaces вместо одного — импорт превратился бы в часы и в счёт за запросы.
//
// Поэтому объект скачивается ОДНИМ потоковым GET во временный файл, и ReaderAt'ом работает файл.
// Память при этом не растёт: копия идёт потоком, а не через []byte.
//
// Потолок 256 МиБ проверяется дважды: сначала по Stat (чтобы не начинать качать заведомо большое),
// потом на самом копировании (Stat описывает объект, а не то, что реально приехало).
func (b *Bucket) GetImportObjectReaderAt(ctx context.Context, objectKey string) (dependency.ReaderAtCloser, int64, error) {
	key := strings.Trim(objectKey, "/")
	if !isManagedKeyInSegment(key, ImportSegment) {
		return nil, 0, fmt.Errorf("%w: %q is not a %s key", ErrManagedKeyNotAllowed, objectKey, ImportSegment)
	}

	obj, err := b.Client.GetObject(ctx, b.S3BucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("get import object %q: %w", key, err)
	}
	defer obj.Close()

	info, err := obj.Stat()
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't stat import object",
			slog.String("key", key), slog.String("err", err.Error()))
		return nil, 0, fmt.Errorf("stat import object %q: %w", key, err)
	}
	if info.Size > MaxArchiveObjectBytes {
		return nil, 0, fmt.Errorf("%w: object %q is %d bytes, max %d",
			ErrArchiveObjectTooLarge, key, info.Size, int64(MaxArchiveObjectBytes))
	}

	f, err := os.CreateTemp("", "techcard-import-*.zip")
	if err != nil {
		return nil, 0, fmt.Errorf("create temp file for import object %q: %w", key, err)
	}
	// Разыменование СРАЗУ: дальше файл существует только как дескриптор.
	if err := os.Remove(f.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		f.Close()
		return nil, 0, fmt.Errorf("unlink temp file for import object %q: %w", key, err)
	}
	ra := &tempFileReaderAt{f: f}

	size, err := io.Copy(f, newCapReader(obj, MaxArchiveObjectBytes))
	if err != nil {
		ra.Close()
		slog.Default().ErrorContext(ctx, "can't download import object",
			slog.String("key", key), slog.String("err", err.Error()))
		return nil, 0, fmt.Errorf("download import object %q: %w", key, err)
	}
	return ra, size, nil
}
