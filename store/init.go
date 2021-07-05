package store

import (
	_ "github.com/go-kivik/couchdb/v4" // The CouchDB driver
	"github.com/tidwall/buntdb"
)

type DB struct {
	BuntDBProductsPath string `env:"BUNT_DB_PRODUCTS_PATH" envDefault:"products.db"`
	BuntDBArticlesPath string `env:"BUNT_DB_ARTICLES_PATH" envDefault:"articles.db"`
	BuntDBSalesPath    string `env:"BUNT_DB_SALES_PATH" envDefault:"sales.db"`
	products           *buntdb.DB
	articles           *buntdb.DB
	sales              *buntdb.DB
}

func GetDB(dbFilePath string) (*buntdb.DB, error) {
	return buntdb.Open(dbFilePath)
}

func (db *DB) InitDB() (*DB, error) {
	productsDB, err := GetDB(db.BuntDBProductsPath)
	if err != nil {
		return nil, err
	}
	articlesDB, err := GetDB(db.BuntDBArticlesPath)
	if err != nil {
		return nil, err
	}
	salesDB, err := GetDB(db.BuntDBSalesPath)
	if err != nil {
		return nil, err
	}

	return &DB{
		products: productsDB,
		articles: articlesDB,
		sales:    salesDB,
	}, nil
}
