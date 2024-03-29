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

func (c *PromoCache) GetPromoById(id int) (*entity.PromoCode, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	promo, found := c.Cache[id]

	if !promo.Allowed || promo.Expiration.Before(time.Now()) {
		return &promo, false
	}

	return &promo, found
}

// GetPromoByName fetches category by category from cache
func (c *PromoCache) GetPromoByName(category string) (entity.PromoCode, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	promo, found := c.IDCache[category]

	if !promo.Allowed || promo.Expiration.Before(time.Now()) {
		return promo, false
	}

	return promo, found
}

func (c *PromoCache) AddPromo(promo entity.PromoCode) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	c.IDCache[promo.Code] = promo
	c.Cache[promo.ID] = promo
}

func (c *PromoCache) DeletePromo(code string) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	delete(c.Cache, c.IDCache[code].ID)
	delete(c.IDCache, code)
}

func (c *PromoCache) DisablePromo(code string) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	codeNormal, ok := c.Cache[c.IDCache[code].ID]
	if !ok {
		return
	}
	codeNormal.Allowed = false
	c.Cache[c.IDCache[code].ID] = codeNormal

	codeIdc, ok := c.IDCache[code]
	if !ok {
		return
	}
	codeIdc.Allowed = false
	c.IDCache[code] = codeIdc

}
