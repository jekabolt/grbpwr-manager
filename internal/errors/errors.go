package gerr

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrAlreadySubscribed = status.Error(codes.AlreadyExists, "submitted email already subscribed")
	BadMailRequest       = status.Error(codes.DataLoss, "bad mail request")
	MailApiLimitReached  = status.Error(codes.ResourceExhausted, "mail api limit reached")

	OrderNotFound = status.Error(codes.NotFound, "order not found")
)
