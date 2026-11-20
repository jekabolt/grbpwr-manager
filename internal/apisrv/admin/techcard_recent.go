package admin

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Recently touched tech cards (home «recent techcards», table tech_card_recent).
//
// WHO: the caller is always «me» — the tenant admins.id of the authenticated account. Each team has
// its own database and admins table, so neither the touch nor the list can cross teams.
//
// WHEN: GetTechCard and every successful tech-card write stamp the row, through ONE best-effort
// helper (touchRecentTechCard) driven by ONE interceptor (RecentTechCardsUnary). A failed touch is a
// warning, never a failed read or write.

// ListMyRecentTechCards returns the caller's recently touched cards, latest first.
func (s *Server) ListMyRecentTechCards(ctx context.Context, req *pb_admin.ListMyRecentTechCardsRequest) (*pb_admin.ListMyRecentTechCardsResponse, error) {
	accountID, ok := s.recentAccountID(ctx)
	if !ok {
		return &pb_admin.ListMyRecentTechCardsResponse{Items: []*pb_admin.RecentTechCard{}}, nil
	}
	recent, err := s.repo.TechCards().ListRecentTechCards(ctx, accountID, int(req.GetLimit()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list recent tech cards", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list recent tech cards")
	}
	items := make([]*pb_admin.RecentTechCard, 0, len(recent))
	for i := range recent {
		items = append(items, &pb_admin.RecentTechCard{
			Card:      dto.ConvertEntityTechCardToListItemPb(&recent[i].Card),
			TouchedAt: timestamppb.New(recent[i].TouchedAt),
		})
	}
	return &pb_admin.ListMyRecentTechCardsResponse{Items: items}, nil
}

// recentAccountID is the caller's tenant admins.id: the actor the auth chain resolved for every
// admin call (populateAdmin → adminIDFor, cached per account, failures included). No second lookup
// here: it would re-ask the DB on every call for exactly the accounts the cache already knows have
// no row. ok=false when there is no account row: such a caller has no «recent» list.
func (s *Server) recentAccountID(ctx context.Context) (int, bool) {
	if id := aiprov.ActorFrom(ctx).AdminID; id != nil && *id > 0 {
		return *id, true
	}
	return 0, false
}

// touchRecentTechCard stamps «I touched this card now» for the caller. BEST-EFFORT: every failure
// (no account, unknown card, a DB error) is at most a warning; the call that touched the card has
// already succeeded and keeps its answer.
func (s *Server) touchRecentTechCard(ctx context.Context, techCardID int) {
	if techCardID <= 0 || s == nil || s.repo == nil {
		return
	}
	accountID, ok := s.recentAccountID(ctx)
	if !ok {
		return
	}
	if err := s.repo.TechCards().TouchTechCardRecent(ctx, accountID, techCardID); err != nil {
		slog.Default().WarnContext(ctx, "can't touch recent tech card; the call stands",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	}
}

// RecentTechCardsUnary stamps the caller's «recent» row after GetTechCard and after every
// successful tech-card write. It sits after the auth interceptor (the caller is resolved) and runs
// the touch only once the handler has answered without error.
func (s *Server) RecentTechCardsUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			s.touchRecentTechCard(ctx, touchedTechCardID(info.FullMethod, req, resp))
		}
		return resp, err
	}
}

// touchedTechCardID is the card a successful call touched, 0 for none:
//
//   - GetTechCard / UpdateTechCard: the request's id; CreateTechCard: the response's id.
//   - DeleteTechCard: none — the row went with the card (FK cascade).
//   - any other AdminService write in the tech-cards section: the request's tech_card_id, else the
//     response's (CommitTechCardImport creates the card it names). A write without one (materials,
//     FX rates, design-run ids) touches nothing.
//
// A switch, not a lookup table: tools/tenantgate ratchets package-level vars.
func touchedTechCardID(fullMethod string, req, resp any) int {
	name, ok := strings.CutPrefix(fullMethod, rbac.MethodPrefix)
	if !ok {
		return 0
	}
	switch name {
	case "GetTechCard", "UpdateTechCard":
		return protoIntField(req, "id")
	case "CreateTechCard":
		return protoIntField(resp, "id")
	case "DeleteTechCard":
		return 0
	}
	r, _, known := rbac.Lookup(fullMethod)
	if !known || r.Section != rbac.SectionTechCards || r.Access != entity.AccessWrite {
		return 0
	}
	if id := protoIntField(req, "tech_card_id"); id > 0 {
		return id
	}
	return protoIntField(resp, "tech_card_id")
}

// protoIntField reads a singular integer field by name, 0 when the message has none.
func protoIntField(v any, name protoreflect.Name) int {
	m, ok := v.(proto.Message)
	if !ok {
		return 0
	}
	r := m.ProtoReflect()
	if !r.IsValid() {
		return 0
	}
	f := r.Descriptor().Fields().ByName(name)
	if f == nil || f.IsList() || f.IsMap() {
		return 0
	}
	switch f.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return int(r.Get(f).Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return int(r.Get(f).Uint())
	}
	return 0
}
