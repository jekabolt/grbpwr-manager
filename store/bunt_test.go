package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/matryer/is"
)

const (
	BuntDBProductsPath    = "../bunt/products.db"
	BuntDBArticlesPath    = "../bunt/articles.db"
	BuntDBSalesPath       = "../bunt/sales.db"
	BuntDBSubscribersPath = "../bunt/subscribers.db"
	BuntDBHeroPath        = "../bunt/hero.db"
)

func TestCreateD(t *testing.T) {
	p := &Product{}
	bs, _ := json.Marshal(p)
	fmt.Println("---", string(bs))
}

func buntFromConst() *BuntDB {
	return &BuntDB{
		BuntDBProductsPath:    BuntDBProductsPath,
		BuntDBArticlesPath:    BuntDBArticlesPath,
		BuntDBSalesPath:       BuntDBSalesPath,
		BuntDBSubscribersPath: BuntDBSubscribersPath,
		BuntDBHeroPath:        BuntDBHeroPath,
	}
}

func TestCRUDProducts(t *testing.T) {
	b := buntFromConst()
	is := is.New(t)

	err := b.InitDB()
	is.NoErr(err)

	prd := &Product{
		MainImage: bucket.MainImage{
			Image: bucket.Image{
				FullSize: "https://main.com/img.jpg",
			},
		},
		Name: "name",
		Price: &Price{
			USD: 1,
			BYN: 1,
			EUR: 1,
			RUB: 1,
		},
		AvailableSizes: &Size{
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
	b := buntFromConst()
	is := is.New(t)

	err := b.InitDB()
	is.NoErr(err)

	art := &ArchiveArticle{
		Title:       "title",
		Description: "desc",
		MainImage: bucket.MainImage{
			Image: bucket.Image{
				FullSize: "img",
			},
		},
		Content: []Content{
			{
				Image: bucket.Image{
					FullSize: "img",
				},
				MediaLink:    "link",
				Description:  "desc",
				TextPosition: "top",
			},
		},
	}

	a, err := b.AddArchiveArticle(art)
	is.NoErr(err)

	found, err := b.GetArchiveArticleById(fmt.Sprint(a.Id))
	is.NoErr(err)

	is.Equal(art, found)

	a.Title = "new title"

	aNew := a

	err = b.ModifyArchiveArticleById(fmt.Sprint(a.Id), aNew)
	is.NoErr(err)

	foundModified, err := b.GetArchiveArticleById(fmt.Sprint(a.Id))
	is.NoErr(err)

	is.Equal(aNew, foundModified)

	err = b.DeleteArchiveArticleById(fmt.Sprint(foundModified.Id))
	is.NoErr(err)

	arts, err := b.GetAllArchiveArticles()
	is.NoErr(err)

	is.Equal(len(arts), 0)

}
