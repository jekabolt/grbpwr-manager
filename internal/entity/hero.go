package entity

type HeroFull struct {
	Entities []HeroEntity
}

type HeroFullInsert struct {
	Entities []HeroEntityInsert
}

type HeroType int32

const (
	HeroTypeUnknown             HeroType = 0
	HeroTypeSingle              HeroType = 1
	HeroTypeDouble              HeroType = 2
	HeroTypeMain                HeroType = 3
	HeroTypeFeaturedProducts    HeroType = 4
	HeroTypeFeaturedProductsTag HeroType = 5
)

type HeroEntity struct {
	Type                HeroType                 `json:"type"`
	Single              *HeroSingle              `json:"single_"`
	Double              *HeroDouble              `json:"double_"`
	Main                *HeroMain                `json:"main_"`
	FeaturedProducts    *HeroFeaturedProducts    `json:"featured_products"`
	FeaturedProductsTag *HeroFeaturedProductsTag `json:"featured_products_tag"`
}

type HeroSingle struct {
	Media       MediaFull `json:"media"`
	Title       string    `json:"title"`
	ExploreLink string    `json:"explore_link"`
	ExploreText string    `json:"explore_text"`
}

type HeroDouble struct {
	Left  HeroSingle `json:"left"`
	Right HeroSingle `json:"right"`
}

type HeroMain struct {
	Single      HeroSingle `json:"single_"`
	Tag         string     `json:"tag"`
	Description string     `json:"description"`
}

type HeroEntityInsert struct {
	Type                HeroType                      `json:"type"`
	Single              HeroSingleInsert              `json:"single_"`
	Double              HeroDoubleInsert              `json:"double_"`
	Main                HeroMainInsert                `json:"main_"`
	FeaturedProducts    HeroFeaturedProductsInsert    `json:"featured_products"`
	FeaturedProductsTag HeroFeaturedProductsTagInsert `json:"featured_products_tag"`
}

type HeroSingleInsert struct {
	MediaId     int    `json:"media_id"`
	Title       string `json:"title"`
	ExploreLink string `json:"explore_link"`
	ExploreText string `json:"explore_text"`
}

type HeroDoubleInsert struct {
	Left  HeroSingleInsert `json:"left"`
	Right HeroSingleInsert `json:"right"`
}

type HeroMainInsert struct {
	Single      HeroSingleInsert `json:"single_"`
	Tag         string           `json:"tag"`
	Description string           `json:"description"`
}

type HeroFeaturedProducts struct {
	Products    []Product `json:"products"`
	Title       string    `json:"title"`
	ExploreText string    `json:"explore_text"`
	ExploreLink string    `json:"explore_link"`
}

type HeroFeaturedProductsTag struct {
	Products    []Product `json:"products"`
	Tag         string    `json:"tag"`
	Title       string    `json:"title"`
	ExploreText string    `json:"explore_text"`
	ExploreLink string    `json:"explore_link"`
}

type HeroFeaturedProductsInsert struct {
	ProductIDs  []int  `json:"product_ids"`
	Title       string `json:"title"`
	ExploreText string `json:"explore_text"`
	ExploreLink string `json:"explore_link"`
}

type HeroFeaturedProductsTagInsert struct {
	Tag         string `json:"tag"`
	Title       string `json:"title"`
	ExploreText string `json:"explore_text"`
	ExploreLink string `json:"explore_link"`
}
