package frontend

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// subscribeServer wires the minimum needed to drive SubscribeNewsletter.
func subscribeServer(t *testing.T) (*Server, *mocks.MockStorefrontAccount, context.Context) {
	t.Helper()
	mockRepo := mocks.NewMockRepository(t)
	acc := mocks.NewMockStorefrontAccount(t)
	subs := mocks.NewMockSubscribers(t)
	mockRepo.EXPECT().StorefrontAccount().Return(acc).Maybe()
	mockRepo.EXPECT().Subscribers().Return(subs).Maybe()
	subs.EXPECT().UpsertSubscription(mock.Anything, mock.Anything, false).Return(false, nil).Maybe()

	srv, err := New(mockRepo, mocks.NewMockMailer(t), nil, nil, nil, nil, storefrontConfig())
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), middleware.ClientIPKey, "1.2.3.4")
	return srv, acc, ctx
}

func subscribeReq(email, lang string) *pb_frontend.SubscribeNewsletterRequest {
	return &pb_frontend.SubscribeNewsletterRequest{
		Email:              email,
		Language:           lang,
		ShoppingPreference: pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_ALL,
	}
}

// TestSubscribeNewsletter_DoesNotSeedLanguageOfExistingAccount pins the fix for the
// unauthenticated language-seed: anyone can submit anyone's address here, so an account that
// already exists must keep its own mail language (default_language drives transactional mail).
func TestSubscribeNewsletter_DoesNotSeedLanguageOfExistingAccount(t *testing.T) {
	srv, acc, ctx := subscribeServer(t)
	const email = "victim@example.com"

	existing := &entity.StorefrontAccount{
		Email:              email,
		ShoppingPreference: entity.StorefrontShoppingAll,
		EmailLanguage:      sql.NullString{String: "en", Valid: true},
	}
	acc.EXPECT().GetAccountByEmail(mock.Anything, email).Return(existing, nil).Once()
	acc.EXPECT().GetOrCreateAccountByEmail(mock.Anything, email).Return(existing, nil).Once()
	acc.EXPECT().UpdateAccountProfile(mock.Anything, email,
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		sql.NullString{}, // default_language untouched
		sql.NullString{String: "en", Valid: true}, // explicit email_language preserved
	).Return(nil).Once()

	_, err := srv.SubscribeNewsletter(ctx, subscribeReq(email, "ja"))
	assert.NoError(t, err)
}

// TestSubscribeNewsletter_SeedsLanguageForNewAccount keeps the intended behavior: a first-time
// address gets the signup locale so its welcome mail is in the right language.
func TestSubscribeNewsletter_SeedsLanguageForNewAccount(t *testing.T) {
	srv, acc, ctx := subscribeServer(t)
	const email = "newcomer@example.com"

	acc.EXPECT().GetAccountByEmail(mock.Anything, email).Return(nil, sql.ErrNoRows).Once()
	acc.EXPECT().GetOrCreateAccountByEmail(mock.Anything, email).Return(&entity.StorefrontAccount{
		Email:              email,
		ShoppingPreference: entity.StorefrontShoppingAll,
	}, nil).Once()
	acc.EXPECT().UpdateAccountProfile(mock.Anything, email,
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		sql.NullString{String: "ja", Valid: true}, // seeded from the signup locale
		sql.NullString{},
	).Return(nil).Once()

	_, err := srv.SubscribeNewsletter(ctx, subscribeReq(email, "ja"))
	assert.NoError(t, err)
}
