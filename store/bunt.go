package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/caarlos0/env/v6"
	"github.com/tidwall/buntdb"
)

type BuntDB struct {
	BuntDBProductsPath    string `env:"BUNT_DB_PRODUCTS_PATH" envDefault:"/tmp/products.db"`
	BuntDBArticlesPath    string `env:"BUNT_DB_ARTICLES_PATH" envDefault:"/tmp/articles.db"`
	BuntDBSalesPath       string `env:"BUNT_DB_SALES_PATH" envDefault:"/tmp/sales.db"`
	BuntDBSubscribersPath string `env:"BUNT_DB_SUBSCRIBERS_PATH" envDefault:"/tmp/subscribers.db"`
	products              *buntdb.DB
	articles              *buntdb.DB
	subscribers           *buntdb.DB
	sales                 *buntdb.DB
}

func BuntFromEnv() (*BuntDB, error) {
	b := &BuntDB{}
	err := env.Parse(b)
	return b, err
}

func (db *BuntDB) InitDB() error {

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
	subscribers, err := buntdb.Open(db.BuntDBSubscribersPath)
	if err != nil {
		return err
	}

	db.products = productsDB
	db.articles = articlesDB
	db.sales = salesDB
	db.subscribers = subscribers
	return nil
}

// products

func (db *BuntDB) AddProduct(p *Product) (*Product, error) {

	now := time.Now().Unix()
	p.Id = now
	p.DateCreated = now
	p.LastActionTime = now

	return p, db.products.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%d", now), p.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetProductById(id string) (*Product, error) {
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

func (db *BuntDB) GetAllProducts() ([]*Product, error) {
	products := []*Product{}
	err := db.products.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, productStr string) bool {
			p := getProductFromString(productStr)
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

func (db *BuntDB) ModifyProductById(id string, pNew *Product) error {

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

// archive

func (db *BuntDB) AddArchiveArticle(aa *ArchiveArticle) (*ArchiveArticle, error) {

	now := time.Now().Unix()
	aa.Id = now
	aa.DateCreated = now

	return aa, db.articles.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%d", now), aa.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetArchiveArticleById(id string) (*ArchiveArticle, error) {
	prd := &ArchiveArticle{}
	err := db.articles.View(func(tx *buntdb.Tx) error {
		articleStr, err := tx.Get(id)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(articleStr), prd)
	})

	if err != nil {
		return nil, fmt.Errorf("GetArchiveArticleById:db.articles.View:err [%v]", err.Error())
	}

	return prd, err
}

func (db *BuntDB) GetAllArchiveArticles() ([]*ArchiveArticle, error) {
	aa := []*ArchiveArticle{}
	err := db.articles.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, articlesStr string) bool {
			article := getArchiveArticleFromString(articlesStr)
			aa = append(aa, &article)
			return true
		})
		return nil
	})
	return aa, err
}

func (db *BuntDB) DeleteArchiveArticleById(id string) error {
	err := db.articles.Update(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(id)
		return err
	})
	return err
}

func (db *BuntDB) ModifyArchiveArticleById(id string, aaNew *ArchiveArticle) error {

	aaOld, err := db.GetArchiveArticleById(id)
	if err != nil {
		return fmt.Errorf("not exist")
	}

	aaNew.Id = aaOld.Id
	aaNew.DateCreated = aaOld.DateCreated

	bs, err := json.Marshal(aaNew)
	if err != nil {
		return fmt.Errorf("ModifyArchiveArticleById:json.Marshal [%v]", err.Error())
	}

	err = db.articles.Update(func(tx *buntdb.Tx) error {
		tx.Set(id, string(bs), nil)
		return nil
	})

	return err
}

// newsletter

func (db *BuntDB) AddSubscriber(s *Subscriber) (*Subscriber, error) {
	return s, db.subscribers.Update(func(tx *buntdb.Tx) error {
		tx.Set(s.Email, s.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetSubscriberByEmail(email string) (*Subscriber, error) {
	nl := &Subscriber{}
	err := db.subscribers.View(func(tx *buntdb.Tx) error {
		subscriberStr, err := tx.Get(email)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(subscriberStr), nl)
	})

	if err != nil {
		return nil, fmt.Errorf("GetSubscriberByEmail:db.articles.View:err [%v]", err.Error())
	}

	return nl, err
}

func (db *BuntDB) GetAllSubscribers() ([]*Subscriber, error) {
	s := []*Subscriber{}
	err := db.subscribers.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, articlesStr string) bool {
			subscriber := getSubscriberFromString(articlesStr)
			s = append(s, subscriber)
			return true
		})
		return nil
	})
	return s, err
}

func (db *BuntDB) DeleteSubscriberByEmail(id string) error {
	err := db.subscribers.Update(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(id)
		return err
	})
	return err
}
