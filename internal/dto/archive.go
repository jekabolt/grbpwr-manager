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

	archiveInsert := &entity.ArchiveInsert{
		Title:       pbArchiveNew.Archive.Title,
		Description: pbArchiveNew.Archive.Description,
	}

	var entityItems []entity.ArchiveItemInsert
	for _, pbItem := range pbArchiveNew.Items {
		entityItem := entity.ArchiveItemInsert{
			Media: pbItem.Media,
			URL:   sql.NullString{String: pbItem.Url, Valid: pbItem.Url != ""},
			Title: sql.NullString{String: pbItem.Title, Valid: pbItem.Title != ""},
		}
		entityItems = append(entityItems, entityItem)
	}

	entityArchiveNew := &entity.ArchiveNew{
		Archive: archiveInsert,
		Items:   entityItems,
	}

	return entityArchiveNew
}

func ConvertPbArchiveItemInsertToEntity(i *pb_common.ArchiveItemInsert) *entity.ArchiveItemInsert {
	return &entity.ArchiveItemInsert{
		Media: i.Media,
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
		ArchiveInsert: &pb_common.ArchiveInsert{
			Title:       af.Archive.Title,
			Description: af.Archive.Description,
		},
	}

	itemsPb := make([]*pb_common.ArchiveItem, 0, len(af.Items))
	for _, item := range af.Items {
		itemPb := &pb_common.ArchiveItem{
			Id:        int32(item.ID),
			ArchiveId: int32(item.ArchiveID),
			ArchiveItemInsert: &pb_common.ArchiveItemInsert{
				Media: item.Media,
				Url:   item.URL.String,
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
