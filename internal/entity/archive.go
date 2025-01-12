package entity

import (
	"time"
)

type ArchiveFull struct {
	Id          int         `db:"id" json:"id"`
	Title       string      `db:"title" json:"title"`
	Description string      `db:"description" json:"description"`
	Tag         string      `db:"tag" json:"tag"`
	Slug        string      `json:"slug"`
	CreatedAt   time.Time   `db:"created_at" json:"created_at"`
	Media       []MediaFull `json:"media"`
}

type ArchiveInsert struct {
	Title       string `db:"title" json:"title"`
	Description string `db:"description" json:"description"`
	Tag         string `db:"tag" json:"tag"`
	MediaIds    []int  `db:"media_ids" json:"media_ids"`
}
