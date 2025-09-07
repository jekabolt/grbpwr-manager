package entity

// WithTranslations structs to match proto definitions
type HeroFullWithTranslations struct {
	Entities    []HeroEntityWithTranslations `json:"entities"`
	NavFeatured NavFeaturedWithTranslations  `json:"nav_featured"`
}

type NavFeaturedWithTranslations struct {
	Men   NavFeaturedEntityWithTranslations `json:"men"`
	Women NavFeaturedEntityWithTranslations `json:"women"`
}

type NavFeaturedEntityWithTranslations struct {
	Media             MediaFull                            `json:"media"`
	FeaturedTag       string                               `json:"featured_tag"`
	FeaturedArchiveId string                               `json:"featured_archive_id"` // Changed to string to match proto
	Translations      []NavFeaturedEntityInsertTranslation `json:"translations"`
}

type HeroEntityWithTranslations struct {
	Type                HeroType                                 `json:"type"`
	Single              *HeroSingleWithTranslations              `json:"single"`
	Double              *HeroDoubleWithTranslations              `json:"double"`
	Main                *HeroMainWithTranslations                `json:"main"`
	FeaturedProducts    *HeroFeaturedProductsWithTranslations    `json:"featured_products"`
	FeaturedProductsTag *HeroFeaturedProductsTagWithTranslations `json:"featured_products_tag"`
	FeaturedArchive     *HeroFeaturedArchiveWithTranslations     `json:"featured_archive"`
}

type HeroSingleWithTranslations struct {
	MediaPortrait  MediaFull                     `json:"media_portrait"`
	MediaLandscape MediaFull                     `json:"media_landscape"`
	ExploreLink    string                        `json:"explore_link"`
	Translations   []HeroSingleInsertTranslation `json:"translations"`
}

type HeroDoubleWithTranslations struct {
	Left  HeroSingleWithTranslations `json:"left"`
	Right HeroSingleWithTranslations `json:"right"`
}

type HeroMainWithTranslations struct {
	Single       HeroSingleWithTranslations  `json:"single"`
	Translations []HeroMainInsertTranslation `json:"translations"`
}

type HeroFeaturedProductsWithTranslations struct {
	Products     []Product                               `json:"products"`
	ExploreLink  string                                  `json:"explore_link"`
	Translations []HeroFeaturedProductsInsertTranslation `json:"translations"`
}

type HeroFeaturedProductsTagWithTranslations struct {
	Tag          string                                     `json:"tag"`
	Products     HeroFeaturedProductsWithTranslations       `json:"products"`
	Translations []HeroFeaturedProductsTagInsertTranslation `json:"translations"`
}

type HeroFeaturedArchiveWithTranslations struct {
	Archive      ArchiveFull                            `json:"archive"`
	Tag          string                                 `json:"tag"`
	Headline     string                                 `json:"headline"`
	ExploreText  string                                 `json:"explore_text"`
	Translations []HeroFeaturedArchiveInsertTranslation `json:"translations"`
}

type HeroSingleInsertTranslation struct {
	LanguageId  int    `json:"language_id"`
	Headline    string `json:"headline"`
	ExploreText string `json:"explore_text"`
}

type HeroMainInsertTranslation struct {
	LanguageId  int    `json:"language_id"`
	Tag         string `json:"tag"`
	Description string `json:"description"`
	Headline    string `json:"headline"`
	ExploreText string `json:"explore_text"`
}

type HeroFeaturedProductsInsertTranslation struct {
	LanguageId  int    `json:"language_id"`
	Headline    string `json:"headline"`
	ExploreText string `json:"explore_text"`
}

type HeroFeaturedProductsTagInsertTranslation struct {
	LanguageId  int    `json:"language_id"`
	Headline    string `json:"headline"`
	ExploreText string `json:"explore_text"`
}

type HeroFeaturedArchiveInsertTranslation struct {
	LanguageId  int    `json:"language_id"`
	Headline    string `json:"headline"`
	ExploreText string `json:"explore_text"`
}

type NavFeaturedEntityInsertTranslation struct {
	LanguageId  int    `json:"language_id"`
	ExploreText string `json:"explore_text"`
}

type HeroFullInsert struct {
	Entities    []HeroEntityInsert
	NavFeatured NavFeaturedInsert
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
	MediaPortraitId  int                           `json:"media_portrait_id"`
	MediaLandscapeId int                           `json:"media_landscape_id"`
	ExploreLink      string                        `json:"explore_link"`
	Translations     []HeroSingleInsertTranslation `json:"translations"`
}

type HeroDoubleInsert struct {
	Left  HeroSingleInsert `json:"left"`
	Right HeroSingleInsert `json:"right"`
}

type HeroMainInsert struct {
	MediaPortraitId  int                         `json:"media_portrait_id"`
	MediaLandscapeId int                         `json:"media_landscape_id"`
	ExploreLink      string                      `json:"explore_link"`
	Translations     []HeroMainInsertTranslation `json:"translations"`
}

type HeroFeaturedProducts struct {
	Products    []Product `json:"products"`
	Headline    string    `json:"headline"`
	ExploreText string    `json:"explore_text"`
	ExploreLink string    `json:"explore_link"`
}

type HeroFeaturedProductsTag struct {
	Tag      string               `json:"tag"`
	Products HeroFeaturedProducts `json:"products"`
}

type HeroFeaturedProductsInsert struct {
	ProductIDs   []int                                   `json:"product_ids"`
	ExploreLink  string                                  `json:"explore_link"`
	Translations []HeroFeaturedProductsInsertTranslation `json:"translations"`
}

type HeroFeaturedProductsTagInsert struct {
	Tag          string                                     `json:"tag"`
	Translations []HeroFeaturedProductsTagInsertTranslation `json:"translations"`
}

type HeroFeaturedArchiveInsert struct {
	ArchiveId    int                                    `json:"archive_id"`
	Tag          string                                 `json:"tag"`
	Translations []HeroFeaturedArchiveInsertTranslation `json:"translations"`
}

type NavFeaturedInsert struct {
	Men   NavFeaturedEntityInsert `json:"men"`
	Women NavFeaturedEntityInsert `json:"women"`
}

type NavFeaturedEntityInsert struct {
	MediaId           int                                  `json:"media_id"`
	FeaturedTag       string                               `json:"featured_tag"`
	FeaturedArchiveId int                                  `json:"featured_archive_id"`
	Translations      []NavFeaturedEntityInsertTranslation `json:"translations"`
}
