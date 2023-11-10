package store

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

func TestMediaCRUD(t *testing.T) {
	db := newTestDB(t)
	ms := db.Media()
	ctx := context.Background()

	err := ms.AddMedia(ctx, &entity.MediaInsert{
		FullSize:   "https://example.com/fullsize.jpg",
		Thumbnail:  "https://example.com/thumb.jpg",
		Compressed: "https://example.com/compressed.jpg",
	})
	assert.NoError(t, err)

	mediaPage, err := ms.ListMediaPaged(ctx, 10, 0, entity.Ascending)
	assert.NoError(t, err)
	assert.Len(t, mediaPage, 1)
	assert.Equal(t, "https://example.com/fullsize.jpg", mediaPage[0].FullSize)

	err = ms.AddMedia(ctx, &entity.MediaInsert{
		FullSize:   "https://example2.com/fullsize.jpg",
		Thumbnail:  "https://example2.com/thumb.jpg",
		Compressed: "https://example2.com/compressed.jpg",
	})
	assert.NoError(t, err)

	mediaPage, err = ms.ListMediaPaged(ctx, 10, 0, entity.Ascending)
	assert.NoError(t, err)
	assert.Len(t, mediaPage, 2)

	mediaPage, err = ms.ListMediaPaged(ctx, 10, 1, entity.Ascending)
	assert.NoError(t, err)
	assert.Len(t, mediaPage, 1)

	err = ms.DeleteMediaById(ctx, mediaPage[0].Id)
	assert.NoError(t, err)

	mediaPage, err = ms.ListMediaPaged(ctx, 10, 0, entity.Ascending)
	assert.NoError(t, err)
	assert.Len(t, mediaPage, 1)
	assert.Equal(t, "https://example.com/fullsize.jpg", mediaPage[0].FullSize)

}
