package jwt

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/stretchr/testify/assert"
)

func TestToken(t *testing.T) {
	const RFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
	const RFC3339 = "2006-01-02T15:04:05Z07:00"

	now := time.Now()
	fmt.Println(time.Parse(RFC3339, now.Format("2006-01-02T15:04:05.999999999Z07:00")))

	jwtAuth := jwtauth.New("HS256", []byte("secret"), nil)
	tok, err := NewToken(jwtAuth, time.Hour)
	assert.NoError(t, err)

	subToken, err := VerifyToken(jwtAuth, tok)
	assert.NoError(t, err)

	t.Log(subToken)

}
