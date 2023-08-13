package dto

import "time"

type Hero struct {
	TimeChanged time.Time
	HeroLeft    HeroElement
	HeroRight   HeroElement
}

type HeroElement struct {
	ContentLink string
	ContentType string
	ExploreLink string
	ExploreText string
}
