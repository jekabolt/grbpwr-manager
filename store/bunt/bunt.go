package bunt

import (
	"github.com/tidwall/buntdb"
)

type BuntDB struct {
	products    *buntdb.DB
	articles    *buntdb.DB
	subscribers *buntdb.DB
	sales       *buntdb.DB
	hero        *buntdb.DB
	collections *buntdb.DB
}

func (c *Config) InitDB() (*BuntDB, error) {

	db := BuntDB{}
	var err error

	db.products, err = buntdb.Open(c.ProductsPath)
	if err != nil {
		return nil, err
	}
	db.articles, err = buntdb.Open(c.ArticlesPath)
	if err != nil {
		return nil, err
	}
	db.sales, err = buntdb.Open(c.SalesPath)
	if err != nil {
		return nil, err
	}
	db.subscribers, err = buntdb.Open(c.SubscribersPath)
	if err != nil {
		return nil, err
	}
	db.hero, err = buntdb.Open(c.HeroPath)
	if err != nil {
		return nil, err
	}
	db.collections, err = buntdb.Open(c.CollectionsPath)
	if err != nil {
		return nil, err
	}

	return &db, nil
}
