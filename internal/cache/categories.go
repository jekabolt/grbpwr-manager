package cache

import (
	"fmt"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type CategoryCache struct {
	IDCache map[entity.CategoryEnum]entity.Category // name to category
	Cache   map[int]entity.Category
	Mutex   sync.RWMutex
}

func newCategoryCache(categories []entity.Category) (*CategoryCache, error) {
	c := &CategoryCache{
		Cache:   make(map[int]entity.Category),
		IDCache: make(map[entity.CategoryEnum]entity.Category),
	}
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	for _, category := range categories {
		if !entity.ValidCategories[entity.CategoryEnum(category.Name)] {
			return nil, fmt.Errorf("invalid category name")
		}
		c.Cache[category.ID] = category
		c.IDCache[entity.CategoryEnum(category.Name)] = category
	}

	if len(c.Cache) != len(entity.ValidCategories) {
		return nil, fmt.Errorf("not all categories are filled with an ID")
	}

	return c, nil
}

func (c *CategoryCache) GetCategoryByID(id int) (*entity.Category, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	category, found := c.Cache[id]
	return &category, found
}

func (c *CategoryCache) GetCategoryByName(category entity.CategoryEnum) (entity.Category, bool) {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()

	ct, found := c.IDCache[category]
	return ct, found
}

func (c *CategoryCache) GetAllCategories() map[int]entity.Category {
	c.Mutex.RLock()
	defer c.Mutex.RUnlock()
	return c.Cache
}
