package preorderpayment

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Session holds pre-order payment session state for idempotency.
type Session struct {
	PaymentIntentID      string
	ClientSecret         string
	StripeIdempotencyKey string
	CartFingerprint      string
	ExpiresAt            time.Time
	CreatedAt            time.Time
}

// Store manages pre-order payment sessions in memory with TTL-based expiration.
type Store struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	ttl        time.Duration
	cleanupCh  chan struct{}
}

// NewStore creates a new pre-order payment session store with the given TTL.
func NewStore(ttl time.Duration) *Store {
	s := &Store{
		sessions:  make(map[string]*Session),
		ttl:       ttl,
		cleanupCh: make(chan struct{}),
	}
	go s.cleanup()
	return s
}

// Get returns the session for the given idempotency key, if it exists and has not expired.
func (s *Store) Get(key string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[key]
	if !ok || sess == nil || time.Now().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}

// Put stores a session for the given key with TTL. If key is empty, generates a new UUID.
func (s *Store) Put(key string, sess *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		key = uuid.New().String()
	}
	sess.ExpiresAt = time.Now().Add(s.ttl)
	sess.CreatedAt = time.Now()
	s.sessions[key] = sess
	return key
}

// Delete removes a session by key.
func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

// cleanup periodically removes expired sessions.
func (s *Store) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.cleanupCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			expired := 0
			for k, sess := range s.sessions {
				if now.After(sess.ExpiresAt) {
					delete(s.sessions, k)
					expired++
				}
			}
			if expired > 0 {
				slog.Default().DebugContext(context.Background(), "preorder payment session cleanup", "expired", expired)
			}
			s.mu.Unlock()
		}
	}
}

// Stop stops the cleanup goroutine.
func (s *Store) Stop() {
	close(s.cleanupCh)
}
