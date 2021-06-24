package store

import (
	_ "github.com/go-kivik/couchdb/v4" // The CouchDB driver
	"github.com/tidwall/buntdb"
)

type DB struct {
	products *buntdb.DB
	articles *buntdb.DB
	sales    *buntdb.DB
}

func GetDB(dbFilePath string) (*buntdb.DB, error) {
	return buntdb.Open(dbFilePath)
}

func InitDB(products, articles, sales string) (*DB, error) {
	productsDB, err := GetDB(products)
	if err != nil {
		return nil, err
	}
	articlesDB, err := GetDB(articles)
	if err != nil {
		return nil, err
	}
	salesDB, err := GetDB(sales)
	if err != nil {
		return nil, err
	}

	return &DB{
		products: productsDB,
		articles: articlesDB,
		sales:    salesDB,
	}, nil
}
