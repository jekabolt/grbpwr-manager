package store

import "fmt"

const (
	BuntDBType = "bunt"
	RedisType  = "redis"
)

type ProductStore interface {
	InitDB() error
	AddProduct(p *Product) error
	GetProductsById(id string) (*Product, error)
	GetAllProducts() ([]Product, error)
	DeleteProductById(id string) error
	ModifyProductById(id string, pNew *Product) error
}

func GetDB(t string) (ProductStore, error) {
	switch t {
	case BuntDBType:
		return &BuntDB{}, nil
	}
	return nil, fmt.Errorf("GetDB: db type [%s] is not exist ", t)
}
