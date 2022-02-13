package bunt

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/tidwall/buntdb"
)

func (db *BuntDB) AddNewsArticle(aa *store.NewsArticle) (*store.NewsArticle, error) {

	now := time.Now().Unix()
	aa.Id = now
	aa.DateCreated = now

	return aa, db.articles.Update(func(tx *buntdb.Tx) error {
		tx.Set(fmt.Sprintf("%d", now), aa.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetNewsArticleById(id string) (*store.NewsArticle, error) {
	prd := &store.NewsArticle{}
	err := db.articles.View(func(tx *buntdb.Tx) error {
		articleStr, err := tx.Get(id)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(articleStr), prd)
	})

	if err != nil {
		return nil, fmt.Errorf("GetNewsArticleById:db.articles.View:err [%v]", err.Error())
	}

	return prd, err
}

func (db *BuntDB) GetAllNewsArticles() ([]*store.NewsArticle, error) {
	aa := []*store.NewsArticle{}
	err := db.articles.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, articlesStr string) bool {
			article := store.GetNewsArticleFromString(articlesStr)
			aa = append(aa, &article)
			return true
		})
		return nil
	})
	return aa, err
}

func (db *BuntDB) DeleteNewsArticleById(id string) error {
	err := db.articles.Update(func(tx *buntdb.Tx) error {
		_, err := tx.Delete(id)
		return err
	})
	return err
}

func (db *BuntDB) ModifyNewsArticleById(id string, aaNew *store.NewsArticle) error {

	aaOld, err := db.GetNewsArticleById(id)
	if err != nil {
		return fmt.Errorf("not exist")
	}

	aaNew.Id = aaOld.Id
	aaNew.DateCreated = aaOld.DateCreated

	bs, err := json.Marshal(aaNew)
	if err != nil {
		return fmt.Errorf("ModifyNewsArticleById:json.Marshal [%v]", err.Error())
	}

	err = db.articles.Update(func(tx *buntdb.Tx) error {
		tx.Set(id, string(bs), nil)
		return nil
	})

	return err
}
