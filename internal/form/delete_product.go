package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type DeleteProductRequest struct {
	*pb_admin.DeleteProductRequest
}

func (f *DeleteProductRequest) Validate() error {
	if f == nil {
		return status.Error(codes.InvalidArgument, "request is nil")
	}
	return ValidateStruct(f,
		v.Field(&f.ProductId, v.Required),
	)
}
