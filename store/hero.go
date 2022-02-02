package store

import (
	"encoding/json"
	"fmt"
)

type Hero struct {
	TimeChanged string `json:"contentLink,omitempty"`
	ContentLink string `json:"contentLink,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	ExploreLink string `json:"exploreLink,omitempty"`
	ExploreText string `json:"exploreText,omitempty"`
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
