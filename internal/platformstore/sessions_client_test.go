package platformstore

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The recorded device of a new session and the touch throttle, without a database (P0 §3.3, §8).

func TestSessionClientArgs(t *testing.T) {
	long := strings.Repeat("я", 300)
	for _, c := range []struct {
		name       string
		in         SessionClient
		ua, ip     any
		wantUARune int
	}{
		{"none", SessionClient{}, nil, nil, 0},
		{"plain", SessionClient{UserAgent: " Mozilla/5.0 Chrome/131 ", IP: "10.0.0.1"}, "Mozilla/5.0 Chrome/131", "10.0.0.1", 22},
		{"control characters dropped", SessionClient{UserAgent: "a\x00b\nc\td\u0085e", IP: "2001:db8::1"}, "abcde", "2001:db8::1", 5},
		{"invalid utf-8 dropped", SessionClient{UserAgent: "ok\xff\xfeok"}, "okok", nil, 4},
		{"cut to 255 runes", SessionClient{UserAgent: long}, strings.Repeat("я", 255), nil, 255},
		{"unknown ip", SessionClient{UserAgent: "x", IP: "unknown"}, "x", nil, 1},
		{"ip too long", SessionClient{IP: strings.Repeat("1", 46)}, nil, nil, 0},
		{"only spaces", SessionClient{UserAgent: "   ", IP: "  "}, nil, nil, 0},
	} {
		ua, ip := sessionClientArgs(WithSessionClient(context.Background(), c.in))
		if ua != c.ua || ip != c.ip {
			t.Errorf("%s: ua %q ip %q, want %q %q", c.name, ua, ip, c.ua, c.ip)
		}
		if s, ok := ua.(string); ok && utf8.RuneCountInString(s) != c.wantUARune {
			t.Errorf("%s: %d runes, want %d", c.name, utf8.RuneCountInString(s), c.wantUARune)
		}
	}
	if ua, ip := sessionClientArgs(context.Background()); ua != nil || ip != nil {
		t.Fatalf("no client on ctx: %v %v", ua, ip)
	}
}

func TestSessionTouchesDue(t *testing.T) {
	tt := newSessionTouches()
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if !tt.due("a", t0) || tt.due("a", t0.Add(TouchEvery-time.Second)) {
		t.Fatal("a second touch inside TouchEvery went to the database")
	}
	if !tt.due("a", t0.Add(TouchEvery)) || !tt.due("b", t0) {
		t.Fatal("a touch after TouchEvery (or of another session) was skipped")
	}
	// A clock that went back does not freeze the session.
	if !tt.due("a", t0.Add(-time.Hour)) {
		t.Fatal("a touch from before the last one was skipped")
	}
}
