package store

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/bucket"
)

type TextPosition string

const (
	Top    TextPosition = "top"
	Bottom TextPosition = "bottom"
	Left   TextPosition = "left"
	Right  TextPosition = "right"
)

func (tp *TextPosition) UnmarshalJSON(b []byte) error {
	var s string
	json.Unmarshal(b, &s)
	TP := TextPosition(s)
	err := TP.IsValid()
	if err != nil {
		return err
	}
	*tp = TP
	return nil
}

func (tp TextPosition) IsValid() error {
	switch tp {
	case Top, Bottom, Left, Right:
		return nil
	}
	return errors.New("invalid text position type")
}

type NewsArticle struct {
	Id               int64            `json:"id"`
	DateCreated      int64            `json:"dateCreated"`
	Title            string           `json:"title"`
	Description      string           `json:"description"`
	ShortDescription string           `json:"shortDescription"`
	MainImage        bucket.MainImage `json:"mainImage"`
	Content          []Content        `json:"content"`
}
type Content struct {
	Image        *bucket.Image `json:"image,omitempty"`
	MediaLink    string        `json:"mediaLink,omitempty"`
	TextPosition TextPosition  `json:"textPosition"`
	Description  string        `json:"description"`
}

func (na *NewsArticle) String() string {
	bs, _ := json.Marshal(na)
	return string(bs)
}
func GetNewsArticleFromString(newsArticle string) NewsArticle {
	na := &NewsArticle{}
	json.Unmarshal([]byte(newsArticle), na)
	return *na
}

func (na *NewsArticle) Validate() error {
	if na == nil {
		return fmt.Errorf("missing NewsArticle")
	}

	if len(na.Title) == 0 {
		return fmt.Errorf("NewsArticle missing title")
	}

	if len(na.Description) == 0 {
		return fmt.Errorf("NewsArticle missing description")
	}

	if len(na.MainImage.FullSize) == 0 {
		return fmt.Errorf("NewsArticle no main image")
	}

	if len(na.Content) == 0 {
		return fmt.Errorf("NewsArticle content should have at least one record")
	}

	for _, c := range na.Content {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil

}

func (c *Content) Validate() error {
	if c.MediaLink == "" {
		err := c.Image.Validate()
		if err != nil {
			return err
		}
	}
	if err := c.TextPosition.IsValid(); err != nil {
		return err
	}

	if len(c.Description) == 0 {
		return fmt.Errorf("missing content description [%v]", c.Description)
	}
	return nil
}
