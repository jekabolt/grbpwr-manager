package bucket

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// testArchiveBucket — бакет с КОНФИГОМ, но без живого клиента. Тот же приём, что у
// TestPresignRejectsUnmanagedKeys: если гард пропустит ключ дальше, обращение к nil-клиенту
// упадёт паникой, и тест покраснеет — то есть «отказ пришёл ДО сети» проверяется не словами, а
// тем, что сети здесь нет вовсе.
func testArchiveBucket() *Bucket {
	return &Bucket{Config: &Config{
		S3BucketName:      "grbpwr",
		S3Endpoint:        "fra1.digitaloceanspaces.com",
		BaseFolder:        "grbpwr-com",
		SubdomainEndpoint: "files.grbpwr.com",
	}}
}

// TestArchiveManagedReadKeyGate фиксирует, из каких сегментов GetManagedObject согласен читать.
// Отдельно — ЗАПРЕТ files-library: она лежит ПОД базовой папкой, поэтому без явного исключения её
// открыл бы сегмент медиа, и у приватных файлов библиотеки появился бы второй читатель без потолка.
func TestArchiveManagedReadKeyGate(t *testing.T) {
	segments := testArchiveBucket().managedReadSegments()
	cases := map[string]struct {
		key  string
		want bool
	}{
		"media under base folder":   {"grbpwr-com/grbpwr-com/2026/august/x-og.webp", true},
		"video under base folder":   {"grbpwr-com/grbpwr-com/2026/august/clip.mp4", true},
		"pattern under base folder": {"grbpwr-com/tech-card-patterns/2026/august/front-abc.dxf", true},
		"pattern without base":      {"tech-card-patterns/2026/august/front.pdf", true},
		"archive object":            {"techcard-archives/deadbeef/techcard-A1-20260825-1730.zip", true},
		"import object":             {"techcard-imports/01HZZZ.zip", true},

		"library file":           {"grbpwr-com/files-library/2026/august/brief.pdf", false},
		"library preview":        {"grbpwr-com/files-library/previews/2026/august/p.webp", false},
		"foreign top segment":    {"secrets/creds.json", false},
		"bare object":            {"x.jpg", false},
		"segment is last":        {"techcard-archives", false},
		"trailing slash":         {"techcard-archives/", false},
		"parent traversal":       {"techcard-archives/../grbpwr-com/files-library/x.pdf", false},
		"dot segment":            {"techcard-imports/./x.zip", false},
		"substring of segment":   {"techcard-archives-public/x.zip", false},
		"base folder substring":  {"grbpwr-com-old/media/x.jpg", false},
		"empty":                  {"", false},
		"base folder is last":    {"grbpwr-com", false},
		"library under archives": {"techcard-archives/files-library/x.pdf", false},
	}
	for name, c := range cases {
		if got := isAllowedManagedReadKey(c.key, segments); got != c.want {
			t.Errorf("%s: isAllowedManagedReadKey(%q) = %v, want %v", name, c.key, got, c.want)
		}
	}

	// Пустая базовая папка не должна превращаться в разрешение «что угодно».
	empty := (&Bucket{Config: &Config{}}).managedReadSegments()
	for _, key := range []string{"grbpwr-com/grbpwr-com/2026/x.jpg", "anything/x.jpg", "/x.jpg"} {
		if isAllowedManagedReadKey(strings.Trim(key, "/"), empty) {
			t.Errorf("unconfigured base folder must not admit %q", key)
		}
	}
}

// TestArchiveReadGateRefusesBeforeS3 — тот же запрет на границе метода. Клиента нет: дойди
// проверка до GetObject/Stat, тест упал бы паникой, а не сравнением.
func TestArchiveReadGateRefusesBeforeS3(t *testing.T) {
	b := testArchiveBucket()
	for _, key := range []string{
		"grbpwr-com/files-library/2026/august/brief.pdf",
		"secrets/creds.json",
		"techcard-archives",
		"techcard-imports/../secrets/x.zip",
		"",
	} {
		if _, _, err := b.GetManagedObject(t.Context(), key); !errors.Is(err, ErrManagedKeyNotAllowed) {
			t.Errorf("GetManagedObject(%q): want ErrManagedKeyNotAllowed, got %v", key, err)
		}
	}

	// У читалки импорта сегмент УЖЕ: медиа и выкройки ей не положены вовсе.
	for _, key := range []string{
		"grbpwr-com/grbpwr-com/2026/august/x.jpg",
		"grbpwr-com/tech-card-patterns/2026/x.dxf",
		"techcard-archives/deadbeef/x.zip",
		"techcard-imports",
		"",
	} {
		if _, _, err := b.GetImportObjectReaderAt(t.Context(), key); !errors.Is(err, ErrManagedKeyNotAllowed) {
			t.Errorf("GetImportObjectReaderAt(%q): want ErrManagedKeyNotAllowed, got %v", key, err)
		}
	}
}

