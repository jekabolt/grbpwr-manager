package cache

import (
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ShipmentCarrierCache definition
type ShipmentCarrierCache struct {
	IDCache map[string]entity.ShipmentCarrier // Carrier to ShipmentCarrier
	Cache   map[int]entity.ShipmentCarrier
	Mutex   sync.RWMutex
}

func newShipmentCarrierCache(shipmentCarriers []entity.ShipmentCarrier) *ShipmentCarrierCache {
	c := &ShipmentCarrierCache{
		Cache:   make(map[int]entity.ShipmentCarrier),
		IDCache: make(map[string]entity.ShipmentCarrier),
	}
	c.Mutex.Lock()
	defer c.Mutex.Unlock()
	for _, shipmentCarrier := range shipmentCarriers {
		c.Cache[shipmentCarrier.ID] = shipmentCarrier
		c.IDCache[shipmentCarrier.Carrier] = shipmentCarrier
	}
	return c
}

// GetShipmentCarrierById fetches ShipmentCarrier by ID from ShipmentCarrierCache
func (c *ShipmentCarrierCache) GetShipmentCarrierById(id int) (*entity.ShipmentCarrier, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	shipmentCarrier, found := c.Cache[id]
	return &shipmentCarrier, found
}

// GetShipmentCarriersByShipmentCarrier fetches ShipmentCarriers by ShipmentCarrier from ShipmentCarrierCache
func (c *ShipmentCarrierCache) GetShipmentCarriersByName(carrier string) (entity.ShipmentCarrier, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	pm, found := c.IDCache[carrier]
	return pm, found
}

// GetAllShipmentCarriers fetches all ShipmentCarriers from ShipmentCarrierCache
func (c *ShipmentCarrierCache) GetAllShipmentCarriers() map[int]entity.ShipmentCarrier {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()
	return c.Cache
}
