package store

import (
	"encoding/json"
	"fmt"
	"testing"
)

const (
	BuntDBProductsPath = "../bunt/products.db"
	BuntDBArticlesPath = "../bunt/articles.db"
	BuntDBSalesPath    = "../bunt/sales.db"
)

func TestCreateD(t *testing.T) {
	p := &Product{}
	bs, _ := json.Marshal(p)
	fmt.Println("---", string(bs))
}

func buntFromConst() *BuntDB {
	return &BuntDB{
		BuntDBProductsPath: BuntDBProductsPath,
		BuntDBArticlesPath: BuntDBArticlesPath,
		BuntDBSalesPath:    BuntDBSalesPath,
	}
}

func TestCRUDProducts(t *testing.T) {
	b := buntFromConst()

	if err := b.InitDB(); err != nil {
		t.Fatal("TestCRUDProducts:InitBucket ", err)
	}

	prd := Product{
		MainImage: "img",
		Name:      "name",
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
		Description:   "desc",
		Categories:    []string{"1", "2"},
		ProductImages: []string{"img1", "img2"},
	}

	p, err := b.AddProduct(&prd)
	if err != nil {
		t.Fatal("TestCRUDProducts:AddProduct ", err)
	}

	found, err := b.GetProductById(fmt.Sprint(p.Id))
	if err != nil {
		t.Fatal("TestCRUDProducts:GetProductById ", err)
	}
	if p.Name != found.Name {
		t.Fatal("TestCRUDProducts:products not math ", err)
	}

	newName := "new name"

	p.Name = newName

	pNew := p

	err = b.ModifyProductById(fmt.Sprint(p.Id), pNew)
	if err != nil {
		t.Fatal("TestCRUDProducts:ModifyProductById ", err)
	}

	foundModified, err := b.GetProductById(fmt.Sprint(p.Id))
	if err != nil {
		t.Fatal("TestCRUDProducts:GetProductById ", err)
	}
	if newName != foundModified.Name {
		t.Fatal("TestCRUDProducts:products name  not math ", err)
	}

	err = b.DeleteProductById(fmt.Sprint(foundModified.Id))
	if err != nil {
		t.Fatal("TestCRUDProducts:DeleteProductById ", err)
	}

	prds, err := b.GetAllProducts()
	if err != nil {
		t.Fatal("TestCRUDProducts:GetAllProducts ", err)
	}

	if len(prds) != 0 {
		t.Fatal("should be zero after deletion", err)
	}

}

func TestCRUDArticles(t *testing.T) {
	b := buntFromConst()

	if err := b.InitDB(); err != nil {
		t.Fatal("TestCRUDArticles:InitBucket ", err)
	}

	art := ArchiveArticle{
		Title:       "title",
		Description: "desc",
		MainImage:   "img",
		Content: []Content{
			{
				MediaLink:              "link",
				Description:            "desc",
				DescriptionAlternative: "alt",
			},
		},
	}

	a, err := b.AddArchiveArticle(&art)
	if err != nil {
		t.Fatal("TestCRUDArticles:AddProduct ", err)
	}

	found, err := b.GetArchiveArticleById(fmt.Sprint(a.Id))
	if err != nil {
		t.Fatal("TestCRUDArticles:GetProductById ", err)
	}
	if a.Title != found.Title {
		t.Fatal("TestCRUDArticles:articles not math ", err)
	}

	newTitle := "new title"

	a.Title = newTitle

	aNew := a

	err = b.ModifyArchiveArticleById(fmt.Sprint(a.Id), aNew)
	if err != nil {
		t.Fatal("TestCRUDArticles:ModifyProductById ", err)
	}

	foundModified, err := b.GetArchiveArticleById(fmt.Sprint(a.Id))
	if err != nil {
		t.Fatal("TestCRUDArticles:GetProductById ", err)
	}
	if newTitle != foundModified.Title {
		t.Fatal("TestCRUDArticles:articles name  not math ", err)
	}

	err = b.DeleteArchiveArticleById(fmt.Sprint(foundModified.Id))
	if err != nil {
		t.Fatal("TestCRUDArticles:DeleteProductById ", err)
	}

	arts, err := b.GetAllArchiveArticles()
	if err != nil {
		t.Fatal("TestCRUDArticles:GetAllProducts ", err)
	}

	if len(arts) != 0 {
		t.Fatal("should be zero after deletion", err)
	}

}
