package cache

import (
	"fmt"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// PaymentMethodCache definition
type PaymentMethodCache struct {
	IDCache map[entity.PaymentMethodName]entity.PaymentMethod // name to size
	Cache   map[int]entity.PaymentMethod
	Mutex   sync.RWMutex
}

func newPaymentMethodCache(paymentMethods []entity.PaymentMethod) (*PaymentMethodCache, error) {
	c := &PaymentMethodCache{
		Cache:   make(map[int]entity.PaymentMethod),
		IDCache: make(map[entity.PaymentMethodName]entity.PaymentMethod),
	}
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	// Check that all methods are from the enum
	for _, paymentMethod := range paymentMethods {
		if !entity.ValidPaymentMethodNames[entity.PaymentMethodName(paymentMethod.Name)] {
			return nil, fmt.Errorf("invalid payment method name")
		}
		c.Cache[paymentMethod.ID] = paymentMethod
		c.IDCache[paymentMethod.Name] = paymentMethod
	}

	// Check if every method is filled with an ID
	if len(c.Cache) != len(entity.ValidPaymentMethodNames) {
		return nil, fmt.Errorf("not all methods are filled with an ID")
	}

	return c, nil
}

// GetPaymentMethodById fetches PaymentMethod by ID from PaymentMethodCache
func (c *PaymentMethodCache) GetPaymentMethodById(id int) (*entity.PaymentMethod, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	paymentMethod, found := c.Cache[id]
	return &paymentMethod, found
}

// GetPaymentMethodsByName fetches PaymentMethod by PaymentMethodName from PaymentMethodCache
func (c *PaymentMethodCache) GetPaymentMethodsByName(paymentMethod entity.PaymentMethodName) (entity.PaymentMethod, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	pm, found := c.IDCache[paymentMethod]
	return pm, found
}

func (c *PaymentMethodCache) UpdatePaymentMethodAllowance(pm string, allowance bool) error {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	paymentMethod, found := c.IDCache[entity.PaymentMethodName(pm)]
	if !found {
		return fmt.Errorf("payment method not found")
	}
	paymentMethod.Allowed = allowance
	c.IDCache[entity.PaymentMethodName(pm)] = paymentMethod
	c.Cache[paymentMethod.ID] = paymentMethod
	return nil
}

// GetAllPaymentMethods fetches all PaymentMethods from PaymentMethodCache
func (c *PaymentMethodCache) GetAllPaymentMethods() map[int]entity.PaymentMethod {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()
	return c.Cache
}
