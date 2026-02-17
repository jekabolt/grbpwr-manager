package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

type contextKey string

const (
	ClientIPKey      contextKey = "client_ip"
	ClientSessionKey contextKey = "client_session"
)

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

// getClientIP extracts the real client IP from request headers
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (proxy/load balancer)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Check CF-Connecting-IP (Cloudflare)
	if cfip := r.Header.Get("CF-Connecting-IP"); cfip != "" {
		return cfip
	}

	// Fall back to RemoteAddr
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
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
