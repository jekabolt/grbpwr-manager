package entity

import (
	"database/sql"
	"time"
)

type ArchiveFull struct {
	Id          int       `db:"id" json:"id"`
	Heading     string    `db:"heading" json:"heading"`
	Description string    `db:"description" json:"description"`
	Tag         string    `db:"tag" json:"tag"`
	Slug        string    `json:"slug"`
	NextSlug    string    `json:"next_slug"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	Media       []MediaFull
	Video       MediaFull
}

type ArchiveInsert struct {
	Heading     string        `db:"heading" json:"heading"`
	Description string        `db:"description" json:"description"`
	Tag         string        `db:"tag" json:"tag"`
	MediaIds    []int         `db:"media_ids" json:"media_ids"`
	VideoId     sql.NullInt32 `db:"video_id" json:"video_id"`
}
