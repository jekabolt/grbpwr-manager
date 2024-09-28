package entity

type HeroFull struct {
	Entities []HeroEntity
}

type HeroFullInsert struct {
	Entities []HeroEntityInsert
}

type HeroType int32

const (
	HeroTypeUnknown          HeroType = 0
	HeroTypeSingleAdd        HeroType = 1
	HeroTypeDoubleAdd        HeroType = 2
	HeroTypeMainAdd          HeroType = 3
	HeroTypeFeaturedProducts HeroType = 4
)

type HeroEntity struct {
	Type             HeroType              `json:"type"`
	SingleAdd        *HeroSingleAdd        `json:"single_add"`
	DoubleAdd        *HeroDoubleAdd        `json:"double_add"`
	MainAdd          *HeroMainAdd          `json:"main_add"`
	FeaturedProducts *HeroFeaturedProducts `json:"featured_products"`
}

type HeroSingleAdd struct {
	Media       MediaFull `json:"media"`
	ExploreLink string    `json:"explore_link"`
	ExploreText string    `json:"explore_text"`
}

type HeroDoubleAdd struct {
	Left  HeroSingleAdd `json:"left"`
	Right HeroSingleAdd `json:"right"`
}

type HeroMainAdd struct {
	SingleAdd HeroSingleAdd `json:"single_add"`
}

type HeroEntityInsert struct {
	Type             HeroType                   `json:"type"`
	SingleAdd        HeroSingleAddInsert        `json:"single_add"`
	DoubleAdd        HeroDoubleAddInsert        `json:"double_add"`
	MainAdd          HeroMainAddInsert          `json:"main_add"`
	FeaturedProducts HeroFeaturedProductsInsert `json:"featured_products"`
}

type HeroSingleAddInsert struct {
	MediaId     int    `json:"media_id"`
	ExploreLink string `json:"explore_link"`
	ExploreText string `json:"explore_text"`
}

type HeroDoubleAddInsert struct {
	Left  HeroSingleAddInsert `json:"left"`
	Right HeroSingleAddInsert `json:"right"`
}

type HeroMainAddInsert struct {
	SingleAdd HeroSingleAddInsert `json:"single_add"`
}

type HeroFeaturedProducts struct {
	Products    []Product `json:"products"`
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
