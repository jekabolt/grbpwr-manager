package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The owner's under/over and the hand placement travel in seams.json both ways; unset stays unset.
func TestSeamArchiveKeepsLayerAndPlacement(t *testing.T) {
	side := []entity.TechCardSeamAnchor{
		{PieceLineKey: "BAG", Samples: []entity.TechCardSeamSample{{U: 0.5, V: 0.5}}, Layer: entity.TechCardSeamLayerOver,
			Placement: &entity.TechCardSeamPlacement{XMm: 12, YMm: 30, RotDeg: 90, Flip: true}},
		{PieceLineKey: "FRONT", Samples: []entity.TechCardSeamSample{{U: 1, V: 0}}},
	}
	back := seamSideFromArchive(seamSideToArchive(side))
	require.Equal(t, side, back)
}
