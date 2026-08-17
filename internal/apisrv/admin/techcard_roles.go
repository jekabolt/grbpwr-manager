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

// ListAdmins is the panel's people picker: id, username, self-declared specialties and the super
// flag. It is ALLOWLISTED (internal/rbac/rbac.go) — a picker of people is needed from sections that
// do not contain one another, and any single section gate breaks it on the other screens as an empty
// list, which reads like "there is nobody to pick" rather than like a refusal.
//
// It runs its OWN narrow read (ListAdminRefs) rather than projecting ListAccounts down: permissions
// and password hashes must not be loaded at all for a call any authenticated account may make, and
// disabled accounts are excluded because a picker offering somebody who left is a wrong answer to
// both questions it is asked. What an account may DO is accounts:read material and travels on
// ListAccounts; what a person IS is what a picker needs.
//
// The whole specialty vocabulary rides along — the chip editor offers it as a list, and a seeded
// entry nobody has picked yet would otherwise be invisible. Failing to read it is NOT fatal: the
// picker still works off the specialties people already carry, and losing the people list over a
// missing dictionary would be the wrong trade.
func (s *Server) ListAdmins(ctx context.Context, _ *pb_admin.ListAdminsRequest) (*pb_admin.ListAdminsResponse, error) {
	refs, err := s.repo.Admin().ListAdminRefs(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list admins for picker", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list admins")
	}
	specialties, err := s.repo.Admin().ListSpecialties(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list admin specialties", slog.String("err", err.Error()))
	}
	return &pb_admin.ListAdminsResponse{
		Admins:      dto.ConvertEntityAdminRefsToPb(refs),
		Specialties: specialties,
	}, nil
}
