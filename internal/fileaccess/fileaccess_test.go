package fileaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
)

const testPepper = "test-pepper-for-library-file-links"

// serveFile ходит через тот же монтаж, что и http.go, — иначе chi.URLParam не увидит токен, и
// тест доказывал бы работу функции, а не маршрута. HEAD смонтирован вместе с GET по той же
// причине, по которой он смонтирован в бою: отказ по методу обязан быть неотличим от отказа по
// токену.
func serveFile(svc *Service, method, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/api/f/{token}", svc.Handler())
	r.Method(http.MethodHead, "/api/f/{token}", svc.Handler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

// fakeFiles — узкий фейк вместо мока всей библиотеки: интерфейс Files ровно за этим и узкий.
type fakeFiles struct {
	rows     map[int]*entity.LibraryFileLinkTarget
	recorded map[int]int64
	lookErr  error
}

func (f *fakeFiles) GetFileByPublicLink(_ context.Context, fileID int) (*entity.LibraryFileLinkTarget, error) {
	if f.lookErr != nil {
		return nil, f.lookErr
	}
	if row, ok := f.rows[fileID]; ok {
		return row, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeFiles) RecordPublicAccess(_ context.Context, counts map[int]int64, _ map[int]time.Time) error {
	if f.recorded == nil {
		f.recorded = map[int]int64{}
	}
	for id, n := range counts {
		f.recorded[id] += n
	}
	return nil
}

// fakePresign записывает, ЧТО именно ему велели подписать: ключ и режим вложения — это и есть
// два решения, которые маршрут принимает за пределами «пустить или нет».
type fakePresign struct {
	key      string
	download bool
	calls    int
	err      error
}

func (p *fakePresign) PresignLibraryObject(_ context.Context, objectKey string, download bool, _ string) (string, time.Time, error) {
	p.calls++
	p.key = objectKey
	p.download = download
	if p.err != nil {
		return "", time.Time{}, p.err
	}
	return "https://bucket.example/" + objectKey + "?signed=1", time.Now().Add(6 * time.Hour), nil
}

func linkTarget(id int, epoch int) *entity.LibraryFileLinkTarget {
	return &entity.LibraryFileLinkTarget{
		FileId:      id,
		FileName:    "макет.pdf",
		ContentType: "application/pdf",
		SizeBytes:   1024,
		ObjectKey:   "files-library/object-" + strings.Repeat("a", 4) + ".pdf",
		AccessLevel: entity.LibraryFileAccessLink,
		Epoch:       epoch,
	}
}

func newTestService(t *testing.T, files *fakeFiles, presign *fakePresign) *Service {
	t.Helper()
	svc, err := New(files, presign, testPepper, "https://backend.example/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

// TestPublicLinkFiveRefusals — ПЯТЬ ОТКАЗОВ ПУБЛИЧНОГО МАРШРУТА в одном месте, потому что
// доказывать надо не каждый по отдельности, а их ОДИНАКОВОСТЬ: снаружи «файла нет», «ссылку
// пересоздали», «уровень вернули в team», «срок вышел» и «токен выдуман» обязаны быть
// неразличимы. Различимость любого из них превращает перебор в способ узнать, что файл есть.
func TestPublicLinkFiveRefusals(t *testing.T) {
	files := &fakeFiles{rows: map[int]*entity.LibraryFileLinkTarget{7: linkTarget(7, 3)}}
	presign := &fakePresign{}
	svc := newTestService(t, files, presign)

	live := svc.minter.Mint(patterntoken.ScopeFile, 7, 3)

	// 1. ЖИВОЙ ТОКЕН — единственный положительный исход, и он 302 на подписанный url.
	w := serveFile(svc, http.MethodGet, "/api/f/"+live)
	if w.Code != http.StatusFound {
		t.Fatalf("live token: want 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "signed=1") {
		t.Fatalf("live token: want a presigned location, got %q", loc)
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("a tokenized resolution must not be cacheable by shared caches, got %q",
			w.Header().Get("Cache-Control"))
	}

	// 2. ПОСЛЕ ПЕРЕСОЗДАНИЯ. Поколение строки уехало вперёд — токен, выданный на прежнем,
	//    обязан умереть НЕМЕДЛЕННО, без списка отзыва и без ожидания срока.
	files.rows[7].Epoch = 4
	assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/"+live), "rotated")
	// Токен НОВОГО поколения при этом работает — отзыв убил ссылку, а не файл.
	if w := serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 4)); w.Code != http.StatusFound {
		t.Fatalf("token of the new epoch: want 302, got %d", w.Code)
	}

	// 3. ПОСЛЕ СМЕНЫ УРОВНЯ НА team. Строка доступа переживает уровень и поколение совпадает —
	//    единственное, что убивает ссылку, это проверка уровня НА СТРОКЕ ФАЙЛА. Без неё
	//    «закрыли обратно» ничего бы не закрыло.
	files.rows[7].AccessLevel = entity.LibraryFileAccessTeam
	assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 4)), "level back to team")
	// `people` — тоже не публичный уровень.
	files.rows[7].AccessLevel = entity.LibraryFileAccessPeople
	assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 4)), "level people")

	// 4. ПО СРОКУ. Прошедший срок НЕ меняет уровень (ничто не закрывает файл за спиной
	//    владельца) — он закрывает МАРШРУТ.
	files.rows[7].AccessLevel = entity.LibraryFileAccessLink
	files.rows[7].ExpiresAt = sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true}
	assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 4)), "expired")
	// Живой срок пропускает.
	files.rows[7].ExpiresAt = sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true}
	if w := serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 4)); w.Code != http.StatusFound {
		t.Fatalf("unexpired link: want 302, got %d", w.Code)
	}
	// Явный отзыв — тот же ответ при любом поколении.
	files.rows[7].RevokedAt = sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}
	assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 4)), "revoked")
	files.rows[7].RevokedAt = sql.NullTime{}

	// 5. НЕСУЩЕСТВУЮЩИЙ. Три разных «нет такого»: чужой файл с валидной подписью, мусор вместо
	//    токена и HEAD по несуществующему — все обязаны выглядеть одинаково.
	assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 4242, 1)), "unknown file")
	assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/not-a-token"), "garbage token")
	assertBare404(t, serveFile(svc, http.MethodHead, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 4242, 1)), "HEAD of a missing file")
	// HEAD смонтирован: без него chi ответил бы 405, и это был бы ЕДИНСТВЕННЫЙ ответ,
	// отличающийся от общего 404, — то есть подтверждение существования пути.
	if w := serveFile(svc, http.MethodHead, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 4)); w.Code != http.StatusFound {
		t.Fatalf("HEAD of a live link: want 302, got %d", w.Code)
	}
}

