package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// Limiter implements a simple in-memory sliding window rate limiter
type Limiter struct {
	mu       sync.RWMutex
	counters map[string]*counter
	window   time.Duration
	max      int
}

type counter struct {
	count     int
	expiresAt time.Time
}

// NewLimiter creates a new rate limiter with the specified window and max requests
func NewLimiter(window time.Duration, max int) *Limiter {
	l := &Limiter{
		counters: make(map[string]*counter),
		window:   window,
		max:      max,
	}
	go l.cleanup()
	return l
}

// Allow checks if a request for the given key is allowed
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	c, exists := l.counters[key]

	if !exists || now.After(c.expiresAt) {
		l.counters[key] = &counter{
			count:     1,
			expiresAt: now.Add(l.window),
		}
		return true
	}

	if c.count >= l.max {
		return false
	}

	c.count++
	return true
}

// GetRemaining returns the number of remaining requests for the given key
func (l *Limiter) GetRemaining(key string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	now := time.Now()
	c, exists := l.counters[key]

	if !exists || now.After(c.expiresAt) {
		return l.max
	}

	remaining := l.max - c.count
	if remaining < 0 {
		return 0
	}
	return remaining
}

// cleanup periodically removes expired counters
func (l *Limiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		l.mu.Lock()
		now := time.Now()
		for key, c := range l.counters {
			if now.After(c.expiresAt) {
				delete(l.counters, key)
			}
		}
		l.mu.Unlock()
	}
}

// MultiKeyLimiter manages multiple rate limiters for different types of operations
type MultiKeyLimiter struct {
	limiters map[string]*Limiter
	mu       sync.RWMutex
}

// NewMultiKeyLimiter creates a new multi-key limiter with default limits
func NewMultiKeyLimiter() *MultiKeyLimiter {
	return &MultiKeyLimiter{
		limiters: map[string]*Limiter{
			"ip_order":      NewLimiter(time.Hour, 100),  // 100 orders per IP per hour
			"email_order":   NewLimiter(time.Hour, 100),  // 100 orders per email per hour
			"ip_validate":   NewLimiter(time.Minute, 20), // 20 validations per IP per minute
			"ip_support":    NewLimiter(time.Hour, 2),    // 5 tickets per IP per hour
			"email_support": NewLimiter(time.Hour, 1),    // 3 tickets per email per hour
		},
	}
}

// NewCustomMultiKeyLimiter creates a limiter with custom limits
func NewCustomMultiKeyLimiter(ipOrderLimit, emailOrderLimit, ipValidateLimit int) *MultiKeyLimiter {
	return &MultiKeyLimiter{
		limiters: map[string]*Limiter{
			"ip_order":    NewLimiter(time.Hour, ipOrderLimit),
			"email_order": NewLimiter(time.Hour, emailOrderLimit),
			"ip_validate": NewLimiter(time.Minute, ipValidateLimit),
		},
	}
}

// CheckOrderCreation verifies if an order can be created from the given IP and email
func (m *MultiKeyLimiter) CheckOrderCreation(ip, email string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.limiters["ip_order"].Allow(ip) {
		return fmt.Errorf("too many orders from this IP address, please try again later")
	}

	if email != "" && !m.limiters["email_order"].Allow(email) {
		return fmt.Errorf("too many orders from this email address, please try again later")
	}

	return nil
}

// CheckValidation verifies if a validation request is allowed from the given IP
func (m *MultiKeyLimiter) CheckValidation(ip string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.limiters["ip_validate"].Allow(ip) {
		return fmt.Errorf("too many validation requests, please slow down")
	}

	return nil
}

// CheckSupportTicket verifies if a support ticket can be submitted from the given IP and email
func (m *MultiKeyLimiter) CheckSupportTicket(ip, email string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.limiters["ip_support"].Allow(ip) {
		return fmt.Errorf("too many support tickets from this IP address, please try again later")
	}

	if email != "" && !m.limiters["email_support"].Allow(email) {
		return fmt.Errorf("too many support tickets from this email address, please try again later")
	}

	return nil
}

// GetOrderLimits returns remaining order attempts for IP and email
func (m *MultiKeyLimiter) GetOrderLimits(ip, email string) (ipRemaining, emailRemaining int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ipRemaining = m.limiters["ip_order"].GetRemaining(ip)
	if email != "" {
		emailRemaining = m.limiters["email_order"].GetRemaining(email)
	} else {
		emailRemaining = -1 // not applicable
	}

	return ipRemaining, emailRemaining
}
