package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/caarlos0/env/v6"
	"github.com/tidwall/buntdb"
)

type BuntDB struct {
	BuntDBProductsPath string `env:"BUNT_DB_PRODUCTS_PATH" envDefault:"/tmp/products.db"`
	BuntDBArticlesPath string `env:"BUNT_DB_ARTICLES_PATH" envDefault:"/tmp/articles.db"`
	BuntDBSalesPath    string `env:"BUNT_DB_SALES_PATH" envDefault:"/tmp/sales.db"`
	BuntDBPageSize     int    `env:"BUNT_DB_PAGE_SIZE" envDefault:"5"`
	products           *buntdb.DB
	articles           *buntdb.DB
	sales              *buntdb.DB
}

func (db *BuntDB) InitDB() error {

	err := env.Parse(db)
	if err != nil {
		return fmt.Errorf("BuntDB:InitDB:env.Parse: %s ", err.Error())
	}

	productsDB, err := buntdb.Open(db.BuntDBProductsPath)
	if err != nil {
		return err
	}
	articlesDB, err := buntdb.Open(db.BuntDBArticlesPath)
	if err != nil {
		return err
	}
	salesDB, err := buntdb.Open(db.BuntDBSalesPath)
	if err != nil {
		return err
	}

	db.products = productsDB
	db.articles = articlesDB
	db.sales = salesDB
	return nil
}

// products

func (db *BuntDB) AddProduct(p *Product) error {

	now := time.Now().Unix()
	p.Id = now
	p.DateCreated = now
	p.LastActionTime = now

	return db.products.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%d", now), p.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetProductsById(id string) (*Product, error) {
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

func (db *BuntDB) GetAllProducts() ([]Product, error) {
	products := []Product{}
	err := db.products.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, productStr string) bool {
			products = append(products, getProductFromString(productStr))
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

func (db *BuntDB) ModifyProductById(id string, pNew *Product) error {

	pNew.LastActionTime = time.Now().Unix()

	pOld, err := db.GetProductsById(id)
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
