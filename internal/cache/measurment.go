package cache

import (
	"fmt"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type MeasurementCache struct {
	IDCache map[entity.MeasurementNameEnum]entity.MeasurementName // name to MeasurementName
	Cache   map[int]entity.MeasurementName
	Mutex   sync.RWMutex
}

func newMeasurementCache(measurements []entity.MeasurementName) (*MeasurementCache, error) {
	c := &MeasurementCache{
		Cache:   make(map[int]entity.MeasurementName),
		IDCache: make(map[entity.MeasurementNameEnum]entity.MeasurementName),
	}
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	for _, measurement := range measurements {
		if !entity.ValidMeasurementNames[entity.MeasurementNameEnum(measurement.Name)] {
			return nil, fmt.Errorf("invalid measurement name")
		}
		c.Cache[measurement.ID] = measurement
		c.IDCache[entity.MeasurementNameEnum(measurement.Name)] = measurement
	}

	if len(c.Cache) != len(entity.ValidMeasurementNames) {
		return nil, fmt.Errorf("not all measurements are filled with an ID")
	}

	return c, nil
}

func (c *MeasurementCache) GetMeasurementById(id int) (*entity.MeasurementName, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	measurement, found := c.Cache[id]
	return &measurement, found
}

func (c *MeasurementCache) GetMeasurementsByName(measurement entity.MeasurementNameEnum) (entity.MeasurementName, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	m, found := c.IDCache[measurement]
	return m, found
}

func (c *MeasurementCache) GetAllMeasurements() map[int]entity.MeasurementName {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()
	return c.Cache
}
