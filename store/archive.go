package store

import (
	"encoding/json"
	"errors"
	"fmt"
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

type ArchiveArticle struct {
	Id               int64     `json:"id"`
	DateCreated      int64     `json:"dateCreated"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	ShortDescription string    `json:"shortDescription"`
	MainImage        string    `json:"mainImage"`
	Content          []Content `json:"content"`
}
type Content struct {
	MediaLink              string       `json:"mediaLink"`
	TextPosition           TextPosition `json:"textPosition"`
	Description            string       `json:"description"`
	DescriptionAlternative string       `json:"descriptionAlternative"` // TODO: deprecated
}

func (aa *ArchiveArticle) String() string {
	bs, _ := json.Marshal(aa)
	return string(bs)
}
func getArchiveArticleFromString(archiveArticle string) ArchiveArticle {
	aa := &ArchiveArticle{}
	json.Unmarshal([]byte(archiveArticle), aa)
	return *aa
}

func (p *ArchiveArticle) Validate() error {

	if len(p.Title) == 0 {
		return fmt.Errorf("missing title")
	}

	if len(p.Description) == 0 {
		return fmt.Errorf("missing description")
	}

	if len(p.MainImage) == 0 {
		return fmt.Errorf("no main image")
	}

	if len(p.Content) == 0 {
		return fmt.Errorf("content should have at least one record")
	}

	for _, c := range p.Content {
		if err := c.TextPosition.IsValid(); err != nil {
			return err
		}
	}
	return nil

}
