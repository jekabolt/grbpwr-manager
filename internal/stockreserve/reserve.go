package stockreserve

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// Limits configures abuse prevention thresholds
type Limits struct {
	MaxItemsPerSession    int             // max distinct product-size reservations per session
	MaxQtyPerItem         decimal.Decimal // max quantity per single product-size per session
	MaxTotalReservations  int             // global cap on all reservations (memory protection)
	MaxTTLRefreshes       int             // max times a reservation's TTL can be refreshed
	ReserveRatePerSession int             // max Reserve() calls per session per minute
}

// DefaultLimits returns sane defaults for a small-medium store
func DefaultLimits() Limits {
	return Limits{
		MaxItemsPerSession:    10,
		MaxQtyPerItem:         decimal.NewFromInt(5),
		MaxTotalReservations:  10000,
		MaxTTLRefreshes:       3,
		ReserveRatePerSession: 30,
	}
}

// Reservation represents a temporary stock hold
type Reservation struct {
	ProductID    int
	SizeID       int
	Quantity     decimal.Decimal
	ExpiresAt    time.Time
	CreatedAt    time.Time
	OrderUUID    string // empty for cart, set when order created
	SessionID    string
	RefreshCount int // how many times TTL was refreshed
}

// productSizeKey is the composite key for the per-product-size index
type productSizeKey struct {
	ProductID int
	SizeID    int
}

// sessionRateEntry tracks per-session Reserve call rate
type sessionRateEntry struct {
	count     int
	expiresAt time.Time
}

// Manager handles stock reservations with TTL and abuse prevention
type Manager struct {
	mu           sync.RWMutex
	reservations map[string]*Reservation        // key: "productID-sizeID-sessionID"
	bySession    map[string][]string             // sessionID -> reservation keys
	byOrder      map[string][]string             // orderUUID -> reservation keys
	byProductSize map[productSizeKey][]string    // (productID, sizeID) -> reservation keys (O(1) lookup)
	sessionRates map[string]*sessionRateEntry    // per-session call rate
	cartTTL      time.Duration
	orderTTL     time.Duration
	limits       Limits
	stopCh       chan struct{}
}

// NewManager creates a new reservation manager with abuse prevention
func NewManager(cartTTL, orderTTL time.Duration, limits Limits) *Manager {
	rm := &Manager{
		reservations:  make(map[string]*Reservation),
		bySession:     make(map[string][]string),
		byOrder:       make(map[string][]string),
		byProductSize: make(map[productSizeKey][]string),
		sessionRates:  make(map[string]*sessionRateEntry),
		cartTTL:       cartTTL,
		orderTTL:      orderTTL,
		limits:        limits,
		stopCh:        make(chan struct{}),
	}
	go rm.cleanup()
	return rm
}

// NewDefaultManager creates a manager with default TTLs and limits
func NewDefaultManager() *Manager {
	return NewManager(15*time.Minute, 30*time.Minute, DefaultLimits())
}

// Stop gracefully shuts down the cleanup goroutine
func (rm *Manager) Stop() {
	close(rm.stopCh)
}

