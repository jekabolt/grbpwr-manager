package cache

import (
	"errors"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// OrderStatusCache definition
type OrderStatusCache struct {
	IDCache map[entity.OrderStatusName]entity.OrderStatus // name to OrderStatus
	Cache   map[int]entity.OrderStatus
	Mutex   sync.RWMutex
}

func newOrderStatusCache(orderStatuses []entity.OrderStatus) (*OrderStatusCache, error) {
	c := &OrderStatusCache{
		Cache:   make(map[int]entity.OrderStatus),
		IDCache: make(map[entity.OrderStatusName]entity.OrderStatus),
	}
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	// Check that all statuses are from the enum
	for _, orderStatus := range orderStatuses {
		if !entity.ValidOrderStatusNames[entity.OrderStatusName(orderStatus.Name)] {
			return nil, errors.New("invalid order status name")
		}
		c.Cache[orderStatus.ID] = orderStatus
		c.IDCache[orderStatus.Name] = orderStatus
	}

	// Check if every status is filled with an ID
	if len(c.Cache) != len(entity.ValidOrderStatusNames) {
		return nil, errors.New("not all statuses are filled with an ID")
	}

	return c, nil
}

// GetOrderStatusByID fetches OrderStatusName by ID from OrderStatusCache
func (c *OrderStatusCache) GetOrderStatusByID(id int) (*entity.OrderStatus, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	orderStatus, found := c.Cache[id]
	return &orderStatus, found
}

// GetOrderStatusByName fetches OrderStatus by OrderStatusName from OrderStatusCache
func (c *OrderStatusCache) GetOrderStatusByName(orderStatus entity.OrderStatusName) (entity.OrderStatus, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	m, found := c.IDCache[orderStatus]
	return m, found
}

// GetAllOrderStatuses fetches all OrderStatuses from OrderStatusCache
func (c *OrderStatusCache) GetAllOrderStatuses() map[int]entity.OrderStatus {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()
	return c.Cache
}
