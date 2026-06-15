package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

type contextKey string

const (
	ClientIPKey      contextKey = "client_ip"
	ClientSessionKey contextKey = "client_session"
)

// defaultTrustedProxyHops is the secure default number of trusted reverse-proxy
// hops in front of this service. On DigitalOcean App Platform exactly one
// trusted edge proxy sits in front of the app and appends the real client IP as
// the right-most entry of X-Forwarded-For, so 1 is the correct value there.
const defaultTrustedProxyHops = 1

// trustedProxyHops holds the number of trusted proxy hops used when parsing
// X-Forwarded-For. It is read on every request via atomic load so it can be
// configured at startup (see SetTrustedProxyHops) without locking on the hot
// path. It defaults to defaultTrustedProxyHops so that, even if configuration
// is never applied, the behaviour stays secure (anti-spoofing) rather than
// trusting the attacker-controllable left-most XFF entry.
var trustedProxyHops int64 = defaultTrustedProxyHops

// SetTrustedProxyHops configures how many trusted reverse-proxy hops sit in
// front of this service. The value is the count of proxies the platform's
// trusted infrastructure contributes to the right side of X-Forwarded-For
// (e.g. 1 for DigitalOcean App Platform's single edge proxy). A non-positive
// value is ignored and the secure default (defaultTrustedProxyHops) is kept,
// preventing accidental misconfiguration from disabling spoofing protection.
func SetTrustedProxyHops(hops int) {
	if hops <= 0 {
		hops = defaultTrustedProxyHops
	}
	atomic.StoreInt64(&trustedProxyHops, int64(hops))
}

// ClientIdentifier extracts client IP and generates session fingerprint
func ClientIdentifier(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract IP
		ip := getClientIP(r)

		// Generate session fingerprint from headers
		fingerprint := generateFingerprint(r)

		ctx := context.WithValue(r.Context(), ClientIPKey, ip)
		ctx = context.WithValue(ctx, ClientSessionKey, fingerprint)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// getClientIP extracts the real client IP from request headers in a way that is
// robust against X-Forwarded-For spoofing.
//
// X-Forwarded-For is a comma-separated chain that grows on the RIGHT as each
// proxy appends the IP it observed: "<client>, <proxy1>, <proxy2>". Only the
// right-most entries — those appended by infrastructure WE trust — are
// authentic; everything to the left can be forged by the client. Blindly taking
// the left-most entry (the previous behaviour) lets an attacker send an
// arbitrary or rotating X-Forwarded-For header to evade per-IP rate limiting and
// login throttling.
//
// We therefore skip `trustedProxyHops` entries from the right (the ones our
// trusted proxies appended) and use the next entry to the left — the first IP
// the trusted infrastructure did NOT let the client forge. Entries are validated
// with net.ParseIP. If X-Forwarded-For is absent, too short for the configured
// hop count, or the selected entry is not a valid IP, we fall back to
// RemoteAddr, which the client cannot spoof.
func getClientIP(r *http.Request) string {
	hops := int(atomic.LoadInt64(&trustedProxyHops))
	if hops <= 0 {
		hops = defaultTrustedProxyHops
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")

		// Index of the entry that is `hops` from the right. With a single
		// trusted hop and chain [client, edge], this selects `client`.
		idx := len(parts) - 1 - hops
		if idx >= 0 {
			if ip := normalizeIP(parts[idx]); ip != "" {
				return ip
			}
		}
		// Chain shorter than expected or selected entry invalid: fall through
		// to RemoteAddr rather than trusting an attacker-controllable value.
	}

	// Fall back to RemoteAddr (the immediate peer; cannot be spoofed at the
	// application layer). It is typically "host:port" but may be a bare host.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := normalizeIP(host); ip != "" {
			return ip
		}
	}
	if ip := normalizeIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

// normalizeIP trims surrounding whitespace and returns the canonical string
// form of the IP if it parses, or "" if the value is not a valid IP address.
func normalizeIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return ""
}

// generateFingerprint creates a unique session identifier from request headers
func generateFingerprint(r *http.Request) string {
	// Combine multiple headers for fingerprinting
	data := strings.Join([]string{
		r.Header.Get("User-Agent"),
		r.Header.Get("Accept-Language"),
		r.Header.Get("Accept-Encoding"),
		getClientIP(r),
	}, "|")

	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:16]) // Use first 16 bytes for shorter ID
}

// GetClientIP retrieves the client IP from context
func GetClientIP(ctx context.Context) string {
	if ip, ok := ctx.Value(ClientIPKey).(string); ok {
		return ip
	}
	return "unknown"
}

// GetClientSession retrieves the client session fingerprint from context
func GetClientSession(ctx context.Context) string {
	if session, ok := ctx.Value(ClientSessionKey).(string); ok {
		return session
	}
	return "unknown"
}
