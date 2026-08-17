package admin

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// minAdminPasswordLen is the minimum length for a password set through the admin
// account-management RPCs. The master-password bootstrap path is unaffected.
const minAdminPasswordLen = 8

// GetCurrentAccount returns the calling account's effective authorization (what
// its token grants), for the admin panel to decide which sections to render. It
// reflects the token, not the database, so a permission change only shows up
// after the account logs in again — consistent with how enforcement works.
func (s *Server) GetCurrentAccount(ctx context.Context, _ *pb_admin.GetCurrentAccountRequest) (*pb_admin.GetCurrentAccountResponse, error) {
	acc := &pb_admin.AdminAccount{Username: authsrv.GetAdminUsername(ctx)}
	authz, ok := authsrv.GetAdminAuthz(ctx)
	if ok && authz.FullAccess() {
		acc.IsSuper = true
		return &pb_admin.GetCurrentAccountResponse{Account: acc}, nil
	}
	// Scoped account (or, defensively, missing authz): expose only granted sections.
	if ok {
		acc.Permissions = permsToProto(authz.Perms)
	}
	return &pb_admin.GetCurrentAccountResponse{Account: acc}, nil
}

// ListAccountSections returns the catalog of grantable sections for the UI's
// permission picker.
func (s *Server) ListAccountSections(_ context.Context, _ *pb_admin.ListAccountSectionsRequest) (*pb_admin.ListAccountSectionsResponse, error) {
	sections := rbac.Sections()
	out := make([]*pb_admin.AdminSectionInfo, 0, len(sections))
	for _, s := range sections {
		out = append(out, &pb_admin.AdminSectionInfo{Key: s.Key, Title: s.Title, Description: s.Description})
	}
	return &pb_admin.ListAccountSectionsResponse{Sections: out}, nil
}

// ListAccounts returns every admin account with its permissions.
func (s *Server) ListAccounts(ctx context.Context, _ *pb_admin.ListAccountsRequest) (*pb_admin.ListAccountsResponse, error) {
	accounts, err := s.repo.Admin().ListAccounts(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to list admin accounts", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to list accounts")
	}
	out := make([]*pb_admin.AdminAccount, 0, len(accounts))
	for i := range accounts {
		out = append(out, toProtoAccount(&accounts[i]))
	}
	return &pb_admin.ListAccountsResponse{Accounts: out}, nil
}

// CreateAccount creates a scoped or super admin account.
func (s *Server) CreateAccount(ctx context.Context, req *pb_admin.CreateAccountRequest) (*pb_admin.CreateAccountResponse, error) {
	username := normalizeUsername(req.Username)
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	if len(req.Password) < minAdminPasswordLen {
		return nil, status.Errorf(codes.InvalidArgument, "password must be at least %d characters", minAdminPasswordLen)
	}
	perms, err := toEntityPermissions(req.IsSuper, req.Permissions)
	if err != nil {
		return nil, err
	}
	pwHash, err := s.pwhash.HashPassword(req.Password)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to hash password", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to create account")
	}
	if err := s.repo.Admin().AddAccount(ctx, username, pwHash, req.IsSuper, perms); err != nil {
		slog.Default().ErrorContext(ctx, "failed to add admin account",
			slog.String("username", username), slog.String("err", err.Error()))
		// A duplicate username violates the unique key; surface it as AlreadyExists.
		if strings.Contains(err.Error(), "Duplicate") {
			return nil, status.Errorf(codes.AlreadyExists, "account %q already exists", username)
		}
		return nil, status.Error(codes.Internal, "failed to create account")
	}
	account, err := s.repo.Admin().GetAccountWithPermissions(ctx, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to reload created account", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "account created but could not be read back")
	}
	return &pb_admin.CreateAccountResponse{Account: toProtoAccount(account)}, nil
}

