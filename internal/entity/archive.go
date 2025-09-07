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
	MainMedia   MediaFull `db:"main_media" json:"main_media"`
	Media       []MediaFull
}

type ArchiveInsert struct {
	Tag          string               `db:"tag" json:"tag"`
	MainMediaId  int                  `db:"main_media_id" json:"main_media_id"`
	ThumbnailId  int                  `db:"thumbnail_id" json:"thumbnail_id"`
	MediaIds     []int                `db:"media_ids" json:"media_ids"`
	Translations []ArchiveTranslation `db:"translations" json:"translations"`
}

type ArchiveTranslation struct {
	LanguageId  int    `db:"language_id" json:"language_id"`
	Heading     string `db:"heading" json:"heading"`
	Description string `db:"description" json:"description"`
}
