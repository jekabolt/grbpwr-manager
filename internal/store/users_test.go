package store

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/stretchr/testify/assert"
)

var (
	admin = dto.Admin{
		Username:     "testUsername",
		PasswordHash: "hash",
	}
	newPwd = "newPwd"
)

func TestUserCRUD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	defer db.Close()
	as := db.Admin()

	err := as.AddAdmin(ctx, admin.Username, admin.PasswordHash)
	assert.NoError(t, err)

	// can't create more than one user with same user name
	err = as.AddAdmin(ctx, admin.Username, admin.PasswordHash)
	assert.Error(t, err)

	adm, err := as.GetAdminByUsername(ctx, admin.Username)
	assert.NoError(t, err)
	assert.Equal(t, adm.PasswordHash, admin.PasswordHash)
	assert.Equal(t, adm.Username, admin.Username)

	err = as.ChangePassword(ctx, admin.Username, newPwd)
	assert.NoError(t, err)

	newAdm, err := as.GetAdminByUsername(ctx, admin.Username)
	assert.NoError(t, err)
	assert.NotEqual(t, newAdm.PasswordHash, adm.PasswordHash)

	_, err = as.GetAdminByUsername(ctx, "not exist")
	assert.Error(t, err)
}
