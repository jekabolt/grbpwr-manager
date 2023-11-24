package cache

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type HeroCache struct {
	hero *entity.HeroFull
}

func newHeroCache(hero *entity.HeroFull) *HeroCache {
	return &HeroCache{
		hero: hero,
	}
}

func (hc *HeroCache) GetHero() *entity.HeroFull {
	return hc.hero
}

func (hc *HeroCache) UpdateHero(hf *entity.HeroFull) {
	hc.hero = hf
}
