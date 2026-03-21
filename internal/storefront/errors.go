package storefront

import "errors"

// Sentinel errors for refresh token rotation (use errors.Is in callers).
var (
	ErrRefreshTokenRevoked = errors.New("refresh token revoked")
	ErrRefreshTokenExpired = errors.New("refresh token expired")
)
