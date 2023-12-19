package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

func TestMail(t *testing.T) {
	db := newTestDB(t)

	ms := db.Mail()
	ctx := context.Background()

	// add unsent mail
	err := ms.AddMail(ctx, &entity.SendEmailRequest{
		From:    "from",
		To:      "to",
		Html:    "html",
		Subject: "subject",
		ReplyTo: "replyTo",
		Sent:    false,
		SentAt:  sql.NullTime{Time: time.Now(), Valid: true},
	})
	assert.NoError(t, err)

	// add unsent mail
	err = ms.AddMail(ctx, &entity.SendEmailRequest{
		From:    "from",
		To:      "to",
		Html:    "html",
		Subject: "subject",
		ReplyTo: "replyTo",
		Sent:    false,
		SentAt:  sql.NullTime{Time: time.Now(), Valid: true},
	})
	assert.NoError(t, err)

	// add sent mail
	err = ms.AddMail(ctx, &entity.SendEmailRequest{
		From:    "from",
		To:      "to",
		Html:    "html",
		Subject: "subject",
		ReplyTo: "replyTo",
		Sent:    true,
		SentAt:  sql.NullTime{Time: time.Now(), Valid: true},
	})
	assert.NoError(t, err)

	// get all unsent
	unsent, err := ms.GetAllUnsent(ctx, false)
	assert.NoError(t, err)
	assert.Len(t, unsent, 2)

	// update sent
	err = ms.UpdateSent(ctx, unsent[0].Id)
	assert.NoError(t, err)

	// add error
	err = ms.AddError(ctx, unsent[1].Id, "error")
	assert.NoError(t, err)

	// get all unsent with error
	unsent, err = ms.GetAllUnsent(ctx, true)
	assert.NoError(t, err)
	assert.Len(t, unsent, 1)
}
