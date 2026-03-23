package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	jwtSecret      = "hehe"
	masterPassword = "FJKqDyBvr9pAQMB3f8Uj4s"

	username    = "testUsername"
	password    = "testPassword"
	newPassword = "newPassword"
)

func TestAuth(t *testing.T) {
	ctx := context.Background()

	as := mocks.NewMockAdmin(t)
	c := &Config{
		JWTSecret:                jwtSecret,
		MasterPassword:           masterPassword,
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 100000,
		JWTTTL:                   "60m",
	}
	authsrv, err := New(c, as)
	assert.NoError(t, err)

	pwHash, err := authsrv.pwhash.HashPassword(password)
	assert.NoError(t, err)
	pwHashNew, err := authsrv.pwhash.HashPassword(newPassword)
	assert.NoError(t, err)

	// Username is converted to lowercase in the Create method
	lowercaseUsername := strings.ToLower(username)
	as.EXPECT().AddAdmin(mock.Anything, lowercaseUsername, mock.Anything).Return(nil)

	_, err = authsrv.Create(ctx, &pb_auth.CreateRequest{
		MasterPassword: masterPassword,
		User: &pb_auth.User{
			Username: username,
			Password: password,
		},
	})
	assert.NoError(t, err)

	as.EXPECT().PasswordHashByUsername(ctx, lowercaseUsername).Return(pwHash, nil).Once()
	as.EXPECT().ChangePassword(ctx, lowercaseUsername, mock.Anything).Return(nil)

	_, err = authsrv.ChangePassword(ctx, &pb_auth.ChangePasswordRequest{
		Username:        username,
		CurrentPassword: password,
		NewPassword:     newPassword,
	})
	assert.NoError(t, err)

	as.EXPECT().PasswordHashByUsername(ctx, lowercaseUsername).Return(pwHashNew, nil).Once()
	resp, err := authsrv.Login(ctx, &pb_auth.LoginRequest{
		Username: username,
		Password: newPassword,
	})
	assert.NoError(t, err)

	token := fmt.Sprintf("Bearer %s", resp.AuthToken)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	handlerAuth := authsrv.WithAuth(nextHandler)

	// create a mock request to use
	req := httptest.NewRequest("GET", "http://testing", nil)
	req.Header.Set(AuthMetadataKey, token)

	rec := httptest.NewRecorder()
	// call the handler using a mock response recorder (we'll not use that anyway)
	handlerAuth.ServeHTTP(rec, req)
	assert.Equal(t, rec.Code, http.StatusOK)

	// bad token case
	req.Header.Set(AuthMetadataKey, "bad token")
	rec = httptest.NewRecorder()
	// call the handler using a mock response recorder (we'll not use that anyway)
	handlerAuth.ServeHTTP(rec, req)
	assert.Equal(t, rec.Code, http.StatusUnauthorized)

	as.EXPECT().DeleteAdmin(mock.Anything, lowercaseUsername).Return(nil)
	_, err = authsrv.Delete(ctx, &pb_auth.DeleteRequest{
		Username:       username,
		MasterPassword: c.MasterPassword,
	})
	assert.NoError(t, err)
}

