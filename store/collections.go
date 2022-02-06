package store

import (
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/bucket"
)

type Collection struct {
	MainImage       bucket.MainImage `json:"mainImage"`
	Season          string           `json:"season"`
	Title           string           `json:"title"`
	Article         NewsArticle      `json:"article"`
	CollectionItems []Product        `json:"collectionItems"`
}

func (c *Collection) String() string {
	bs, _ := json.Marshal(c)
	return string(bs)
}

func (c *Collection) Collection() string {
	bs, _ := json.Marshal(c)
	return string(bs)
}

func GetCollectionFromString(newsArticle string) Collection {
	c := &Collection{}
	json.Unmarshal([]byte(newsArticle), c)
	return *c
}

func (p *Collection) Validate() error {

	if len(p.Title) == 0 {
		return fmt.Errorf("missing title")
	}
	if len(p.Season) == 0 {
		return fmt.Errorf("missing season")
	}
	if len(p.MainImage.FullSize) == 0 {
		return fmt.Errorf("no main image")
	}

	if len(p.Article.Content) == 0 {
		return fmt.Errorf("content should have at least one record")
	}

	for _, c := range p.Article.Content {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil

}
