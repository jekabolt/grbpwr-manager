package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AssignTechCardRole assigns an admin account a role on a tech card (Q5), multi per role. A duplicate
// (card, role, admin) or a missing card/admin is returned as a field-tagged InvalidArgument.
func (s *Server) AssignTechCardRole(ctx context.Context, req *pb_admin.AssignTechCardRoleRequest) (*pb_admin.AssignTechCardRoleResponse, error) {
	if req.TechCardId <= 0 {
		return nil, apierr.Invalid(entity.NewFieldViolation("tech_card_id", "required", "", "provide the tech card id"))
	}
	if req.AdminId <= 0 {
		return nil, apierr.Invalid(entity.NewFieldViolation("admin_id", "required", "", "pick an admin account (ListAdmins)"))
	}
	role := dto.TechCardRoleFromPb(req.Role)
	if !entity.IsValidTechCardRole(role) {
		return nil, apierr.Invalid(entity.NewFieldViolation("role", "invalid", "", "choose a known role (designer, constructor, technologist, pattern_maker, grader, approver, other)"))
	}
	assignment, err := s.repo.TechCards().AssignTechCardRole(ctx, entity.TechCardRoleAssignment{
		TechCardId: int(req.TechCardId),
		Role:       role,
		AdminId:    int(req.AdminId),
		AssignedBy: authsrv.GetAdminUsername(ctx),
	})
	if err != nil {
		if s.repo.IsErrUniqueViolation(err) {
			return nil, apierr.Invalid(entity.NewFieldViolation("admin_id", "already_assigned", "",
				"this admin already holds this role on this card"))
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, apierr.Invalid(entity.NewFieldViolation("admin_id", "not_found", "",
				"the tech card or the admin account does not exist"))
		}
		slog.Default().ErrorContext(ctx, "can't assign tech card role", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't assign tech card role")
	}
	return &pb_admin.AssignTechCardRoleResponse{Assignment: dto.TechCardRoleAssignmentToPb(assignment)}, nil
}

// RemoveTechCardRoleAssignment removes one role assignment by id.
func (s *Server) RemoveTechCardRoleAssignment(ctx context.Context, req *pb_admin.RemoveTechCardRoleAssignmentRequest) (*pb_admin.RemoveTechCardRoleAssignmentResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "role assignment id is required")
	}
	if err := s.repo.TechCards().RemoveTechCardRoleAssignment(ctx, int(req.Id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "role assignment not found")
		}
		slog.Default().ErrorContext(ctx, "can't remove tech card role assignment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't remove tech card role assignment")
	}
	return &pb_admin.RemoveTechCardRoleAssignmentResponse{}, nil
}

// ListTechCardRoleAssignments lists a card's role assignments with resolved usernames.
func (s *Server) ListTechCardRoleAssignments(ctx context.Context, req *pb_admin.ListTechCardRoleAssignmentsRequest) (*pb_admin.ListTechCardRoleAssignmentsResponse, error) {
	if req.TechCardId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	rows, err := s.repo.TechCards().ListTechCardRoleAssignments(ctx, int(req.TechCardId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tech card role assignments", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list tech card role assignments")
	}
	resp := &pb_admin.ListTechCardRoleAssignmentsResponse{}
	for i := range rows {
		resp.Assignments = append(resp.Assignments, dto.TechCardRoleAssignmentToPb(rows[i]))
	}
	return resp, nil
}

// ListAdmins is the lightweight admin-account picker source for role assignment (id + username).
// Reuses the accounts store read and projects it, so a role-assigner needs only tech_cards:read.
func (s *Server) ListAdmins(ctx context.Context, _ *pb_admin.ListAdminsRequest) (*pb_admin.ListAdminsResponse, error) {
	accounts, err := s.repo.Admin().ListAccounts(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list admins for picker", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list admins")
	}
	resp := &pb_admin.ListAdminsResponse{Admins: make([]*pb_admin.AdminRef, 0, len(accounts))}
	for i := range accounts {
		resp.Admins = append(resp.Admins, &pb_admin.AdminRef{
			Id:       int32(accounts[i].Id),
			Username: accounts[i].Username,
		})
	}
	return resp, nil
}
