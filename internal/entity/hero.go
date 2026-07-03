package entity

import "time"

// ─── shared fragments ───────────────────────────────────────────────────────

// HeroMedia is a portrait/landscape media pair addressed by id (write side).
// DisableOverlay lives here so the scrim can be toggled per media slot.
type HeroMedia struct {
	PortraitId     int  `json:"portrait_id"`
	LandscapeId    int  `json:"landscape_id"`
	DisableOverlay bool `json:"disable_overlay"`
}

// HeroMediaFull is the resolved form of HeroMedia (read side).
type HeroMediaFull struct {
	Portrait       MediaFull `json:"portrait"`
	Landscape      MediaFull `json:"landscape"`
	DisableOverlay bool      `json:"disable_overlay"`
}

// HeroCopyTranslation is the single, shared translation for every hero block.
// Each block type uses only the subset of fields it needs.
type HeroCopyTranslation struct {
	LanguageId  int    `json:"language_id"`
	Tag         string `json:"tag"`
	Headline    string `json:"headline"`
	Subhead     string `json:"subhead"`
	Body        string `json:"body"`
	CtaText     string `json:"cta_text"`
	ExploreText string `json:"explore_text"`
	Caption     string `json:"caption"`
	Placeholder string `json:"placeholder"`
	SuccessText string `json:"success_text"`
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
	HeroTypeEmbed               HeroType = 7
	HeroTypeDrop                HeroType = 8
	HeroTypeLastChance          HeroType = 9
	HeroTypeMarquee             HeroType = 10
	HeroTypeNewArrivals         HeroType = 11
	HeroTypeSlideshow           HeroType = 12
	HeroTypeMosaic              HeroType = 13
	HeroTypeSplit               HeroType = 14
	HeroTypeVideo               HeroType = 15
	HeroTypeProductSpotlight    HeroType = 16
	HeroTypeNewsletter          HeroType = 17
	HeroTypeStatement           HeroType = 18
	HeroTypeLookbook            HeroType = 19
)

// HeroAudience is the TARGETING modifier (carried through; enforcement pending
// the frontend GetHero learning the viewer's tier).
type HeroAudience int32

const (
	HeroAudienceUnknown HeroAudience = 0
	HeroAudienceAll     HeroAudience = 1
	HeroAudienceGuests  HeroAudience = 2
	HeroAudienceMembers HeroAudience = 3
	HeroAudienceTier    HeroAudience = 4
)

// ─── read side (resolved) ───────────────────────────────────────────────────

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
	FeaturedArchiveId string                               `json:"featured_archive_id"` // string to match proto
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
	Embed               *HeroEmbedWithTranslations               `json:"embed"`
	Drop                *HeroDropWithTranslations                `json:"drop"`
	LastChance          *HeroLastChanceWithTranslations          `json:"last_chance"`
	Marquee             *HeroMarqueeWithTranslations             `json:"marquee"`
	NewArrivals         *HeroNewArrivalsWithTranslations         `json:"new_arrivals"`
	Slideshow           *HeroSlideshowWithTranslations           `json:"slideshow"`
	Mosaic              *HeroMosaicWithTranslations              `json:"mosaic"`
	Split               *HeroSplitWithTranslations               `json:"split"`
	Video               *HeroVideoWithTranslations               `json:"video"`
	ProductSpotlight    *HeroProductSpotlightWithTranslations    `json:"product_spotlight"`
	Newsletter          *HeroNewsletterWithTranslations          `json:"newsletter"`
	Statement           *HeroStatementWithTranslations           `json:"statement"`
	Lookbook            *HeroLookbookWithTranslations            `json:"lookbook"`
	Audience            HeroAudience                             `json:"audience"`
	MinTierId           int                                      `json:"min_tier_id"`
}

