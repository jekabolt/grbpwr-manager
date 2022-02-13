package bunt

type Config struct {
	ProductsPath    string `env:"BUNT_DB_PRODUCTS_PATH" envDefault:"/tmp/products.db"`
	ArticlesPath    string `env:"BUNT_DB_ARTICLES_PATH" envDefault:"/tmp/articles.db"`
	CollectionsPath string `env:"BUNT_DB_COLLECTIONS_PATH" envDefault:"/tmp/collections.db"`
	SalesPath       string `env:"BUNT_DB_SALES_PATH" envDefault:"/tmp/sales.db"`
	SubscribersPath string `env:"BUNT_DB_SUBSCRIBERS_PATH" envDefault:"/tmp/subscribers.db"`
	HeroPath        string `env:"BUNT_DB_HERO_PATH" envDefault:"/tmp/hero.db"`
}
