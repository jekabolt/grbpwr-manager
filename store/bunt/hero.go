package bunt

import (
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/tidwall/buntdb"
)

const (
	heroKey = "hero"
)

func (db *BuntDB) UpsertHero(h *store.Hero) (*store.Hero, error) {
	return h, db.hero.Update(func(tx *buntdb.Tx) error {
		tx.Set(heroKey, h.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetHero() (*store.Hero, error) {
	h := &store.Hero{}
	err := db.hero.View(func(tx *buntdb.Tx) error {
		heroStr, err := tx.Get(heroKey)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(heroStr), h)
	})

	if err != nil {
		return nil, fmt.Errorf("GetHero [%v]", err.Error())
	}

	return h, err
}
