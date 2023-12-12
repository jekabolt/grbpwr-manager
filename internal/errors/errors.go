package gerr

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrAlreadySubscribed = status.Error(codes.AlreadyExists, "submitted email already subscribed")
)
