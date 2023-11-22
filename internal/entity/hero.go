package entity

import "time"

type HeroInsert struct {
	ContentLink string `db:"content_link" valid:"required,url"`
	ContentType string `db:"content_type" valid:"required"`
	ExploreLink string `db:"explore_link" valid:"required,url"`
	ExploreText string `db:"explore_text" valid:"required"`
}

type HeroFull struct {
	Id               int          `db:"id"`
	CreatedAt        time.Time    `db:"created_at"`
	Main             HeroInsert   `valid:"required"`
	Ads              []HeroInsert `valid:"required"`
	ProductsFeatured []Product
}
