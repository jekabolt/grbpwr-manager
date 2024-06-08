package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConvertEntityToCommonMedia converts an entity.Media object to a common.MediaFull object.
func ConvertEntityToCommonMedia(eMedia *entity.MediaFull) *pb_common.MediaFull {
	// Convert time.Time to *timestamppb.Timestamp
	createdAt := timestamppb.New(eMedia.CreatedAt)

	// Convert MediaItem
	MediaItem := &pb_common.MediaItem{
		FullSize: &pb_common.MediaInfo{
			MediaUrl: eMedia.FullSizeMediaURL,
			Width:    int32(eMedia.FullSizeWidth),
			Height:   int32(eMedia.FullSizeHeight),
		},
		Thumbnail: &pb_common.MediaInfo{
			MediaUrl: eMedia.ThumbnailMediaURL,
			Width:    int32(eMedia.ThumbnailWidth),
			Height:   int32(eMedia.ThumbnailHeight),
		},
		Compressed: &pb_common.MediaInfo{
			MediaUrl: eMedia.CompressedMediaURL,
			Width:    int32(eMedia.CompressedWidth),
			Height:   int32(eMedia.CompressedHeight),
		},
	}

	return &pb_common.MediaFull{
		Id:        int32(eMedia.Id), // Assuming the conversion from int to int32 is safe and acceptable
		CreatedAt: createdAt,
		Media:     MediaItem,
	}
}
