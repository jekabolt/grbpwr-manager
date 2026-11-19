package platform

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DeleteTeam hides a team at once (status deleted); its data is wiped when purge_at passes, and
// until then RestoreTeam brings it back. The operator confirms with their own console password,
// checked under the sign-in limiters: wrong, empty or no password = PERMISSION_DENIED
// wrong_password, a refused attempt = RESOURCE_EXHAUSTED. Then team #1 = team_one, a team that is
// not active / read-only = wrong_status. The password never reaches a log, the audit or an error.
func (s *Server) DeleteTeam(ctx context.Context, req *pb_admin.DeleteTeamRequest) (*pb_admin.DeleteTeamResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.confirmPassword(ctx, c.UserID, req.GetPassword()); err != nil {
		return nil, err
	}
	t, err := s.teamWithDeleted(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if t.ID == tenant.TeamOneID {
		return nil, status.Error(codes.FailedPrecondition, "team_one")
	}
	switch tenant.Status(t.Status) {
	case tenant.StatusActive, tenant.StatusSuspended:
	default:
		return nil, status.Error(codes.FailedPrecondition, "wrong_status")
	}
	del, err := s.st.DeleteTeam(ctx, t.ID, s.nowUTC(), platformstore.DeleteRetention)
	if err != nil {
		return nil, storeErr(ctx, "delete team", err)
	}
	s.forgetTeam(t.ID)
	if s.dropSession != nil {
		for _, sid := range del.SIDs {
			s.dropSession(sid)
		}
	}
	// The audit and the answer follow the team as the transaction locked it, by id: a rename that
	// committed after the read above does not turn a committed delete into NOT_FOUND.
	s.audit(ctx, c.UserID, t.ID, "team_delete", map[string]string{
		"slug": del.Slug, "name": del.Name, "purge_at": del.PurgeAt.UTC().Format(time.RFC3339),
	})
	team, err := s.refreshedByID(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return &pb_admin.DeleteTeamResponse{Team: team}, nil
}

// RestoreTeam brings a deleted team whose data is not wiped yet back to the status it had (active
// or read-only). No password. Sessions and invites revoked by the delete stay revoked.
func (s *Server) RestoreTeam(ctx context.Context, req *pb_admin.RestoreTeamRequest) (*pb_admin.RestoreTeamResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.teamWithDeleted(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if t.Status != string(tenant.StatusDeleted) {
		return nil, status.Error(codes.FailedPrecondition, "wrong_status")
	}
	rs, err := s.st.RestoreTeam(ctx, t.ID, s.nowUTC())
	if err != nil {
		return nil, storeErr(ctx, "restore team", err)
	}
	s.forgetTeam(t.ID)
	// Audited as the transaction restored it, before anything can fail; answered by id, so a rename
	// right after the restore does not turn it into NOT_FOUND.
	s.audit(ctx, c.UserID, t.ID, "team_restore", map[string]string{"slug": rs.Slug, "status": consoleStatus(rs.Status)})
	team, err := s.refreshedByID(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return &pb_admin.RestoreTeamResponse{Team: team}, nil
}

// confirmPassword maps the operator password check to the console's answers.
func (s *Server) confirmPassword(ctx context.Context, userID int64, password string) error {
	if s.checkPassword == nil {
		return status.Error(codes.Unavailable, "unavailable")
	}
	err := s.checkPassword(ctx, userID, password)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, platformauth.ErrWrongPassword):
		return status.Error(codes.PermissionDenied, "wrong_password")
	case errors.Is(err, platformauth.ErrTooManyAttempts):
		return status.Error(codes.ResourceExhausted, "too_many_attempts")
	}
	slog.Default().ErrorContext(ctx, "platform: operator password check", slog.String("err", err.Error()))
	return status.Error(codes.Internal, "internal error")
}

// forgetTeam drops everything this instance caches of a team whose status the console just
// changed to or from deleted: the directory entry and the edge's feature switches (dropTeam), and
// the team's dictionary snapshot.
func (s *Server) forgetTeam(teamID int64) {
	s.dropTeam(teamID)
	cache.ForgetTeam(teamID)
}
