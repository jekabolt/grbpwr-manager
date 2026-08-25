package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.5 — ЦИКЛИЧЕСКИЙ ТЕСТ: экспорт → импорт в ТУ ЖЕ базу → экспорт → равенство.
//
// Это главный гейт сохранности фичи. Всё остальное в ней проверено на моках: читалка своими
// байтами, писалка своей читалкой, резолвер выдуманным каталогом. Ни один из тех тестов не может
// увидеть ПОТЕРЮ — поле, которое экспорт не собрал, а импорт поэтому и не записал, выглядит
// одинаково зелёным с обеих сторон. Круг видит: карточка уезжает в файл и возвращается, и всё, что
// по дороге выпало, становится расхождением двух protobuf-сообщений.
//
// ПОЧЕМУ proto.Equal, А НЕ СРАВНЕНИЕ ТЕКСТА. protojson недетерминирован НАМЕРЕННО (пакет
// впрыскивает случайный пробел, чтобы никто не полагался на побайтовую стабильность), так что
// сравнение двух card.json как строк краснело бы через раз без единой потери. Сравниваются
// разобранные сообщения.
//
// ЧТО ЗНАЧИТ «НОРМАЛИЗОВАТЬ». Ровно три класса полей имеют право отличаться, и каждый назван в
// FORMAT.md: собственные номера строк исходной базы (§6.2 — «каждый id либо ремапится, либо
// выбрасывается»), штампы времени и авторства (§4.1 №3/4/10/11 — «едут как текст, не пишутся»), и
// журнал ревизий (§4.2 — не переносится в принципе). Плюс артикул, который импорт СОЗНАТЕЛЬНО
// меняет при коллизии — это отдельная проверка ниже, а не поблажка. ВСЁ ОСТАЛЬНОЕ обязано совпасть
// поле в поле. Ослабить это до «сравнили счётчики» — значит выкинуть весь смысл теста.
//
// БЕЗОПАСНОСТЬ. Тест живёт в пакете, чей TestMain в НЕ-CI режиме читает config/config.toml —
// боевой DSN — и ДРОПАЕТ там все таблицы на выходе. Отсюда сторож в первых строках: без CI и без
// локального DSN тест пропускается. Запускать ТОЛЬКО против одноразового контейнера
// (память проекта: store-tests-safe-container-method).
// ─────────────────────────────────────────────────────────────────────────────

// rtBucketHost — хост, которым притворяется бакет теста. Он должен быть НАСТОЯЩИМ хостом в глазах
// dto.managedPatternHosts: конвертер отказывает выкройке, чей url не на нашем хосте и не в сегменте
// tech-card-patterns, и без этого импорт выкроек падал бы целиком.
const rtBucketHost = "cdn.roundtrip.test"

// rtMediaFolder — базовая папка медиа-объектов (bucket.GetBaseFolder()).
const rtMediaFolder = "media"

// ────────────────────────────── бакет ──────────────────────────────

// rtObjects — содержимое «бакета»: ключ объекта → байты. Заведено отдельной структурой, а не
// полями мока, потому что мок здесь — только диспетчер вызовов; состояние живёт тут.
type rtObjects struct {
	mu sync.Mutex
	m  map[string][]byte
	n  int
}

func newRTObjects() *rtObjects { return &rtObjects{m: map[string][]byte{}} }

func (o *rtObjects) put(key string, b []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := make([]byte, len(b))
	copy(cp, b)
	o.m[key] = cp
}

func (o *rtObjects) get(key string) ([]byte, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, ok := o.m[key]
	return b, ok
}

func (o *rtObjects) del(key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.m, key)
}

func (o *rtObjects) next() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.n++
	return o.n
}

func (o *rtObjects) url(key string) string { return "https://" + rtBucketHost + "/" + key }

// rtKeyFromURL — обратная операция; повторяет archiveObjectKeyFromURL экспорта.
func rtKeyFromURL(raw string) string {
	s := strings.TrimPrefix(raw, "https://"+rtBucketHost+"/")
	return strings.Trim(s, "/")
}

func rtSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newRTBucket собирает FileStore на моке — тем же приёмом, каким его собирают все соседние тесты
// этой фичи (internal/apisrv/admin/techcard_archive_*_test.go): mocks.MockFileStore с RunAndReturn,
// за которыми стоит карта объектов в памяти. Ни minio, ни второго контейнера: вопрос бакета в этой
// фиче решён моком, и третьего способа тут не заводится.
//
// ОДНА ВЕЩЬ ЗДЕСЬ НЕ ФИКЦИЯ, И ОНА КЛЮЧЕВАЯ: UploadContentImageVerbatim ПИШЕТ НАСТОЯЩУЮ СТРОКУ
// media в НАСТОЯЩУЮ базу, с content_hash = sha256 тех самых байтов, которые лежат в объекте — ровно
// то, что делает боевой bucket.uploadVerbatimImageObj. Без этого переиспользование по содержимому
// (§6.2, бонус-ассерт ниже) проверять было бы не на чем.
//
// Всё, что мок НЕ объявляет, вызывать нельзя: mockery уронит тест на неожиданном вызове. Это
// нарочно — так видно, что конвейер не трогает бакет мимо перечисленных здесь дверей.
func newRTBucket(t *testing.T, media dependency.Media, objs *rtObjects) *mocks.MockFileStore {
	t.Helper()
	b := mocks.NewMockFileStore(t)

	b.EXPECT().GetBaseFolder().Return(rtMediaFolder).Maybe()

	// ── чтение объекта: чем экспорт кладёт медиа и выкройки в архив ──
	b.EXPECT().GetManagedObject(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, key string) (io.ReadCloser, int64, error) {
			raw, ok := objs.get(key)
			if !ok {
				return nil, 0, fmt.Errorf("no such object %q", key)
			}
			return io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
		}).Maybe()

	// ── архив: экспорт стримит его сюда ──
	b.EXPECT().UploadArchiveObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, r io.Reader, name string) (string, error) {
			body, err := io.ReadAll(r)
			if err != nil {
				return "", err
			}
			key := fmt.Sprintf("%s%d/%s", techcardarchive.BucketPrefixArchives, objs.next(), name)
			objs.put(key, body)
			return key, nil
		}).Maybe()

	b.EXPECT().PresignArchiveObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, key string, ttl time.Duration) (string, time.Time, error) {
			return objs.url(key) + "?signed=1", time.Now().Add(ttl), nil
		}).Maybe()

	// ── импорт: приём архива и чтение его обратно ──
	b.EXPECT().UploadImportObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, r io.Reader, importID string) (string, error) {
			body, err := io.ReadAll(r)
			if err != nil {
				return "", err
			}
			key := techcardarchive.BucketPrefixImports + importID + ".zip"
			objs.put(key, body)
			return key, nil
		}).Maybe()

	b.EXPECT().GetImportObjectReaderAt(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, key string) (dependency.ReaderAtCloser, int64, error) {
			raw, ok := objs.get(key)
			if !ok {
				return nil, 0, fmt.Errorf("no such import object %q", key)
			}
			return rtReaderAt{bytes.NewReader(raw)}, int64(len(raw)), nil
		}).Maybe()

	// ── импорт: заливка медиа ВЕРБАТИМОМ, с настоящей строкой в media ──
	b.EXPECT().UploadContentImageVerbatim(mock.Anything, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, raw []byte, folder, name string) (*pb_common.MediaFull, error) {
			return rtStoreMedia(ctx, media, objs, folder, name, raw, rtImageExt(raw))
		}).Maybe()

	b.EXPECT().UploadContentVideo(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, raw []byte, folder, name, contentType string) (*pb_common.MediaFull, error) {
			return rtStoreMedia(ctx, media, objs, folder, name, raw, "webm")
		}).Maybe()

	// ── импорт: заливка выкроек ──
	b.EXPECT().UploadPatternFile(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, raw []byte, objectName string) (string, int64, error) {
			ext := "dxf"
			if bytes.HasPrefix(raw, []byte("%PDF")) {
				ext = "pdf"
			}
			key := fmt.Sprintf("tech-card-patterns/%s.%s", objectName, ext)
			objs.put(key, raw)
			return objs.url(key), int64(len(raw)), nil
		}).Maybe()

	// ── компенсация: вызывается только на неудачных путях ──
	//
	// ВАРИАДИЧЕСКИЕ МЕТОДЫ ОБЪЯВЛЯЮТСЯ ПО КАЖДОЙ АРНОСТИ ОТДЕЛЬНО. Мок раскрывает `...string` в
	// отдельные аргументы, а testify сопоставляет вызов по ИХ ЧИСЛУ: одна запись с двумя
	// mock.Anything ловит ровно один url и роняет тест на двух — причём роняет ПОВЕРХ настоящей
	// ошибки, ради компенсации которой её и позвали.
	for n := 1; n <= 32; n++ {
		args := make([]any, 0, n+1)
		for i := 0; i <= n; i++ {
			args = append(args, mock.Anything)
		}
		b.On("DeleteObjects", args...).Return(nil).Maybe()
		b.On("RemoveObjectsByKeys", args...).Return(nil).Maybe()
	}

	return b
}

