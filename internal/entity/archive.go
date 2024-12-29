package entity

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/asaskevich/govalidator"
)

type ArchiveFull struct {
	Archive *Archive          `json:"archive"`
	Items   []ArchiveItemFull `json:"items"`
}

type ArchiveNew struct {
	Archive *ArchiveBody        `valid:"required"`
	Items   []ArchiveItemInsert `valid:"required"`
}

type Archive struct {
	Id        int       `db:"id" json:"id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
	ArchiveBody
}

type ArchiveBody struct {
	Heading string `db:"heading" json:"heading"`
	Text    string `db:"text" json:"text"`
}

type ArchiveItemFull struct {
	Id        int `db:"id" json:"id"`
	ArchiveID int `db:"archive_id" json:"archive_id"`
	ArchiveItem
}

func (ai *ArchiveItemFull) UnmarshalJSON(data []byte) error {
	type Alias ArchiveItemFull
	aux := &struct {
		URL  interface{} `json:"url"`
		Name interface{} `json:"name"`
		*Alias
	}{
		Alias: (*Alias)(ai),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return fmt.Errorf("failed to unmarshal archive item: %w", err)
	}

	switch v := aux.URL.(type) {
	case string:
		ai.URL = sql.NullString{String: v, Valid: true}
	case map[string]interface{}:
		if valid, ok := v["Valid"].(bool); ok && valid {
			if str, ok := v["String"].(string); ok {
				ai.URL = sql.NullString{String: str, Valid: true}
			}
		} else {
			ai.URL = sql.NullString{Valid: false}
		}
	default:
		ai.URL = sql.NullString{Valid: false}
	}

	switch v := aux.Name.(type) {
	case string:
		ai.Name = sql.NullString{String: v, Valid: true}
	case map[string]interface{}:
		if valid, ok := v["Valid"].(bool); ok && valid {
			if str, ok := v["String"].(string); ok {
				ai.Name = sql.NullString{String: str, Valid: true}
			}
		} else {
			ai.Name = sql.NullString{Valid: false}
		}
	default:
		ai.Name = sql.NullString{Valid: false}
	}

	return nil
}

type ArchiveItemInsert struct {
	MediaId int            `db:"media_id"`
	URL     sql.NullString `db:"url"`
	Name    sql.NullString `db:"name"`
}

// ValidateArchiveNew validates the ArchiveNew struct
func (an *ArchiveNew) ValidateArchiveNew() error {
	_, err := govalidator.ValidateStruct(an)
	if err != nil {
		return err
	}
	for _, item := range an.Items {
		_, err := govalidator.ValidateStruct(&item)
		if err != nil {
			return err
		}
	}
	return nil
}

type ArchiveItem struct {
	Media MediaFull      `json:"media"`
	URL   sql.NullString `json:"url"`
	Name  sql.NullString `json:"name"`
}
