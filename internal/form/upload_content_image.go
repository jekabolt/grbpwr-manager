package form

import (
	"encoding/base64"
	"strings"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type UploadContentImageRequest struct {
	*pb_admin.UploadContentImageRequest
}

func (f *UploadContentImageRequest) Validate() error {
	if f == nil {
		return status.Error(codes.InvalidArgument, "request is nil")
	}

	validateRawB64Image := v.NewStringRuleWithError(
		func(value string) bool {
			// Split header and data parts
			imageParts := strings.SplitN(value, ",", 2)
			if len(imageParts) != 2 {
				return false
			}
			// Check if the data part is a valid Base64 string
			_, err := base64.StdEncoding.DecodeString(imageParts[1])
			return err == nil
		}, v.ErrInInvalid.SetMessage("invalid base64 image"),
	)

	return ValidateStruct(f,
		v.Field(&f.Folder, v.Required),
		v.Field(&f.ImageName, v.Required),
		v.Field(&f.RawB64Image, v.Required, validateRawB64Image),
	)
}
