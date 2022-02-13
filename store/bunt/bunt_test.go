package bunt

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/matryer/is"
)

const (
	BuntDBProductsPath    = "../../bunt/products.db"
	BuntDBArticlesPath    = "../../bunt/articles.db"
	BuntDBCollectionsPath = "../../bunt/collections.db"
	BuntDBSalesPath       = "../../bunt/sales.db"
	BuntDBSubscribersPath = "../../bunt/subscribers.db"
	BuntDBHeroPath        = "../../bunt/hero.db"
)

func TestCreateD(t *testing.T) {
	p := &store.Collection{}
	bs, _ := json.Marshal(p)
	fmt.Println("---", string(bs))
}

func buntFromConst() *Config {
	return &Config{
		ProductsPath:    BuntDBProductsPath,
		ArticlesPath:    BuntDBArticlesPath,
		CollectionsPath: BuntDBCollectionsPath,
		SalesPath:       BuntDBSalesPath,
		SubscribersPath: BuntDBSubscribersPath,
		HeroPath:        BuntDBHeroPath,
	}
}

func TestCRUDProducts(t *testing.T) {
	is := is.New(t)

	c := buntFromConst()
	b, err := c.InitDB()
	is.NoErr(err)

	prd := &store.Product{
		MainImage: bucket.MainImage{
			Image: bucket.Image{
				FullSize: "https://main.com/img.jpg",
			},
		},
		Name: "name",
		Price: &store.Price{
			USD: 1,
			BYN: 1,
			EUR: 1,
			RUB: 1,
		},
		AvailableSizes: &store.Size{
			XXS: 1,
			XS:  1,
			S:   1,
			M:   1,
			L:   1,
			XL:  1,
			XXL: 1,
			OS:  1,
		},
		ShortDescription:    "desc",
		DetailedDescription: []string{"desc", "desc2"},
		Categories:          []string{"1", "2"},
		ProductImages: []bucket.Image{
			{
				FullSize: "https://ProductImages.com/img.jpg",
			},
			{
				FullSize: "https://ProductImages2.com/img.jpg",
			},
		},
	}

	p, err := b.AddProduct(prd)
	is.NoErr(err)

	found, err := b.GetProductById(fmt.Sprint(p.Id))
	is.NoErr(err)
	is.Equal(prd, found)

	p.Name = "new name"

	pNew := p

	err = b.ModifyProductById(fmt.Sprint(p.Id), pNew)
	is.NoErr(err)

	foundModified, err := b.GetProductById(fmt.Sprint(p.Id))
	is.NoErr(err)

	is.Equal(pNew, foundModified)

	err = b.DeleteProductById(fmt.Sprint(foundModified.Id))
	is.NoErr(err)

	prds, err := b.GetAllProducts()
	is.NoErr(err)

	is.Equal(len(prds), 0)

}

func TestCRUDArticles(t *testing.T) {
	is := is.New(t)

	c := buntFromConst()
	b, err := c.InitDB()
	is.NoErr(err)
	art := &store.NewsArticle{
		Title:       "title",
		Description: "desc",
		MainImage: bucket.MainImage{
			Image: bucket.Image{
				FullSize: "img",
			},
		},
		Content: []store.Content{
			{
				Image: &bucket.Image{
					FullSize: "img",
				},
				MediaLink:    "link",
				Description:  "desc",
				TextPosition: "top",
			},
		},
	}

	a, err := b.AddNewsArticle(art)
	is.NoErr(err)

	found, err := b.GetNewsArticleById(fmt.Sprint(a.Id))
	is.NoErr(err)

	is.Equal(art, found)

	a.Title = "new title"

	aNew := a

	err = b.ModifyNewsArticleById(fmt.Sprint(a.Id), aNew)
	is.NoErr(err)

	foundModified, err := b.GetNewsArticleById(fmt.Sprint(a.Id))
	is.NoErr(err)

	is.Equal(aNew, foundModified)

	err = b.DeleteNewsArticleById(fmt.Sprint(foundModified.Id))
	is.NoErr(err)

	arts, err := b.GetAllNewsArticles()
	is.NoErr(err)

	is.Equal(len(arts), 0)

}

func TestCRUDCollections(t *testing.T) {
	is := is.New(t)

	c := buntFromConst()
	b, err := c.InitDB()
	is.NoErr(err)
	art := &store.Collection{
		MainImage: &bucket.MainImage{
			Image: bucket.Image{
				FullSize: "img",
			},
		},
		Title:  "title",
		Season: "desc",
		Article: &store.NewsArticle{
			Title:       "title",
			Description: "desc",
			MainImage: bucket.MainImage{
				Image: bucket.Image{
					FullSize: "img",
				},
			},
			Content: []store.Content{
				{
					Image: &bucket.Image{
						FullSize: "img",
					},
					MediaLink:    "link",
					Description:  "desc",
					TextPosition: "top",
				},
			},
		},
	}

	a, err := b.AddCollection(art)
	is.NoErr(err)

	found, err := b.GetCollectionBySeason(fmt.Sprint(a.Season))
	is.NoErr(err)

	is.Equal(art, found)

	a.Title = "new title"

	aNew := a

	err = b.ModifyCollectionBySeason(fmt.Sprint(a.Season), aNew)
	is.NoErr(err)

	foundModified, err := b.GetCollectionBySeason(fmt.Sprint(a.Season))
	is.NoErr(err)

	is.Equal(aNew, foundModified)

	err = b.DeleteCollectionBySeason(fmt.Sprint(foundModified.Season))
	is.NoErr(err)

	arts, err := b.GetAllCollections()
	is.NoErr(err)

	is.Equal(len(arts), 0)

}

func TestCRUDSubscribers(t *testing.T) {
	is := is.New(t)

	c := buntFromConst()
	b, err := c.InitDB()
	is.NoErr(err)

	s := &store.Subscriber{
		Email:    "test",
		IP:       "test",
		City:     "test",
		Region:   "test",
		Country:  "test",
		Loc:      "test",
		Org:      "test",
		Postal:   "test",
		Timezone: "test",
	}

	sUpserted, err := b.AddSubscriber(s)
	is.NoErr(err)

	sFound, err := b.GetAllSubscribers()
	is.NoErr(err)

	is.Equal(sUpserted, sFound[0])

}

func TestCRUDHero(t *testing.T) {
	is := is.New(t)

	c := buntFromConst()
	b, err := c.InitDB()
	is.NoErr(err)

	h := &store.Hero{
		TimeChanged: 14,
		ContentLink: "-",
		ContentType: "-",
		ExploreLink: "-",
		ExploreText: "-",
	}

	hUpserted, err := b.UpsertHero(h)
	is.NoErr(err)

	hFound, err := b.GetHero()
	is.NoErr(err)

	is.Equal(hUpserted, hFound)

}
