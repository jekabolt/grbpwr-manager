package store

const (
	BuntDBType = "bunt"
	RedisType  = "redis"
)

type Store interface {
	ProductCRUD
	NewsArticleCRUD
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

type NewsArticleCRUD interface {
	AddNewsArticle(aa *NewsArticle) (*NewsArticle, error)
	GetNewsArticleById(id string) (*NewsArticle, error)
	GetAllNewsArticles() ([]*NewsArticle, error)
	DeleteNewsArticleById(id string) error
	ModifyNewsArticleById(id string, aNew *NewsArticle) error
}

type HeroCRUD interface {
	UpsertHero(h *Hero) (*Hero, error)
	GetHero() (*Hero, error)
}
