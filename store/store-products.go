package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/buntdb"
)

func getProductsById(db *DB, id string) (*Product, error) {
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

func getAllProducts(db *DB) ([]string, error) {
	prds := []string{}
	err := db.products.View(func(tx *buntdb.Tx) error {
		tx.Ascend("date", func(_, productStr string) bool {
			prds = append(prds, productStr)
			return true
		})
		return nil
	})

	return prds, err
}

// func getAllProductsInCategory(db *DB, category string) error {

// }

// func getFeaturedProducts(db *DB) error {
// }

func deleteProductById(db *DB, id string) error {
	err := db.products.View(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(id)
		return err
	})
	return err
}

func addProduct(db *DB, p *Product) error {

	bs, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("addProduct:json.Marshal [%v]", err.Error())
	}

	err = db.products.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%s", time.Now().UnixNano()), string(bs), nil)
		return nil
	})

	return err
}

func modifyProductById(db *DB, id string, pNew *Product) error {

	_, err := getProductsById(db, id)
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
