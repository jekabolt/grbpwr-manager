package cache

import (
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type PromoCache struct {
	IDCache map[string]entity.PromoCode // promo to promo entity
	Cache   map[int]entity.PromoCode
	Mutex   sync.RWMutex
}

func newPromoCache(categories []entity.PromoCode) *PromoCache {
	c := &PromoCache{
		Cache:   make(map[int]entity.PromoCode),
		IDCache: make(map[string]entity.PromoCode),
	}
	c.Mutex.Lock()
	defer c.Mutex.Unlock()
	for _, category := range categories {
		c.IDCache[category.Code] = category
		c.Cache[category.ID] = category
	}
	return c
}

func (c *PromoCache) GetPromoByID(id int) (*entity.PromoCode, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	promo, found := c.Cache[id]

	if promo.Expiration < time.Now().Unix() || !promo.Allowed {
		return &promo, false
	}
	return &promo, found
}

// GetPromoByName fetches category by category from cache
func (c *PromoCache) GetPromoByName(category string) (entity.PromoCode, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	m, found := c.IDCache[category]

	if m.Expiration < time.Now().Unix() || !m.Allowed {
		return m, false
	}

	return m, found
}

// GetAllPromos fetches all categories from cache
func (c *PromoCache) GetAllPromos() map[int]entity.PromoCode {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()
	return c.Cache
}
