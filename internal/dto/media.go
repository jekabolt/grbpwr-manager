package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ConvertEntityMediaListToPbMedia(media []entity.MediaFull) []*pb_common.MediaFull {
	var pbMedia []*pb_common.MediaFull
	for _, m := range media {
		pbMedia = append(pbMedia, ConvertEntityToCommonMedia(&m))
	}
	return pbMedia
}

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
		Blurhash: eMedia.BlurHash,
	}

	return &pb_common.MediaFull{
		Id:        int32(eMedia.Id), // Assuming the conversion from int to int32 is safe and acceptable
		CreatedAt: createdAt,
		Media:     MediaItem,
	}
}

func ConvertPbMediaFullToEntity(m *pb_common.MediaFull) entity.MediaFull {
	return entity.MediaFull{
		Id:        int(m.Id),
		CreatedAt: m.CreatedAt.AsTime(),
		MediaItem: convertPbMediaItemToEntity(m.Media),
	}
}

// Convert a protobuf MediaItem to an entity MediaItem
func convertPbMediaItemToEntity(m *pb_common.MediaItem) entity.MediaItem {
	return entity.MediaItem{
		FullSizeMediaURL:   m.FullSize.MediaUrl,
		FullSizeWidth:      int(m.FullSize.Width),
		FullSizeHeight:     int(m.FullSize.Height),
		ThumbnailMediaURL:  m.Thumbnail.MediaUrl,
		ThumbnailWidth:     int(m.Thumbnail.Width),
		ThumbnailHeight:    int(m.Thumbnail.Height),
		CompressedMediaURL: m.Compressed.MediaUrl,
		CompressedWidth:    int(m.Compressed.Width),
		CompressedHeight:   int(m.Compressed.Height),
		BlurHash:           m.Blurhash,
	}
}
