package store

type Product struct {
	Id             int64    `json:"id"`
	DateCreated    int64    `json:"dateCreated"`
	LastActionTime int64    `json:"lat"`
	MainImage      string   `json:"mainImage"`
	Name           string   `json:"name"`
	Price          *Price   `json:"price"`
	AvailableSizes *Size    `json:"availableSizes"`
	Description    string   `json:"description,omitempty"`
	Categories     []string `json:"categories,omitempty"`
	ImageURLs      []string `json:"imageURLs,omitempty"`
}

type Price struct {
	USD float64 `json:"usd"`
	RUB float64 `json:"rub"`
	BYN float64 `json:"byn"`
	EUR float64 `json:"eur"`
}

type Size struct {
	XXS int `json:"xxs,omitempty"`
	XS  int `json:"xs,omitempty"`
	S   int `json:"s,omitempty"`
	M   int `json:"m,omitempty"`
	L   int `json:"l,omitempty"`
	XL  int `json:"xl,omitempty"`
	XXL int `json:"xxl,omitempty"`
	OS  int `json:"os,omitempty"`
}
