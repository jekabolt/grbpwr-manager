package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// !!! UNRESOLVED CONTRADICTION — VERIFY ON BETA BEFORE TRUSTING THIS !!!
//
// These tests pin the CURRENT behaviour of the hop math; they do NOT prove it is right.
// The package's own comments disagree about the shape of X-Forwarded-For behind
// DigitalOcean App Platform:
//
//   - clientid.go:22   says the edge appends the real client IP as the RIGHT-MOST entry;
//   - clientid.go:~90  says the chain is [client, edge] and hops=1 selects len-2.
//
// Both cannot hold. If the first is right, an honest request arrives with a ONE-element
// XFF, idx = -1, and every client on the internet collapses into the RemoteAddr bucket
// (the load balancer) — while an attacker who sends his own XFF lands on idx=0, i.e. his
// own forged value, escaping the shared bucket and rotating it at will. That matters more
// now than it used to: this function is the only limiter identity in front of the
// UNAUTHENTICATED /api/p/{token} endpoint, and it also stamps the audit log's "ip" field.
//
// Settle it against the real edge, then fix the math (and this test) if needed:
//
//	curl -H 'X-Forwarded-For: 9.9.9.9' https://backend-beta.grbpwr.com/api/p/<valid-token>
//
// and read the `ip` field of the emitted "pattern access" log line. 9.9.9.9 in the log
// means the code trusts a client-supplied value and the hop count is off by one.
func TestGetClientIPHopMath(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxyHops(defaultTrustedProxyHops) })
	SetTrustedProxyHops(1)

	cases := []struct {
		name       string
		xff        string
		remoteAddr string
		want       string
	}{
		{
			// The interpretation the code implements: [client, edge], one trusted hop,
			// so the entry one from the right is the client.
			name: "two element chain picks the left entry", xff: "203.0.113.7, 10.0.0.1",
			remoteAddr: "10.0.0.1:5000", want: "203.0.113.7",
		},
		{
			// The interpretation the header comment implies. Under it the honest case
			// yields idx = -1 and everyone shares the RemoteAddr bucket.
			name: "single element chain falls back to RemoteAddr", xff: "203.0.113.7",
			remoteAddr: "10.0.0.1:5000", want: "10.0.0.1",
		},
		{
			// A forged header on a single-hop platform: the attacker's own left entry is
			// selected. This is the case the beta curl above is meant to expose.
			name: "forged two element chain is trusted", xff: "9.9.9.9, 203.0.113.7",
			remoteAddr: "10.0.0.1:5000", want: "9.9.9.9",
		},
		{
			name: "no xff uses RemoteAddr", xff: "",
			remoteAddr: "198.51.100.4:1234", want: "198.51.100.4",
		},
		{
			name: "invalid selected entry falls back", xff: "not-an-ip, 10.0.0.1",
			remoteAddr: "198.51.100.4:1234", want: "198.51.100.4",
		},
		{
			name: "spaces are trimmed", xff: "  203.0.113.7 ,  10.0.0.1 ",
			remoteAddr: "10.0.0.1:5000", want: "203.0.113.7",
		},
		{
			name: "bare RemoteAddr host", xff: "",
			remoteAddr: "198.51.100.4", want: "198.51.100.4",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/p/x", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := ClientIPFromRequest(r); got != c.want {
				t.Fatalf("ClientIPFromRequest = %q, want %q", got, c.want)
			}
		})
	}
}

// TestSetTrustedProxyHopsIgnoresNonPositive locks the anti-footgun: a zero/negative
// configuration must not disable spoofing protection by selecting the left-most entry.
func TestSetTrustedProxyHopsIgnoresNonPositive(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxyHops(defaultTrustedProxyHops) })
	SetTrustedProxyHops(2)
	SetTrustedProxyHops(0)
	SetTrustedProxyHops(-3)

	r := httptest.NewRequest(http.MethodGet, "/api/p/x", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.7, 10.0.0.1")
	// A non-positive value does not merely fail to apply — it restores the secure
	// DEFAULT (one hop), so the entry one from the right wins, never the left-most
	// (attacker-controllable) one.
	if got := ClientIPFromRequest(r); got != "203.0.113.7" {
		t.Fatalf("non-positive hops must fall back to the default, got %q", got)
	}
}
