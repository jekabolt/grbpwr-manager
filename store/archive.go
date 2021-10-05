package store

import (
	"encoding/json"
	"fmt"
)

type ArchiveArticle struct {
	Id          int64     `json:"id"`
	DateCreated int64     `json:"dateCreated"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	MainImage   string    `json:"mainImage"`
	Content     []Content `json:"content"`
}
type Content struct {
	MediaLink              string `json:"mediaLink"`
	Description            string `json:"description"`
	DescriptionAlternative string `json:"descriptionAlternative"`
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

	return nil

}
