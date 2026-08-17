package bucket

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// TestIsManagedPatternKey locks the gate that stands between an UNAUTHENTICATED endpoint
// and PresignedGetObject: only keys under the dedicated pattern folder may ever be signed.
func TestIsManagedPatternKey(t *testing.T) {
	cases := map[string]struct {
		key  string
		want bool
	}{
		"canonical":            {"base/tech-card-patterns/2026/august/x-deadbeef.pdf", true},
		"no base folder":       {"tech-card-patterns/2026/august/x.dxf", true},
		"nested base":          {"a/b/tech-card-patterns/2026/x.pdf", true},
		"folder is last":       {"base/tech-card-patterns", false},
		"folder is last slash": {"base/tech-card-patterns/", false},
		"other folder":         {"base/media/2026/august/x.pdf", false},
		"labels folder":        {"base/shipping-labels/2026/x.pdf", false},
		"empty":                {"", false},
		"dot segment":          {"base/tech-card-patterns/./x.pdf", false},
		"parent segment":       {"base/tech-card-patterns/../../secrets/x.pdf", false},
		"parent before folder": {"../tech-card-patterns/x.pdf", false},
		"substring only":       {"base/tech-card-patterns-public/x.pdf", false},
	}
	for name, c := range cases {
		if got := isManagedKeyInSegment(c.key, patternObjectPathSegment); got != c.want {
			t.Errorf("%s: isManagedKeyInSegment(%q, pattern) = %v, want %v", name, c.key, got, c.want)
		}
	}
}

// TestIsManagedLibraryKey is the same gate for the files library. The two folders must
// stay mutually exclusive: a pattern key is not signable as a library object and vice
// versa, which is what keeps the unauthenticated pattern endpoints from reaching library
// files even if a key were somehow smuggled into them.
func TestIsManagedLibraryKey(t *testing.T) {
	cases := map[string]struct {
		key  string
		want bool
	}{
		"canonical":      {"base/files-library/2026/august/x-deadbeef.pdf", true},
		"preview nested": {"base/files-library/previews/2026/august/x.webp", true},
		"no base folder": {"files-library/2026/august/x.xlsx", true},
		"folder is last": {"base/files-library", false},
		"pattern key":    {"base/tech-card-patterns/2026/x.pdf", false},
		"media key":      {"base/media/2026/august/x.jpg", false},
		"parent segment": {"base/files-library/../../secrets/x.pdf", false},
		"substring only": {"base/files-library-public/x.pdf", false},
		"empty":          {"", false},
		"multipart spec": {"base/files-library/previews/x.webp", true},
	}
	for name, c := range cases {
		if got := isManagedKeyInSegment(c.key, libraryFolder); got != c.want {
			t.Errorf("%s: isManagedKeyInSegment(%q, library) = %v, want %v", name, c.key, got, c.want)
		}
	}
	// A pattern key must never pass the library guard, and a library key must never pass
	// the pattern one — assert the crossing explicitly, not by implication.
	if isManagedKeyInSegment("base/files-library/2026/x.pdf", patternObjectPathSegment) {
		t.Error("library key passed the pattern guard")
	}
	if isManagedKeyInSegment("base/tech-card-patterns/2026/x.pdf", libraryFolder) {
		t.Error("pattern key passed the library guard")
	}
}

// TestPresignRejectsUnmanagedKeys is the same rule at the method boundary — the guard must
// fire before any signing happens, on a Bucket with no live client (a nil-client panic
// would prove the check ran too late).
func TestPresignRejectsUnmanagedKeys(t *testing.T) {
	b := &Bucket{Config: &Config{S3BucketName: "grbpwr", S3Endpoint: "fra1.digitaloceanspaces.com"}}
	for _, key := range []string{
		"base/media/2026/x.jpg",
		"base/shipping-labels/2026/x.pdf",
		"base/tech-card-patterns/../media/x.jpg",
		"",
	} {
		if _, _, err := b.PresignPatternObject(t.Context(), key, false, ""); err == nil {
			t.Errorf("key %q must be refused before signing", key)
		}
	}
}

