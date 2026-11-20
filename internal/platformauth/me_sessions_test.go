package platformauth

import (
	"regexp"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// The device list without a database: the User-Agent matcher, the device key and the fold of live
// sessions into devices (P0 §3.3).

func TestUserAgentMatcher(t *testing.T) {
	for _, c := range []struct{ ua, browser, os string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", "chrome", "macos"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15", "safari", "macos"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.2903.70", "edge", "windows"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0", "firefox", "windows"},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 OPR/115.0.0.0", "opera", "linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.6778.73 Mobile/15E148 Safari/604.1", "chrome", "ios"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1", "safari", "ios"},
		{"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/133.0 Mobile/15E148 Safari/605.1.15", "firefox", "ios"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.81 Mobile Safari/537.36", "chrome", "android"},
		{"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36 EdgA/131.0.2903.87", "edge", "android"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", "chrome", "linux"},
		{"curl/8.7.1", "browser", ""},
		{"", "browser", ""},
	} {
		if got := UserAgentBrowser(c.ua); got != c.browser {
			t.Errorf("browser of %q = %q, want %q", c.ua, got, c.browser)
		}
		if got := UserAgentOS(c.ua); got != c.os {
			t.Errorf("os of %q = %q, want %q", c.ua, got, c.os)
		}
	}
}

func TestDeviceKey(t *testing.T) {
	k := deviceKey("chain-sid-1")
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(k) {
		t.Fatalf("key %q, want 16 hex", k)
	}
	if deviceKey("chain-sid-1") != k || deviceKey("chain-sid-2") == k {
		t.Fatal("the key is not a stable function of the chain")
	}
}

func TestDevicesFold(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	const mac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	const phone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"
	const win = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0"
	rows := []platformstore.LiveSession{
		// Device "root-a": its root (mac) and two team sessions; a team one claims another browser.
		{SID: "root-a", Chain: "root-a", Aud: "root", LastSeenAt: t0, UserAgent: mac, IP: "10.0.0.1"},
		{SID: "team-a1", Chain: "root-a", Aud: "team", LastSeenAt: t0.Add(3 * time.Minute), UserAgent: win, IP: "10.0.0.2"},
		{SID: "team-a2", Chain: "root-a", Aud: "team", LastSeenAt: t0.Add(time.Minute)},
		// Device "team-b": a team session on its own, seen last.
		{SID: "team-b", Chain: "team-b", Aud: "team", LastSeenAt: t0.Add(10 * time.Minute), UserAgent: phone, IP: "10.0.0.9"},
		// Device "root-c": the root has no user agent; the oldest team session's is used.
		{SID: "root-c", Chain: "root-c", Aud: "root", LastSeenAt: t0.Add(-time.Hour), IP: "10.0.0.5"},
		{SID: "team-c1", Chain: "root-c", Aud: "team", LastSeenAt: t0.Add(-time.Hour), UserAgent: win},
		{SID: "team-c2", Chain: "root-c", Aud: "team", LastSeenAt: t0.Add(-time.Hour), UserAgent: phone},
	}
	got := devices(rows, "root-c")
	if len(got) != 3 {
		t.Fatalf("%d devices, want 3", len(got))
	}
	type view struct {
		key, browser, os, ip string
		seen                 time.Time
		this                 bool
	}
	want := []view{
		{deviceKey("root-c"), "firefox", "windows", "10.0.0.5", t0.Add(-time.Hour), true},
		{deviceKey("team-b"), "safari", "ios", "10.0.0.9", t0.Add(10 * time.Minute), false},
		{deviceKey("root-a"), "chrome", "macos", "10.0.0.2", t0.Add(3 * time.Minute), false},
	}
	for i, d := range got {
		v := view{d.GetKey(), d.GetBrowser(), d.GetOs(), d.GetIp(), d.GetLastSeenAt().AsTime(), d.GetThisDevice()}
		if v != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, v, want[i])
		}
	}
	if len(devices(nil, "x")) != 0 {
		t.Fatal("no sessions, devices listed")
	}
}
