package entity

import "time"

// Hero represents the hero table
type Hero struct {
	TimeChanged      time.Time `db:"time_changed"`
	ContentLinkLeft  string    `db:"content_link_left"`
	ContentTypeLeft  string    `db:"content_type_left"`
	ExploreLinkLeft  string    `db:"explore_link_left"`
	ExploreTextLeft  string    `db:"explore_text_left"`
	ContentLinkRight string    `db:"content_link_right"`
	ContentTypeRight string    `db:"content_type_right"`
	ExploreLinkRight string    `db:"explore_link_right"`
	ExploreTextRight string    `db:"explore_text_right"`
}
