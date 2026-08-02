package admin

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDeleteTechCardNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().DeleteTechCardAndListOrphanedPatternURLs(mock.Anything, 404).
		Return(nil, fmt.Errorf("delete tech card: %w", sql.ErrNoRows))

	s := &Server{repo: repo}
	_, err := s.DeleteTechCard(context.Background(), &pb_admin.DeleteTechCardRequest{Id: 404})
	require.Equal(t, codes.NotFound, status.Code(err))
}
