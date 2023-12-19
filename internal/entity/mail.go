package entity

import (
	"database/sql"
	"time"
)

type SendEmailRequest struct {
	Id        int            `db:"id"`
	From      string         `db:"from_email"`
	To        string         `db:"to_email"`
	Html      string         `db:"html"`
	Subject   string         `db:"subject"`
	ReplyTo   string         `db:"reply_to"`
	Sent      bool           `db:"sent"`
	SentAt    sql.NullTime   `db:"sent_at"`
	CreatedAt time.Time      `db:"created_at"`
	ErrMsg    sql.NullString `db:"error_msg"`
}
