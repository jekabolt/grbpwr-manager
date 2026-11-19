package admin

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGetTechCardReturnsAuthenticatedPatternURLForDedicatedTeam(t *testing.T) {
	ctx := tenant.With(context.Background(), tenant.Team{ID: 2})
	raw := "https://files.grbpwr.com/base/t/0123456789abcdef0123456789abcdef/tech-card-patterns/main.dxf"
	dxfSHA := strings.Repeat("a", 64)
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	accounting := mocks.NewMockAccounting(t)
	repo.EXPECT().TechCards().Return(cards).Twice()
	repo.EXPECT().Accounting().Return(accounting).Once()
	accounting.EXPECT().GetVatRatesFor(mock.Anything, mock.Anything).Return(nil, nil).Once()
	cards.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil).Once()
	cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 169).Return(&entity.TechCard{
		Id: 169,
		TechCardInsert: entity.TechCardInsert{
			Patterns: []entity.TechCardSizePattern{{
				LineKey: "01M5HJEQ163VWKDHWH5Y2XV5H6", URL: raw,
				SizeBytes: sql.NullInt64{Int64: 60241, Valid: true},
				SHA256:    sql.NullString{String: dxfSHA, Valid: true},
				Version:   13, SavedRowRevision: 6,
			}},
		},
	}, nil).Once()

	server := &Server{repo: repo}
	result, err := server.GetTechCard(ctx, &pb_admin.GetTechCardRequest{Id: 169})
	require.NoError(t, err)
	require.Empty(t, result.GetPatternViewerToken())
	patterns := result.GetTechCard().GetTechCard().GetPatterns()
	require.Len(t, patterns, 1)
	sheet := patterns[0]
	require.Equal(t, raw, sheet.GetUrl())
	require.Equal(t, dxfSHA, sheet.GetSha256())
	require.EqualValues(t, 6, sheet.GetSavedRowRevision())
	proxy, err := url.Parse(sheet.GetDownloadUrl())
	require.NoError(t, err)
	require.Empty(t, proxy.Host)
	require.Equal(t, "/api"+FileProxyPath, proxy.Path)
	require.Equal(t, raw, proxy.Query().Get("url"))
}
