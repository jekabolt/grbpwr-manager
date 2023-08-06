package dto

type FilterField string

const (
	FilterFieldSize     FilterField = "size"
	FilterFieldCategory FilterField = "category"
)

type SortField string

const (
	SortFieldDateAdded SortField = "created_at"
	SortFieldPriceUSD  SortField = "pr.USD"
)

type SortOrder int

const (
	SortOrderAsc  SortOrder = iota // Ascending order
	SortOrderDesc                  // Descending order
)

type FilterCondition struct {
	Field FilterField // Filter field
	Value string      // Filter value
}

type SortFactor struct {
	Field SortField // Sort field
	Order SortOrder // Sort order
}
