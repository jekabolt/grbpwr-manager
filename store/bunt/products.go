package bunt

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/tidwall/buntdb"
)

func (db *BuntDB) AddProduct(p *store.Product) (*store.Product, error) {
	now := time.Now().Unix()
	p.Id = now
	p.DateCreated = now
	p.LastActionTime = now

	return p, db.products.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%d", now), p.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetProductById(id string) (*store.Product, error) {
	prd := &store.Product{}
	err := db.products.View(func(tx *buntdb.Tx) error {
		productStr, err := tx.Get(id)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(productStr), prd)
	})

	if err != nil {
		return nil, fmt.Errorf("getProductsById:db.products.View:err [%v]", err.Error())
	}

	return prd, err
}

func (db *BuntDB) GetAllProducts() ([]*store.Product, error) {
	products := []*store.Product{}
	err := db.products.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, productStr string) bool {
			p := store.GetProductFromString(productStr)
			if p != nil {
				products = append(products, p)
			}
			return true
		})
		return nil
	})
	return products, err
}

func (db *BuntDB) DeleteProductById(id string) error {
	err := db.products.Update(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(id)
		return err
	})
	return err
}

func (db *BuntDB) ModifyProductById(id string, pNew *store.Product) error {

	pNew.LastActionTime = time.Now().Unix()

	pOld, err := db.GetProductById(id)
	if err != nil {
		return fmt.Errorf("not exist")
	}

	pNew.Id = pOld.Id
	pNew.DateCreated = pOld.DateCreated

	bs, err := json.Marshal(pNew)
	if err != nil {
		return fmt.Errorf("addProduct:json.Marshal [%v]", err.Error())
	}

	err = db.products.Update(func(tx *buntdb.Tx) error {
		tx.Set(id, string(bs), nil)
		return nil
	})

	return err
}
