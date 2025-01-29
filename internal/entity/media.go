package entity

import (
	"database/sql"
	"time"
)

type MediaFull struct {
	Id        int       `db:"id" json:"id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	MediaItem
}

type MediaItem struct {
	FullSizeMediaURL   string         `db:"full_size" json:"full_size"`
	FullSizeWidth      int            `db:"full_size_width" json:"full_size_width"`
	FullSizeHeight     int            `db:"full_size_height" json:"full_size_height"`
	ThumbnailMediaURL  string         `db:"thumbnail" json:"thumbnail"`
	ThumbnailWidth     int            `db:"thumbnail_width" json:"thumbnail_width"`
	ThumbnailHeight    int            `db:"thumbnail_height" json:"thumbnail_height"`
	CompressedMediaURL string         `db:"compressed" json:"compressed"`
	CompressedWidth    int            `db:"compressed_width" json:"compressed_width"`
	CompressedHeight   int            `db:"compressed_height" json:"compressed_height"`
	BlurHash           sql.NullString `db:"blur_hash" json:"blur_hash"`
}
