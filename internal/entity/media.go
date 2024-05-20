package entity

import "time"

type MediaFull struct {
	Id        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	MediaInsert
}

type MediaInsert struct {
	FullSizeMediaURL   string `db:"full_size"`
	FullSizeWidth      int    `db:"full_size_width"`
	FullSizeHeight     int    `db:"full_size_height"`
	ThumbnailMediaURL  string `db:"thumbnail"`
	ThumbnailWidth     int    `db:"thumbnail_width"`
	ThumbnailHeight    int    `db:"thumbnail_height"`
	CompressedMediaURL string `db:"compressed"`
	CompressedWidth    int    `db:"compressed_width"`
	CompressedHeight   int    `db:"compressed_height"`
}
