package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConvertEntityToCommonMedia converts an entity.Media object to a common.Media object.
func ConvertEntityToCommonMedia(eMedia entity.Media) *pb_common.Media {
	// Convert time.Time to *timestamppb.Timestamp
	createdAt := timestamppb.New(eMedia.CreatedAt)

	// Convert MediaInsert
	mediaInsert := &pb_common.MediaInsert{
		FullSize:   eMedia.FullSize,
		Thumbnail:  eMedia.Thumbnail,
		Compressed: eMedia.Compressed,
	}

	return &pb_common.Media{
		Id:        int32(eMedia.Id), // Assuming the conversion from int to int32 is safe and acceptable
		CreatedAt: createdAt,
		Media:     mediaInsert,
	}
}
