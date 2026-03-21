package jwt

import (
	"testing"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/stretchr/testify/assert"
)

func TestToken(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	tok, err := NewToken(jwtAuth, time.Hour)
	assert.NoError(t, err)

	subToken, err := VerifyToken(jwtAuth, tok)
	assert.NoError(t, err)

	t.Log(subToken)
}

func TestTokenWithSubject(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	tok, err := NewTokenWithSubject(jwtAuth, time.Hour, "user@example.com")
	assert.NoError(t, err)

	sub, err := VerifyToken(jwtAuth, tok)
	assert.NoError(t, err)
	assert.Equal(t, "user@example.com", sub)
}

func TestTokenIncludesIat(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	tok, err := NewToken(jwtAuth, time.Hour)
	assert.NoError(t, err)

	parsed, err := jwtauth.VerifyToken(jwtAuth, tok)
	assert.NoError(t, err)
	assert.NotZero(t, parsed.IssuedAt(), "iat claim should be set")
}

func TestTokenWithExpectations_IssuerAudience(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	opts := &IssueOpts{Issuer: "https://api.test", Audience: "grbpwr-storefront"}
	tok, err := NewTokenWithSubjectOpts(jwtAuth, time.Hour, "user@example.com", opts)
	assert.NoError(t, err)

	exp := &Expectations{Issuer: "https://api.test", Audience: "grbpwr-storefront"}
	sub, err := VerifyTokenWithExpectations(jwtAuth, tok, exp)
	assert.NoError(t, err)
	assert.Equal(t, "user@example.com", sub)
}

func TestTokenWithExpectations_WrongIssuerFails(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	opts := &IssueOpts{Issuer: "https://api.test", Audience: "grbpwr-storefront"}
	tok, err := NewTokenWithSubjectOpts(jwtAuth, time.Hour, "user@example.com", opts)
	assert.NoError(t, err)

	exp := &Expectations{Issuer: "https://wrong-issuer", Audience: "grbpwr-storefront"}
	_, err = VerifyTokenWithExpectations(jwtAuth, tok, exp)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidClaims)
}

func TestTokenWithExpectations_WrongAudienceFails(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	opts := &IssueOpts{Issuer: "https://api.test", Audience: "grbpwr-storefront"}
	tok, err := NewTokenWithSubjectOpts(jwtAuth, time.Hour, "user@example.com", opts)
	assert.NoError(t, err)

	exp := &Expectations{Issuer: "https://api.test", Audience: "wrong-audience"}
	_, err = VerifyTokenWithExpectations(jwtAuth, tok, exp)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidClaims)
}

func TestTokenWithExpectations_NilSkipsCheck(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	opts := &IssueOpts{Issuer: "https://api.test", Audience: "grbpwr-storefront"}
	tok, err := NewTokenWithSubjectOpts(jwtAuth, time.Hour, "user@example.com", opts)
	assert.NoError(t, err)

	sub, err := VerifyTokenWithExpectations(jwtAuth, tok, nil)
	assert.NoError(t, err)
	assert.Equal(t, "user@example.com", sub)
}

func TestTokenWithExpectations_EmptyExpectationsSkipsCheck(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	tok, err := NewTokenWithSubject(jwtAuth, time.Hour, "user@example.com")
	assert.NoError(t, err)

	exp := &Expectations{Issuer: "", Audience: ""}
	sub, err := VerifyTokenWithExpectations(jwtAuth, tok, exp)
	assert.NoError(t, err)
	assert.Equal(t, "user@example.com", sub)
}

func TestTokenWithJti(t *testing.T) {
	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	opts := &IssueOpts{IncludeJti: true}
	tok, err := NewTokenWithSubjectOpts(jwtAuth, time.Hour, "user@example.com", opts)
	assert.NoError(t, err)

	sub, jti, expAt, err := VerifyTokenFull(jwtAuth, tok, nil)
	assert.NoError(t, err)
	assert.Equal(t, "user@example.com", sub)
	assert.NotEmpty(t, jti)
	assert.False(t, expAt.IsZero())
}