// Reserve temporarily holds stock for a cart with abuse prevention checks
func (rm *Manager) Reserve(ctx context.Context, sessionID string, productID, sizeID int, qty decimal.Decimal) error {
	if qty.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("quantity must be positive")
	}

	if qty.GreaterThan(rm.limits.MaxQtyPerItem) {
		return fmt.Errorf("quantity %s exceeds maximum %s per item", qty.String(), rm.limits.MaxQtyPerItem.String())
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Rate limit per session
	if !rm.allowSessionRate(sessionID) {
		return fmt.Errorf("too many reservation requests, please slow down")
	}

	key := fmt.Sprintf("%d-%d-%s", productID, sizeID, sessionID)
	psKey := productSizeKey{ProductID: productID, SizeID: sizeID}
	existing, isUpdate := rm.reservations[key]

	// Per-session item count check (only for new reservations)
	if !isUpdate {
		sessionKeys := rm.bySession[sessionID]
		if len(sessionKeys) >= rm.limits.MaxItemsPerSession {
			return fmt.Errorf("maximum %d items per cart reached", rm.limits.MaxItemsPerSession)
		}
	}

	// Global capacity check (only for new reservations)
	if !isUpdate && len(rm.reservations) >= rm.limits.MaxTotalReservations {
		slog.Default().WarnContext(ctx, "global reservation capacity reached",
			slog.Int("capacity", rm.limits.MaxTotalReservations),
		)
		return fmt.Errorf("service is busy, please try again later")
	}

	now := time.Now()

	if isUpdate {
		// TTL refresh protection: don't extend expiry, keep original.
		// Only allow limited refreshes to prevent infinite holding.
		refreshCount := existing.RefreshCount
		expiry := existing.ExpiresAt
		createdAt := existing.CreatedAt

		if refreshCount >= rm.limits.MaxTTLRefreshes {
			// Allow quantity update but don't extend TTL at all
			existing.Quantity = qty
			slog.Default().DebugContext(ctx, "reservation quantity updated (TTL refresh limit reached)",
				slog.String("session_id", sessionID),
				slog.Int("product_id", productID),
				slog.Int("size_id", sizeID),
			)
			return nil
		}

		rm.reservations[key] = &Reservation{
			ProductID:    productID,
			SizeID:       sizeID,
			Quantity:     qty,
			ExpiresAt:    expiry, // preserve original expiry
			CreatedAt:    createdAt,
			SessionID:    sessionID,
			RefreshCount: refreshCount + 1,
		}
	} else {
		// New reservation
		rm.reservations[key] = &Reservation{
			ProductID: productID,
			SizeID:    sizeID,
			Quantity:  qty,
			ExpiresAt: now.Add(rm.cartTTL),
			CreatedAt: now,
			SessionID: sessionID,
		}

		// Track by session
		rm.bySession[sessionID] = append(rm.bySession[sessionID], key)

		// Track by product-size for O(1) quantity lookups
		rm.byProductSize[psKey] = append(rm.byProductSize[psKey], key)
	}

	slog.Default().DebugContext(ctx, "stock reserved",
		slog.String("session_id", sessionID),
		slog.Int("product_id", productID),
		slog.Int("size_id", sizeID),
		slog.String("quantity", qty.String()),
		slog.Bool("is_update", isUpdate),
	)

	return nil
}

// Commit converts cart reservation to order reservation (extends TTL to payment window)
func (rm *Manager) Commit(ctx context.Context, sessionID, orderUUID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	keys := rm.bySession[sessionID]
	newExpiry := time.Now().Add(rm.orderTTL)

	for _, key := range keys {
		if res, exists := rm.reservations[key]; exists {
			res.OrderUUID = orderUUID
			res.ExpiresAt = newExpiry
			res.RefreshCount = 0 // reset for order phase

			// Track by order
			rm.byOrder[orderUUID] = append(rm.byOrder[orderUUID], key)

			slog.Default().DebugContext(ctx, "reservation committed to order",
				slog.String("order_uuid", orderUUID),
				slog.String("session_id", sessionID),
				slog.Int("product_id", res.ProductID),
				slog.Int("size_id", res.SizeID),
			)
		}
	}
}

// Release frees reservation when order is paid/cancelled
func (rm *Manager) Release(ctx context.Context, orderUUID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	keys := rm.byOrder[orderUUID]
	for _, key := range keys {
		if res, exists := rm.reservations[key]; exists {
			slog.Default().DebugContext(ctx, "reservation released",
				slog.String("order_uuid", orderUUID),
				slog.Int("product_id", res.ProductID),
				slog.Int("size_id", res.SizeID),
			)
			rm.removeReservation(key, res)
		}
	}

	delete(rm.byOrder, orderUUID)
}

// ReleaseSession frees all reservations for a session (cart abandoned)
func (rm *Manager) ReleaseSession(ctx context.Context, sessionID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	keys := rm.bySession[sessionID]
	for _, key := range keys {
		if res, exists := rm.reservations[key]; exists && res.OrderUUID == "" {
			slog.Default().DebugContext(ctx, "session reservation released",
				slog.String("session_id", sessionID),
				slog.Int("product_id", res.ProductID),
				slog.Int("size_id", res.SizeID),
			)
			rm.removeReservation(key, res)
		}
	}

	delete(rm.bySession, sessionID)
	delete(rm.sessionRates, sessionID)
}

