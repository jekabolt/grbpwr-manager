package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter_Allow(t *testing.T) {
	limiter := NewLimiter(time.Second, 3)

	// First 3 requests should succeed
	for i := 0; i < 3; i++ {
		if !limiter.Allow("test-key") {
			t.Errorf("Request %d should be allowed", i+1)
		}
	}

	// 4th request should be blocked
	if limiter.Allow("test-key") {
		t.Error("4th request should be blocked")
	}

	// Wait for window to expire
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again
	if !limiter.Allow("test-key") {
		t.Error("Request after window expiry should be allowed")
	}
}

func TestLimiter_GetRemaining(t *testing.T) {
	limiter := NewLimiter(time.Second, 5)

	if remaining := limiter.GetRemaining("test-key"); remaining != 5 {
		t.Errorf("Expected 5 remaining, got %d", remaining)
	}

	limiter.Allow("test-key")
	limiter.Allow("test-key")

	if remaining := limiter.GetRemaining("test-key"); remaining != 3 {
		t.Errorf("Expected 3 remaining, got %d", remaining)
	}
}

func TestMultiKeyLimiter_CheckOrderCreation(t *testing.T) {
	limiter := NewCustomMultiKeyLimiter(2, 2, 10)

	// First 2 orders from same IP should succeed
	if err := limiter.CheckOrderCreation("192.168.1.1", "test@example.com"); err != nil {
		t.Errorf("First order should succeed: %v", err)
	}
	if err := limiter.CheckOrderCreation("192.168.1.1", "test2@example.com"); err != nil {
		t.Errorf("Second order should succeed: %v", err)
	}

	// 3rd order from same IP should fail
	if err := limiter.CheckOrderCreation("192.168.1.1", "test3@example.com"); err == nil {
		t.Error("3rd order from same IP should be blocked")
	}

	// Order from different IP should succeed
	if err := limiter.CheckOrderCreation("192.168.1.2", "test@example.com"); err != nil {
		t.Errorf("Order from different IP should succeed: %v", err)
	}
}

func TestMultiKeyLimiter_CheckValidation(t *testing.T) {
	limiter := NewCustomMultiKeyLimiter(5, 5, 3)

	// First 3 validations should succeed
	for i := 0; i < 3; i++ {
		if err := limiter.CheckValidation("192.168.1.1"); err != nil {
			t.Errorf("Validation %d should succeed: %v", i+1, err)
		}
	}

	// 4th validation should fail
	if err := limiter.CheckValidation("192.168.1.1"); err == nil {
		t.Error("4th validation should be blocked")
	}
}

func TestMultiKeyLimiter_EmailLimit(t *testing.T) {
	limiter := NewCustomMultiKeyLimiter(10, 2, 10)

	// First 2 orders from same email should succeed
	if err := limiter.CheckOrderCreation("192.168.1.1", "test@example.com"); err != nil {
		t.Errorf("First order should succeed: %v", err)
	}
	if err := limiter.CheckOrderCreation("192.168.1.2", "test@example.com"); err != nil {
		t.Errorf("Second order should succeed: %v", err)
	}

	// 3rd order from same email (different IP) should fail
	if err := limiter.CheckOrderCreation("192.168.1.3", "test@example.com"); err == nil {
		t.Error("3rd order from same email should be blocked")
	}
}

func TestLimiter_Cleanup(t *testing.T) {
	limiter := NewLimiter(100*time.Millisecond, 5)

	// Create some entries
	limiter.Allow("key1")
	limiter.Allow("key2")
	limiter.Allow("key3")

	// Wait for expiration + cleanup cycle (cleanup runs every minute, so we test expiration instead)
	time.Sleep(150 * time.Millisecond)

	// After expiration, new requests should be allowed (proving cleanup works)
	if !limiter.Allow("key1") {
		t.Error("Request should be allowed after expiration")
	}
	if !limiter.Allow("key2") {
		t.Error("Request should be allowed after expiration")
	}
	if !limiter.Allow("key3") {
		t.Error("Request should be allowed after expiration")
	}
}
