package frontend

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	testStorefrontSecret  = "test-access-jwt-secret-32bytes!!"
	testLoginPepper       = "test-login-pepper"
	testRefreshPepper     = "test-refresh-pepper"
	testMagicLinkBaseURL  = "https://example.com/login"
	testEmail             = "user@example.com"
)

func storefrontConfig() *storefront.Config {
	return &storefront.Config{
		AccessJWTSecret:             testStorefrontSecret,
		AccessJWTTTL:                "15m",
		RefreshTTL:                  "24h",
		LoginChallengeTTL:            "10m",
		LoginPepper:                 testLoginPepper,
		RefreshPepper:               testRefreshPepper,
		MagicLinkBaseURL:            testMagicLinkBaseURL,
		AccessJtiRevocationEnabled: false,
	}
}

func TestRequestAccountLogin_InvalidEmail(t *testing.T) {
	ctx := context.Background()
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-forwarded-for", "1.2.3.4"))
	ctx = context.WithValue(ctx, middleware.ClientIPKey, "1.2.3.4")

	mockRepo := mocks.NewMockRepository(t)
	mockStorefrontAcc := mocks.NewMockStorefrontAccount(t)
	mockRepo.EXPECT().StorefrontAccount().Return(mockStorefrontAcc).Maybe()
	mockMailer := mocks.NewMockMailer(t)

	srv, err := New(mockRepo, mockMailer, nil, nil, nil, nil, storefrontConfig())
	assert.NoError(t, err)

	_, err = srv.RequestAccountLogin(ctx, &pb_frontend.RequestAccountLoginRequest{Email: "invalid"})
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestRequestAccountLogin_EmptyEmail(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.ClientIPKey, "1.2.3.4")

	mockRepo := mocks.NewMockRepository(t)
	mockStorefrontAcc := mocks.NewMockStorefrontAccount(t)
	mockRepo.EXPECT().StorefrontAccount().Return(mockStorefrontAcc).Maybe()
	mockMailer := mocks.NewMockMailer(t)

	srv, err := New(mockRepo, mockMailer, nil, nil, nil, nil, storefrontConfig())
	assert.NoError(t, err)

	_, err = srv.RequestAccountLogin(ctx, &pb_frontend.RequestAccountLoginRequest{Email: ""})
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestRequestAccountLogin_Success(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.ClientIPKey, "1.2.3.4")

	mockRepo := mocks.NewMockRepository(t)
	mockStorefrontAcc := mocks.NewMockStorefrontAccount(t)
	mockRepo.EXPECT().StorefrontAccount().Return(mockStorefrontAcc).Once()
	mockMailer := mocks.NewMockMailer(t)

	mockStorefrontAcc.EXPECT().
		InsertLoginChallenge(mock.Anything, testEmail, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.Anything).
		Return(nil)
	mockMailer.EXPECT().
		QueueAccountLogin(mock.Anything, mock.Anything, testEmail, mock.AnythingOfType("string"), mock.AnythingOfType("string")).
		Return(nil)

	srv, err := New(mockRepo, mockMailer, nil, nil, nil, nil, storefrontConfig())
	assert.NoError(t, err)

	resp, err := srv.RequestAccountLogin(ctx, &pb_frontend.RequestAccountLoginRequest{Email: testEmail})
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestVerifyAccountLoginCode_EmptyCode(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.ClientIPKey, "1.2.3.4")

	mockRepo := mocks.NewMockRepository(t)
	mockStorefrontAcc := mocks.NewMockStorefrontAccount(t)
	mockRepo.EXPECT().StorefrontAccount().Return(mockStorefrontAcc).Maybe()
	mockMailer := mocks.NewMockMailer(t)

	srv, err := New(mockRepo, mockMailer, nil, nil, nil, nil, storefrontConfig())
	assert.NoError(t, err)

	_, err = srv.VerifyAccountLoginCode(ctx, &pb_frontend.VerifyAccountLoginCodeRequest{
		Email: testEmail,
		Code:  "",
	})
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestRefreshAccountSession_EmptyToken(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.ClientIPKey, "1.2.3.4")

	mockRepo := mocks.NewMockRepository(t)
	mockStorefrontAcc := mocks.NewMockStorefrontAccount(t)
	mockRepo.EXPECT().StorefrontAccount().Return(mockStorefrontAcc).Maybe()
	mockMailer := mocks.NewMockMailer(t)

	srv, err := New(mockRepo, mockMailer, nil, nil, nil, nil, storefrontConfig())
	assert.NoError(t, err)

	_, err = srv.RefreshAccountSession(ctx, &pb_frontend.RefreshAccountSessionRequest{RefreshToken: ""})
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestGetAccount_NoMetadata(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.ClientIPKey, "1.2.3.4")

	mockRepo := mocks.NewMockRepository(t)
	mockStorefrontAcc := mocks.NewMockStorefrontAccount(t)
	mockRepo.EXPECT().StorefrontAccount().Return(mockStorefrontAcc).Maybe()
	mockMailer := mocks.NewMockMailer(t)

	srv, err := New(mockRepo, mockMailer, nil, nil, nil, nil, storefrontConfig())
	assert.NoError(t, err)

	_, err = srv.GetAccount(ctx, &pb_frontend.GetAccountRequest{})
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestAddSavedAddress_MaxLimit(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.ClientIPKey, "1.2.3.4")

	// Create context with valid access token
	accessToken := mustIssueTestToken(t, storefrontConfig(), testEmail)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+accessToken))

	mockRepo := mocks.NewMockRepository(t)
	mockStorefrontAcc := mocks.NewMockStorefrontAccount(t)
	mockRepo.EXPECT().StorefrontAccount().Return(mockStorefrontAcc).Times(2) // accountID + ListSavedAddresses
	mockMailer := mocks.NewMockMailer(t)

	// Return maxSavedAddresses addresses to trigger limit
	addrs := make([]entity.StorefrontSavedAddress, maxSavedAddresses)
	for i := range addrs {
		addrs[i] = entity.StorefrontSavedAddress{ID: i + 1, AccountID: 1}
	}
	mockStorefrontAcc.EXPECT().GetAccountByEmail(mock.Anything, testEmail).Return(&entity.StorefrontAccount{ID: 1, Email: testEmail}, nil)
	mockStorefrontAcc.EXPECT().ListSavedAddresses(mock.Anything, 1).Return(addrs, nil)

	srv, err := New(mockRepo, mockMailer, nil, nil, nil, nil, storefrontConfig())
	assert.NoError(t, err)

	_, err = srv.AddSavedAddress(ctx, &pb_frontend.AddSavedAddressRequest{
		Address: &pb_frontend.StorefrontSavedAddress{
			Country:        "US",
			City:           "NYC",
			AddressLineOne: "123 Main St",
			PostalCode:     "10001",
		},
	})
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}

// mustIssueTestToken creates an access token for testing using the same secret as storefront config.
func mustIssueTestToken(t *testing.T, cfg *storefront.Config, email string) string {
	t.Helper()
	rt, err := newStorefrontAuthRuntime(cfg)
	if err != nil {
		t.Fatalf("newStorefrontAuthRuntime: %v", err)
	}
	tok, err := jwt.NewTokenWithSubjectOpts(rt.accessJwtAuth, rt.accessTTL, email, rt.accessIssueOpts)
	if err != nil {
		t.Fatalf("NewTokenWithSubjectOpts: %v", err)
	}
	return tok
}