// assertBare404 требует именно ГОЛЫЙ 404: ни причины, ни намёка на неё в теле.
func assertBare404(t *testing.T, w *httptest.ResponseRecorder, what string) {
	t.Helper()
	if w.Code != http.StatusNotFound {
		t.Fatalf("%s: want 404, got %d", what, w.Code)
	}
	body := strings.ToLower(w.Body.String())
	for _, leak := range []string{"epoch", "revok", "expire", "level", "team", "rate", "scope", "token"} {
		if strings.Contains(body, leak) {
			t.Fatalf("%s: the response names the reason (%q) — every refusal must look the same: %q",
				what, leak, w.Body.String())
		}
	}
}

// TestPublicLinkRefusesForeignScopes: id файла живёт в том же числовом диапазоне, что id
// выкройки, тех-карты и прогона. Без allowlist'а токен наряда открыл бы файл с тем же номером.
func TestPublicLinkRefusesForeignScopes(t *testing.T) {
	files := &fakeFiles{rows: map[int]*entity.LibraryFileLinkTarget{7: linkTarget(7, 1)}}
	presign := &fakePresign{}
	svc := newTestService(t, files, presign)

	for _, scope := range []patterntoken.Scope{
		patterntoken.ScopeInternal, patterntoken.ScopePrint, patterntoken.ScopeCard, patterntoken.ScopeRunPack,
	} {
		token := svc.minter.Mint(scope, 7, 1)
		assertBare404(t, serveFile(svc, http.MethodGet, "/api/f/"+token), "scope "+string(rune(scope)))
	}
	if presign.calls != 0 {
		t.Fatalf("a foreign scope must be refused BEFORE anything is signed, got %d presign calls", presign.calls)
	}
}

