package entity

import "time"

type Media struct {
	Id        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	MediaInsert
}

type MediaInsert struct {
	FullSize   string `db:"full_size"`
	Thumbnail  string `db:"thumbnail"`
	Compressed string `db:"compressed"`
}