func TestCreateWithInvalidMasterPassword(t *testing.T) {
	ctx := context.Background()

	as := mocks.NewMockAdmin(t)
	c := &Config{
		JWTSecret:                jwtSecret,
		MasterPassword:           masterPassword,
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 100000,
		JWTTTL:                   "60m",
	}
	authsrv, err := New(c, as)
	assert.NoError(t, err)

	t.Run("Create with wrong master password is rejected", func(t *testing.T) {
		_, err := authsrv.Create(ctx, &pb_auth.CreateRequest{
			MasterPassword: "wrong-password",
			User: &pb_auth.User{
				Username: username,
				Password: password,
			},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not authenticated")
	})

	t.Run("Create with correct master password succeeds", func(t *testing.T) {
		lowercaseUsername := strings.ToLower(username)
		as.EXPECT().AddAdmin(mock.Anything, lowercaseUsername, mock.Anything).Return(nil)

		resp, err := authsrv.Create(ctx, &pb_auth.CreateRequest{
			MasterPassword: masterPassword,
			User: &pb_auth.User{
				Username: username,
				Password: password,
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.NotEmpty(t, resp.AuthToken)
	})
}

func TestUnaryAdminAuthInterceptor(t *testing.T) {
	ctx := context.Background()

	as := mocks.NewMockAdmin(t)
	c := &Config{
		JWTSecret:                jwtSecret,
		MasterPassword:           masterPassword,
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 100000,
		JWTTTL:                   "60m",
	}
	authsrv, err := New(c, as)
	assert.NoError(t, err)

	interceptor := authsrv.UnaryAdminAuthInterceptor()

	t.Run("Non-admin RPC passes through without auth", func(t *testing.T) {
		handlerCalled := false
		handler := func(ctx context.Context, req any) (any, error) {
			handlerCalled = true
			return "response", nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/frontend.FrontendService/GetProduct",
		}

		resp, err := interceptor(ctx, nil, info, handler)
		assert.NoError(t, err)
		assert.Equal(t, "response", resp)
		assert.True(t, handlerCalled)
	})

	t.Run("Admin RPC without token is rejected", func(t *testing.T) {
		handlerCalled := false
		handler := func(ctx context.Context, req any) (any, error) {
			handlerCalled = true
			return "response", nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/admin.AdminService/GetProduct",
		}

		resp, err := interceptor(ctx, nil, info, handler)
		assert.Error(t, err)
		assert.Nil(t, resp)
		assert.False(t, handlerCalled)
		assert.Contains(t, err.Error(), "missing auth token")
	})

	t.Run("Admin RPC with invalid token is rejected", func(t *testing.T) {
		handlerCalled := false
		handler := func(ctx context.Context, req any) (any, error) {
			handlerCalled = true
			return "response", nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/admin.AdminService/GetProduct",
		}

		md := metadata.New(map[string]string{
			strings.ToLower(AuthMetadataKey): "invalid-token",
		})
		ctxWithMD := metadata.NewIncomingContext(ctx, md)

		resp, err := interceptor(ctxWithMD, nil, info, handler)
		assert.Error(t, err)
		assert.Nil(t, resp)
		assert.False(t, handlerCalled)
		assert.Contains(t, err.Error(), "invalid auth token")
	})

	t.Run("Admin RPC with valid token succeeds", func(t *testing.T) {
		// Create a valid token
		pwHash, err := authsrv.pwhash.HashPassword(password)
		assert.NoError(t, err)

		lowercaseUsername := strings.ToLower(username)
		as.EXPECT().PasswordHashByUsername(ctx, lowercaseUsername).Return(pwHash, nil).Once()

		loginResp, err := authsrv.Login(ctx, &pb_auth.LoginRequest{
			Username: username,
			Password: password,
		})
		assert.NoError(t, err)

		handlerCalled := false
		handler := func(ctx context.Context, req any) (any, error) {
			handlerCalled = true
			// Verify username was extracted into context
			username := GetAdminUsername(ctx)
			assert.Equal(t, lowercaseUsername, username)
			return "response", nil
		}

		info := &grpc.UnaryServerInfo{
			FullMethod: "/admin.AdminService/GetProduct",
		}

		md := metadata.New(map[string]string{
			strings.ToLower(AuthMetadataKey): fmt.Sprintf("Bearer %s", loginResp.AuthToken),
		})
		ctxWithMD := metadata.NewIncomingContext(ctx, md)

		resp, err := interceptor(ctxWithMD, nil, info, handler)
		assert.NoError(t, err)
		assert.Equal(t, "response", resp)
		assert.True(t, handlerCalled)
	})
}