// TestManagedHosts pins the host allowlist used by write validation: the CDN subdomain and
// the bucket's VIRTUAL-HOSTED origin (bucket.endpoint), which is also the host presigned
// urls carry — signatures bind Host, so a CDN-hosted presign would never verify.
func TestManagedHosts(t *testing.T) {
	hosts := ManagedHosts(&Config{
		SubdomainEndpoint: "files.grbpwr.com",
		S3Endpoint:        "fra1.digitaloceanspaces.com",
		S3BucketName:      "grbpwr",
	})
	want := map[string]bool{"files.grbpwr.com": true, "grbpwr.fra1.digitaloceanspaces.com": true}
	if len(hosts) != len(want) {
		t.Fatalf("want %d hosts, got %v", len(want), hosts)
	}
	for _, h := range hosts {
		if !want[h] {
			t.Errorf("unexpected managed host %q", h)
		}
	}
	if got := ManagedHosts(nil); got != nil {
		t.Errorf("nil config must yield no hosts, got %v", got)
	}
	// A scheme-carrying config value is normalised to the bare host.
	hosts = ManagedHosts(&Config{SubdomainEndpoint: "https://files.grbpwr.com"})
	if len(hosts) != 1 || hosts[0] != "files.grbpwr.com" {
		t.Fatalf("scheme must be stripped, got %v", hosts)
	}
}