// TestArchiveObjectName — имя объекта строится из style_number, то есть из набранного человеком.
// Проверяется, что из ключа не может уехать ни слэш (объект оказался бы в другой папке), ни
// управляющий символ, и что basename остаётся читаемым именем файла: он же уходит в
// Content-Disposition.
func TestArchiveObjectName(t *testing.T) {
	ok := map[string]string{
		"techcard-A1-20260825-1730.zip": "techcard-A1-20260825-1730.zip",
		"techcard-A1-20260825-1730":     "techcard-A1-20260825-1730.zip",
		"techcard-ЖИЛЕТ-20260825.zip":   "techcard-------20260825.zip",
		"../../etc/passwd.zip":          "passwd.zip",
		"dir/sub/techcard-A1.zip":       "techcard-A1.zip",
		`C:\Windows\techcard-A1.zip`:    "techcard-A1.zip",
		"techcard A1 (v2).zip":          "techcard-A1--v2-.zip",
		"techcard\r\nA1.zip":            "techcard--A1.zip",
		"techcard-A1.zip.zip":           "techcard-A1.zip.zip",
		strings.Repeat("a", 400) + ".z": strings.Repeat("a", maxArchiveNameLen) + ".zip",
	}
	for in, want := range ok {
		got, err := archiveObjectName(in)
		if err != nil {
			t.Errorf("archiveObjectName(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("archiveObjectName(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, "/\\") || strings.Contains(got, "..") {
			t.Errorf("archiveObjectName(%q) = %q leaks a path shape", in, got)
		}
	}
	for _, bad := range []string{"", "   ", "...", "///", "..", ".zip", "---"} {
		if got, err := archiveObjectName(bad); !errors.Is(err, ErrInvalidArchiveUpload) {
			t.Errorf("archiveObjectName(%q) = %q, %v; want ErrInvalidArchiveUpload", bad, got, err)
		}
	}
}

// TestArchiveUploadNameGate — негодное имя отвергается ДО PutObject (клиента нет), и ключ,
// который строит удачный путь, лежит в своём сегменте и проходит гард подписи.
func TestArchiveUploadNameGate(t *testing.T) {
	b := testArchiveBucket()
	if _, err := b.UploadArchiveObject(t.Context(), nil, "techcard-A1.zip"); !errors.Is(err, ErrInvalidArchiveUpload) {
		t.Errorf("nil reader must be refused, got %v", err)
	}
	if _, err := b.UploadArchiveObject(t.Context(), strings.NewReader("x"), "///"); !errors.Is(err, ErrInvalidArchiveUpload) {
		t.Errorf("unusable name must be refused before PutObject, got %v", err)
	}

	// Форма ключа: сегмент, энтропия, чистое имя файла. Собирается тем же кодом, что и в
	// UploadArchiveObject, — иначе тест проверял бы сам себя.
	name, err := archiveObjectName("techcard-A1-20260825-1730.zip")
	if err != nil {
		t.Fatalf("archiveObjectName: %v", err)
	}
	entropy, err := archiveKeyEntropy()
	if err != nil {
		t.Fatalf("archiveKeyEntropy: %v", err)
	}
	key := ArchiveSegment + "/" + entropy + "/" + name
	if len(entropy) != 32 {
		t.Errorf("archive key entropy = %q, want 32 hex chars", entropy)
	}
	if !isManagedKeyInSegment(key, ArchiveSegment) {
		t.Errorf("built key %q does not pass its own presign gate", key)
	}
	if got := key[strings.LastIndexByte(key, '/')+1:]; got != "techcard-A1-20260825-1730.zip" {
		t.Errorf("download basename = %q, want the FORMAT.md file name", got)
	}
	// Два ключа подряд обязаны различаться: иначе два экспорта одной карты в одну минуту
	// затирали бы друг друга.
	second, err := archiveKeyEntropy()
	if err != nil {
		t.Fatalf("archiveKeyEntropy: %v", err)
	}
	if second == entropy {
		t.Error("archive key entropy repeated")
	}
}

// TestArchiveImportIDGate — import id уходит В КЛЮЧ, поэтому всё, что могло бы стать вторым
// сегментом пути или пустотой, отвергается до обращения к бакету.
func TestArchiveImportIDGate(t *testing.T) {
	good := []string{"01HZZZZZZZZZZZZZZZZZZZZZZZ", "abc-123_XYZ", "0"}
	for _, id := range good {
		if !isValidImportID(id) {
			t.Errorf("isValidImportID(%q) = false, want true", id)
		}
	}
	bad := []string{"", "..", "a/b", "a\\b", "a.zip", "a b", "../../secrets", strings.Repeat("a", 65), "ключ"}
	for _, id := range bad {
		if isValidImportID(id) {
			t.Errorf("isValidImportID(%q) = true, want false", id)
		}
	}

	b := testArchiveBucket()
	for _, id := range bad {
		if _, err := b.UploadImportObject(t.Context(), strings.NewReader("x"), id); !errors.Is(err, ErrInvalidArchiveUpload) {
			t.Errorf("UploadImportObject(%q): want ErrInvalidArchiveUpload before PutObject, got %v", id, err)
		}
	}
	if _, err := b.UploadImportObject(t.Context(), nil, good[0]); !errors.Is(err, ErrInvalidArchiveUpload) {
		t.Errorf("nil reader must be refused, got %v", err)
	}
	// Ключ импорта детерминированный: строка tech_card_import ссылается на объект, и
	// восстановить одно из другого должно быть можно без второго чтения.
	key := ImportSegment + "/" + good[0] + archiveNameExt
	if !isManagedKeyInSegment(key, ImportSegment) {
		t.Errorf("import key %q does not pass its own read gate", key)
	}
}

// TestArchivePresignGate — подписывается ТОЛЬКО сегмент архивов и только на разумный срок.
// Клиента нет, поэтому каждый отказ здесь заведомо случился до PresignedGetObject.
func TestArchivePresignGate(t *testing.T) {
	b := testArchiveBucket()
	for _, key := range []string{
		"grbpwr-com/files-library/2026/x.pdf",
		"grbpwr-com/tech-card-patterns/2026/x.dxf",
		"grbpwr-com/grbpwr-com/2026/x.jpg",
		"techcard-imports/01HZZZ.zip",
		"techcard-archives",
		"techcard-archives/../grbpwr-com/files-library/x.pdf",
		"",
	} {
		if _, _, err := b.PresignArchiveObject(t.Context(), key, ArchiveLinkTTL); !errors.Is(err, ErrManagedKeyNotAllowed) {
			t.Errorf("PresignArchiveObject(%q): want ErrManagedKeyNotAllowed, got %v", key, err)
		}
	}

	const okKey = "techcard-archives/deadbeef/techcard-A1-20260825-1730.zip"
	for _, ttl := range []time.Duration{0, -time.Minute, maxArchivePresignTTL + time.Second, 24 * time.Hour} {
		if _, _, err := b.PresignArchiveObject(t.Context(), okKey, ttl); !errors.Is(err, ErrInvalidArchiveUpload) {
			t.Errorf("PresignArchiveObject(ttl=%s): want ErrInvalidArchiveUpload, got %v", ttl, err)
		}
	}

	// Числа владельца (решение B-5) — здесь, а не россыпью у вызывающих.
	if ArchiveLinkTTL != 10*time.Minute {
		t.Errorf("ArchiveLinkTTL = %s, want 10m", ArchiveLinkTTL)
	}
	if ArchiveRetention != 7*24*time.Hour {
		t.Errorf("ArchiveRetention = %s, want 168h", ArchiveRetention)
	}
	if MaxArchiveObjectBytes != 256*1024*1024 {
		t.Errorf("MaxArchiveObjectBytes = %d, want 256 MiB", MaxArchiveObjectBytes)
	}
	if ArchiveLinkTTL > maxArchivePresignTTL {
		t.Error("the ttl callers are told to pass must itself be signable")
	}
}

// TestArchiveCleanupSegmentGate — «воркер чистки не трогает никакие другие сегменты» держится
// гардом, а не дисциплиной воркера: перепутанный аргумент это отказ, а не выборка медиатеки на
// снос. Клиента нет — дойди вызов до ListObjects, была бы паника.
func TestArchiveCleanupSegmentGate(t *testing.T) {
	b := testArchiveBucket()
	for _, segment := range []string{
		"grbpwr-com", "files-library", "tech-card-patterns", "shipping-labels", "",
		"techcard-archives/", "/techcard-archives", "techcard-archives-public",
	} {
		if _, err := b.ListObjectsOlderThan(t.Context(), segment, ArchiveRetention); !errors.Is(err, ErrManagedKeyNotAllowed) {
			t.Errorf("ListObjectsOlderThan(%q): want ErrManagedKeyNotAllowed, got %v", segment, err)
		}
	}
	for _, age := range []time.Duration{0, -time.Hour} {
		if _, err := b.ListObjectsOlderThan(t.Context(), ArchiveSegment, age); !errors.Is(err, ErrInvalidArchiveUpload) {
			t.Errorf("ListObjectsOlderThan(age=%s): want ErrInvalidArchiveUpload, got %v", age, err)
		}
	}
	// Положительный контроль: разрешённые сегменты гард пропускает — иначе тест выше был бы
	// зелёным и на методе, который отказывает всегда.
	for _, segment := range []string{ArchiveSegment, ImportSegment} {
		if !isCleanableSegment(segment) {
			t.Errorf("isCleanableSegment(%q) = false, want true", segment)
		}
	}
}

// TestArchiveCapReader — отказ, а не обрезка. io.LimitReader на границе молча отдаёт EOF, и
// усечённый zip был бы неотличим от повреждённого.
func TestArchiveCapReader(t *testing.T) {
	// Ровно потолок проходит.
	exact := bytes.Repeat([]byte("a"), 64)
	got, err := io.ReadAll(newCapReader(bytes.NewReader(exact), int64(len(exact))))
	if err != nil {
		t.Fatalf("exactly-at-limit read failed: %v", err)
	}
	if len(got) != len(exact) {
		t.Fatalf("read %d bytes, want %d", len(got), len(exact))
	}
	// Потолок плюс байт — ошибка.
	over := bytes.Repeat([]byte("a"), 65)
	if _, err := io.ReadAll(newCapReader(bytes.NewReader(over), int64(len(exact)))); !errors.Is(err, ErrArchiveObjectTooLarge) {
		t.Fatalf("over-limit read: want ErrArchiveObjectTooLarge, got %v", err)
	}
	// Ошибка залипает: второй Read не должен вдруг отдать данные.
	c := newCapReader(bytes.NewReader(over), 1)
	if _, err := io.ReadAll(c); !errors.Is(err, ErrArchiveObjectTooLarge) {
		t.Fatalf("first read: want ErrArchiveObjectTooLarge, got %v", err)
	}
	if n, err := c.Read(make([]byte, 8)); n != 0 || !errors.Is(err, ErrArchiveObjectTooLarge) {
		t.Fatalf("second read: got (%d, %v), want (0, ErrArchiveObjectTooLarge)", n, err)
	}
}

// TestArchiveTempFileReaderAtCloseRemoves — временный файл импорта не должен пережить Close.
// Внутри GetImportObjectReaderAt он вдобавок разыменовывается сразу после создания; здесь
// проверяется вторая половина — что Close убирает файл и там, где разыменование не сработало.
func TestArchiveTempFileReaderAtCloseRemoves(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "techcard-import-*.zip")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	name := f.Name()
	if _, err := f.Write([]byte("PK\x03\x04payload")); err != nil {
		t.Fatalf("write: %v", err)
	}
	ra := &tempFileReaderAt{f: f}

	buf := make([]byte, 4)
	if _, err := ra.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "PK\x03\x04" {
		t.Fatalf("ReadAt returned %q", buf)
	}
	if err := ra.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file survived Close: %v", err)
	}
	// Повторный Close не должен превращать уже убранный файл в ошибку.
	_ = ra.Close()
}