// UpdateAccountPermissions replaces an account's super flag and permission set.
func (s *Server) UpdateAccountPermissions(ctx context.Context, req *pb_admin.UpdateAccountPermissionsRequest) (*pb_admin.UpdateAccountPermissionsResponse, error) {
	username := normalizeUsername(req.Username)
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	perms, err := toEntityPermissions(req.IsSuper, req.Permissions)
	if err != nil {
		return nil, err
	}
	current, err := s.repo.Admin().GetAccountWithPermissions(ctx, username)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "account %q not found", username)
	}
	// Demoting the last remaining super-admin would leave nobody able to manage
	// accounts; block it.
	if current.IsSuper && !req.IsSuper {
		if err := s.ensureNotLastSuper(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Admin().SetAccountPermissions(ctx, username, req.IsSuper, perms); err != nil {
		slog.Default().ErrorContext(ctx, "failed to update admin permissions",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to update permissions")
	}
	account, err := s.repo.Admin().GetAccountWithPermissions(ctx, username)
	if err != nil {
		return nil, status.Error(codes.Internal, "permissions updated but account could not be read back")
	}
	return &pb_admin.UpdateAccountPermissionsResponse{Account: toProtoAccount(account)}, nil
}

// SetAccountDisabled enables or disables an account.
func (s *Server) SetAccountDisabled(ctx context.Context, req *pb_admin.SetAccountDisabledRequest) (*pb_admin.SetAccountDisabledResponse, error) {
	username := normalizeUsername(req.Username)
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	if req.Disabled {
		current, err := s.repo.Admin().GetAccountWithPermissions(ctx, username)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "account %q not found", username)
		}
		if current.IsSuper {
			if err := s.ensureNotLastSuper(ctx); err != nil {
				return nil, err
			}
		}
	}
	if err := s.repo.Admin().SetAccountDisabled(ctx, username, req.Disabled); err != nil {
		slog.Default().ErrorContext(ctx, "failed to set account disabled",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to update account")
	}
	return &pb_admin.SetAccountDisabledResponse{}, nil
}

// ResetAccountPassword sets a new password for an account.
func (s *Server) ResetAccountPassword(ctx context.Context, req *pb_admin.ResetAccountPasswordRequest) (*pb_admin.ResetAccountPasswordResponse, error) {
	username := normalizeUsername(req.Username)
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	if len(req.NewPassword) < minAdminPasswordLen {
		return nil, status.Errorf(codes.InvalidArgument, "password must be at least %d characters", minAdminPasswordLen)
	}
	pwHash, err := s.pwhash.HashPassword(req.NewPassword)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to hash password", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to reset password")
	}
	if err := s.repo.Admin().ChangePassword(ctx, username, pwHash); err != nil {
		slog.Default().ErrorContext(ctx, "failed to reset password",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Errorf(codes.NotFound, "account %q not found", username)
	}
	return &pb_admin.ResetAccountPasswordResponse{}, nil
}

// DeleteAccount removes an admin account.
func (s *Server) DeleteAccount(ctx context.Context, req *pb_admin.DeleteAccountRequest) (*pb_admin.DeleteAccountResponse, error) {
	username := normalizeUsername(req.Username)
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	// Deleting your own account mid-session is almost always a mistake and can lock
	// you out; require it to be done from another account.
	if caller := authsrv.GetAdminUsername(ctx); caller != "" && caller == username {
		return nil, status.Error(codes.FailedPrecondition, "cannot delete your own account")
	}
	current, err := s.repo.Admin().GetAccountWithPermissions(ctx, username)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "account %q not found", username)
	}
	if current.IsSuper {
		if err := s.ensureNotLastSuper(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Admin().DeleteAdmin(ctx, username); err != nil {
		slog.Default().ErrorContext(ctx, "failed to delete account",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to delete account")
	}
	return &pb_admin.DeleteAccountResponse{}, nil
}

// accountsWriteAccess reports whether the caller may manage OTHER people's accounts. Fails closed on
// a context that never passed the RBAC interceptor — a handler-side requirement that defaulted to
// «allowed» would be worse than none, because it would look enforced. Mirrors productionWriteAccess.
func accountsWriteAccess(ctx context.Context) bool {
	az, ok := authsrv.GetAdminAuthz(ctx)
	if !ok {
		return false
	}
	if az.FullAccess() {
		return true
	}
	lvl, ok := az.Perms[rbac.SectionAccounts]
	return ok && lvl.Covers(entity.AccessWrite)
}

// SetAccountSpecialties replaces what an account says it does.
//
// СВОЮ СПЕЦИАЛЬНОСТЬ ЧЕЛОВЕК ПРАВИТ САМ, ЧУЖУЮ — ТОЛЬКО С accounts:write. Именно поэтому метод
// стоит в allowlist карты прав, а проверка живёт здесь: держи мы его в wr(SectionAccounts),
// интерсептор отрезал бы правку СВОЕГО аккаунта до входа в хендлер, и разрешать было бы уже негде.
//
// Довод за само разрешение: специальность не несёт ни грамма прав — это самоописание, которым ищут
// человека в пикере владельцев и в упоминаниях. Поле, которое нельзя заполнить без администратора
// аккаунтов, остаётся пустым, а пустой словарь обесценивает и пикер, и поиск людей. Цена ошибки —
// неверная подпись у себя, и её правит чужая рука с accounts:write.
func (s *Server) SetAccountSpecialties(ctx context.Context, req *pb_admin.SetAccountSpecialtiesRequest) (*pb_admin.SetAccountSpecialtiesResponse, error) {
	username := normalizeUsername(req.Username)
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	caller := normalizeUsername(authsrv.GetAdminUsername(ctx))
	// Пустое имя вызывающего (путь мимо интерсептора) НЕ считается совпадением: иначе
	// неаутентифицированный контекст правил бы аккаунт с пустым именем и попадал в ветку «своё».
	if caller == "" || caller != username {
		if !accountsWriteAccess(ctx) {
			return nil, status.Error(codes.PermissionDenied,
				"accounts:write is required to edit somebody else's specialties")
		}
	}
	specialtyIDs, newSpecialties, err := dto.ConvertPbSpecialtySelectionToEntity(req.SpecialtyIds, req.NewSpecialties)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	account, err := s.repo.Admin().GetAdminByUsername(ctx, username)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "account %q not found", username)
	}
	if err := s.repo.Admin().SetSpecialties(ctx, account.Id, specialtyIDs, newSpecialties); err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "specialty_id does not reference an existing specialty")
		}
		if errors.Is(err, entity.ErrAdminSpecialtyVocabularyFull) {
			// FailedPrecondition, а не InvalidArgument: запрос корректен, переполнен мир.
			return nil, status.Errorf(codes.FailedPrecondition,
				"the shared specialty vocabulary is full (%d); pick an existing one instead of adding a new name",
				entity.MaxAdminSpecialtyVocabulary)
		}
		slog.Default().ErrorContext(ctx, "failed to set account specialties",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to set specialties")
	}
	// Перечитываем: имя, набранное в другом регистре, схлопывается на существующую
	// запись словаря, и чипы обязаны показать то, что ЛЕЖИТ, а не то, что отправили.
	stored, err := s.repo.Admin().GetAccountWithPermissions(ctx, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "specialties saved but could not be read back",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "specialties were saved but could not be read back")
	}
	return &pb_admin.SetAccountSpecialtiesResponse{Specialties: stored.Specialties}, nil
}

