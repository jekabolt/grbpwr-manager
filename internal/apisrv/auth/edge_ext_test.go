package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Teams mode off with the real binder as the edge (the wiring http.Start does): team #1's legacy
// admin is unchanged — every feature, any origin, writes pass; a platform token is refused.
func TestBinderModeOffKeepsTheLegacyAdmin(t *testing.T) {
	b, err := tenantbind.New(tenant.TeamOne("grbpwr_beta"))
	if err != nil {
		t.Fatal(err)
	}
	as := mocks.NewMockAdmin(t)
	as.EXPECT().GetAdminByUsername(mock.Anything, mock.Anything).Return(&entity.Admin{Id: 1}, nil).Maybe()
	srv, err := auth.New(&auth.Config{
		JWTSecret:                "test-jwt-secret-at-least-32-bytes-long!!",
		MasterPassword:           "FJKqDyBvr9pAQMB3f8Uj4s",
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 100000,
		JWTTTL:                   "60m",
	}, as)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetEdge(b)

	call := func(method, token string) (context.Context, error) {
		md := metadata.New(map[string]string{auth.AuthMetadataKey: "Bearer " + token, "grpcgateway-origin": "https://anything.example"})
		var got context.Context
		_, err := srv.UnaryAdminAuthInterceptor()(metadata.NewIncomingContext(context.Background(), md), nil,
			&grpc.UnaryServerInfo{FullMethod: method}, func(ctx context.Context, _ any) (any, error) { got = ctx; return nil, nil })
		return got, err
	}

	legacy, err := authjwt.NewAdminToken(srv.JwtAuth, time.Hour, "u", true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := call("/admin.AdminService/UpdateColorway", legacy)
	if err != nil {
		t.Fatalf("legacy write refused: %v", err)
	}
	w, ok := auth.GetWorkspace(ctx)
	if !ok || w.Team.ID != 1 || w.Team.DBName != "grbpwr_beta" || w.Role != "owner" || w.ReadOnly {
		t.Fatalf("workspace %+v", w)
	}
	if len(w.Features) != len(feature.Catalog()) {
		t.Fatalf("%d features on, want all %d", len(w.Features), len(feature.Catalog()))
	}

	team, err := authjwt.NewTeamTokenAt(srv.JwtAuth, time.Hour, authjwt.TeamClaims{
		Handle: "u", UserID: 5, TeamID: 1, SessionID: "s", Audience: authjwt.AudTeam, Super: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call("/admin.AdminService/ListOrders", team); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("team token with teams mode off: %v", err)
	}
	if _, err := call("/admin.PlatformService/ListTeams", team); status.Code(err) != codes.Unimplemented {
		t.Fatalf("platform call with teams mode off: %v", err)
	}
}
