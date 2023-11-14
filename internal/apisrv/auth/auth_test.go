package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

	as := mocks.NewAdmin(t)
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

	as.EXPECT().AddAdmin(mock.Anything, mock.Anything, mock.Anything).Return(nil)

	_, err = authsrv.Create(ctx, &pb_auth.CreateRequest{
		MasterPassword: masterPassword,
		User: &pb_auth.User{
			Username: username,
			Password: password,
		},
	})
	assert.NoError(t, err)

	as.EXPECT().PasswordHashByUsername(ctx, mock.Anything).Return(pwHash, nil).Once()
	as.EXPECT().ChangePassword(ctx, mock.Anything, mock.Anything).Return(nil)

	_, err = authsrv.ChangePassword(ctx, &pb_auth.ChangePasswordRequest{
		Username:        username,
		CurrentPassword: password,
		NewPassword:     newPassword,
	})
	assert.NoError(t, err)

	fmt.Printf(" \n\n test hash new %v ", pwHashNew)
	fmt.Printf(" \n\n test hash old %v ", pwHash)
	as.EXPECT().PasswordHashByUsername(mock.Anything, mock.Anything).Return(pwHashNew, nil).Once()
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

	as.EXPECT().DeleteAdmin(mock.Anything, mock.Anything).Return(nil)
	_, err = authsrv.Delete(ctx, &pb_auth.DeleteRequest{
		Username:       username,
		MasterPassword: c.MasterPassword,
	})
	assert.NoError(t, err)
}
