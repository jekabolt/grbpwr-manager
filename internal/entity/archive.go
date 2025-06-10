package entity

import (
	"time"
)

type ArchiveList struct {
	Id          int       `db:"id" json:"id"`
	Heading     string    `db:"heading" json:"heading"`
	Description string    `db:"description" json:"description"`
	Tag         string    `db:"tag" json:"tag"`
	Slug        string    `json:"slug"`
	NextSlug    string    `json:"next_slug"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	Thumbnail   MediaFull `db:"thumbnail" json:"thumbnail"`
}

type ArchiveFull struct {
	ArchiveList ArchiveList
	MainMedia   MediaFull `db:"main_media" json:"main_media"`
	Media       []MediaFull
}

type ArchiveInsert struct {
	Heading     string `db:"heading" json:"heading"`
	Description string `db:"description" json:"description"`
	Tag         string `db:"tag" json:"tag"`
	MainMediaId int    `db:"main_media_id" json:"main_media_id"`
	ThumbnailId int    `db:"thumbnail_id" json:"thumbnail_id"`
	MediaIds    []int  `db:"media_ids" json:"media_ids"`
}