// ensureNotLastSuper returns FailedPrecondition when only one enabled super-admin
// remains, so the caller does not remove or demote the last one.
func (s *Server) ensureNotLastSuper(ctx context.Context) error {
	n, err := s.repo.Admin().CountSuperAdmins(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to count super admins", slog.String("err", err.Error()))
		return status.Error(codes.Internal, "failed to verify super-admin count")
	}
	if n <= 1 {
		return status.Error(codes.FailedPrecondition, "cannot remove the last super-admin")
	}
	return nil
}

func normalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// toEntityPermissions validates and converts request permissions. When super is
// true, per-section grants are irrelevant and dropped. Duplicate or unknown
// sections and unspecified access levels are rejected.
func toEntityPermissions(super bool, in []*pb_admin.AdminPermission) ([]entity.AdminPermission, error) {
	if super {
		return nil, nil
	}
	out := make([]entity.AdminPermission, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, p := range in {
		if !rbac.ValidSection(p.Section) {
			return nil, status.Errorf(codes.InvalidArgument, "unknown section %q", p.Section)
		}
		access, err := toEntityAccess(p.Access)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[p.Section]; dup {
			return nil, status.Errorf(codes.InvalidArgument, "duplicate section %q", p.Section)
		}
		seen[p.Section] = struct{}{}
		out = append(out, entity.AdminPermission{Section: p.Section, Access: access})
	}
	return out, nil
}

func toEntityAccess(a pb_admin.AccessLevel) (entity.AccessLevel, error) {
	switch a {
	case pb_admin.AccessLevel_ACCESS_LEVEL_READ:
		return entity.AccessRead, nil
	case pb_admin.AccessLevel_ACCESS_LEVEL_WRITE:
		return entity.AccessWrite, nil
	default:
		return "", status.Error(codes.InvalidArgument, "access level must be READ or WRITE")
	}
}

func toProtoAccess(a entity.AccessLevel) pb_admin.AccessLevel {
	switch a {
	case entity.AccessRead:
		return pb_admin.AccessLevel_ACCESS_LEVEL_READ
	case entity.AccessWrite:
		return pb_admin.AccessLevel_ACCESS_LEVEL_WRITE
	default:
		return pb_admin.AccessLevel_ACCESS_LEVEL_UNSPECIFIED
	}
}

func toProtoAccount(a *entity.AdminAccount) *pb_admin.AdminAccount {
	perms := make([]*pb_admin.AdminPermission, 0, len(a.Permissions))
	for _, p := range a.Permissions {
		perms = append(perms, &pb_admin.AdminPermission{Section: p.Section, Access: toProtoAccess(p.Access)})
	}
	acc := &pb_admin.AdminAccount{
		Username:    a.Username,
		IsSuper:     a.IsSuper,
		Disabled:    a.Disabled,
		Permissions: perms,
		Specialties: a.Specialties,
	}
	if !a.CreatedAt.IsZero() {
		acc.CreatedAt = timestamppb.New(a.CreatedAt)
	}
	if !a.UpdatedAt.IsZero() {
		acc.UpdatedAt = timestamppb.New(a.UpdatedAt)
	}
	return acc
}

// permsToProto converts a resolved section→access map (sorted by section for a
// stable response) to proto permissions.
func permsToProto(perms map[string]entity.AccessLevel) []*pb_admin.AdminPermission {
	out := make([]*pb_admin.AdminPermission, 0, len(perms))
	for section, access := range perms {
		out = append(out, &pb_admin.AdminPermission{Section: section, Access: toProtoAccess(access)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Section < out[j].Section })
	return out
}
