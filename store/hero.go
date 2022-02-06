package store

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type Hero struct {
	TimeChanged string `json:"timeChanged" env:"HERO_TIME_CHANGED" envDefault:""`
	ContentLink string `json:"contentLink" env:"HERO_CONTENT_LINK" envDefault:""`
	ContentType string `json:"contentType" env:"HERO_CONTENT_TYPE" envDefault:""`
	ExploreLink string `json:"exploreLink" env:"HERO_EXPLORE_LINK" envDefault:""`
	ExploreText string `json:"exploreText" env:"HERO_EXPLORE_TEXT" envDefault:""`
}

func (h *Hero) Bind(r *http.Request) error {
	return h.Validate()
}

func (h *Hero) Validate() error {

	if len(h.TimeChanged) == 0 {
		return fmt.Errorf("missing TimeChanged")
	}

	if len(h.ContentLink) == 0 {
		return fmt.Errorf("missing ContentLink")
	}

	if len(h.ContentType) == 0 {
		return fmt.Errorf("missing ContentType")
	}

	if len(h.ExploreLink) == 0 {
		return fmt.Errorf("missing ExploreLink")
	}
	if len(h.ExploreText) == 0 {
		return fmt.Errorf("missing ExploreText")
	}

	return nil

}

func (h *Hero) String() string {
	bs, _ := json.Marshal(h)
	return string(bs)
}