// rtStoreMedia кладёт байты в «бакет» и заводит строку media — с content_hash ПОЛНОГО РАЗМЕРА, как
// боевой uploadVerbatimImageObj. Производные варианты (compressed/thumbnail) указывают на тот же
// объект: они не участвуют ни в экспорте (§5.5 возит full-size), ни в дедупликации, и городить
// ресайз в фикстуре значило бы проверять image/draw, а не архив.
func rtStoreMedia(ctx context.Context, media dependency.Media, objs *rtObjects,
	folder, name string, raw []byte, ext string) (*pb_common.MediaFull, error) {

	key := fmt.Sprintf("%s/%s-og.%s", strings.Trim(folder, "/"), name, ext)
	objs.put(key, raw)
	url := objs.url(key)
	id, err := media.AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: url, FullSizeWidth: rtMediaW, FullSizeHeight: rtMediaH,
		CompressedMediaURL: url, CompressedWidth: rtMediaW, CompressedHeight: rtMediaH,
		ThumbnailMediaURL: url, ThumbnailWidth: rtMediaW, ThumbnailHeight: rtMediaH,
		ContentHash: sql.NullString{String: rtSHA(raw), Valid: true},
	})
	if err != nil {
		return nil, err
	}
	info := &pb_common.MediaInfo{MediaUrl: url, Width: rtMediaW, Height: rtMediaH}
	return &pb_common.MediaFull{
		Id:    int32(id),
		Media: &pb_common.MediaItem{FullSize: info, Thumbnail: info, Compressed: info},
	}, nil
}

// rtMediaW/rtMediaH — размеры, которыми подписан КАЖДЫЙ медиа-объект теста. Одно число на все
// картинки нарочно: ширина/высота едут в media/index.json и обязаны совпасть у обеих карточек, а
// разные размеры добавили бы к сравнению ось, которую этот тест не измеряет.
const (
	rtMediaW = 800
	rtMediaH = 1200
)

// rtReaderAt — io.ReaderAt + Close поверх среза; то, что zip.NewReader хочет от импортного объекта.
type rtReaderAt struct{ *bytes.Reader }

func (rtReaderAt) Close() error { return nil }

// rtImageExt — расширение по магическим байтам. Тот же выбор, что делает bucket.sniffImageType;
// «на вход пришло не то» здесь невозможно — байты кладёт этот же файл.
func rtImageExt(raw []byte) string {
	switch {
	case len(raw) >= 8 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "jpg"
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return "gif"
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	default:
		return "bin"
	}
}

// ────────────────────────────── стенд ──────────────────────────────

// rtRig — всё, что нужно, чтобы прогнать круг: настоящий стор, настоящий admin.Server поверх него,
// «бакет» в памяти и контекст с админской авторизацией.
type rtRig struct {
	store *MYSQLStore
	srv   *admin.Server
	objs  *rtObjects
	ctx   context.Context
}

// rtActor — имя, которым подписан ЭКСПОРТ. Импорт подписывается другим (rtImporter): created_by у
// новой карточки обязан быть импортёром, а не автором архива (FORMAT.md §4.1 №10/11), и разные
// имена — единственный способ увидеть, если это перестанет быть так.
const (
	rtActor    = "rt-author"
	rtImporter = "rt-importer"
)

func newRTRig(t *testing.T, ctx context.Context) *rtRig {
	t.Helper()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	{
		di, derr := s.Cache().GetDictionaryInfo(ctx)
		require.NoError(t, derr)
		hf, herr := s.Hero().GetHero(ctx)
		require.NoError(t, herr)
		require.NoError(t, cache.InitConsts(ctx, di, hf))
	}

	// Каталог работ шага (0329) публикуется процессу из store.New — а NewForTest его не зовёт, так
	// что без этой строки КОНВЕРТЕР ИМПОРТА отказывает любому шагу с работой словами «сервер не
	// загрузил каталог». Это свойство тестового конструктора, а не фичи: снимок процессный.
	{
		works, wErr := s.TechCards().GetOperationWorkCatalog(ctx)
		require.NoError(t, wErr)
		require.NotEmpty(t, works, "миграция 0329 не засеяла operation_work — шаг с работой проверять нечем")
		entity.SetOperationWorkCatalog(works)
	}

	// Без этого конвертер отвергает КАЖДУЮ выкройку: managedPatternHosts фейл-клоузед и пуст, пока
	// его не наполнят из конфига бакета. Восстанавливать нечего — стартовое состояние пустое, а
	// пустое значит «ни один url не годится», так что добавление хоста никому в пакете не мешает.
	dto.SetManagedPatternHosts(rtBucketHost)

	objs := newRTObjects()
	fs := newRTBucket(t, s.Media(), objs)
	srv, err := admin.New(s, fs, nil, nil, nil, nil, nil, nil, nil, nil,
		entity.LabelAddress{}, "", "", nil, jpk.Taxpayer{}, decimal.Zero)
	require.NoError(t, err)

	return &rtRig{store: s, srv: srv, objs: objs, ctx: ctx}
}

// adminCtx — контекст, каким его отдаёт интерцептор: имя и полный доступ. Пишущие ручки архива
// (экспорт и коммит) свою RBAC не перепроверяют — карта прав живёт в internal/rbac — а вот HTTP-роут
// загрузки проверяет сам, и без авторизации в контексте он отвечает 403.
func (r *rtRig) adminCtx(username string) context.Context {
	return authsrv.PutAdminAuthz(
		authsrv.PutAdminUsername(r.ctx, username),
		authsrv.AdminAuthz{Super: true},
	)
}

// export гоняет ExportTechCardArchive и возвращает БАЙТЫ архива — те самые, что ушли в бакет.
// Ответ RPC отдаёт только ссылку, поэтому байты снимаются с объекта по ключу пресайна.
func (r *rtRig) export(t *testing.T, techCardID int) ([]byte, *pb_admin.ExportTechCardArchiveResponse) {
	t.Helper()
	resp, err := r.srv.ExportTechCardArchive(r.adminCtx(rtActor),
		&pb_admin.ExportTechCardArchiveRequest{TechCardId: int32(techCardID)})
	require.NoError(t, err, "экспорт карточки %d", techCardID)
	require.NotEmpty(t, resp.GetUrl())

	key := rtKeyFromURL(strings.TrimSuffix(resp.GetUrl(), "?signed=1"))
	raw, ok := r.objs.get(key)
	require.True(t, ok, "пресайн назвал ключ %q, которого в бакете нет", key)
	require.NotEmpty(t, raw)
	return raw, resp
}