type HeroSingleWithTranslations struct {
	Media        HeroMediaFull         `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroDoubleWithTranslations struct {
	Left  HeroSingleWithTranslations `json:"left"`
	Right HeroSingleWithTranslations `json:"right"`
}

type HeroMainWithTranslations struct {
	Media        HeroMediaFull         `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroFeaturedProductsWithTranslations struct {
	Products     []Product             `json:"products"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroFeaturedProductsTagWithTranslations struct {
	Tag          string                               `json:"tag"`
	Products     HeroFeaturedProductsWithTranslations `json:"products"`
	Translations []HeroCopyTranslation                `json:"translations"`
}

type HeroFeaturedArchiveWithTranslations struct {
	Archive      ArchiveFull           `json:"archive"`
	Tag          string                `json:"tag"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroEmbedWithTranslations struct {
	EmbedUrl     string                `json:"embed_url"`
	Fallback     HeroMediaFull         `json:"fallback"`
	CtaLink      string                `json:"cta_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroDropWithTranslations struct {
	Media        HeroMediaFull         `json:"media"`
	ReleaseAt    time.Time             `json:"release_at"`
	ExploreLink  string                `json:"explore_link"`
	Tag          string                `json:"tag"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroLastChanceWithTranslations struct {
	Products     []Product             `json:"products"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroMarqueeWithTranslations struct {
	Link         string                `json:"link"`
	Speed        int                   `json:"speed"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroNewArrivalsWithTranslations struct {
	Products     []Product             `json:"products"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroSlideshowWithTranslations struct {
	Slides     []HeroSingleWithTranslations `json:"slides"`
	IntervalMs int                          `json:"interval_ms"`
}

type HeroMosaicWithTranslations struct {
	Tiles   []HeroSingleWithTranslations `json:"tiles"`
	Columns int                          `json:"columns"`
}

type HeroSplitWithTranslations struct {
	Media     HeroSingleWithTranslations `json:"media"`
	Products  []Product                  `json:"products"`
	MediaLeft bool                       `json:"media_left"`
}

type HeroVideoWithTranslations struct {
	Media        MediaFull             `json:"media"`
	PosterMedia  MediaFull             `json:"poster_media"`
	Autoplay     bool                  `json:"autoplay"`
	Loop         bool                  `json:"loop"`
	Muted        bool                  `json:"muted"`
	CtaLink      string                `json:"cta_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroProductSpotlightWithTranslations struct {
	Product      Product               `json:"product"`
	Media        HeroMediaFull         `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroNewsletterWithTranslations struct {
	Media        HeroMediaFull         `json:"media"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroStatementWithTranslations struct {
	Media        HeroMediaFull         `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroLookbookWithTranslations struct {
	Frames       []HeroSingleWithTranslations `json:"frames"`
	ExploreLink  string                       `json:"explore_link"`
	Translations []HeroCopyTranslation        `json:"translations"`
}

// ─── translations (nav still carries its own minimal shape) ─────────────────

type NavFeaturedEntityInsertTranslation struct {
	LanguageId  int    `json:"language_id"`
	ExploreText string `json:"explore_text"`
}

// ─── write side (insert) — this is what is persisted in the hero table ──────

type HeroFullInsert struct {
	Entities    []HeroEntityInsert `json:"entities"`
	NavFeatured NavFeaturedInsert  `json:"nav_featured"`
}

type HeroEntityInsert struct {
	Type                HeroType                      `json:"type"`
	Single              HeroSingleInsert              `json:"single"`
	Double              HeroDoubleInsert              `json:"double"`
	Main                HeroMainInsert                `json:"main"`
	FeaturedProducts    HeroFeaturedProductsInsert    `json:"featured_products"`
	FeaturedProductsTag HeroFeaturedProductsTagInsert `json:"featured_products_tag"`
	FeaturedArchive     HeroFeaturedArchiveInsert     `json:"featured_archive"`
	Embed               HeroEmbedInsert               `json:"embed"`
	Drop                HeroDropInsert                `json:"drop"`
	LastChance          HeroLastChanceInsert          `json:"last_chance"`
	Marquee             HeroMarqueeInsert             `json:"marquee"`
	NewArrivals         HeroNewArrivalsInsert         `json:"new_arrivals"`
	Slideshow           HeroSlideshowInsert           `json:"slideshow"`
	Mosaic              HeroMosaicInsert              `json:"mosaic"`
	Split               HeroSplitInsert               `json:"split"`
	Video               HeroVideoInsert               `json:"video"`
	ProductSpotlight    HeroProductSpotlightInsert    `json:"product_spotlight"`
	Newsletter          HeroNewsletterInsert          `json:"newsletter"`
	Statement           HeroStatementInsert           `json:"statement"`
	Lookbook            HeroLookbookInsert            `json:"lookbook"`
	Audience            HeroAudience                  `json:"audience"`
	MinTierId           int                           `json:"min_tier_id"`
}

type HeroSingleInsert struct {
	Media        HeroMedia             `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroDoubleInsert struct {
	Left  HeroSingleInsert `json:"left"`
	Right HeroSingleInsert `json:"right"`
}

type HeroMainInsert struct {
	Media        HeroMedia             `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroFeaturedProductsInsert struct {
	ProductIDs   []int                 `json:"product_ids"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroFeaturedProductsTagInsert struct {
	Tag          string                `json:"tag"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroFeaturedArchiveInsert struct {
	ArchiveId    int                   `json:"archive_id"`
	Tag          string                `json:"tag"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroEmbedInsert struct {
	EmbedUrl     string                `json:"embed_url"`
	Fallback     HeroMedia             `json:"fallback"`
	CtaLink      string                `json:"cta_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroDropInsert struct {
	Media        HeroMedia             `json:"media"`
	ReleaseAt    time.Time             `json:"release_at"`
	ExploreLink  string                `json:"explore_link"`
	Tag          string                `json:"tag"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroLastChanceInsert struct {
	StockThreshold int                   `json:"stock_threshold"`
	Limit          int                   `json:"limit"`
	ExploreLink    string                `json:"explore_link"`
	Translations   []HeroCopyTranslation `json:"translations"`
}

type HeroMarqueeInsert struct {
	Link         string                `json:"link"`
	Speed        int                   `json:"speed"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroNewArrivalsInsert struct {
	Limit        int                   `json:"limit"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroSlideshowInsert struct {
	Slides     []HeroSingleInsert `json:"slides"`
	IntervalMs int                `json:"interval_ms"`
}

type HeroMosaicInsert struct {
	Tiles   []HeroSingleInsert `json:"tiles"`
	Columns int                `json:"columns"`
}

type HeroSplitInsert struct {
	Media      HeroSingleInsert `json:"media"`
	ProductIDs []int            `json:"product_ids"`
	MediaLeft  bool             `json:"media_left"`
}

type HeroVideoInsert struct {
	MediaId       int                   `json:"media_id"`
	PosterMediaId int                   `json:"poster_media_id"`
	Autoplay      bool                  `json:"autoplay"`
	Loop          bool                  `json:"loop"`
	Muted         bool                  `json:"muted"`
	CtaLink       string                `json:"cta_link"`
	Translations  []HeroCopyTranslation `json:"translations"`
}

type HeroProductSpotlightInsert struct {
	ProductId    int                   `json:"product_id"`
	Media        HeroMedia             `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroNewsletterInsert struct {
	Media        HeroMedia             `json:"media"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroStatementInsert struct {
	Media        HeroMedia             `json:"media"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
}

type HeroLookbookInsert struct {
	Frames       []HeroSingleInsert    `json:"frames"`
	ExploreLink  string                `json:"explore_link"`
	Translations []HeroCopyTranslation `json:"translations"`
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