// TestPublicLinkNeverServesSvgOrHtmlInline — вся XSS-история фичи в одном тесте. Presigned url
// смотрит в origin бакета, поэтому отрисованный на месте svg исполнил бы скрипты в его
// контексте. `?dl=1` может сделать вложением безопасный тип, но обратной кнопки нет.
func TestPublicLinkNeverServesSvgOrHtmlInline(t *testing.T) {
	for _, tc := range []struct {
		ct           string
		query        string
		wantDownload bool
	}{
		{ct: "image/svg+xml", wantDownload: true},
		{ct: "text/html; charset=utf-8", wantDownload: true},
		{ct: "application/xhtml+xml", wantDownload: true},
		{ct: "application/pdf", wantDownload: false},
		{ct: "image/png", wantDownload: false},
		{ct: "application/pdf", query: "?dl=1", wantDownload: true},
		// Явная просьба «покажи на месте» ничего не меняет для небезопасного типа: параметра,
		// который включал бы inline, в маршруте нет вовсе.
		{ct: "image/svg+xml", query: "?dl=0", wantDownload: true},
	} {
		row := linkTarget(7, 1)
		row.ContentType = tc.ct
		files := &fakeFiles{rows: map[int]*entity.LibraryFileLinkTarget{7: row}}
		presign := &fakePresign{}
		svc := newTestService(t, files, presign)

		w := serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 1)+tc.query)
		if w.Code != http.StatusFound {
			t.Fatalf("%s%s: want 302, got %d", tc.ct, tc.query, w.Code)
		}
		if presign.download != tc.wantDownload {
			t.Fatalf("%s%s: download=%v, want %v", tc.ct, tc.query, presign.download, tc.wantDownload)
		}
		if presign.key != row.ObjectKey {
			t.Fatalf("the signed key must come from the file row, got %q", presign.key)
		}
	}
}

// TestPublicLinkJSONMode: страница приземления читает метаданные, и в них нет ни ключа объекта,
// ни чего-либо, чего нет в узком чтении.
func TestPublicLinkJSONMode(t *testing.T) {
	files := &fakeFiles{rows: map[int]*entity.LibraryFileLinkTarget{7: linkTarget(7, 1)}}
	svc := newTestService(t, files, &fakePresign{})

	w := serveFile(svc, http.MethodGet, "/api/f/"+svc.minter.Mint(patterntoken.ScopeFile, 7, 1)+"?mode=json")
	if w.Code != http.StatusOK {
		t.Fatalf("mode=json: want 200, got %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("mode=json: body is not json: %v", err)
	}
	if got["file_name"] != "макет.pdf" {
		t.Fatalf("mode=json: want the stored file name, got %v", got["file_name"])
	}
	if _, ok := got["object_key"]; ok {
		t.Fatalf("mode=json must not hand out the bucket key")
	}
}

// TestLinkURLIsDeterministicAndScoped: «пересоздать» работает ровно потому, что адрес
// детерминирован — одно поколение одного файла даёт одну строку, и поколение входит в подпись.
func TestLinkURLIsDeterministicAndScoped(t *testing.T) {
	svc := newTestService(t, &fakeFiles{}, &fakePresign{})

	first := svc.LinkURL(7, 1)
	if first != svc.LinkURL(7, 1) {
		t.Fatal("the same file at the same epoch must yield the same url")
	}
	if first == svc.LinkURL(7, 2) {
		t.Fatal("bumping the epoch must change the url — otherwise rotation revokes nothing")
	}
	if !strings.HasPrefix(first, "https://backend.example/api/f/f") {
		t.Fatalf("the url must be absolute, mounted at /api/f and carry the 'f' scope byte, got %q", first)
	}
	// nil-получатель безопасен: сборка без сервиса отдаёт блок доступа без url, а не падает.
	var nilSvc *Service
	if nilSvc.LinkURL(7, 1) != "" {
		t.Fatal("a nil service must mint nothing rather than panic")
	}
}

// TestStatsAreDebouncedAndFlushed: маршрут публичный, поэтому запись в базу на каждый заход —
// способ уронить базу чужими руками. Пачка сворачивается и уезжает одним сбросом.
func TestStatsAreDebouncedAndFlushed(t *testing.T) {
	files := &fakeFiles{rows: map[int]*entity.LibraryFileLinkTarget{7: linkTarget(7, 1)}}
	svc := newTestService(t, files, &fakePresign{})

	token := svc.minter.Mint(patterntoken.ScopeFile, 7, 1)
	for range 3 {
		if w := serveFile(svc, http.MethodGet, "/api/f/"+token); w.Code != http.StatusFound {
			t.Fatalf("want 302, got %d", w.Code)
		}
	}
	if len(files.recorded) != 0 {
		t.Fatalf("hits must not reach the database one by one, got %v", files.recorded)
	}
	svc.Stop() // идемпотентен, поэтому повторный вызов из t.Cleanup безвреден
	if files.recorded[7] != 3 {
		t.Fatalf("the pending batch must be flushed on stop, got %v", files.recorded)
	}
}

// TestEmptyPepperFailsClosed: пустой ключ HMAC сделал бы подделываемой каждую ссылку.
func TestEmptyPepperFailsClosed(t *testing.T) {
	if _, err := New(&fakeFiles{}, &fakePresign{}, "  ", "https://backend.example"); err == nil {
		t.Fatal("an empty pepper must refuse to start")
	}
}
