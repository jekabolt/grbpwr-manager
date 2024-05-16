package entity

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/asaskevich/govalidator"
)

type ArchiveFull struct {
	Archive *Archive
	Items   []ArchiveItem
}

type ArchiveNew struct {
	Archive *ArchiveInsert      `valid:"required"`
	Items   []ArchiveItemInsert `valid:"required"`
}

type Archive struct {
	ID        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	ArchiveInsert
}

type ArchiveInsert struct {
	Title       string `db:"title" valid:"required,utfletternum"`
	Description string `db:"description" valid:"required,utfletternum"`
}

type ArchiveItem struct {
	ID        int `db:"id"`
	ArchiveID int `db:"archive_id"`
	ArchiveItemInsert
}

func (ai *ArchiveItem) UnmarshalJSON(data []byte) error {
	type Alias ArchiveItem
	aux := &struct {
		URL   *string `json:"url"`
		Title *string `json:"title"`
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

	if aux.Title != nil {
		ai.Title = sql.NullString{String: *aux.Title, Valid: true}
	} else {
		ai.Title = sql.NullString{Valid: false}
	}

	return nil
}

type ArchiveItemInsert struct {
	Media string         `db:"media" valid:"required,url"`
	URL   sql.NullString `db:"url" valid:"url"`
	Title sql.NullString `db:"title" valid:"utfletternum"`
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
