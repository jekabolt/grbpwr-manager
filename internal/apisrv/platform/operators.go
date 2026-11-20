package platform

import (
	"context"
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListOperators lists the active operators and their authenticator-app state (wave teams, gap 4).
func (s *Server) ListOperators(ctx context.Context, _ *pb_admin.ListOperatorsRequest) (*pb_admin.ListOperatorsResponse, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	rows, err := s.st.ListOperatorSecondSteps(ctx)
	if err != nil {
		return nil, storeErr(ctx, "list operators", err)
	}
	out := &pb_admin.ListOperatorsResponse{}
	for _, r := range rows {
		out.Operators = append(out.Operators, &pb_admin.PlatformOperator{
			Email: r.Email.String, AppSetUp: r.ConfirmedAt.Valid, RecoveryCodesLeft: int32(r.RecoveryLeft),
		})
	}
	return out, nil
}

// ResetOperatorApp removes an operator's authenticator app and recovery codes (lost phone), signs
// them out of the console and mails every operator. The caller confirms with their own password
// (as DeleteTeam): wrong = PERMISSION_DENIED wrong_password, refused = RESOURCE_EXHAUSTED. An email
// that names no active operator = NOT_FOUND operator_not_found.
func (s *Server) ResetOperatorApp(ctx context.Context, req *pb_admin.ResetOperatorAppRequest) (*pb_admin.ResetOperatorAppResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.confirmPassword(ctx, c.UserID, req.GetPassword()); err != nil {
		return nil, err
	}
	by := ""
	if me, err := s.st.UserByID(ctx, c.UserID); err == nil && me.Email.Valid {
		by = me.Email.String
	}
	email, err := platformauth.NormalizeEmail(req.GetEmail())
	if err != nil {
		return nil, status.Error(codes.NotFound, "operator_not_found")
	}
	sids, err := s.st.ResetOperatorSecondStep(ctx, email, by, s.nowUTC())
	if errors.Is(err, platformstore.ErrNotOperator) {
		return nil, status.Error(codes.NotFound, "operator_not_found")
	}
	if err != nil {
		return nil, storeErr(ctx, "reset operator app", err)
	}
	if s.dropSession != nil {
		for _, sid := range sids {
			s.dropSession(sid)
		}
	}
	s.audit(ctx, c.UserID, 0, "operator_app_reset", map[string]string{"email": email})
	return &pb_admin.ResetOperatorAppResponse{}, nil
}
