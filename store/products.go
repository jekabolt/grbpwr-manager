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
	XXS int `json:"xxs"`
	XS  int `json:"xs"`
	S   int `json:"s"`
	M   int `json:"m"`
	L   int `json:"l"`
	XL  int `json:"xl"`
	XXL int `json:"xxl"`
	OS  int `json:"os"`
}