// TestSanitizeDownloadName — the value is interpolated into a quoted Content-Disposition
// parameter, so quotes, control characters and any path shape must not survive.
func TestSanitizeDownloadName(t *testing.T) {
	cases := map[string]string{
		"перед.pdf":                  "перед.pdf",
		"  spaced.dxf  ":             "spaced.dxf",
		`evil".pdf`:                  "evil.pdf",
		"a\r\nContent-Length: 0.pdf": "aContent-Length: 0.pdf",
		"../../etc/passwd":           "passwd",
		"C:\\Windows\\x.pdf":         "C:Windowsx.pdf",
		"":                           "",
		"   ":                        "",
		"..":                         "",
		"/":                          "",
	}
	for in, want := range cases {
		if got := sanitizeDownloadName(in); got != want {
			t.Errorf("sanitizeDownloadName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{`"`, "\\", "/", "\r", "\n"} {
		if strings.Contains(sanitizeDownloadName(`a`+bad+`b.pdf`), bad) {
			t.Errorf("sanitized name must not contain %q", bad)
		}
	}
}

// TestPresignWindow documents the expiry arithmetic the memoization depends on: expiry is
// snapped to a 6h grid and set two windows out, so TTL is always within [6h, 12h] — never
// zero at an exact boundary, never past minio's 7-day presign ceiling.
func TestPresignWindow(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), // exact boundary
		time.Date(2026, 8, 5, 5, 59, 59, 0, time.UTC),
		time.Date(2026, 8, 5, 6, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 5, 23, 59, 59, 0, time.UTC),
	} {
		expiresAt := at.Truncate(presignWindow).Add(2 * presignWindow)
		ttl := expiresAt.Sub(at)
		if ttl < presignWindow || ttl > 2*presignWindow {
			t.Errorf("at %s: ttl %s outside [6h,12h]", at, ttl)
		}
		if ttl > 7*24*time.Hour {
			t.Errorf("at %s: ttl %s exceeds the presign ceiling", at, ttl)
		}
	}
}

// offlinePresignBucket собирает Bucket, у которого подпись считается ЛОКАЛЬНО: регион задан
// явно, поэтому minio-go не ходит за location бакета и тест не зависит от сети. Ключи выдуманные —
// подпись проверяется по форме url, а не бакетом.
func offlinePresignBucket(t *testing.T) *Bucket {
	t.Helper()
	cli, err := minio.New("fra1.digitaloceanspaces.com", &minio.Options{
		Creds:  credentials.NewStaticV4("AKIAEXAMPLE", "secret-not-a-real-key", ""),
		Secure: true,
		Region: "fra1",
	})
	if err != nil {
		t.Fatalf("minio.New: %v", err)
	}
	return &Bucket{Client: cli, Config: &Config{
		S3BucketName: "grbpwr", S3Endpoint: "fra1.digitaloceanspaces.com",
	}}
}

func presignCacheSize() int {
	presignMu.Lock()
	defer presignMu.Unlock()
	return len(presignCache)
}

// TestPublicLinkPresignIsShortLivedAndUncached — ПРИЁМКА ОТЗЫВА ПУБЛИЧНОЙ ССЫЛКИ.
//
// «Пересоздать» двигает поколение, и маршрут после этого честно отвечает 404. Но отзыв реален
// ровно настолько, насколько короток УЖЕ ВЫДАННЫЙ bucket-url: подпись живёт в бакете и про наше
// поколение не знает. Оконный presign отдавал бы её на 6–12 часов ДА ЕЩЁ и мемоизированной, то есть
// «прежняя больше не работает» в журнале было бы неправдой почти на полсуток. Тот же разрыв ломал
// чип «срок ссылки: 24 ч».
//
// Три утверждения, и все три про это: срок в url'е — минуты, строка НЕ кладётся в presignCache, а
// панельный подписыватель по тому же ключу продолжает и округлять, и мемоизировать.
func TestPublicLinkPresignIsShortLivedAndUncached(t *testing.T) {
	if publicLinkPresignTTL < 5*time.Minute || publicLinkPresignTTL > 15*time.Minute {
		t.Fatalf("публичный presign обязан жить минуты, а не часы: %s", publicLinkPresignTTL)
	}
	b := offlinePresignBucket(t)
	const key = "base/files-library/2026/смета.pdf"

	before := presignCacheSize()
	signed, expiresAt, err := b.PresignLibraryObjectShortLived(t.Context(), key, false, "смета.pdf")
	if err != nil {
		t.Fatalf("PresignLibraryObjectShortLived: %v", err)
	}
	if got := presignCacheSize(); got != before {
		t.Fatalf("публичная подпись обязана идти МИМО presignCache: было %d записей, стало %d", before, got)
	}
	q, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("presigned url is unparsable: %v", err)
	}
	if got := q.Query().Get("X-Amz-Expires"); got != "600" {
		t.Fatalf("X-Amz-Expires = %q, want 600 (срок обязан ехать в САМОЙ подписи, а не только в ответе)", got)
	}
	if ttl := time.Until(expiresAt); ttl <= 0 || ttl > publicLinkPresignTTL+time.Minute {
		t.Fatalf("expires_at %s даёт ttl %s — ответ маршрута обязан называть тот же срок, что подпись", expiresAt, ttl)
	}

	// Второй вызов подписывается ЗАНОВО: у публичного маршрута нет потребителя, которому нужна
	// постоянная строка, зато есть требование, чтобы срок отсчитывался от текущего момента.
	if _, secondExpiry, err := b.PresignLibraryObjectShortLived(t.Context(), key, false, "смета.pdf"); err != nil {
		t.Fatalf("second PresignLibraryObjectShortLived: %v", err)
	} else if secondExpiry.Before(expiresAt) {
		t.Fatalf("повторная подпись обязана считать срок от «сейчас», got %s < %s", secondExpiry, expiresAt)
	}
	if got := presignCacheSize(); got != before {
		t.Fatalf("повторная публичная подпись тоже обязана идти мимо кэша: %d != %d", got, before)
	}

	// А панельный подписыватель по ТОМУ ЖЕ ключу мемоизирует — иначе <object>-эмбед перемонтировался
	// бы на каждый ответ API. Разделение методов именно за этим и заведено.
	panelURL, panelExpiry, err := b.PresignLibraryObject(t.Context(), key, false, "смета.pdf")
	if err != nil {
		t.Fatalf("PresignLibraryObject: %v", err)
	}
	if presignCacheSize() != before+1 {
		t.Fatalf("панельная подпись обязана попасть в кэш — иначе мемоизация мертва")
	}
	again, _, err := b.PresignLibraryObject(t.Context(), key, false, "смета.pdf")
	if err != nil {
		t.Fatalf("PresignLibraryObject (repeat): %v", err)
	}
	if again != panelURL {
		t.Fatal("панельная подпись обязана быть постоянной внутри окна")
	}
	if time.Until(panelExpiry) <= publicLinkPresignTTL {
		t.Fatalf("панельная подпись живёт окнами (6–12 ч), а не минутами: %s", time.Until(panelExpiry))
	}
}

// TestContentDispositionSurvivesNonASCII locks the fix for the failure that would
// have made the download link dead for most of this library: a Content-Disposition
// carrying a raw UTF-8 filename is rejected by S3 with 400 InvalidArgument, and
// Russian filenames are the norm here, not the exception.
func TestContentDispositionSurvivesNonASCII(t *testing.T) {
	cases := map[string]struct {
		name         string
		wantASCII    string
		wantEncoded  string
		mustNotHaveW bool
	}{
		"cyrillic": {
			name:        "макет бирки.pdf",
			wantASCII:   `filename="_____ _____.pdf"`,
			wantEncoded: `filename*=UTF-8''%D0%BC%D0%B0%D0%BA%D0%B5%D1%82%20%D0%B1%D0%B8%D1%80%D0%BA%D0%B8.pdf`,
		},
		"ascii is left alone": {
			name:        "guideline-v2.pdf",
			wantASCII:   `filename="guideline-v2.pdf"`,
			wantEncoded: `filename*=UTF-8''guideline-v2.pdf`,
		},
		"space encodes as %20 not plus": {
			name:        "a b.png",
			wantASCII:   `filename="a b.png"`,
			wantEncoded: `filename*=UTF-8''a%20b.png`,
		},
		// An ASCII extension is enough to make the fallback meaningful, so the name
		// keeps its shape rather than collapsing to a placeholder.
		"non-ascii stem, ascii extension": {
			name:        "макет.ai",
			wantASCII:   `filename="_____.ai"`,
			wantEncoded: `filename*=UTF-8''%D0%BC%D0%B0%D0%BA%D0%B5%D1%82.ai`,
		},
		// Nothing ASCII anywhere: underscores alone would name every such file the
		// same, so it falls back to a neutral stand-in.
		"nothing ascii at all": {
			name:      "макет.дизайн",
			wantASCII: `filename="file"`,
		},
	}
	for label, c := range cases {
		got := contentDisposition(c.name)
		if !strings.HasPrefix(got, "attachment; ") {
			t.Errorf("%s: missing attachment prefix: %s", label, got)
		}
		if !strings.Contains(got, c.wantASCII) {
			t.Errorf("%s: want ascii part %s, got %s", label, c.wantASCII, got)
		}
		if c.wantEncoded != "" && !strings.Contains(got, c.wantEncoded) {
			t.Errorf("%s: want encoded part %s, got %s", label, c.wantEncoded, got)
		}
		// The legacy parameter must be pure ASCII or the header is rejected outright.
		asciiPart := got[:strings.Index(got, "filename*=")]
		for _, r := range asciiPart {
			if r > 0x7f {
				t.Errorf("%s: non-ASCII rune %q leaked into the legacy filename: %s", label, r, got)
				break
			}
		}
		// '+' as a space would be read literally by clients.
		if strings.Contains(got[strings.Index(got, "filename*="):], "+") {
			t.Errorf("%s: '+' in the encoded filename: %s", label, got)
		}
	}
}