// GetReservedQuantity returns total reserved qty for a product-size (excluding current session)
// Uses the byProductSize index for O(k) where k = reservations for this product-size, not O(n) total.
func (rm *Manager) GetReservedQuantity(productID, sizeID int, excludeSessionID string) decimal.Decimal {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	total := decimal.Zero
	now := time.Now()

	psKey := productSizeKey{ProductID: productID, SizeID: sizeID}
	keys := rm.byProductSize[psKey]

	for _, key := range keys {
		if res, exists := rm.reservations[key]; exists {
			if now.Before(res.ExpiresAt) && res.SessionID != excludeSessionID {
				total = total.Add(res.Quantity)
			}
		}
	}

	return total
}

// GetAvailableStock returns actual available stock minus reservations
func (rm *Manager) GetAvailableStock(totalStock decimal.Decimal, productID, sizeID int, excludeSessionID string) decimal.Decimal {
	reserved := rm.GetReservedQuantity(productID, sizeID, excludeSessionID)
	available := totalStock.Sub(reserved)

	if available.LessThan(decimal.Zero) {
		return decimal.Zero
	}

	return available
}

// cleanup periodically removes expired reservations
func (rm *Manager) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-rm.stopCh:
			return
		case <-ticker.C:
			rm.mu.Lock()
			now := time.Now()
			expiredCount := 0

			for key, res := range rm.reservations {
				if now.After(res.ExpiresAt) {
					rm.removeReservation(key, res)

					// Clean up byOrder
					if res.OrderUUID != "" {
						if orderKeys, ok := rm.byOrder[res.OrderUUID]; ok {
							rm.byOrder[res.OrderUUID] = removeKey(orderKeys, key)
							if len(rm.byOrder[res.OrderUUID]) == 0 {
								delete(rm.byOrder, res.OrderUUID)
							}
						}
					}

					expiredCount++
				}
			}

			// Clean up expired session rate entries
			for sid, entry := range rm.sessionRates {
				if now.After(entry.expiresAt) {
					delete(rm.sessionRates, sid)
				}
			}

			if expiredCount > 0 {
				slog.Default().Debug("cleaned up expired reservations",
					slog.Int("count", expiredCount),
					slog.Int("remaining", len(rm.reservations)),
				)
			}

			rm.mu.Unlock()
		}
	}
}

// removeReservation removes a reservation and cleans up all indexes.
// Must be called with rm.mu held.
func (rm *Manager) removeReservation(key string, res *Reservation) {
	delete(rm.reservations, key)

	// Clean up bySession
	if sessionKeys, ok := rm.bySession[res.SessionID]; ok {
		rm.bySession[res.SessionID] = removeKey(sessionKeys, key)
		if len(rm.bySession[res.SessionID]) == 0 {
			delete(rm.bySession, res.SessionID)
		}
	}

	// Clean up byProductSize
	psKey := productSizeKey{ProductID: res.ProductID, SizeID: res.SizeID}
	if psKeys, ok := rm.byProductSize[psKey]; ok {
		rm.byProductSize[psKey] = removeKey(psKeys, key)
		if len(rm.byProductSize[psKey]) == 0 {
			delete(rm.byProductSize, psKey)
		}
	}
}

// allowSessionRate checks if a session is within its Reserve call rate limit.
// Must be called with rm.mu held.
func (rm *Manager) allowSessionRate(sessionID string) bool {
	now := time.Now()
	entry, exists := rm.sessionRates[sessionID]

	if !exists || now.After(entry.expiresAt) {
		rm.sessionRates[sessionID] = &sessionRateEntry{
			count:     1,
			expiresAt: now.Add(time.Minute),
		}
		return true
	}

	if entry.count >= rm.limits.ReserveRatePerSession {
		return false
	}

	entry.count++
	return true
}

// GetStats returns current reservation statistics
func (rm *Manager) GetStats() map[string]interface{} {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	cartReservations := 0
	orderReservations := 0

	for _, res := range rm.reservations {
		if res.OrderUUID == "" {
			cartReservations++
		} else {
			orderReservations++
		}
	}

	return map[string]interface{}{
		"total_reservations":  len(rm.reservations),
		"cart_reservations":   cartReservations,
		"order_reservations":  orderReservations,
		"active_sessions":     len(rm.bySession),
		"active_orders":       len(rm.byOrder),
		"capacity_pct":        float64(len(rm.reservations)) / float64(rm.limits.MaxTotalReservations) * 100,
	}
}

// Helper functions
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func removeKey(slice []string, key string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != key {
			result = append(result, s)
		}
	}
	return result
}
