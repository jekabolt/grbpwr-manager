package dto

import (
	"database/sql"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ConvertPbArchiveNewToEntity(pbArchiveNew *pb_common.ArchiveNew) *entity.ArchiveNew {
	if pbArchiveNew == nil {
		return nil
	}

	ArchiveBody := &entity.ArchiveBody{
		Title:       pbArchiveNew.Archive.Heading,
		Description: pbArchiveNew.Archive.Description,
	}

	var entityItems []entity.ArchiveItemInsert
	for _, pbItem := range pbArchiveNew.ItemsInsert {
		entityItem := entity.ArchiveItemInsert{
			MediaId: int(pbItem.MediaId),
			URL:     sql.NullString{String: pbItem.Url, Valid: pbItem.Url != ""},
			Title:   sql.NullString{String: pbItem.Title, Valid: pbItem.Title != ""},
		}
		entityItems = append(entityItems, entityItem)
	}

	entityArchiveNew := &entity.ArchiveNew{
		Archive: ArchiveBody,
		Items:   entityItems,
	}

	return entityArchiveNew
}

func ConvertPbArchiveItemToEntity(i *pb_common.ArchiveItem) *entity.ArchiveItem {
	return &entity.ArchiveItem{
		Media: entity.MediaFull{
			Id:        int(i.Media.Id),
			CreatedAt: i.Media.CreatedAt.AsTime(),
			MediaItem: entity.MediaItem{
				FullSizeMediaURL:   i.Media.Media.FullSize.MediaUrl,
				FullSizeWidth:      int(i.Media.Media.FullSize.Width),
				FullSizeHeight:     int(i.Media.Media.FullSize.Height),
				ThumbnailMediaURL:  i.Media.Media.Thumbnail.MediaUrl,
				ThumbnailWidth:     int(i.Media.Media.Thumbnail.Width),
				ThumbnailHeight:    int(i.Media.Media.Thumbnail.Height),
				CompressedMediaURL: i.Media.Media.Compressed.MediaUrl,
				CompressedWidth:    int(i.Media.Media.Compressed.Width),
				CompressedHeight:   int(i.Media.Media.Compressed.Height),
			},
		},
		URL:   sql.NullString{String: i.Url, Valid: i.Url != ""},
		Title: sql.NullString{String: i.Title, Valid: i.Title != ""},
	}
}

func ConvertArchiveFullEntityToPb(af *entity.ArchiveFull) *pb_common.ArchiveFull {
	if af == nil {
		return nil
	}

	archivePb := &pb_common.Archive{
		Id:        int32(af.Archive.ID),
		CreatedAt: timestamppb.New(af.Archive.CreatedAt),
		UpdatedAt: timestamppb.New(af.Archive.UpdatedAt),
		ArchiveBody: &pb_common.ArchiveBody{
			Heading:     af.Archive.Title,
			Description: af.Archive.Description,
		},
	}

	itemsPb := make([]*pb_common.ArchiveItemFull, 0, len(af.Items))
	for _, item := range af.Items {
		url := ""
		if item.URL.Valid {
			url = item.URL.String
		}
		itemPb := &pb_common.ArchiveItemFull{
			Id:        int32(item.ID),
			ArchiveId: int32(item.ArchiveID),
			ArchiveItem: &pb_common.ArchiveItem{
				Media: &pb_common.MediaFull{
					Id:        int32(item.Media.Id),
					CreatedAt: timestamppb.New(item.Media.CreatedAt),
					Media: &pb_common.MediaItem{
						FullSize: &pb_common.MediaInfo{
							MediaUrl: item.Media.FullSizeMediaURL,
							Width:    int32(item.Media.FullSizeWidth),
							Height:   int32(item.Media.FullSizeHeight),
						},
						Thumbnail: &pb_common.MediaInfo{
							MediaUrl: item.Media.ThumbnailMediaURL,
							Width:    int32(item.Media.FullSizeWidth),
							Height:   int32(item.Media.FullSizeHeight),
						},
						Compressed: &pb_common.MediaInfo{
							MediaUrl: item.Media.CompressedMediaURL,
							Width:    int32(item.Media.CompressedWidth),
							Height:   int32(item.Media.CompressedHeight),
						},
					},
				},
				Url:   url,
				Title: item.Title.String,
			},
		}
		itemsPb = append(itemsPb, itemPb)
	}

	return &pb_common.ArchiveFull{
		Archive: archivePb,
		Items:   itemsPb,
	}
}
