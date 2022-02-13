package bunt

import (
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/tidwall/buntdb"
)

func (db *BuntDB) AddCollection(c *store.Collection) (*store.Collection, error) {
	return c, db.collections.Update(func(tx *buntdb.Tx) error {
		tx.Set(c.Season, c.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetCollectionBySeason(season string) (*store.Collection, error) {
	prd := &store.Collection{}
	err := db.collections.View(func(tx *buntdb.Tx) error {
		articleStr, err := tx.Get(season)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(articleStr), prd)
	})

	if err != nil {
		return nil, fmt.Errorf("GetCollectionBySeason:db.collections.View:err [%v]", err.Error())
	}

	return prd, err
}

func (db *BuntDB) GetAllCollections() ([]*store.Collection, error) {
	aa := []*store.Collection{}
	err := db.collections.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, collectionsStr string) bool {
			collection := store.GetCollectionFromString(collectionsStr)
			aa = append(aa, &collection)
			return true
		})
		return nil
	})
	return aa, err
}

func (db *BuntDB) DeleteCollectionBySeason(season string) error {
	err := db.collections.Update(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(season)
		return err
	})
	return err
}

func (db *BuntDB) ModifyCollectionBySeason(season string, cNew *store.Collection) error {
	_, err := db.GetCollectionBySeason(season)
	if err != nil {
		return fmt.Errorf("not exist")
	}
	bs, err := json.Marshal(cNew)
	if err != nil {
		return fmt.Errorf("ModifyCollectionBySeason:json.Marshal [%v]", err.Error())
	}
	err = db.collections.Update(func(tx *buntdb.Tx) error {
		tx.Set(season, string(bs), nil)
		return nil
	})

	return err
}
