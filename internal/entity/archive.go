package entity

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/asaskevich/govalidator"
)

type ArchiveFull struct {
	Archive *Archive
	Items   []ArchiveItemFull
}

type ArchiveNew struct {
	Archive *ArchiveBody        `valid:"required"`
	Items   []ArchiveItemInsert `valid:"required"`
}

type Archive struct {
	Id        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	ArchiveBody
}

type ArchiveBody struct {
	Heading string `db:"heading"`
	Text    string `db:"text"`
}

type ArchiveItemFull struct {
	Id        int `db:"id" json:"id"`
	ArchiveID int `db:"archive_id" json:"archive_id"`
	ArchiveItem
}

func (ai *ArchiveItemFull) UnmarshalJSON(data []byte) error {
	type Alias ArchiveItemFull
	aux := &struct {
		URL  *string `json:"url"`
		Name *string `json:"name"`
		*Alias
	}{
		Alias: (*Alias)(ai),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.URL != nil {
		ai.URL = sql.NullString{String: *aux.URL, Valid: true}
	} else {
		ai.URL = sql.NullString{Valid: false}
	}

	if aux.Name != nil {
		ai.Name = sql.NullString{String: *aux.Name, Valid: true}
	} else {
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
