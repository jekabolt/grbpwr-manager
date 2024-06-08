package entity

import "time"

type HeroInsert struct {
	MediaId     int    `db:"media_id" valid:"required"`
	ExploreLink string `db:"explore_link" valid:"required,url"`
	ExploreText string `db:"explore_text" valid:"required"`
	IsMain      bool   `db:"main"`
}

type HeroItem struct {
	Media       *MediaFull
	ExploreLink string `db:"explore_link" valid:"required,url"`
	ExploreText string `db:"explore_text" valid:"required"`
	IsMain      bool   `db:"main"`
}

type HeroFull struct {
	Main             *HeroItem  `valid:"required"`
	Ads              []HeroItem `valid:"required"`
	ProductsFeatured []Product
}

type HeroFullInsert struct {
	CreatedAt        time.Time  `db:"created_at"`
	Main             *HeroItem  `valid:"required"`
	Ads              []HeroItem `valid:"required"`
	ProductsFeatured []Product
}
