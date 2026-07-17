package admin

// Dictionary CRUD handlers (R9). Controlled merch dictionaries — colour / collection / tag — plus the
// closed ISO country dictionary. Every mutation carries an optimistic expected_version and returns the
// new namespace revision (common.DictionaryRevision); there is no delete, only archive (FK RESTRICT).
// Mutations refresh the in-process dictionary cache best-effort; the versioned revision poller
// (internal/cache) is the authoritative cross-instance invalidation path.

import (
	"context"
	"errors"
	mysql "github.com/go-sql-driver/mysql"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---- projection helpers ----------------------------------------------------

func pbColor(c entity.Color) *pb_common.Color {
	return &pb_common.Color{
		Id:       int32(c.ID),
		Code:     c.Code,
		Name:     c.Name,
		Hex:      c.Hex.String,
		Archived: c.ArchivedAt.Valid,
	}
}

func pbCollectionDict(c entity.CollectionDict) *pb_common.Collection {
	return &pb_common.Collection{
		Id:       int32(c.ID),
		Code:     c.Code,
		Name:     c.Name,
		Archived: c.ArchivedAt.Valid,
	}
}

func pbTagDict(t entity.TagDict) *pb_common.Tag {
	return &pb_common.Tag{
		Id:       int32(t.ID),
		Code:     t.Code,
		Name:     t.Name,
		Archived: t.ArchivedAt.Valid,
	}
}

func pbCountry(c entity.Country) *pb_common.Country {
	return &pb_common.Country{
		Code:   c.Code,
		Name:   c.DisplayName,
		Active: c.Active,
	}
}

func pbDictRevision(ns entity.DictionaryNamespace, rev int64) *pb_common.DictionaryRevision {
	return &pb_common.DictionaryRevision{
		Namespace: string(ns),
		Revision:  uint64(rev),
	}
}

// dictMutationError maps a dictionary store error to a gRPC status. The store returns sentinels for the
// two contract-level failures (stale version, in-use code) and descriptive wrapped errors otherwise;
// user-input failures (missing name, bad code, not found) are surfaced as InvalidArgument/NotFound.
func dictMutationError(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, entity.ErrDictionaryVersionConflict):
		return status.Errorf(codes.Aborted, "%s: %v", op, err)
	case errors.Is(err, entity.ErrDictionaryCodeInUse):
		return status.Errorf(codes.FailedPrecondition, "%s: %v", op, err)
	}
	// Typed check first: ER_DUP_ENTRY via the driver error, not error prose — user-controlled
	// names must not be able to flip the classification (review finding backend-004).
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return status.Errorf(codes.AlreadyExists, "%s: %v", op, err)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found"):
		return status.Errorf(codes.NotFound, "%s: %v", op, err)
	case strings.Contains(msg, "already archived"):
		return status.Errorf(codes.FailedPrecondition, "%s: %v", op, err)
	case strings.Contains(msg, "required") || strings.Contains(msg, "invalid") || strings.Contains(msg, "no alphanumeric"):
		return status.Errorf(codes.InvalidArgument, "%s: %v", op, err)
	}
	slog.Default().ErrorContext(ctx, "dictionary mutation failed", slog.String("op", op), slog.String("err", msg))
	return status.Errorf(codes.Internal, "%s: %v", op, err)
}

// refreshDictionaryCache reloads the in-process dictionary cache after a mutation (best-effort). The
// authoritative cross-instance invalidation is the revision poller; this just tightens same-instance
// read-after-write for the admin UI.
func (s *Server) refreshDictionaryCache(ctx context.Context) {
	if di, err := s.repo.Cache().GetDictionaryInfo(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh dictionary cache", slog.String("err", err.Error()))
	} else {
		cache.RefreshDictionary(di)
	}
}

// ---- Colour ----------------------------------------------------------------

func (s *Server) ListColors(ctx context.Context, req *pb_admin.ListColorsRequest) (*pb_admin.ListColorsResponse, error) {
	colors, err := s.repo.Dictionary().ListColors(ctx, req.GetIncludeArchived())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list colours: %v", err)
	}
	out := make([]*pb_common.Color, 0, len(colors))
	for _, c := range colors {
		out = append(out, pbColor(c))
	}
	return &pb_admin.ListColorsResponse{Colors: out}, nil
}

