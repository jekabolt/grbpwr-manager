package entity

import "github.com/shopspring/decimal"

type OrderFactor string

const (
	Ascending  OrderFactor = "ASC"
	Descending OrderFactor = "DESC"
)

func (of *OrderFactor) String() string {
	if of != nil {
		if *of == Ascending {
			return "ASC"
		}
		return "DESC"
	}
	return "ASC"
}

type SortFactor string

const (
	CreatedAt SortFactor = "created_at"
	UpdatedAt SortFactor = "updated_at"
	Name      SortFactor = "name"
	Price     SortFactor = "price"
)

var validSortFactors = map[SortFactor]bool{
	CreatedAt: true,
	UpdatedAt: true,
	Name:      true,
	Price:     true,
}

func IsValidSortFactor(factor string) bool {
	return validSortFactors[SortFactor(factor)]
}

func SortFactorsToSS(factors []SortFactor) []string {
	ss := make([]string, len(factors))
	for i, factor := range factors {
		ss[i] = string(factor)
	}
	return ss
}

type FilterConditions struct {
	From        decimal.Decimal
	To          decimal.Decimal
	OnSale      bool
	Gender      GenderEnum
	Color       string
	CategoryIds []int
	SizesIds    []int
	Preorder    bool
	ByTag       string
}