// upload прогоняет архив через НАСТОЯЩИЙ HTTP-роут импорта (multipart), а не через внутренности:
// dry-run отчёт, строка tech_card_import и объект в бакете — всё это его работа, и обойти его
// значило бы проверить конвейер короче настоящего.
func (r *rtRig) upload(t *testing.T, zip []byte) (string, *pb_admin.TechCardImportReport) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("archive", "techcard.zip")
	require.NoError(t, err)
	_, err = part.Write(zip)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/techcard-archive/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(r.adminCtx(rtImporter))

	rec := httptest.NewRecorder()
	r.srv.TechCardArchiveUploadHandler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "загрузка архива: %s", rec.Body.String())

	var out struct {
		ImportID string          `json:"import_id"`
		DryRun   bool            `json:"dry_run"`
		Report   json.RawMessage `json:"report"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.True(t, out.DryRun)
	require.Len(t, out.ImportID, 26)

	rep := &pb_admin.TechCardImportReport{}
	require.NoError(t, protojson.Unmarshal(out.Report, rep))
	return out.ImportID, rep
}

// commit подтверждает импорт и отдаёт id новой карточки.
func (r *rtRig) commit(t *testing.T, importID string) (int, *pb_admin.TechCardImportReport) {
	t.Helper()
	resp, err := r.srv.CommitTechCardImport(r.adminCtx(rtImporter),
		&pb_admin.CommitTechCardImportRequest{ImportId: importID})
	require.NoError(t, err, "коммит импорта %s", importID)
	require.Positive(t, resp.GetTechCardId())
	id := int(resp.GetTechCardId())
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })
	return id, resp.GetReport()
}

// importArchive — загрузка + коммит одним движением.
func (r *rtRig) importArchive(t *testing.T, zip []byte) (int, *pb_admin.TechCardImportReport) {
	t.Helper()
	importID, _ := r.upload(t, zip)
	return r.commit(t, importID)
}

// ────────────────────────────── нормализация ──────────────────────────────

// rtClearedNames — ИСЧЕРПЫВАЮЩИЙ список того, чему разрешено отличаться у двух карточек, и у
// каждого имени есть строка контракта, которая это разрешает. Список закрытый: всё, что не здесь,
// обязано совпасть. Добавлять сюда имя, чтобы тест позеленел, — значит объявить потерю нормой.
//
//   - id, tech_card_id — собственные номера строк исходной базы (FORMAT.md §6.2: «каждый id либо
//     ремапится, либо выбрасывается»; сам card.json их возит как справку, §4.1 №1).
//   - lock_version — токен оптимистической блокировки источника (§4.1 №6, «travels»).
//   - created_at / updated_at / created_by / updated_by — часы и авторство ИСТОЧНИКА; принимающая
//     сторона штампует свои (§4.1 №3/4/10/11, «travel … not written»).
//   - style_number — импорт МЕНЯЕТ его при коллизии, и это не поблажка: ветка перенумерации
//     проверяется отдельным ассертом ниже, здесь она просто выведена из-под proto.Equal.
//   - revisions — журнал не переносится в принципе (§4.2).
//   - piece_ids / bom_item_ids на шаге — РАЗРЕШЁННЫЕ FK строк источника (поля 22 и 24), числовая
//     проекция ключевых списков piece_line_keys (21) и bom_line_keys (23). Ключи едут вербатимом и
//     сравниваются здесь же, так что гашение проекции ничего не прячет: разъедься связь шага с
//     деталью — разъедутся КЛЮЧИ, и тест это увидит.
//   - uploaded_at у выкройки — серверный штамп «когда объект лёг в бакет ЭТОГО инстанса». Байты на
//     принимающей стороне заливаются заново, у нового объекта своё время, и штамп источника был бы
//     ложью о чужом хранилище. Тот же класс, что created_at/updated_at.
//   - stale у скоупа замеров — ВЕРДИКТ ЧТЕНИЯ, а не факт: сервер считает его на каждом чтении,
//     сравнивая сегодняшний отпечаток листов с тем, под которым мерили. У импортированной карточки
//     листы — свежезалитые объекты со своей личностью, и импорт нарочно пишет доменно-разделённый
//     отпечаток, чтобы скоуп читался «мерили там, файлы с тех пор другие» (см. шапку
//     insertImportedPieceAreas и §4.1). Расхождение здесь ЗАЛОЖЕНО, а не потеряно.
//
// Заметьте, чего здесь НЕТ: parsed_by / parsed_at замеров площадей. Они едут и ПИШУТСЯ как есть
// (§4.1 «measured piece areas»), поэтому обязаны совпасть — и если перестанут, тест покраснеет.
var rtClearedNames = map[string]bool{
	"id":           true,
	"tech_card_id": true,
	"lock_version": true,
	"created_at":   true,
	"updated_at":   true,
	"created_by":   true,
	"updated_by":   true,
	"style_number": true,
	"revisions":    true,
	"piece_ids":    true,
	"bom_item_ids": true,
	"stale":        true,
	"uploaded_at":  true,
}

// rtNormalizeCard приводит card.json к сравнимому виду.
//
// ДВА ПРОХОДА, И ПОРЯДОК ВАЖЕН. Сначала media_id переводятся в КАНОНИЧЕСКИЙ номер — порядковый
// номер содержимого (sha256) картинки среди всех картинок архива. Это не «обнулили id»: связь
// «слот → эти байты» сохраняется целиком, и если импорт привяжет выноску к другой картинке, тест
// это увидит. Только потом гасятся имена из rtClearedNames — в том числе MediaFull.id внутри
// resolved_*_media, который к этому моменту уже не несёт информации, которой нет в слотах.
func rtNormalizeCard(t *testing.T, card *pb_common.TechCard, canon map[int64]int64, what string) *pb_common.TechCard {
	t.Helper()
	out := proto.Clone(card).(*pb_common.TechCard)

	var misses []string
	techcardarchive.RemapIntFieldsDeep(out.ProtoReflect(), techcardarchive.MediaFieldNames, canon,
		func(field string, old int64) { misses = append(misses, fmt.Sprintf("%s=%d", field, old)) })
	sort.Strings(misses)
	require.Empty(t, misses, "%s: в card.json есть ссылки на медиа, которых нет в media/index.json — "+
		"это потеря байтов, а не повод ослабить нормализацию", what)

	techcardarchive.RedactFieldsDeep(out.ProtoReflect(), rtClearedNames)
	return out
}

// rtMediaCanon строит «media_id источника → порядковый номер содержимого». Ключ — sha256: у обоих
// архивов множество sha одинаково по построению (импорт кладёт full-size вербатимом), поэтому
// один и тот же снимок получает один и тот же канонический номер в обоих.
func rtMediaCanon(t *testing.T, index []techcardarchive.MediaIndexEntry, what string) map[int64]int64 {
	t.Helper()
	shas := make([]string, 0, len(index))
	seen := map[string]bool{}
	for _, e := range index {
		require.NotEmpty(t, e.SHA256, "%s: строка media/index.json без sha256", what)
		if !seen[e.SHA256] {
			seen[e.SHA256] = true
			shas = append(shas, e.SHA256)
		}
	}
	sort.Strings(shas)
	rank := make(map[string]int64, len(shas))
	for i, s := range shas {
		rank[s] = int64(i + 1)
	}
	out := make(map[int64]int64, len(index))
	for _, e := range index {
		out[int64(e.Ref)] = rank[e.SHA256]
	}
	return out
}

// ────────────────────────────── диагностика ──────────────────────────────

// rtProtoDiff перечисляет расхождения двух сообщений путями полей. Только для сообщения об ошибке:
// вердикт выносит proto.Equal, а это — чтобы человек увидел, ЧТО именно потерялось, вместо
// «сообщения не равны».
func rtProtoDiff(a, b protoreflect.Message, at string, out *[]string) {
	if len(*out) >= 60 {
		return
	}
	fds := a.Descriptor().Fields()
	for i := 0; i < fds.Len(); i++ {
		fd := fds.Get(i)
		p := at + "." + string(fd.Name())
		av, bv := a.Get(fd), b.Get(fd)
		switch {
		case fd.IsMap():
			am, bm := av.Map(), bv.Map()
			if am.Len() != bm.Len() {
				*out = append(*out, fmt.Sprintf("%s: map size %d != %d", p, am.Len(), bm.Len()))
				continue
			}
			am.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
				if !bm.Has(k) {
					*out = append(*out, fmt.Sprintf("%s[%s]: отсутствует справа", p, k.String()))
					return true
				}
				if fd.MapValue().Kind() == protoreflect.MessageKind {
					rtProtoDiff(v.Message(), bm.Get(k).Message(), fmt.Sprintf("%s[%s]", p, k.String()), out)
				} else if v.String() != bm.Get(k).String() {
					*out = append(*out, fmt.Sprintf("%s[%s]: %q != %q", p, k.String(), v.String(), bm.Get(k).String()))
				}
				return true
			})
		case fd.IsList():
			al, bl := av.List(), bv.List()
			if al.Len() != bl.Len() {
				*out = append(*out, fmt.Sprintf("%s: длина %d != %d", p, al.Len(), bl.Len()))
				continue
			}
			for j := 0; j < al.Len(); j++ {
				if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
					rtProtoDiff(al.Get(j).Message(), bl.Get(j).Message(), fmt.Sprintf("%s[%d]", p, j), out)
				} else if al.Get(j).String() != bl.Get(j).String() {
					*out = append(*out, fmt.Sprintf("%s[%d]: %q != %q", p, j, al.Get(j).String(), bl.Get(j).String()))
				}
			}
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			if a.Has(fd) != b.Has(fd) {
				*out = append(*out, fmt.Sprintf("%s: задано %v != %v", p, a.Has(fd), b.Has(fd)))
				continue
			}
			if a.Has(fd) {
				rtProtoDiff(av.Message(), bv.Message(), p, out)
			}
		default:
			if av.String() != bv.String() {
				*out = append(*out, fmt.Sprintf("%s: %q != %q", p, av.String(), bv.String()))
			}
		}
	}
}

func rtRequireProtoEqual(t *testing.T, want, got *pb_common.TechCard) {
	t.Helper()
	if proto.Equal(want, got) {
		return
	}
	var diff []string
	rtProtoDiff(want.ProtoReflect(), got.ProtoReflect(), "card", &diff)
	t.Fatalf("card.json экспорта A и экспорта B разошлись после нормализации — это ПОТЕРЯ ФИЧИ, "+
		"а не повод расширять rtClearedNames:\n  %s", strings.Join(diff, "\n  "))
}

// ────────────────────────────── сайдкары ──────────────────────────────

// rtSidecar читает один JSON-сайдкар архива в v. Отсутствие файла — законное состояние (§1), и
// вызывающий узнаёт об этом по ok.
func rtSidecar(t *testing.T, a *techcardarchive.Archive, name string, v any) bool {
	t.Helper()
	if !a.Has(name) {
		return false
	}
	raw, err := a.ReadFile(name)
	require.NoError(t, err, "чтение %s", name)
	require.NoError(t, json.Unmarshal(raw, v), "разбор %s", name)
	return true
}

// rtMediaIndex — media/index.json архива.
func rtMediaIndex(t *testing.T, a *techcardarchive.Archive) []techcardarchive.MediaIndexEntry {
	t.Helper()
	var idx []techcardarchive.MediaIndexEntry
	rtSidecar(t, a, techcardarchive.FileMediaIndex, &idx)
	return idx
}

// rtCompareSidecars сравнивает всё, что едет рядом с card.json.
//
// Сравниваются РАЗОБРАННЫЕ структуры, а не байты: json.Marshal стабилен, но сайдкар — это
// содержание, а не текст, и сравнение структур называет расхождение полем, а не смещением.
//
// Файлы (media/<sha>.ext, patterns/<sha>.ext) сравниваются ПО SHA, потому что по построению они
// равны: имя файла в архиве — это и есть sha его содержимого, а читалка сверяет цифру на каждом
// чтении. Совпали имена — совпали байты.
func rtCompareSidecars(t *testing.T, a, b *techcardarchive.Archive, canonA, canonB map[int64]int64) {
	t.Helper()

	// ── sizechart.json ──
	var scA, scB techcardarchive.SizeChart
	okA := rtSidecar(t, a, techcardarchive.FileSizeChart, &scA)
	okB := rtSidecar(t, b, techcardarchive.FileSizeChart, &scB)
	require.Equal(t, okA, okB, "sizechart.json есть у одного архива и нет у другого")
	require.Equal(t, scA, scB, "размерная таблица разошлась: обе оси едут ИМЕНАМИ (§5.1), "+
		"так что совпадать она обязана до ячейки")

	// ── assembly.json ──
	var asA, asB []techcardarchive.AssemblyLink
	rtSidecar(t, a, techcardarchive.FileAssembly, &asA)
	rtSidecar(t, b, techcardarchive.FileAssembly, &asB)
	require.Equal(t, asA, asB, "связь сборки разошлась: компонент едет НОМЕРОМ СТИЛЯ (§5.2)")

	// ── materials/index.json ──
	var mtA, mtB []techcardarchive.MaterialPassport
	rtSidecar(t, a, techcardarchive.FileMaterialsIndex, &mtA)
	rtSidecar(t, b, techcardarchive.FileMaterialsIndex, &mtB)
	sort.Slice(mtA, func(i, j int) bool { return mtA[i].Ref < mtA[j].Ref })
	sort.Slice(mtB, func(i, j int) bool { return mtB[i].Ref < mtB[j].Ref })
	require.Equal(t, mtA, mtB, "паспорта материалов разошлись: импорт СОПОСТАВЛЯЕТ артикулы, а не "+
		"заводит их (§5.4), значит в той же базе обе карточки обязаны сослаться на те же строки каталога")

	// ── patterns/index.json ──
	var ptA, ptB []techcardarchive.PatternIndexEntry
	rtSidecar(t, a, techcardarchive.FilePatternsIndex, &ptA)
	rtSidecar(t, b, techcardarchive.FilePatternsIndex, &ptB)
	sort.Slice(ptA, func(i, j int) bool { return ptA[i].LineKey < ptA[j].LineKey })
	sort.Slice(ptB, func(i, j int) bool { return ptB[i].LineKey < ptB[j].LineKey })
	require.Equal(t, ptA, ptB, "индекс выкроек разошёлся (в нём нет ни одного чужого id — "+
		"line_key едет вербатимом, размер именем, файл — своей sha)")

	// ── markers/index.json ──
	var mkA, mkB []techcardarchive.MarkerIndexEntry
	rtSidecar(t, a, techcardarchive.FileMarkersIndex, &mkA)
	rtSidecar(t, b, techcardarchive.FileMarkersIndex, &mkB)
	sort.Slice(mkA, func(i, j int) bool { return mkA[i].File < mkA[j].File })
	sort.Slice(mkB, func(i, j int) bool { return mkB[i].File < mkB[j].File })
	require.Equal(t, mkA, mkB, "индекс раскладок разошёлся")

	// ── media/index.json: единственный сайдкар с чужим id, и он ремапится ──
	idxA, idxB := rtMediaIndex(t, a), rtMediaIndex(t, b)
	require.Equal(t, len(idxA), len(idxB), "число строк media/index.json разошлось")
	normMedia := func(idx []techcardarchive.MediaIndexEntry, canon map[int64]int64) []techcardarchive.MediaIndexEntry {
		out := append([]techcardarchive.MediaIndexEntry(nil), idx...)
		for i := range out {
			out[i].Ref = int32(canon[int64(out[i].Ref)])
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
		return out
	}
	require.Equal(t, normMedia(idxA, canonA), normMedia(idxB, canonB),
		"media/index.json разошёлся: sha, вид, подпись и размеры описывают САМИ БАЙТЫ и обязаны пережить круг")

	// ── блобы раскладок ──
	rtCompareMarkerBlobs(t, a, b, mkA)
}

// rtCompareMarkerBlobs сравнивает раскладки — единственную запись архива, которая едет сырым
// protojson. §5.7 перечисляет ВСЁ, что импорт в ней трогает; всё остальное обязано доехать
// нетронутым, и здесь это и меряется.
func rtCompareMarkerBlobs(t *testing.T, a, b *techcardarchive.Archive, index []techcardarchive.MarkerIndexEntry) {
	t.Helper()
	for _, e := range index {
		rawA, err := a.ReadFile(e.File)
		require.NoError(t, err)
		rawB, err := b.ReadFile(e.File)
		require.NoError(t, err, "архив B не несёт раскладки %s", e.File)

		mA, mB := &pb_common.TechCardMarker{}, &pb_common.TechCardMarker{}
		require.NoError(t, protojson.Unmarshal(rawA, mA))
		require.NoError(t, protojson.Unmarshal(rawB, mB))
		// summary.id / summary.tech_card_id — собственные номера строк источника, §5.7 их
		// «ignored and re-minted». Больше в раскладке ремапить нечего: size_id идут через тот же
		// словарь той же базы, а piece_id раскладко-локальны и не идентичности.
		techcardarchive.RedactFieldsDeep(mA.ProtoReflect(), rtClearedNames)
		techcardarchive.RedactFieldsDeep(mB.ProtoReflect(), rtClearedNames)
		if !proto.Equal(mA, mB) {
			var diff []string
			rtProtoDiff(mA.ProtoReflect(), mB.ProtoReflect(), "marker", &diff)
			t.Fatalf("раскладка %s не пережила круг:\n  %s", e.File, strings.Join(diff, "\n  "))
		}
	}
}

// ────────────────────────────── фикстура ──────────────────────────────

// Стабильные ключи фикстуры. Все 26 символов и в верхнем регистре — так их пишет конвертер, и так
// они переживают круг вербатимом (line_key нигде не ремапится: §5.3/§5.6).
const (
	rtLineShell   = "01RTLINESHELL00000000000A1"
	rtLineLining  = "01RTLINELINING0000000000A2"
	rtLineFusing  = "01RTLINEFUSING0000000000A3"
	rtLineZipper  = "01RTLINEZIPPER0000000000A4"
	rtLineSpare   = "01RTLINESPAREKITBAG00000A5"
	rtLineTote    = "01RTLINETOTEBAG000000000A6"
	rtLineLabel   = "01RTLINELABEL00000000000A7"
	rtLineThread  = "01RTLINETHREAD0000000000A8"
	rtLinePrint   = "01RTLINEPRINT00000000000A9"
	rtPieceFront  = "01RTPIECEFRONT0000000000P1"
	rtPieceBack   = "01RTPIECEBACK00000000000P2"
	rtPieceCuff   = "01RTPIECECUFF00000000000P3"
	rtSheetGraded = "01RTSHEETGRADED000000000S1"
	rtSheetUni    = "01RTSHEETUNI000000000000S2"
	// Ключи УЗЛОВ сборки. Пространство имён у узлов и деталей ЕДИНО (правило 6), поэтому они
	// нарочно не похожи ни на один line_key детали.
	rtUnitShell   = "RT-SHELL-UNIT"
	rtUnitGarment = "RT-GARMENT-UNIT"

	rtMachineProfile = "01RTPROFILEMACHINE000000M1"
	rtPressProfile   = "01RTPROFILEPRESS00000000M2"
)

// rtFixture — то, что фикстура рассказывает о себе тесту после того, как построила карточку.
type rtFixture struct {
	cardID      int
	auxID       int
	auxNumber   string
	styleNo     string
	sizeA       int
	sizeB       int
	fabricMat   int
	zipperMat   int
	mediaIDs    []int // все медиа карточки, в порядке заведения
	reusedMedia int   // единственная строка media с проставленным content_hash
}

// rtPNG рисует крошечный настоящий PNG. Байты обязаны быть НАСТОЯЩИМИ: маршрутизатор импорта
// (tcflMediaRoute) нюхает магические байты, а не расширение, и «просто мусор» уехал бы в дыру
// media_upload_failed вместо того, чтобы стать картинкой.
func rtPNG(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// rtSeedMedia заводит строку media вокруг готовых байтов: объект в «бакете», строка в базе.
//
// withHash решает, ПОВЕДЁТ ли себя импорт как переиспользование. Строка без content_hash — это
// ровно то, чем является каждая картинка, залитая до миграции 0336: она обязана не совпасть ни с
// чем и заставить импорт залить байты заново. Строка с хешем — то, что кладёт сегодняшняя заливка,
// и её импорт обязан узнать по содержимому.
func rtSeedMedia(t *testing.T, ctx context.Context, s *MYSQLStore, objs *rtObjects,
	name string, raw []byte, withHash bool) int {
	t.Helper()
	key := fmt.Sprintf("%s/%s-og.%s", rtMediaFolder, name, rtImageExt(raw))
	objs.put(key, raw)
	url := objs.url(key)
	item := &entity.MediaItem{
		FullSizeMediaURL: url, FullSizeWidth: rtMediaW, FullSizeHeight: rtMediaH,
		CompressedMediaURL: url, CompressedWidth: rtMediaW, CompressedHeight: rtMediaH,
		ThumbnailMediaURL: url, ThumbnailWidth: rtMediaW, ThumbnailHeight: rtMediaH,
	}
	if withHash {
		item.ContentHash = sql.NullString{String: rtSHA(raw), Valid: true}
	}
	id, err := s.Media().AddMedia(ctx, item)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM media WHERE id = ?", id) })
	return id
}

// rtCountMedia — сколько всего строк в media. Бонус-ассерт меряет именно это.
func rtCountMedia(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&n))
	return n
}

// rtBuildMaximalCard строит «максимальную карточку»: по строке в каждую секцию, которую формат
// обещает возить, и по значению в каждое поле, которое волна фичи добавила последней.
//
// Не «богатая для красоты»: каждая строка ниже — это ветка конвейера, которую иначе не пройдёт
// НИЧТО. Секция, которой нет в фикстуре, экспортом не собирается, импортом не пишется и в круге
// выглядит идеально сохранной.
func rtBuildMaximalCard(t *testing.T, rig *rtRig) rtFixture {
	t.Helper()
	ctx := rig.ctx
	s := rig.store
	T := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
	nb := func(v bool) sql.NullBool { return sql.NullBool{Bool: v, Valid: true} }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NewNullDecimal(decimal.RequireFromString(v))
	}
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }

	var fx rtFixture

	// ── словарь: два размера и категория-тройка ──
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&fx.sizeA))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", fx.sizeA).Scan(&fx.sizeB))

	// Тройка категорий берётся из базы, а не выдумывается: импорт разрешает категорию, ШАГАЯ ПО
	// ИМЕНАМ вниз по дереву (§4.1 №22-24), и путь из имён, которых в дереве нет, дал бы дыру
	// category_unknown вместо проверки.
	var typeCat int
	err := testDB.QueryRowContext(ctx, `
		SELECT c3.id FROM category c3
		  JOIN category c2 ON c3.parent_id = c2.id
		  JOIN category c1 ON c2.parent_id = c1.id
		 WHERE c3.level_id = 3 AND c2.level_id = 2 AND c1.level_id = 1
		 ORDER BY c3.id LIMIT 1`).Scan(&typeCat)
	require.NoError(t, err, "в словаре нет ни одной тройки категорий — путь категории проверять нечем")

	// ── каталог материалов ──
	fx.fabricMat, err = T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "RT Melton 320", Section: "fabric", Unit: ns("m"),
		Supplier: ns("RT Lanificio"), SupplierRef: ns("RT-320-BLK"),
		Composition: ns("80% wool, 20% pa"), Spec: ns("150 cm / 320 gsm"),
		FabricWidth: nd("150"), FabricWeightGsm: nd("320"),
		CuttingCoefficient: nd("1.03"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", fx.fabricMat)
	})

	fx.zipperMat, err = T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "RT Zipper 60cm", Section: "hardware", Unit: ns("pcs"),
		Supplier: ns("RT YKK"), SupplierRef: ns("RT-Z60"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", fx.zipperMat)
	})

	// ── медиа ──
	// ЧЕТЫРЕ БЕЗ content_hash И ОДНА С НИМ. Первые четыре — то, чем является каждая картинка,
	// залитая до 0336: импорт обязан не узнать их и залить байты заново, поэтому у второй карточки
	// будут ДРУГИЕ строки media. Пятая — то, что кладёт сегодняшняя заливка: её импорт обязан узнать
	// по содержимому и переиспользовать, не плодя копию.
	mTech := rtSeedMedia(t, ctx, s, rig.objs, "rt-technical", rtPNG(t, 250, 10, 10), false)
	mMood := rtSeedMedia(t, ctx, s, rig.objs, "rt-moodboard", rtPNG(t, 10, 250, 10), false)
	mCallout := rtSeedMedia(t, ctx, s, rig.objs, "rt-callout", rtPNG(t, 10, 10, 250), false)
	mOp := rtSeedMedia(t, ctx, s, rig.objs, "rt-operation", rtPNG(t, 200, 200, 10), false)
	mDetail := rtSeedMedia(t, ctx, s, rig.objs, "rt-detail", rtPNG(t, 10, 200, 200), true)
	fx.mediaIDs = []int{mTech, mMood, mCallout, mOp, mDetail}
	fx.reusedMedia = mDetail

	// ── файлы выкроек ──
	// Ключ обязан лежать под сегментом tech-card-patterns: конвертер отказывает всему остальному, и
	// это ровно тот отказ, из-за которого импорт выкроек проверять больше нечем.
	dxfGraded := []byte("0\nSECTION\n2\nENTITIES\n0\nLINE\n0\nENDSEC\n0\nEOF\nRT-GRADED\n")
	dxfUni := []byte("0\nSECTION\n2\nENTITIES\n0\nLINE\n0\nENDSEC\n0\nEOF\nRT-UNI\n")
	keyGraded := "tech-card-patterns/rt-graded.dxf"
	keyUni := "tech-card-patterns/rt-uni.dxf"
	rig.objs.put(keyGraded, dxfGraded)
	rig.objs.put(keyUni, dxfUni)

	// ── вспомогательная карточка для связи сборки ──
	fx.auxNumber = "RT-AUX-0001"
	fx.auxID, err = T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "RT Care Label", StyleNumber: ns(fx.auxNumber),
		StyleNumberSource: entity.StyleNumberSourceGenerated,
		Stage:             entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm, MeasurementUnitSet: true,
		Purpose: entity.TechCardPurposeAuxiliary, AuxSubtype: ns("care_label"),
		SizeIds: []int{fx.sizeA},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", fx.auxID)
	})

	// ── работа шага: токен берётся из словаря базы, а не пишется литералом ──
	// И БЕРЁТСЯ ПО ГЛАГОЛУ: работа принадлежит одному виду шага, и конвертер отказывает паре
	// «машинная работа на утюжке» поимённо (work_verb_mismatch).
	workFor := func(verb string) string {
		var tok string
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT token FROM operation_work WHERE verb = ? ORDER BY sort, token LIMIT 1", verb).Scan(&tok),
			"в каталоге работ нет ни одной работы вида %q", verb)
		return tok
	}
	workMachine, workPress := workFor(string(entity.OpTypeMachine)), workFor(string(entity.OpTypePress))

	// ── спецификация: по строке в каждую секцию, которую формат обещает возить ──
	bom := []entity.TechCardBomItem{
		// Рулонные секции: у них есть НАЗНАЧЕНИЕ и направление, но нет счётных количеств.
		{
			LineKey: rtLineShell, Section: entity.BomSectionFabric, Name: "Основная ткань",
			MaterialId: sql.NullInt64{Int64: int64(fx.fabricMat), Valid: true},
			Purpose:    ns(string(entity.BomPurposeMain)), Unit: ns("m"),
			Supplier: ns("RT Lanificio"), SupplierRef: ns("RT-320-BLK"),
			Composition: ns("80% wool, 20% pa"), Spec: ns("150 cm / 320 gsm"), Color: ns("black"),
			FabricWidth: nd("150"), FabricWeightGsm: nd("320"),
			FabricDirection: ns(string(entity.FabricDirectionAny)),
			WastagePercent:  nd("4.5"), WastageSource: string(entity.BomWastageSourceManual),
			Comment: ns("плотная шерсть"),
		},
		{
			LineKey: rtLineLining, Section: entity.BomSectionLining, Name: "Подкладка",
			Purpose: ns(string(entity.BomPurposeLining)), Unit: ns("m"),
			FabricDirection: ns(string(entity.FabricDirectionOneWay)),
			WastageSource:   string(entity.BomWastageSourceManual),
		},
		{
			LineKey: rtLineFusing, Section: entity.BomSectionInterlining, Name: "Клеевая",
			Purpose: ns(string(entity.BomPurposeInterfacing)), Unit: ns("m"),
			FabricDirection: ns(string(entity.FabricDirectionAny)),
			WastageSource:   string(entity.BomWastageSourceManual),
		},
		// Счётные секции: количества 0333 законны ТОЛЬКО здесь.
		{
			LineKey: rtLineZipper, Section: entity.BomSectionHardware, Name: "Молния 60 см",
			MaterialId: sql.NullInt64{Int64: int64(fx.zipperMat), Valid: true},
			Kind:       ns(string(entity.BomKindZipper)), Unit: ns("pcs"),
			QtyPerGarment: nd("1"), SpareQty: nd("0.5"), IsSample: true,
			WastageSource: string(entity.BomWastageSourceManual),
		},
		{
			LineKey: rtLineSpare, Section: entity.BomSectionPackaging, Name: "Мешочек запаски",
			Kind: ns(string(entity.BomKindSpareKitBag)), Unit: ns("pcs"),
			QtyPerGarment: nd("1"), WastageSource: string(entity.BomWastageSourceManual),
		},
		{
			LineKey: rtLineTote, Section: entity.BomSectionPackaging, Name: "Шоппер",
			Kind: ns(string(entity.BomKindToteBag)), Unit: ns("pcs"),
			QtyPerGarment: nd("1"), SpareQty: nd("0"), WastageSource: string(entity.BomWastageSourceManual),
		},
		{
			LineKey: rtLinePrint, Section: entity.BomSectionDecoration, Name: "Принт спина",
			Kind: ns(string(entity.BomKindPrint)), Unit: ns("pcs"),
			QtyPerGarment: nd("1"), WastageSource: string(entity.BomWastageSourceManual),
		},
		// Секция label счётных количеств не берёт и вида не берёт — она в списке ровно затем, чтобы
		// это состояние тоже проехало круг.
		{
			LineKey: rtLineLabel, Section: entity.BomSectionLabel, Name: "Основной лейбл",
			Unit: ns("pcs"), WastageSource: string(entity.BomWastageSourceManual),
		},
		{
			LineKey: rtLineThread, Section: entity.BomSectionThread, Name: "Нить 40/2",
			Kind: ns(string(entity.BomKindSewingThread)), Unit: ns("m"),
			WastageSource: string(entity.BomWastageSourceManual),
		},
	}

	pieces := []entity.TechCardPiece{
		{
			LineKey: rtPieceFront, Name: "перед", PiecesPerGarment: 2, Grainline: "lengthwise",
			CutSymmetry: ns(string(entity.PieceCutSymmetryMirrored)),
			Fused:       true, FusingMode: ns(string(entity.PieceFusingModeStrip)), FusingWidthMm: nd("12"),
			CalloutNumber: ni(1), Note: ns("клеевая полосой по борту"),
		},
		{
			LineKey: rtPieceBack, Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise",
			CutSymmetry: ns(string(entity.PieceCutSymmetryFold)),
			Fused:       true, FusingMode: ns(string(entity.PieceFusingModeFull)),
		},
		{
			// UNI: деталь без градации. Это ЗАЯВЛЕНИЕ конструктора (0302), а не вывод из чертежа, и
			// у неё не бывает по-размерных площадей.
			LineKey: rtPieceCuff, Name: "манжета", PiecesPerGarment: 2, Grainline: "crosswise",
			CutSymmetry: ns(string(entity.PieceCutSymmetryIdentical)), Ungraded: true,
		},
	}

	patterns := []entity.TechCardSizePattern{
		{
			LineKey: rtSheetGraded, SizeId: fx.sizeA, URL: rig.objs.url(keyGraded),
			Filename: ns("rt-graded.dxf"), Name: ns("основная, размер A"),
			SizeBytes: sql.NullInt64{Int64: int64(len(dxfGraded)), Valid: true}, Version: 3,
			FabricPurpose: ns(string(entity.BomPurposeMain)),
		},
		{
			// БЕЗ РАЗМЕРА (0284): лист градуирован внутри самого DXF. Легальное и обычное состояние,
			// и именно оно ломалось бы, если бы формат возил размер числом.
			LineKey: rtSheetUni, SizeId: 0, URL: rig.objs.url(keyUni),
			Filename: ns("rt-uni.dxf"), Name: ns("основная, без размера"),
			SizeBytes: sql.NullInt64{Int64: int64(len(dxfUni)), Valid: true}, Version: 1,
			FabricPurpose: ns(string(entity.BomPurposeMain)),
		},
	}

	operations := []entity.TechCardOperation{
		{
			OperationNumber: ni(10), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
			SMV: nd("1.4"), Note: ns("стачать боковые"), Work: ns(workMachine),
			StitchesPerCm: nd("4.5"), SeamClass: ns(string(entity.SeamClassBound)),
			SeamAllowanceMm: nd("10"),
			TopstitchMode:   ns(string(entity.TopstitchEdge)), TopstitchWidthMm: nd("2"), TopstitchRows: ni(2),
			AttachmentKind: ns("edge_guide"), AttachmentSizeMm: nd("6"),
			MachineType: ns("lockstitch"), MachineProfileKey: ns(rtMachineProfile),
			ThreadCount: ni(2), NeedleType: ns("universal"), NeedleSizeNm: ni(90),
			ThreadTension: ns("normal"), ThreadTensionNote: ns("верхняя 3.5"), StitchWidthMm: nd("0"),
			CalloutNumber: ni(1),
			PieceLineKeys: []string{rtPieceFront, rtPieceBack},
			BomLineKeys:   []string{rtLineShell, rtLineThread},
			// Джойну нужно ДВА РАЗЛИЧНЫХ входа: узел из одного входа — это обработка, а не узел
			// (правило 3 сборочного графа). Заполняется AssemblyInputs, а НЕ InputKeys: последний
			// — сырой провод, который стор не читает вовсе (канонический список строит конвертер).
			AssemblyInputs: []entity.OperationInput{
				{Kind: entity.AssemblyInputPiece, Key: rtPieceFront},
				{Kind: entity.AssemblyInputPiece, Key: rtPieceBack},
			},
			OutputUnitKey: ns(rtUnitShell), OutputUnitName: ns("корпус"),
			Media: []entity.TechCardOperationMedia{{
				MediaId: mOp, Caption: ns("узел борта"), DisplayOrder: 0,
			}},
		},
		{
			// ВТО: пресс с обеими осями 0325. press_toward законен ТОЛЬКО при press_action=to_one_side
			// и там же обязателен — пара, а не два независимых поля.
			OperationNumber: ni(20), OperationType: entity.OpTypePress, Zone: entity.ZoneOuter,
			SMV: nd("0.6"), Note: ns("разутюжить шов"), Work: ns(workPress),
			PressEquipment: ns("iron"), PressProfileKey: ns(rtPressProfile),
			PressTemperatureC: ni(150), PressDwellSec: ni(8), PressPressureNCm2: nd("20"),
			PressSteam: nb(true), PressCloth: ns("press_cloth"),
			PressAction: ns("to_one_side"), PressToward: ns("away_from_center"),
			// ОБРАБОТКА: шаг берёт узел со стола и НИЧЕГО не производит, поэтому узел остаётся
			// доступен следующим шагам.
			AssemblyInputs: []entity.OperationInput{{Kind: entity.AssemblyInputUnit, Key: rtUnitShell}},
		},
		{
			// Глагол волны 0324 со своим дискриминатором.
			OperationNumber: ni(30), OperationType: entity.OpTypeHardwareSet, Zone: entity.ZoneOuter,
			SMV: nd("0.9"), Note: ns("установить молнию"),
			AttachMethod: ns("threaded"), HolePrep: ns("punch"), Reinforcement: ns("patch"),
			PlacementCount: ni(1),
			BomLineKeys:    []string{rtLineZipper},
			BomQuantities:  []entity.OperationBomQty{{LineKey: rtLineZipper, QtyPerGarment: d("1")}},
			AssemblyInputs: []entity.OperationInput{
				{Kind: entity.AssemblyInputUnit, Key: rtUnitShell},
				{Kind: entity.AssemblyInputPiece, Key: rtPieceCuff},
			},
			OutputUnitKey: ns(rtUnitGarment), OutputUnitName: ns("изделие"),
		},
		{
			OperationNumber: ni(40), OperationType: entity.OpTypePrint, Zone: entity.ZoneOuter,
			SMV: nd("0.5"), Note: ns("нанести принт"),
			PrintMethod: ns("heat_transfer"), PeelMode: ns("warm"), SecondPressSec: ni(8),
			BomLineKeys: []string{rtLinePrint},
		},
	}

	card := &entity.TechCardInsert{
		Name: "RT Maximal Jacket", StyleNumber: ns("RT-MAX-0001"),
		StyleNumberSource: entity.StyleNumberSourceGenerated,
		// Автор архива и импортёр — РАЗНЫЕ люди: без этого нечем показать, что новая карточка
		// подписывается тем, кто её импортировал, а не тем, кто её нарисовал (§4.1 №10/11).
		CreatedBy: rtActor, UpdatedBy: rtActor,
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		Purpose:         entity.TechCardPurposeSellable,
		MeasurementUnit: entity.TechCardUnitMm, MeasurementUnitSet: true,
		Brand:      ns("b"),
		SeasonCode: ns("SS"), SeasonYear: ni(2026), Collection: ns("RT capsule"),
		CategoryId: ni(int32(typeCat)), TargetGender: ns("unisex"),
		Fit: ns("regular"), Composition: ns("80% wool, 20% pa"),
		CareInstructions:   ns("30,do_not_bleach"),
		ModelWearsHeightCm: ni(183), ModelWearsSizeId: ni(int32(fx.sizeA)),
		Concept: ns("тёплый прямой жакет"), Notes: ns("прототип круга"),
		RequiredSeamAllowanceMm: nd("10"),
		BaseSampleSizeId:        ni(int32(fx.sizeA)),
		SizeIds:                 []int{fx.sizeA, fx.sizeB},
		Media: []entity.TechCardMediaItem{
			{MediaId: mTech, Category: entity.TechCardMediaCategoryTechnical,
				Kind: entity.TechCardMediaFront, Caption: ns("технический перед")},
			{MediaId: mMood, Category: entity.TechCardMediaCategoryMoodboard,
				Kind: entity.TechCardMediaMoodboard, Caption: ns("настроение")},
		},
		Callouts: []entity.TechCardCallout{
			{
				Number: 1, Part: ns("борт"), Description: ns("ширина подборта"),
				Dimensions: ns("60 мм"), MediaId: ni(int32(mCallout)),
				PosX: nd("0.25"), PosY: nd("0.4"),
				Kind: entity.AnnotationKindDim, Color: entity.AnnotationColorRed, Dashed: true,
				Points: []entity.TechCardAnnotationPoint{
					{X: d("0.2"), Y: d("0.3")}, {X: d("0.5"), Y: d("0.55")},
				},
				Parts: []string{rtPieceFront},
			},
			{
				Number: 2, Part: ns("спинка"), Description: ns("зона принта"),
				PosX: nd("0.6"), PosY: nd("0.6"),
				Kind: entity.AnnotationKindPolygon, Color: entity.AnnotationColorBlue, Filled: true,
				Points: []entity.TechCardAnnotationPoint{
					{X: d("0.5"), Y: d("0.5")}, {X: d("0.8"), Y: d("0.5")}, {X: d("0.65"), Y: d("0.8")},
				},
			},
		},
		Details: []entity.TechCardDetail{
			{Key: ns("closure"), Text: ns("центральная молния"), MediaIds: []int{mDetail}},
		},
		BomItems: bom,
		Pieces:   pieces,
		Patterns: patterns,
		PieceDxfAliases: []entity.TechCardPieceDxfAlias{
			{FabricPurpose: string(entity.BomPurposeMain), BlockName: "FRONT", PieceLineKey: rtPieceFront},
			{FabricPurpose: string(entity.BomPurposeMain), BlockName: "BACK", PieceLineKey: rtPieceBack},
			{FabricPurpose: string(entity.BomPurposeMain), BlockName: "CUFF", PieceLineKey: rtPieceCuff},
		},
		PieceDxfAliasesSet: true,
		Operations:         operations,
		MachineFieldsAware: true, AssemblyAware: true, BomQtyAware: true,
		Construction: &entity.TechCardConstruction{
			HemFinish: ns("подгибка 3 см"), Notes: ns("все швы обмётаны"),
			DefaultSeamClass: ns("ss_plain"), DefaultStitchesPerCm: nd("4"),
			EquipmentDefaults: &entity.TechCardEquipmentDefaults{
				Machines: []entity.TechCardMachineProfile{{
					ProfileKey: rtMachineProfile, Label: ns("Челнок 1"), MachineType: "lockstitch",
					ThreadCount: ni(2), NeedleType: ns("universal"), NeedleSizeNm: ni(90),
					BedType: ns("flatbed"), Automation: ns("basic"),
					ThreadTension: ns("normal"), ThreadTensionNote: ns("верхняя 3.5"),
					AttachmentKind: ns("edge_guide"),
					StitchesPerCm:  nd("4.5"), StitchWidthMm: nd("0"), Note: ns("основная машина"),
				}},
				Presses: []entity.TechCardPressProfile{{
					ProfileKey: rtPressProfile, Label: ns("Утюг 1"), PressEquipment: "iron",
					PressTemperatureC: ni(150), PressDwellSec: ni(8), PressPressureNCm2: nd("20"),
					PressSteam: nb(true), PressCloth: ns("press_cloth"), Note: ns("через проутюжильник"),
				}},
			},
		},
		Labels: []entity.TechCardLabel{
			{LabelType: entity.LabelTypeMain, Content: ns("GRBPWR"), Placement: ns("centre back neck"),
				Attachment: ns("вшивной"), Size: ns("30x15"), Note: ns("артворк A-14")},
			{LabelType: entity.LabelTypeCare, Content: ns("care"), Placement: ns("left side seam")},
		},
		Packaging: &entity.TechCardPackaging{
			FoldingMethod: ns("втрое"), Polybag: ns("60x40"), BagSticker: ns("наклейка размера"),
			Inserts: ns("вкладыш"), UnitsPerBox: ni(10), BoxMarking: ns("RT"),
			BoxDimensions: ns("60x40x30"), WeightNetGrams: ni(900), WeightGrossGrams: ni(1100),
			Notes: ns("не прессовать"),
		},
		Issues: []entity.TechCardIssue{
			{OperationNumber: ni(10), CalloutNumber: ni(1), RaisedBy: ns(rtActor),
				Severity: entity.IssueSeverityMedium, Status: entity.IssueStatusOpen,
				Description: "подборт тянет"},
		},
		SizeQuantities: []entity.TechCardSizeQuantity{
			{SizeId: fx.sizeA, OrderQty: 40}, {SizeId: fx.sizeB, OrderQty: 60},
		},
	}

	fx.cardID, err = T.AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", fx.cardID)
	})
	fx.styleNo = "RT-MAX-0001"

	// ── размерная таблица: обе оси именами (§5.1) ──
	var meas1, meas2 int
	rows, err := testDB.QueryContext(ctx, "SELECT id FROM measurement_name ORDER BY id LIMIT 2")
	require.NoError(t, err)
	got := []int{}
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		got = append(got, id)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	require.Len(t, got, 2, "в словаре меньше двух мерок — размерную таблицу строить не из чего")
	meas1, meas2 = got[0], got[1]

	_, err = T.UpdateStyleSizeChart(ctx, fx.cardID, 0,
		[]entity.StyleSizeChartCell{
			{SizeID: fx.sizeA, MeasurementNameID: meas1, Value: d("50")},
			{SizeID: fx.sizeB, MeasurementNameID: meas1, Value: d("52")},
			{SizeID: fx.sizeA, MeasurementNameID: meas2, Value: d("64")},
			{SizeID: fx.sizeB, MeasurementNameID: meas2, Value: d("66")},
		},
		fx.sizeB,
		[]entity.StyleSizeChartGradeStep{
			{MeasurementNameID: meas1, Step: d("2")},
			{MeasurementNameID: meas2, Step: d("2")},
		})
	require.NoError(t, err)

	// ── связь сборки: компонент едет НОМЕРОМ СТИЛЯ, не id (§5.2) ──
	require.NoError(t, T.UpsertStyleAssembly(ctx, fx.cardID, []entity.StyleAssemblyInsert{{
		ComponentTechCardId: fx.auxID,
		SizeId:              sql.NullInt32{}, // null = на все размеры
		Qty:                 d("1"),
		PrintNote:           ns("состав + страна"),
		PositionNote:        ns("левый боковой шов"),
		Active:              true,
	}}, rtActor))

	// ── раскладка: единственная запись архива, которая едет сырым protojson ──
	layout := &pb_common.TechCardMarkerLayout{
		SchemaVersion: 1,
		Pieces: []*pb_common.TechCardMarkerPiece{{
			PieceId: 1, Name: "FRONT", Source: "rt-graded.dxf", Quantity: 1,
			Poly: []*pb_common.TechCardMarkerPoint{
				{XCm: 0, YCm: 0}, {XCm: 30, YCm: 0}, {XCm: 30, YCm: 70}, {XCm: 0, YCm: 70},
			},
			BboxWCm: 30, BboxHCm: 70, AreaCm2: 2100,
			PieceLineKey: rtPieceFront, BlockName: "FRONT",
		}},
		Placements: []*pb_common.TechCardMarkerPlacement{
			{PieceId: 1, Instance: 0, RotDeg: 0, XCm: 1, YCm: 1},
		},
	}
	facts, err := dto.MarkerLayoutFactsFromPb(layout)
	require.NoError(t, err)
	blob, err := protojson.Marshal(layout)
	require.NoError(t, err)

	mk := entity.TechCardMarkerInsert{
		Name: "RT · основная 150", Source: entity.MarkerSourceManual,
		BomLineKey:    rtLineShell,
		FabricWidthCm: d("150"), GapCm: d("0.5"), EdgeMarginCm: d("1"), SelvedgeCm: d("1.5"),
		UsedLengthCm:  d("512.4"),
		EfficiencyPct: nd("73.5"),
		PlacedCount:   1, TotalCount: 1,
		SeamAllowanceMm: nd("10"), ContourAllowanceMm: nd("0"),
		ContourLayer: ns("1"), GrainLayer: ns("7"), AllowFlip: nb(false),
		Layout: string(blob), LayoutFacts: facts,
	}
	mk.SizeId = sql.NullInt64{Int64: int64(fx.sizeA), Valid: true}
	mk.Sets = sql.NullInt64{Int64: 1, Valid: true}
	mk.Composition = []entity.MarkerCompositionEntry{{SizeId: fx.sizeA, Quantity: 1}}
	_, err = T.SaveMarker(ctx, fx.cardID, 0, mk, rtActor)
	require.NoError(t, err)

	// ── измеренные площади деталей (§4.1 «measured piece areas») ──
	// Комплект обязан быть ПОЛНЫМ: ровно те детали, у которых в этом скоупе есть привязка блока
	// чертежа. У UNI-детали по-размерных строк не бывает — только безразмерная.
	_, err = T.SaveTechCardPieceAreas(ctx, entity.PieceAreaWrite{
		TechCardId:    fx.cardID,
		ScopeKey:      string(entity.BomPurposeMain),
		SheetLineKeys: []string{rtSheetGraded, rtSheetUni},
		ParsedBy:      rtActor,
		Rows: []entity.PieceAreaInput{
			{PieceLineKey: rtPieceFront, SizeId: sql.NullInt64{Int64: int64(fx.sizeA), Valid: true},
				AreaCm2: d("2100"), PerimeterCm: nd("200"), ContourLayer: "1", SeamAllowanceMm: d("10")},
			{PieceLineKey: rtPieceFront, SizeId: sql.NullInt64{Int64: int64(fx.sizeB), Valid: true},
				AreaCm2: d("2250"), PerimeterCm: nd("208"), ContourLayer: "1", SeamAllowanceMm: d("10")},
			{PieceLineKey: rtPieceBack, SizeId: sql.NullInt64{Int64: int64(fx.sizeA), Valid: true},
				AreaCm2: d("2400"), PerimeterCm: nd("214"), ContourLayer: "1", SeamAllowanceMm: d("10")},
			{PieceLineKey: rtPieceBack, SizeId: sql.NullInt64{Int64: int64(fx.sizeB), Valid: true},
				AreaCm2: d("2560"), PerimeterCm: nd("221"), ContourLayer: "1", SeamAllowanceMm: d("10")},
			{PieceLineKey: rtPieceCuff, AreaCm2: d("180"), PerimeterCm: nd("60"),
				ContourLayer: "1", SeamAllowanceMm: d("10")},
		},
	})
	require.NoError(t, err)

	return fx
}

// ────────────────────────────── сам круг ──────────────────────────────

// TestTechCardArchiveRoundtrip — ГЛАВНЫЙ ГЕЙТ ФИЧИ.
//
// SAFE ONLY against a local container DSN — см. mysql_test.go и память проекта
// (store-tests-drop-prod-db: не-CI TestMain говорит с боевой базой и ДРОПАЕТ таблицы).
func TestTechCardArchiveRoundtrip(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	rig := newRTRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)

	// ── ЭКСПОРТ A ──
	zipA, respA := rig.export(t, fx.cardID)
	archA, err := techcardarchive.OpenArchive(bytes.NewReader(zipA), int64(len(zipA)))
	require.NoError(t, err, "архив, который не открывается нашей же читалкой, — это провал экспорта")
	require.Equal(t, techcardarchive.MoneyPolicyStrippedV1, archA.Manifest.MoneyPolicy)
	require.Equal(t, fx.styleNo, archA.Manifest.Source.StyleNumber)
	require.Empty(t, archA.Manifest.ExportHoles,
		"экспорт максимальной карточки не должен терять НИЧЕГО: %+v", archA.Manifest.ExportHoles)
	require.NotEmpty(t, respA.GetManifest().GetCounters(),
		"ответ RPC обязан нести счётчики архива — паспорт без них не паспорт")

	mediaBefore := rtCountMedia(t, ctx)

	// ── ИМПОРТ В ТУ ЖЕ БАЗУ ──
	cardB, reportB := rig.importArchive(t, zipA)
	require.NotEqual(t, fx.cardID, cardB, "импорт обязан создать НОВУЮ карточку")

	// ── ветка перенумерации: артикул занят собственной карточкой-источником ──
	require.NotEqual(t, fx.styleNo, reportB.GetStyleNumber(),
		"артикул исходной карточки занят ЕЮ ЖЕ; импорт обязан перенумеровать, а не отказать")
	require.NotEmpty(t, reportB.GetStyleNumber(),
		"сезон у карточки есть, значит кандидат обязан найтись, и карточка НЕ должна остаться без номера")
	require.Equal(t, string(entity.TechCardStageProto), reportB.GetStage(),
		"перенумерованная кандидатом карточка остаётся на своей стадии; `idea` — это только исчерпание")
	rtRequireReportLine(t, reportB, techcardarchive.EntityCard, "style_number=")

	// ── ЭКСПОРТ B ──
	zipB, _ := rig.export(t, cardB)
	archB, err := techcardarchive.OpenArchive(bytes.NewReader(zipB), int64(len(zipB)))
	require.NoError(t, err)
	require.Empty(t, archB.Manifest.ExportHoles,
		"вторая карточка тоже обязана экспортироваться без единой дыры: %+v", archB.Manifest.ExportHoles)

	// ── СРАВНЕНИЕ ──
	require.Equal(t, archA.Manifest.Contents, archB.Manifest.Contents,
		"паспорт архива считает медиа, выкройки, раскладки и материалы — все четыре числа обязаны совпасть")

	idxA, idxB := rtMediaIndex(t, archA), rtMediaIndex(t, archB)
	canonA := rtMediaCanon(t, idxA, "A")
	canonB := rtMediaCanon(t, idxB, "B")

	cardJSONA, err := archA.CardJSON()
	require.NoError(t, err)
	cardJSONB, err := archB.CardJSON()
	require.NoError(t, err)

	// Стороннее наблюдение, которое обязано быть ВЕРНЫМ ДО нормализации: автор и импортёр — разные
	// люди, и новая карточка подписана ИМПОРТЁРОМ (§4.1 №10/11), а не автором архива.
	require.Equal(t, rtActor, cardJSONA.GetCreatedBy())
	require.Equal(t, rtImporter, cardJSONB.GetCreatedBy(),
		"импортированная карточка обязана быть подписана тем, кто её импортировал, а не автором архива")

	// ДВА ПОСЛЕДСТВИЯ НОРМАЛИЗАЦИИ, КОТОРЫЕ ОБЯЗАНЫ БЫТЬ ПРОВЕРЕНЫ, А НЕ ПРОСТО ВЫВЕДЕНЫ ИЗ-ПОД
	// СРАВНЕНИЯ. Гашение поля — это утверждение о поле; утверждение без проверки — это поблажка.
	//
	// (1) piece_ids/bom_item_ids гасятся потому, что их КЛЮЧЕВЫЕ двойники едут и сравниваются. Если
	//     ключей в архиве нет, гашение прячет ровно ту потерю, ради которой тест написан.
	rtRequireOperationKeysTravel(t, cardJSONA, "A")
	rtRequireOperationKeysTravel(t, cardJSONB, "B")
	// (2) stale гасится потому, что у импортированного скоупа он ЗАЛОЖЕННО другой. Проверяется, что
	//     он именно такой: у источника — «замер актуален», у импортированной — «файлы с тех пор
	//     другие» (шапка insertImportedPieceAreas).
	require.NotEmpty(t, cardJSONA.GetPieceAreaScopes(), "фикстура обязана нести замеренные площади")
	for _, sc := range cardJSONA.GetPieceAreaScopes() {
		require.False(t, sc.GetStale(), "скоуп %q исходной карточки мерили по её же сегодняшним листам", sc.GetScopeKey())
	}
	for _, sc := range cardJSONB.GetPieceAreaScopes() {
		require.True(t, sc.GetStale(), "скоуп %q импортированной карточки обязан читаться «файлы с тех пор другие»: "+
			"листы здесь свежезалиты, и честного совпадения отпечатков быть не может", sc.GetScopeKey())
	}

	rtRequireProtoEqual(t,
		rtNormalizeCard(t, cardJSONA, canonA, "A"),
		rtNormalizeCard(t, cardJSONB, canonB, "B"))

	rtCompareSidecars(t, archA, archB, canonA, canonB)

	// ── БОНУС 1: строки медиа у двух карточек РАЗНЫЕ там, где содержимое базе не было известно ──
	slotsA := rtCardMediaIDs(cardJSONA)
	slotsB := rtCardMediaIDs(cardJSONB)
	require.Equal(t, len(slotsA), len(slotsB))
	require.Contains(t, slotsA, int32(fx.reusedMedia))
	require.Contains(t, slotsB, int32(fx.reusedMedia),
		"единственная строка media с проставленным content_hash обязана быть УЗНАНА по содержимому "+
			"и переиспользована, а не залита второй копией")
	for _, id := range fx.mediaIDs {
		if id == fx.reusedMedia {
			continue
		}
		require.NotContains(t, slotsB, int32(id),
			"строка media без content_hash (какими являются все картинки до миграции 0336) "+
				"не может совпасть ни с чем: импорт обязан залить байты заново и завести НОВУЮ строку")
	}
	mediaAfterFirst := rtCountMedia(t, ctx)
	require.Equal(t, mediaBefore+len(fx.mediaIDs)-1, mediaAfterFirst,
		"первый импорт заводит по строке на каждую картинку, содержимого которой база не знала")

	// ── БОНУС 2: ПОВТОРНЫЙ импорт того же архива не плодит строк медиа ──
	cardC, reportC := rig.importArchive(t, zipA)
	require.NotEqual(t, cardB, cardC)
	require.NotEqual(t, reportB.GetStyleNumber(), reportC.GetStyleNumber(),
		"третья карточка не может получить номер второй")
	require.Equal(t, mediaAfterFirst, rtCountMedia(t, ctx),
		"переиспользование по content_hash: второй импорт того же архива не имеет права завести "+
			"ни одной новой строки media — байты уже лежат в базе, и их sha равна sha архива")

	// ── ВТОРАЯ ветка перенумерации: кандидата нет вовсе ──
	//
	// У вспомогательной карточки нет сезона, а значит машине не из чего предложить замену. Контракт
	// говорит, что карточка всё равно приземляется — БЕЗ НОМЕРА и на стадии `idea`, потому что
	// артикул обязателен начиная с `proto` и карточка без него на любой более поздней стадии
	// незаписуема. Терять целый импорт из-за имени, которое человек впишет за пять секунд, нельзя.
	t.Run("артикул нечем заменить: карточка без номера на стадии идеи", func(t *testing.T) {
		zipAux, _ := rig.export(t, fx.auxID)
		auxCopy, repAux := rig.importArchive(t, zipAux)

		require.Empty(t, repAux.GetStyleNumber(),
			"сезона нет — предложить нечего, и карточка обязана приземлиться БЕЗ номера")
		require.Equal(t, string(entity.TechCardStageIdea), repAux.GetStage(),
			"безномерная карточка законна только на стадии `idea`")
		rtRequireReportReason(t, repAux, techcardarchive.EntityCard,
			"style_number="+fx.auxNumber, string(techcardarchive.ReasonStyleNumberTaken))

		var number sql.NullString
		var stage string
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT style_number, stage FROM tech_card WHERE id = ?", auxCopy).Scan(&number, &stage))
		require.False(t, number.Valid, "в базе тоже NULL, а не пустая строка")
		require.Equal(t, string(entity.TechCardStageIdea), stage)
	})
}

// rtRequireReportReason требует строку отчёта с конкретной причиной — код, а не прозу: проза
// контракта не несёт и переписывается, а по коду клиент ветвится.
func rtRequireReportReason(t *testing.T, rep *pb_admin.TechCardImportReport, entityName, ref, reason string) {
	t.Helper()
	for _, l := range rep.GetLines() {
		if l.GetEntity() == entityName && l.GetRef() == ref && l.GetReason() == reason {
			return
		}
	}
	t.Fatalf("в отчёте нет строки %s/%s с причиной %q; строки: %+v", entityName, ref, reason, rep.GetLines())
}

// rtRequireOperationKeysTravel проверяет, что связи шага едут КЛЮЧАМИ, а не только разрешёнными
// номерами строк. Это условие, при котором законно гасить piece_ids/bom_item_ids.
func rtRequireOperationKeysTravel(t *testing.T, card *pb_common.TechCard, what string) {
	t.Helper()
	pieces, boms := 0, 0
	for _, op := range card.GetTechCard().GetOperations() {
		pieces += len(op.GetPieceLineKeys())
		boms += len(op.GetBomLineKeys())
		require.Len(t, op.GetPieceLineKeys(), len(op.GetPieceIds()),
			"%s: у шага %d число ключей деталей разошлось с числом разрешённых id", what, op.GetOperationNumber())
		require.Len(t, op.GetBomLineKeys(), len(op.GetBomItemIds()),
			"%s: у шага %d число ключей BOM разошлось с числом разрешённых id", what, op.GetOperationNumber())
	}
	require.NotZero(t, pieces, "%s: ни один шаг не назвал детали ключами — гасить piece_ids было бы нельзя", what)
	require.NotZero(t, boms, "%s: ни один шаг не назвал строки BOM ключами — гасить bom_item_ids было бы нельзя", what)
}

// rtCardMediaIDs — все media_id, на которые ссылается карточка, из ЗАПИСЫВАЕМОЙ половины.
func rtCardMediaIDs(card *pb_common.TechCard) []int32 {
	seen := map[int32]bool{}
	out := []int32{}
	add := func(id int32) {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	ins := card.GetTechCard()
	for _, m := range ins.GetMoodboardMedia() {
		add(m.GetMediaId())
	}
	for _, m := range ins.GetTechnicalMedia() {
		add(m.GetMediaId())
	}
	for _, c := range ins.GetCallouts() {
		add(c.GetMediaId())
	}
	for _, d := range ins.GetDetails() {
		for _, id := range d.GetMediaIds() {
			add(id)
		}
	}
	for _, op := range ins.GetOperations() {
		for _, m := range op.GetMedia() {
			add(m.GetMediaId())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// rtRequireReportLine требует строку отчёта про конкретную сущность и ссылку.
func rtRequireReportLine(t *testing.T, rep *pb_admin.TechCardImportReport, entityName, refPrefix string) {
	t.Helper()
	for _, l := range rep.GetLines() {
		if l.GetEntity() == entityName && strings.HasPrefix(l.GetRef(), refPrefix) {
			return
		}
	}
	t.Fatalf("в отчёте нет строки %s/%s* — потеря без строки в отчёте это и есть тихая потеря; строки: %+v",
		entityName, refPrefix, rep.GetLines())
}
