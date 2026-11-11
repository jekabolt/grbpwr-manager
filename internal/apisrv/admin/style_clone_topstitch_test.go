package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A season clone copies the pieces, so a fresh fingerprint would read a stale topstitch line as measured
// on today's pattern. The clone keeps each line's freshness, as a restore does.
func TestCloneStyleForSeasonKeepsStaleTopstitchLinesStale(t *testing.T) {
	dto.SetManagedPatternHosts(restoreHost)
	t.Cleanup(func() { dto.SetManagedPatternHosts() })
	source := restoreCardAtV6()
	header := cloneSourceCard()
	source.Id, source.LockVersion = header.Id, header.LockVersion
	source.StyleNumber, source.StyleNumberSource = header.StyleNumber, header.StyleNumberSource
	source.SeasonCode, source.SeasonYear = header.SeasonCode, header.SeasonYear
	source.BomItems = append(source.BomItems, entity.TechCardBomItem{
		Id: 12, LineKey: restoreTopThread, Section: entity.BomSectionThread, Name: "thread",
		Unit:        sql.NullString{String: "m", Valid: true},
		ThreadPerMm: decimal.NewNullDecimal(decimal.RequireFromString("2.5")),
	})
	for i := range source.Operations {
		source.Operations[i].BomLineKeys = []string{restoreTopThread}
	}
	source.Operations[0].TopstitchLines = []entity.TechCardTopstitchLine{{
		LineKey: restoreTopStale, BomLineKey: restoreTopThread,
		Parts: []entity.TechCardSeamAnchor{restoreTopstitchAnchor(keyFront)}, Rows: 1, Stale: true,
	}}
	source.Operations[1].TopstitchLines = []entity.TechCardTopstitchLine{{
		LineKey: restoreTopFresh, BomLineKey: restoreTopThread,
		Parts: []entity.TechCardSeamAnchor{restoreTopstitchAnchor(keyCollar)}, Rows: 1,
	}}

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(source, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	var written *entity.TechCardInsert
	tc.EXPECT().CloneTechCardForSeason(mock.Anything, 7, 4, mock.AnythingOfType("*entity.TechCardInsert")).
		Run(func(_ context.Context, _, _ int, insert *entity.TechCardInsert) { written = insert }).Return(11, nil)

	_, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.NoError(t, err)
	require.NotNil(t, written)
	stale := written.Operations[0].TopstitchLines[0]
	fresh := written.Operations[1].TopstitchLines[0]
	require.True(t, stale.RestoreSourceFingerprint)
	require.True(t, stale.RestoreStale)
	require.True(t, fresh.RestoreSourceFingerprint)
	require.False(t, fresh.RestoreStale)
}
