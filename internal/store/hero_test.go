package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetHero(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	content := "example.com/content"
	contentType := "text/html"
	exploreLink := "example.com/explore"
	exploreText := "Explore more"

	err := db.SetHero(context.Background(), content, contentType, exploreLink, exploreText)
	assert.NoError(t, err)

	hero, err := db.GetHero(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, content, hero.ContentLink)
}