func (s *Server) CreateColor(ctx context.Context, req *pb_admin.CreateColorRequest) (*pb_admin.CreateColorResponse, error) {
	color, rev, err := s.repo.Dictionary().CreateColor(ctx, req.GetCode(), req.GetName(), req.GetHex(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "create colour", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.CreateColorResponse{Color: pbColor(color), Revision: pbDictRevision(entity.DictNamespaceColor, rev)}, nil
}

func (s *Server) UpdateColor(ctx context.Context, req *pb_admin.UpdateColorRequest) (*pb_admin.UpdateColorResponse, error) {
	rev, err := s.repo.Dictionary().UpdateColor(ctx, req.GetCode(), req.GetName(), req.GetHex(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "update colour", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.UpdateColorResponse{Revision: pbDictRevision(entity.DictNamespaceColor, rev)}, nil
}

func (s *Server) ArchiveColor(ctx context.Context, req *pb_admin.ArchiveColorRequest) (*pb_admin.ArchiveColorResponse, error) {
	rev, err := s.repo.Dictionary().ArchiveColor(ctx, req.GetCode(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "archive colour", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.ArchiveColorResponse{Revision: pbDictRevision(entity.DictNamespaceColor, rev)}, nil
}

// ---- Collection ------------------------------------------------------------

func (s *Server) ListCollections(ctx context.Context, req *pb_admin.ListCollectionsRequest) (*pb_admin.ListCollectionsResponse, error) {
	rows, err := s.repo.Dictionary().ListCollections(ctx, req.GetIncludeArchived())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list collections: %v", err)
	}
	out := make([]*pb_common.Collection, 0, len(rows))
	for _, c := range rows {
		out = append(out, pbCollectionDict(c))
	}
	return &pb_admin.ListCollectionsResponse{Collections: out}, nil
}

func (s *Server) CreateCollection(ctx context.Context, req *pb_admin.CreateCollectionRequest) (*pb_admin.CreateCollectionResponse, error) {
	c, rev, err := s.repo.Dictionary().CreateCollection(ctx, req.GetName(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "create collection", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.CreateCollectionResponse{Collection: pbCollectionDict(c), Revision: pbDictRevision(entity.DictNamespaceCollection, rev)}, nil
}

func (s *Server) UpdateCollection(ctx context.Context, req *pb_admin.UpdateCollectionRequest) (*pb_admin.UpdateCollectionResponse, error) {
	rev, err := s.repo.Dictionary().UpdateCollection(ctx, int(req.GetId()), req.GetName(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "update collection", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.UpdateCollectionResponse{Revision: pbDictRevision(entity.DictNamespaceCollection, rev)}, nil
}

func (s *Server) ArchiveCollection(ctx context.Context, req *pb_admin.ArchiveCollectionRequest) (*pb_admin.ArchiveCollectionResponse, error) {
	rev, err := s.repo.Dictionary().ArchiveCollection(ctx, int(req.GetId()), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "archive collection", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.ArchiveCollectionResponse{Revision: pbDictRevision(entity.DictNamespaceCollection, rev)}, nil
}

// ---- Tag -------------------------------------------------------------------

func (s *Server) ListTags(ctx context.Context, req *pb_admin.ListTagsRequest) (*pb_admin.ListTagsResponse, error) {
	rows, err := s.repo.Dictionary().ListTags(ctx, req.GetIncludeArchived())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tags: %v", err)
	}
	out := make([]*pb_common.Tag, 0, len(rows))
	for _, t := range rows {
		out = append(out, pbTagDict(t))
	}
	return &pb_admin.ListTagsResponse{Tags: out}, nil
}

func (s *Server) CreateTag(ctx context.Context, req *pb_admin.CreateTagRequest) (*pb_admin.CreateTagResponse, error) {
	t, rev, err := s.repo.Dictionary().CreateTag(ctx, req.GetName(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "create tag", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.CreateTagResponse{Tag: pbTagDict(t), Revision: pbDictRevision(entity.DictNamespaceTag, rev)}, nil
}

func (s *Server) UpdateTag(ctx context.Context, req *pb_admin.UpdateTagRequest) (*pb_admin.UpdateTagResponse, error) {
	rev, err := s.repo.Dictionary().UpdateTag(ctx, int(req.GetId()), req.GetName(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "update tag", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.UpdateTagResponse{Revision: pbDictRevision(entity.DictNamespaceTag, rev)}, nil
}

func (s *Server) ArchiveTag(ctx context.Context, req *pb_admin.ArchiveTagRequest) (*pb_admin.ArchiveTagResponse, error) {
	rev, err := s.repo.Dictionary().ArchiveTag(ctx, int(req.GetId()), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "archive tag", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.ArchiveTagResponse{Revision: pbDictRevision(entity.DictNamespaceTag, rev)}, nil
}

// ---- Country (closed dictionary: set-active only) --------------------------

func (s *Server) ListCountries(ctx context.Context, req *pb_admin.ListCountriesRequest) (*pb_admin.ListCountriesResponse, error) {
	rows, err := s.repo.Dictionary().ListCountries(ctx, req.GetActiveOnly())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list countries: %v", err)
	}
	out := make([]*pb_common.Country, 0, len(rows))
	for _, c := range rows {
		out = append(out, pbCountry(c))
	}
	return &pb_admin.ListCountriesResponse{Countries: out}, nil
}

func (s *Server) SetCountryActive(ctx context.Context, req *pb_admin.SetCountryActiveRequest) (*pb_admin.SetCountryActiveResponse, error) {
	rev, err := s.repo.Dictionary().SetCountryActive(ctx, req.GetCode(), req.GetActive(), int64(req.GetExpectedVersion()))
	if err != nil {
		return nil, dictMutationError(ctx, "set country active", err)
	}
	s.refreshDictionaryCache(ctx)
	return &pb_admin.SetCountryActiveResponse{Revision: pbDictRevision(entity.DictNamespaceCountry, rev)}, nil
}
