package cache

import (
	"fmt"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type SizeCache struct {
	IDCache map[entity.SizeEnum]entity.Size // name to size
	Cache   map[int]entity.Size
	Mutex   sync.RWMutex
}

func newSizeCache(sizes []entity.Size) (*SizeCache, error) {
	c := &SizeCache{
		Cache:   make(map[int]entity.Size),
		IDCache: make(map[entity.SizeEnum]entity.Size),
	}
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	for _, size := range sizes {
		if !entity.ValidSizes[entity.SizeEnum(size.Name)] {
			return nil, fmt.Errorf("invalid size name")
		}
		c.Cache[size.ID] = size
		c.IDCache[entity.SizeEnum(size.Name)] = size
	}

	if len(c.Cache) != len(entity.ValidSizes) {
		return nil, fmt.Errorf("not all sizes are filled with an ID")
	}

	return c, nil
}

func (c *SizeCache) GetSizeByID(id int) (*entity.Size, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	size, found := c.Cache[id]
	return &size, found
}

func (c *SizeCache) GetSizesByName(size entity.SizeEnum) (entity.Size, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	sz, found := c.IDCache[size]
	return sz, found
}

func (c *SizeCache) GetAllSizes() map[int]entity.Size {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()
	return c.Cache
}
