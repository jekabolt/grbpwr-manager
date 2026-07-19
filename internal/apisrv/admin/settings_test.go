package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// newSettingsTestServer builds a Server wired just enough for UpdateSettings: a mock repo plus a
// no-op revalidation path (UpdateSettings always fires revalidateAsync at the end).
func newSettingsTestServer(t *testing.T, repo *mocks.MockRepository) *Server {
	re := mocks.NewMockRevalidationService(t)
	re.EXPECT().RevalidateAll(mock.Anything, mock.Anything).Return(nil).Maybe()
	return &Server{
		repo:          repo,
		re:            re,
		revalidateSem: make(chan struct{}, 1),
		revalCtx:      context.Background(),
	}
}

// TestUpdateSettingsEmptyRequestTouchesNothing locks A1's core guarantee: a request with no fields set
// must apply NO setter — it must not disable the site, zero max-order-items, or wipe the announce
// banner. The mock repo has zero Settings() expectations, so any setter call fails the test.
func TestUpdateSettingsEmptyRequestTouchesNothing(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := newSettingsTestServer(t, repo)

	_, err := s.UpdateSettings(context.Background(), &pb_admin.UpdateSettingsRequest{})
	require.NoError(t, err)
	// No repo.Settings() / setter expectations were registered; a strict mock would have panicked on
	// any call. Reaching here with no error proves the empty request was a no-op.
}

// TestUpdateSettingsAppliesOnlyPresentFields locks A1's presence semantics: only explicitly-present
// scalar fields are applied, and an explicit `false`/`0` IS applied (presence, not truthiness). Here
// only site_available=false is present, so ONLY SetSiteAvailability(false) may be called.
func TestUpdateSettingsAppliesOnlyPresentFields(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	settings := mocks.NewMockSettings(t)
	repo.EXPECT().Settings().Return(settings)
	settings.EXPECT().SetSiteAvailability(mock.Anything, false).Return(nil).Once()

	s := newSettingsTestServer(t, repo)

	_, err := s.UpdateSettings(context.Background(), &pb_admin.UpdateSettingsRequest{
		SiteAvailable: proto.Bool(false), // explicit false -> must be applied, not skipped as zero value
		// max_order_items / big_menu / announce / order_expiration_seconds / is_prod all omitted:
		// no setter for them may be called (unregistered -> strict-mock failure).
	})
	require.NoError(t, err)
}

// TestUpdateSettingsAppliesAllPresentScalars covers the admin-UI case (full form submit): every scalar
// present -> every setter called exactly once with the submitted value.
func TestUpdateSettingsAppliesAllPresentScalars(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	settings := mocks.NewMockSettings(t)
	repo.EXPECT().Settings().Return(settings)
	settings.EXPECT().SetSiteAvailability(mock.Anything, true).Return(nil).Once()
	settings.EXPECT().SetMaxOrderItems(mock.Anything, 7).Return(nil).Once()
	settings.EXPECT().SetBigMenu(mock.Anything, true).Return(nil).Once()
	settings.EXPECT().SetOrderExpirationSeconds(mock.Anything, 900).Return(nil).Once()
	settings.EXPECT().SetPaymentIsProd(mock.Anything, true).Return(nil).Once()

	s := newSettingsTestServer(t, repo)

	_, err := s.UpdateSettings(context.Background(), &pb_admin.UpdateSettingsRequest{
		SiteAvailable:          proto.Bool(true),
		MaxOrderItems:          proto.Int32(7),
		BigMenu:                proto.Bool(true),
		OrderExpirationSeconds: proto.Int32(900),
		IsProd:                 proto.Bool(true),
		// announce omitted -> SetAnnounce must NOT be called.
	})
	require.NoError(t, err)
}
