package dto

import (
	"database/sql"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Convert a protobuf ArchiveNew to an entity ArchiveNew
func ConvertPbArchiveNewToEntity(pbArchiveNew *pb_common.ArchiveNew) *entity.ArchiveNew {
	if pbArchiveNew == nil {
		return nil
	}

	ArchiveBody := &entity.ArchiveBody{
		Title:       pbArchiveNew.Archive.Heading,
		Description: pbArchiveNew.Archive.Description,
	}

	entityItems := convertPbArchiveItemsInsertToEntity(pbArchiveNew.ItemsInsert)

	return &entity.ArchiveNew{
		Archive: ArchiveBody,
		Items:   entityItems,
	}
}

// Convert a slice of protobuf ArchiveItemInsert to a slice of entity ArchiveItemInsert
func convertPbArchiveItemsInsertToEntity(pbItemsInsert []*pb_common.ArchiveItemInsert) []entity.ArchiveItemInsert {
	var entityItems []entity.ArchiveItemInsert
	for _, pbItem := range pbItemsInsert {
		entityItem := entity.ArchiveItemInsert{
			MediaId: int(pbItem.MediaId),
			URL:     sql.NullString{String: pbItem.Url, Valid: pbItem.Url != ""},
			Title:   sql.NullString{String: pbItem.Title, Valid: pbItem.Title != ""},
		}
		entityItems = append(entityItems, entityItem)
	}
	return entityItems
}

// Convert a protobuf ArchiveItem to an entity ArchiveItem
func ConvertPbArchiveItemToEntity(i *pb_common.ArchiveItem) *entity.ArchiveItem {
	return &entity.ArchiveItem{
		Media: ConvertPbMediaFullToEntity(i.Media),
		URL:   sql.NullString{String: i.Url, Valid: i.Url != ""},
		Title: sql.NullString{String: i.Title, Valid: i.Title != ""},
	}
}

// Convert a protobuf MediaFull to an entity MediaFull

// Convert an entity ArchiveFull to a protobuf ArchiveFull
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

	itemsPb := convertArchiveItemsToPb(af.Items)

	return &pb_common.ArchiveFull{
		Archive: archivePb,
		Items:   itemsPb,
	}
}

// Convert a slice of entity ArchiveItem to a slice of protobuf ArchiveItemFull
func convertArchiveItemsToPb(items []entity.ArchiveItemFull) []*pb_common.ArchiveItemFull {
	itemsPb := make([]*pb_common.ArchiveItemFull, 0, len(items))
	for _, item := range items {
		url := ""
		if item.URL.Valid {
			url = item.URL.String
		}
		itemPb := &pb_common.ArchiveItemFull{
			Id:        int32(item.ID),
			ArchiveId: int32(item.ArchiveID),
			ArchiveItem: &pb_common.ArchiveItem{
				Media: ConvertEntityToCommonMedia(&item.Media),
				Url:   url,
				Title: item.Title.String,
			},
		}
		itemsPb = append(itemsPb, itemPb)
	}
	return itemsPb
}
