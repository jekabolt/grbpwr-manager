package dto

import "time"

type Hero struct {
	TimeChanged time.Time
	ContentLink string
	ContentType string
	ExploreLink string
	ExploreText string
}
