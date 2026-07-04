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
	Items       []ArchiveItemFull `json:"items"`
}

type ArchiveInsert struct {
	Tag          string               `db:"tag" json:"tag"`
	ThumbnailId  int                  `db:"thumbnail_id" json:"thumbnail_id"`
	Items        []ArchiveItemInsert  `json:"items"`
	Translations []ArchiveTranslation `db:"translations" json:"translations"`
}

// ArchiveTranslation is the archive's translatable copy: the title (heading)
// only — the archive has no description.
type ArchiveTranslation struct {
	LanguageId int    `db:"language_id" json:"language_id"`
	Heading    string `db:"heading" json:"heading"`
}

// ArchiveItemType discriminates a timeline body block.
type ArchiveItemType int32

const (
	ArchiveItemTypeUnknown          ArchiveItemType = 0
	ArchiveItemTypeMainMedia        ArchiveItemType = 1
	ArchiveItemTypeMediaLine        ArchiveItemType = 2
	ArchiveItemTypeText             ArchiveItemType = 3
	ArchiveItemTypeEmbed            ArchiveItemType = 4
	ArchiveItemTypeMediaWithCaption ArchiveItemType = 5
	ArchiveItemTypeProduct          ArchiveItemType = 6
	ArchiveItemTypeProductsTag      ArchiveItemType = 7
	ArchiveItemTypeProductsManual   ArchiveItemType = 8
)

// ArchiveMediaAspectRatio is the presentation aspect ratio for the media blocks.
type ArchiveMediaAspectRatio int32

const (
	ArchiveMediaAspectRatioUnknown ArchiveMediaAspectRatio = 0
	ArchiveMediaAspectRatio16x9    ArchiveMediaAspectRatio = 1
	ArchiveMediaAspectRatio2x1     ArchiveMediaAspectRatio = 2
	ArchiveMediaAspectRatio1x1     ArchiveMediaAspectRatio = 3
	ArchiveMediaAspectRatio3x4     ArchiveMediaAspectRatio = 4
)

// ArchiveItemTranslation is the per-block translation. text is used by TEXT
// blocks; caption by media_with_caption/embed/product/products blocks.
type ArchiveItemTranslation struct {
	LanguageId int    `json:"language_id"`
	Caption    string `json:"caption"`
	Text       string `json:"text"`
}

// ─── write side (stored body blocks) ─────────────────────────────────────────

type ArchiveMainMediaInsert struct {
	MediaId     int                     `json:"media_id"`
	AspectRatio ArchiveMediaAspectRatio `json:"aspect_ratio"`
}

type ArchiveMediaLineInsert struct {
	MediaIds    []int                   `json:"media_ids"`
	AspectRatio ArchiveMediaAspectRatio `json:"aspect_ratio"`
}

type ArchiveTextInsert struct {
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveEmbedInsert struct {
	EmbedUrl     string                   `json:"embed_url"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveMediaWithCaptionInsert struct {
	MediaId      int                      `json:"media_id"`
	Link         string                   `json:"link"`
	AspectRatio  ArchiveMediaAspectRatio  `json:"aspect_ratio"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveProductInsert struct {
	ProductId    int                      `json:"product_id"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveProductsTagInsert struct {
	Tag          string                   `json:"tag"`
	Limit        int                      `json:"limit"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveProductsManualInsert struct {
	ProductIds   []int                    `json:"product_ids"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

// ArchiveItemInsert is a single timeline body block as persisted (write side).
// Exactly one payload pointer is set, selected by Type.
type ArchiveItemInsert struct {
	Type             ArchiveItemType                `json:"type"`
	MainMedia        *ArchiveMainMediaInsert        `json:"main_media,omitempty"`
	MediaLine        *ArchiveMediaLineInsert        `json:"media_line,omitempty"`
	Text             *ArchiveTextInsert             `json:"text,omitempty"`
	Embed            *ArchiveEmbedInsert            `json:"embed,omitempty"`
	MediaWithCaption *ArchiveMediaWithCaptionInsert `json:"media_with_caption,omitempty"`
	Product          *ArchiveProductInsert          `json:"product,omitempty"`
	ProductsTag      *ArchiveProductsTagInsert      `json:"products_tag,omitempty"`
	ProductsManual   *ArchiveProductsManualInsert   `json:"products_manual,omitempty"`
}

// ─── read side (resolved) ────────────────────────────────────────────────────

type ArchiveMainMediaFull struct {
	Media       MediaFull               `json:"media"`
	AspectRatio ArchiveMediaAspectRatio `json:"aspect_ratio"`
}

type ArchiveMediaLineFull struct {
	Media       []MediaFull             `json:"media"`
	AspectRatio ArchiveMediaAspectRatio `json:"aspect_ratio"`
}

type ArchiveTextFull struct {
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveEmbedFull struct {
	EmbedUrl     string                   `json:"embed_url"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveMediaWithCaptionFull struct {
	Media        MediaFull                `json:"media"`
	Link         string                   `json:"link"`
	AspectRatio  ArchiveMediaAspectRatio  `json:"aspect_ratio"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveProductFull struct {
	Product      *Product                 `json:"product"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveProductsTagFull struct {
	Tag          string                   `json:"tag"`
	Products     []Product                `json:"products"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

type ArchiveProductsManualFull struct {
	Products     []Product                `json:"products"`
	Translations []ArchiveItemTranslation `json:"translations"`
}

// ArchiveItemFull is a single resolved timeline body block (read side). Exactly
// one payload pointer is set, selected by Type.
type ArchiveItemFull struct {
	Type             ArchiveItemType              `json:"type"`
	MainMedia        *ArchiveMainMediaFull        `json:"main_media,omitempty"`
	MediaLine        *ArchiveMediaLineFull        `json:"media_line,omitempty"`
	Text             *ArchiveTextFull             `json:"text,omitempty"`
	Embed            *ArchiveEmbedFull            `json:"embed,omitempty"`
	MediaWithCaption *ArchiveMediaWithCaptionFull `json:"media_with_caption,omitempty"`
	Product          *ArchiveProductFull          `json:"product,omitempty"`
	ProductsTag      *ArchiveProductsTagFull      `json:"products_tag,omitempty"`
	ProductsManual   *ArchiveProductsManualFull   `json:"products_manual,omitempty"`
}
