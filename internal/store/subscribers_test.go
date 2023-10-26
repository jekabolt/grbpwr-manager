package store

// import (
// 	"context"
// 	"testing"

// 	"github.com/stretchr/testify/assert"
// )

// func TestSubscribers_Subscribe(t *testing.T) {
// 	db := newTestDB(t)
// 	defer db.Close()
// 	ss := db.Subscribers()

// 	ctx := context.Background()

// 	email := "test"
// 	err := ss.Subscribe(ctx, email)
// 	assert.NoError(t, err)

// 	subs, err := ss.GetActiveSubscribers(ctx)
// 	assert.NoError(t, err)
// 	assert.Len(t, subs, 1)
// 	assert.Equal(t, email, subs[0])

// 	err = ss.Unsubscribe(ctx, email)
// 	assert.NoError(t, err)

// 	subs, err = ss.GetActiveSubscribers(ctx)
// 	assert.NoError(t, err)
// 	assert.Len(t, subs, 0)

// }
