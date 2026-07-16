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
	From           decimal.Decimal
	To             decimal.Decimal
	Currency       string // ISO 4217 currency code for price filtering (e.g., USD, EUR, JPY)
	OnSale         bool
	Gender         []GenderEnum
	ColorCodes     []string
	TopCategoryIds []int
	// ExcludeTopCategoryIds lists top category ids to exclude from results
	// (e.g. hide the "object" category from the men's catalog).
	ExcludeTopCategoryIds []int
	SubCategoryIds        []int
	TypeIds               []int
	SizesIds              []int
	Preorder              bool
	ByTag                 string
	Collections           []string
	Seasons               []SeasonEnum
	// ViewerTier is the loyalty tier code (0/1/2/99) of the requesting customer
	// (0 for guests). Applied as a visibility gate on public listings.
	ViewerTier int16
}
