package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const techCardSnapshotPageSize = 100

// ListTechCardSnapshots returns lightweight history metadata newest-first. The card body remains
// behind RestoreTechCardSnapshot and is never sent just to open the save-chip dropdown.
func (s *Server) ListTechCardSnapshots(
	ctx context.Context,
	req *pb_admin.ListTechCardSnapshotsRequest,
) (*pb_admin.ListTechCardSnapshotsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	if req.GetBeforeSnapshotId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "before snapshot id must not be negative")
	}
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 || pageSize > techCardSnapshotPageSize {
		pageSize = techCardSnapshotPageSize
	}
	rows, err := s.repo.TechCards().ListTechCardSnapshots(
		ctx, int(req.GetTechCardId()), req.GetBeforeSnapshotId(), pageSize+1)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't list tech card snapshots",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list tech card history; try again")
	}
	response := &pb_admin.ListTechCardSnapshotsResponse{}
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		response.NextBeforeSnapshotId = rows[len(rows)-1].Id
	}
	response.Items = make([]*pb_admin.TechCardSnapshotListItem, 0, len(rows))
	for _, row := range rows {
		response.Items = append(response.Items, &pb_admin.TechCardSnapshotListItem{
			SnapshotId:  row.Id,
			LockVersion: int32(row.LockVersion),
			Reason:      row.Reason,
			CreatedAt:   timestamppb.New(row.CreatedAt),
			AuthorLabel: techCardSnapshotAuthorLabel(row.CreatedBy.String),
			Changes:     append([]string(nil), row.ChangedPaths...),
			Pinned:      row.Pinned,
		})
	}
	return response, nil
}

func techCardSnapshotAuthorLabel(author string) string {
	author = strings.TrimSpace(author)
	if local, _, ok := strings.Cut(author, "@"); ok && local != "" {
		return local
	}
	return author
}
