package bucket

import (
	"strings"
	"testing"
	"time"
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
		if got := isManagedPatternKey(c.key); got != c.want {
			t.Errorf("%s: isManagedPatternKey(%q) = %v, want %v", name, c.key, got, c.want)
		}
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
