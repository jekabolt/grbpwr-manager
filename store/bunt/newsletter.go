package bunt

import (
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/tidwall/buntdb"
)

func (db *BuntDB) AddSubscriber(s *store.Subscriber) (*store.Subscriber, error) {
	return s, db.subscribers.Update(func(tx *buntdb.Tx) error {
		tx.Set(s.Email, s.String(), nil)
		return nil
	})
}

func (db *BuntDB) GetSubscriberByEmail(email string) (*store.Subscriber, error) {
	nl := &store.Subscriber{}
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

func (db *BuntDB) GetAllSubscribers() ([]*store.Subscriber, error) {
	s := []*store.Subscriber{}
	err := db.subscribers.View(func(tx *buntdb.Tx) error {
		tx.Ascend("", func(_, articlesStr string) bool {
			subscriber := store.GetSubscriberFromString(articlesStr)
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
