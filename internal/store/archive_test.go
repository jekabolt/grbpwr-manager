package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

func TestArchiveCRUD(t *testing.T) {
	db := newTestDB(t)
	as := db.Archive()
	ctx := context.Background()

	an := &entity.ArchiveNew{
		Archive: &entity.ArchiveInsert{
			Title:       "test",
			Description: "test",
		},
		Items: []entity.ArchiveItemInsert{
			{
				Media: "1",
				URL: sql.NullString{
					String: "test url 1",
					Valid:  true,
				},
				Title: sql.NullString{
					String: "test title 1",
					Valid:  true,
				},
			},
			{
				Media: "2",
				URL: sql.NullString{
					String: "test url 2",
					Valid:  true,
				},
				Title: sql.NullString{
					String: "test title 2",
					Valid:  true,
				},
			},
		},
	}
	aid, err := as.AddArchive(ctx, an)
	assert.NoError(t, err)

	err = as.AddArchiveItems(ctx, aid, []entity.ArchiveItemInsert{
		{
			Media: "3",
			URL: sql.NullString{
				String: "test url 3",
				Valid:  true,
			},
			Title: sql.NullString{
				String: "test title 3",
				Valid:  true,
			},
		},
	})
	assert.NoError(t, err)

	archive, err := as.GetArchiveById(ctx, aid)
	assert.NoError(t, err)
	assert.Equal(t, "test", archive.Archive.Title)
	assert.Len(t, archive.Items, 3)

	err = as.DeleteArchiveItem(ctx, archive.Items[0].ID)
	assert.NoError(t, err)

	archive, err = as.GetArchiveById(ctx, aid)
	assert.NoError(t, err)
	assert.Len(t, archive.Items, 2)

	an.Archive.Title = "test2"
	aidNew, err := as.AddArchive(ctx, an)
	assert.NoError(t, err)

	archives, err := as.GetArchivesPaged(ctx, 10, 0, entity.Ascending)
	assert.NoError(t, err)
	assert.Len(t, archives, 2)

	err = as.DeleteArchiveById(ctx, aidNew)
	assert.NoError(t, err)

	archives, err = as.GetArchivesPaged(ctx, 10, 0, entity.Ascending)
	assert.NoError(t, err)
	assert.Len(t, archives, 1)

	err = as.DeleteArchiveById(ctx, aid)
	assert.NoError(t, err)

}
