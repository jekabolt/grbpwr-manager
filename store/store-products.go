package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/buntdb"
)

func (db *DB) AddProduct(p *Product) error {

	now := time.Now().Unix()
	p.Id = now
	p.DateCreated = now
	p.LastActionTime = now

	return db.products.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%s", time.Now().UnixNano()), p.String(), nil)
		return nil
	})
}

func (db *DB) GetProductsById(id string) (*Product, error) {
	prd := &Product{}
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

func (db *DB) GetAllProducts() ([]string, error) {
	products := []string{}
	err := db.products.View(func(tx *buntdb.Tx) error {
		tx.Ascend("date", func(_, productStr string) bool {
			products = append(products, productStr)
			return true
		})
		return nil
	})
	return products, err
}

func (db *DB) GetAllProductsInCategory(category string) ([]string, error) {
	products := []string{}
	err := db.products.View(func(tx *buntdb.Tx) error {
		tx.Ascend("date", func(_, productStr string) bool {
			if isCategoryExist(productStr, category) {
				products = append(products, productStr)
				return true
			}
			return false
		})
		return nil
	})
	return products, err
}

func (db *DB) DeleteProductById(id string) error {
	err := db.products.View(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(id)
		return err
	})
	return err
}

func (db *DB) ModifyProductById(id string, pNew *Product) error {

	_, err := db.GetProductsById(id)
	if err != nil {
		return fmt.Errorf("not exist")
	}

	bs, err := json.Marshal(pNew)
	if err != nil {
		return fmt.Errorf("addProduct:json.Marshal [%v]", err.Error())
	}

	err = db.products.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%s", time.Now().UnixNano()), string(bs), nil)
		return nil
	})

	return err
}
