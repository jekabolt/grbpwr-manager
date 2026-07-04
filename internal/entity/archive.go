package entity

import (
	"time"
)

type ArchiveList struct {
	Id           int                  `db:"id" json:"id"`
	Translations []ArchiveTranslation `db:"translations" json:"translations"`
	Tag          string               `db:"tag" json:"tag"`
	Slug         string               `json:"slug"`
	CreatedAt    time.Time            `db:"created_at" json:"created_at"`
	Thumbnail    MediaFull            `db:"thumbnail" json:"thumbnail"`
}

type ArchiveFull struct {
	ArchiveList ArchiveList
	MainMedia   []MediaFull       `db:"main_media" json:"main_media"`
	Items       []ArchiveItemFull `json:"items"`
}

type ArchiveInsert struct {
	Tag          string               `db:"tag" json:"tag"`
	MainMediaIds []int                `db:"main_media_ids" json:"main_media_ids"`
	ThumbnailId  int                  `db:"thumbnail_id" json:"thumbnail_id"`
	Items        []ArchiveItemInsert  `json:"items"`
	Translations []ArchiveTranslation `db:"translations" json:"translations"`
}

type ArchiveTranslation struct {
	LanguageId  int    `db:"language_id" json:"language_id"`
	Heading     string `db:"heading" json:"heading"`
	Description string `db:"description" json:"description"`
}

// ArchiveItemType discriminates a timeline body block.
type ArchiveItemType int32

const (
	ArchiveItemTypeUnknown        ArchiveItemType = 0
	ArchiveItemTypeMedia          ArchiveItemType = 1
	ArchiveItemTypeText           ArchiveItemType = 2
	ArchiveItemTypeEmbed          ArchiveItemType = 3
	ArchiveItemTypeProduct        ArchiveItemType = 4
	ArchiveItemTypeProductsTag    ArchiveItemType = 5
	ArchiveItemTypeProductsManual ArchiveItemType = 6
)

// ArchiveItemTranslation is the per-block translation. text is used by TEXT
// blocks; caption by media/embed/product/products blocks.
type ArchiveItemTranslation struct {
	LanguageId int    `json:"language_id"`
	Caption    string `json:"caption"`
	Text       string `json:"text"`
}

// ArchiveItemInsert is a single timeline body block as persisted (write side).
// Only the fields relevant to Type are meaningful.
type ArchiveItemInsert struct {
	Type         ArchiveItemType          `json:"type"`
	MediaId      int                      `json:"media_id"`     // MEDIA
	EmbedUrl     string                   `json:"embed_url"`    // EMBED
	ProductId    int                      `json:"product_id"`   // PRODUCT
	Tag          string                   `json:"tag"`          // PRODUCTS_TAG
	Limit        int                      `json:"limit"`        // PRODUCTS_TAG cap
	ProductIds   []int                    `json:"product_ids"`  // PRODUCTS_MANUAL
	Translations []ArchiveItemTranslation `json:"translations"` // caption / text
}

// ArchiveItemFull is the resolved timeline body block (read side).
type ArchiveItemFull struct {
	Type         ArchiveItemType          `json:"type"`
	Media        MediaFull                `json:"media"`        // MEDIA
	EmbedUrl     string                   `json:"embed_url"`    // EMBED
	Product      *Product                 `json:"product"`      // PRODUCT
	Tag          string                   `json:"tag"`          // PRODUCTS_TAG label
	Products     []Product                `json:"products"`     // PRODUCTS_TAG + PRODUCTS_MANUAL
	Translations []ArchiveItemTranslation `json:"translations"` // caption / text
}
