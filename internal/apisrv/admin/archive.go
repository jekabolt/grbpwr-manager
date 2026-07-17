package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) AddArchive(ctx context.Context, req *pb_admin.AddArchiveRequest) (*pb_admin.AddArchiveResponse, error) {
	if err := s.validateArchiveItems(req.ArchiveInsert); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	an, err := dto.ConvertPbArchiveInsertToEntity(req.ArchiveInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert pb archive insert to entity archive insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb archive insert to entity archive insert")
	}

	archiveId, err := s.repo.Archive().AddArchive(ctx, an)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add archive")
	}

	s.revalidateAsync(&dto.RevalidationData{
		Archive: archiveId,
	})

	return &pb_admin.AddArchiveResponse{
		Id: int32(archiveId),
	}, nil
}

func (s *Server) UpdateArchive(ctx context.Context, req *pb_admin.UpdateArchiveRequest) (*pb_admin.UpdateArchiveResponse, error) {
	if err := s.validateArchiveItems(req.ArchiveInsert); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	upd, err := dto.ConvertPbArchiveInsertToEntity(req.ArchiveInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert pb archive insert to entity archive insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb archive insert to entity archive insert")
	}

	err = s.repo.Archive().UpdateArchive(ctx,
		int(req.Id),
		upd,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "archive not found")
		}
		slog.Default().ErrorContext(ctx, "can't update archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update archive")
	}

	s.revalidateAsync(&dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})

	return &pb_admin.UpdateArchiveResponse{}, nil
}

func (s *Server) DeleteArchiveById(ctx context.Context, req *pb_admin.DeleteArchiveByIdRequest) (*pb_admin.DeleteArchiveByIdResponse, error) {
	err := s.repo.Archive().DeleteArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete archive by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to delete archive: %v", err)
	}

	s.revalidateAsync(&dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})

	return &pb_admin.DeleteArchiveByIdResponse{}, nil
}

func (s *Server) GetArchiveByID(ctx context.Context, req *pb_admin.GetArchiveByIDRequest) (*pb_admin.GetArchiveByIDResponse, error) {

	af, err := s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "archive not found")
		}
		slog.Default().ErrorContext(ctx, "can't get archive by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get archive: %v", err)
	}

	pbAf, err := dto.ConvertArchiveFullEntityToPb(af)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert archive to pb",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to convert archive: %v", err)
	}

	return &pb_admin.GetArchiveByIDResponse{
		Archive: pbAf,
	}, nil
}

// maxArchiveMediaLine is the upper bound on media in a single MEDIA_LINE block.
const maxArchiveMediaLine = 4

// countNonZero returns how many ids are non-zero (unselected media/product slots
// arrive as 0 and would otherwise pass a naive len() check then vanish on read).
func countNonZero(ids []int32) int {
	n := 0
	for _, id := range ids {
		if id != 0 {
			n++
		}
	}
	return n
}

// validateArchiveItems enforces per-block invariants on the timeline body so that
// a block which would silently vanish on read (its required reference missing) is
// instead rejected up front. It also enforces the shared iframe embed allowlist on
// EMBED blocks and the 1..4 media count on MEDIA_LINE.
func (s *Server) validateArchiveItems(ai *pb_common.ArchiveInsert) error {
	if ai == nil {
		return nil
	}
	for i, it := range ai.Items {
		switch it.Type {
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_MAIN_MEDIA:
			if it.MainMedia == nil || it.MainMedia.MediaId == 0 {
				return fmt.Errorf("archive item %d: main_media requires a media id", i)
			}
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_MEDIA_LINE:
			n := 0
			if it.MediaLine != nil {
				n = countNonZero(it.MediaLine.MediaIds)
			}
			if n < 1 || n > maxArchiveMediaLine {
				return fmt.Errorf("archive item %d: media_line must have 1..%d media, got %d", i, maxArchiveMediaLine, n)
			}
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_MEDIA_WITH_CAPTION:
			if it.MediaWithCaption == nil || it.MediaWithCaption.MediaId == 0 {
				return fmt.Errorf("archive item %d: media_with_caption requires a media id", i)
			}
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_EMBED:
			var url string
			if it.Embed != nil {
				url = it.Embed.EmbedUrl
			}
			if err := s.validateEmbedURL(url); err != nil {
				return fmt.Errorf("archive item %d: %w", i, err)
			}
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_PRODUCT:
			if it.Product == nil || it.Product.ColorwayId == 0 {
				return fmt.Errorf("archive item %d: product requires a product id", i)
			}
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_PRODUCTS_TAG:
			if it.ProductsTag == nil || it.ProductsTag.Tag == "" {
				return fmt.Errorf("archive item %d: products_tag requires a tag", i)
			}
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_PRODUCTS_MANUAL:
			if it.ProductsManual == nil || countNonZero(it.ProductsManual.ColorwayIds) == 0 {
				return fmt.Errorf("archive item %d: products_manual requires at least one product id", i)
			}
		case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_UNKNOWN:
			return fmt.Errorf("archive item %d: block type is unset", i)
		}
	}
	return nil
}

// GetArchivesPaged is the ADMIN archive list. Unlike the storefront projection it returns the full
// id-bearing shape (R3: internal ids stay on the admin surface) — the admin write path
// (UpdateArchive/DeleteArchiveById/HeroFeaturedArchiveInsert) keys on those ids, and without this
// list the admin UI could not recover them after the storefront list went code-keyed (A-final gap).
func (s *Server) GetArchivesPaged(ctx context.Context, req *pb_admin.GetArchivesPagedRequest) (*pb_admin.GetArchivesPagedResponse, error) {
	limit, offset := clampPagination(int(req.Limit), int(req.Offset))

	afs, count, err := s.repo.Archive().GetArchivesPaged(ctx,
		limit,
		offset,
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get archives paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to list archives")
	}

	pbAfs := make([]*pb_common.ArchiveList, 0, len(afs))
	for i := range afs {
		pbAfs = append(pbAfs, dto.ConvertEntityToCommonArchiveList(&afs[i]))
	}

	return &pb_admin.GetArchivesPagedResponse{
		Archives: pbAfs,
		Total:    int32(count),
	}, nil
}
