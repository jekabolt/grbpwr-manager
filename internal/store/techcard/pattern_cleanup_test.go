package techcard

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestPatternURLsRemovedByPayloadIgnoresServerFilteredRows(t *testing.T) {
	const (
		originURL = "https://patterns.fra1.digitaloceanspaces.com/base/tech-card-patterns/2026/august/sheet.pdf"
		cdnURL    = "https://cdn.example/base/tech-card-patterns/2026/august/sheet.pdf"
		removed   = "https://cdn.example/base/tech-card-patterns/2026/august/removed.pdf"
	)
	prior := []patternHistoryRow{
		{URL: originURL, SizeId: 10},
		{URL: originURL, SizeId: 20},
		{URL: removed, SizeId: 10},
	}
	payload := []entity.TechCardSizePattern{
		// The current size range may filter this row later. It still proves the user carried the object.
		{URL: cdnURL, SizeId: 999},
	}

	require.Equal(t, []string{removed}, patternURLsRemovedByPayload(prior, payload))
}
