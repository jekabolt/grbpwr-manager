package store

import "fmt"

const (
	BuntDBType = "bunt"
	RedisType  = "redis"
)

type ProductStore interface {
	InitDB() error
	ProductCRUD
	ArchiveArticleCRUD
}

type ProductCRUD interface {
	AddProduct(p *Product) error
	GetProductById(id string) (*Product, error)
	GetAllProducts() ([]*Product, error)
	DeleteProductById(id string) error
	ModifyProductById(id string, pNew *Product) error
}

type ArchiveArticleCRUD interface {
	AddArchiveArticle(aa *ArchiveArticle) error
	GetArchiveArticleById(id string) (*ArchiveArticle, error)
	GetAllArchiveArticles() ([]*ArchiveArticle, error)
	DeleteArchiveArticleById(id string) error
	ModifyArchiveArticleById(id string, aNew *ArchiveArticle) error
}

func GetDB(t string) (ProductStore, error) {
	switch t {
	case BuntDBType:
		return &BuntDB{}, nil
	}
	return nil, fmt.Errorf("GetDB: db type [%s] is not exist ", t)
}
