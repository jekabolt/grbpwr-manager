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
	HeroTypeFeaturedArchive     HeroType = 6
)

type HeroEntity struct {
	Type                HeroType                 `json:"type"`
	Single              *HeroSingle              `json:"single"`
	Double              *HeroDouble              `json:"double"`
	Main                *HeroMain                `json:"main"`
	FeaturedProducts    *HeroFeaturedProducts    `json:"featured_products"`
	FeaturedProductsTag *HeroFeaturedProductsTag `json:"featured_products_tag"`
	FeaturedArchive     *HeroFeaturedArchive     `json:"featured_archive"`
}

type HeroSingle struct {
	Media       MediaFull `json:"media"`
	Headline    string    `json:"headline"`
	ExploreLink string    `json:"explore_link"`
	ExploreText string    `json:"explore_text"`
}

type HeroDouble struct {
	Left  HeroSingle `json:"left"`
	Right HeroSingle `json:"right"`
}

type HeroMain struct {
	Single      HeroSingle `json:"single"`
	Tag         string     `json:"tag"`
	Description string     `json:"description"`
}

type HeroEntityInsert struct {
	Type                HeroType                      `json:"type"`
	Single              HeroSingleInsert              `json:"single"`
	Double              HeroDoubleInsert              `json:"double"`
	Main                HeroMainInsert                `json:"main"`
	FeaturedProducts    HeroFeaturedProductsInsert    `json:"featured_products"`
	FeaturedProductsTag HeroFeaturedProductsTagInsert `json:"featured_products_tag"`
	FeaturedArchive     HeroFeaturedArchiveInsert     `json:"featured_archive"`
}

type HeroSingleInsert struct {
	MediaId     int    `json:"media_id"`
	Headline    string `json:"headline"`
	ExploreLink string `json:"explore_link"`
	ExploreText string `json:"explore_text"`
}

type HeroDoubleInsert struct {
	Left  HeroSingleInsert `json:"left"`
	Right HeroSingleInsert `json:"right"`
}

type HeroMainInsert struct {
	Single      HeroSingleInsert `json:"single"`
	Tag         string           `json:"tag"`
	Description string           `json:"description"`
}

type HeroFeaturedProducts struct {
	Products    []Product `json:"products"`
	Headline    string    `json:"headline"`
	ExploreText string    `json:"explore_text"`
	ExploreLink string    `json:"explore_link"`
}

type HeroFeaturedProductsTag struct {
	Products    []Product `json:"products"`
	Tag         string    `json:"tag"`
	Headline    string    `json:"headline"`
	ExploreText string    `json:"explore_text"`
	ExploreLink string    `json:"explore_link"`
}

type HeroFeaturedProductsInsert struct {
	ProductIDs  []int  `json:"product_ids"`
	Headline    string `json:"headline"`
	ExploreText string `json:"explore_text"`
	ExploreLink string `json:"explore_link"`
}

type HeroFeaturedProductsTagInsert struct {
	Tag         string `json:"tag"`
	Headline    string `json:"headline"`
	ExploreText string `json:"explore_text"`
	ExploreLink string `json:"explore_link"`
}

type HeroFeaturedArchiveInsert struct {
	ArchiveId int    `json:"archive_id"`
	Tag       string `json:"tag"`
}

type HeroFeaturedArchive struct {
	Archive ArchiveFull `json:"archive_full"`
	Tag     string      `json:"tag"`
}
