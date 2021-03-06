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
	SubscriberCRUD
	HeroCRUD
}

type SubscriberCRUD interface {
	AddSubscriber(n *Subscriber) (*Subscriber, error)
	GetSubscriberByEmail(id string) (*Subscriber, error)
	GetAllSubscribers() ([]*Subscriber, error)
	DeleteSubscriberByEmail(email string) error
}

type ProductCRUD interface {
	AddProduct(p *Product) (*Product, error)
	GetProductById(id string) (*Product, error)
	GetAllProducts() ([]*Product, error)
	DeleteProductById(id string) error
	ModifyProductById(id string, pNew *Product) error
}

type ArchiveArticleCRUD interface {
	AddArchiveArticle(aa *ArchiveArticle) (*ArchiveArticle, error)
	GetArchiveArticleById(id string) (*ArchiveArticle, error)
	GetAllArchiveArticles() ([]*ArchiveArticle, error)
	DeleteArchiveArticleById(id string) error
	ModifyArchiveArticleById(id string, aNew *ArchiveArticle) error
}

type HeroCRUD interface {
	UpsertHero(h *Hero) (*Hero, error)
	GetHero() (*Hero, error)
}

func GetDB(t string) (ProductStore, error) {
	switch t {
	case BuntDBType:
		return BuntFromEnv()
	}
	return nil, fmt.Errorf("GetDB: db type [%s] is not exist ", t)
}
