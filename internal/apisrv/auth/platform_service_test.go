package auth

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// teamAdmins is an in-memory admins table: only the calls the join and legacy Login make.
type teamAdmins struct {
	dependency.Admin
	mu   sync.Mutex
	rows map[string]*entity.AdminAccount
}

func (a *teamAdmins) row(username string) (*entity.AdminAccount, error) {
	r, ok := a.rows[username]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *r
	return &cp, nil
}

func (a *teamAdmins) GetAdminByUsername(_ context.Context, username string) (*entity.Admin, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.row(username)
	if err != nil {
		return nil, err
	}
	return &r.Admin, nil
}

func (a *teamAdmins) GetAccountWithPermissions(_ context.Context, username string) (*entity.AdminAccount, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.row(username)
}

func (a *teamAdmins) AddAccount(_ context.Context, username, pwHash string, isSuper bool, perms []entity.AdminPermission) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows[username] = &entity.AdminAccount{Admin: entity.Admin{Username: username, PasswordHash: pwHash, IsSuper: isSuper}, Permissions: perms}
	return nil
}

func (a *teamAdmins) SetAccountPermissions(_ context.Context, username string, isSuper bool, perms []entity.AdminPermission) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.rows[username]
	if !ok {
		return sql.ErrNoRows
	}
	r.IsSuper, r.Permissions = isSuper, perms
	return nil
}

func (a *teamAdmins) SetAccountDisabled(_ context.Context, username string, disabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.rows[username]
	if !ok {
		return sql.ErrNoRows
	}
	r.Disabled, r.JoinPending = disabled, false
	return nil
}

// StageJoin and FinishJoin follow *admin.Store (covered against MySQL by the store's own test).
func (a *teamAdmins) StageJoin(_ context.Context, username, pwHash string, isSuper bool, perms []entity.AdminPermission) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.rows[username]
	if !ok {
		r = &entity.AdminAccount{Admin: entity.Admin{Username: username, PasswordHash: pwHash}}
		a.rows[username] = r
	}
	r.Disabled, r.JoinPending, r.IsSuper, r.Permissions = true, true, isSuper, perms
	return nil
}

func (a *teamAdmins) FinishJoin(_ context.Context, username string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.rows[username]
	if !ok || !r.JoinPending {
		return false, nil
	}
	r.Disabled, r.JoinPending = false, false
	return true, nil
}

// FreezeJoin follows *admin.Store: only an enabled row is frozen, and it gets the join marker.
func (a *teamAdmins) FreezeJoin(_ context.Context, username string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.rows[username]
	if !ok || r.Disabled {
		return false, nil
	}
	r.Disabled, r.JoinPending = true, true
	return true, nil
}

// TestInviteCrashAfterStagingKeepsLegacyLoginOut: a removed member keeps a usable password in their
// disabled admins row. The join's step 2 raises the row's rights but leaves it disabled, so if the
// join stops there (crash, or activation refused) legacy Login still refuses that password. Only
// step 4, after the control plane made the member active, lets it in. A brand-new staged row is
// disabled and has no usable password.
func TestInviteCrashAfterStagingKeepsLegacyLoginOut(t *testing.T) {
	admins := &teamAdmins{rows: map[string]*entity.AdminAccount{}}
	srv, err := New(&Config{
		JWTSecret:                jwtSecret,
		MasterPassword:           masterPassword,
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 1000,
		JWTTTL:                   "60m",
	}, admins)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.StopRateLimiter)
	ctx := context.Background()
	hash, err := srv.pwhash.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	admins.rows["kim"] = &entity.AdminAccount{Admin: entity.Admin{Username: "kim", PasswordHash: hash, Disabled: true}}
	login := func(user string) error {
		_, err := srv.Login(ctx, &pb_auth.LoginRequest{Username: user, Password: password})
		return err
	}
	if err := login("kim"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("removed member before the join: %v", err)
	}

	write := []entity.AdminPermission{{Section: "products", Access: entity.AccessWrite}}
	if err := srv.stageTeamAdmin(ctx, "kim", false, write); err != nil {
		t.Fatal(err)
	}
	if r := admins.rows["kim"]; !r.Disabled || len(r.Permissions) != 1 {
		t.Fatalf("staged row = %+v", r)
	}
	if err := login("kim"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("legacy login after step 2 = %v, want Unauthenticated", err)
	}
	// An enabled row of an already active member is disabled before its rights change.
	admins.rows["lea"] = &entity.AdminAccount{Admin: entity.Admin{Username: "lea", PasswordHash: hash}}
	if err := srv.stageTeamAdmin(ctx, "lea", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := login("lea"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("legacy login of a re-staged row = %v, want Unauthenticated", err)
	}
	// A new row is staged disabled, with a hash nobody can match.
	if err := srv.stageTeamAdmin(ctx, "noa", false, write); err != nil {
		t.Fatal(err)
	}
	if r := admins.rows["noa"]; !r.Disabled || srv.pwhash.Validate(password, r.PasswordHash) == nil {
		t.Fatalf("new staged row = disabled %v", r.Disabled)
	}

	// Step 4: enabled once, the password works; a second finish finds nothing to do.
	if ok, err := srv.enableTeamAdmin(ctx, "kim"); err != nil || !ok {
		t.Fatalf("finish = %v, %v", ok, err)
	}
	if ok, err := srv.enableTeamAdmin(ctx, "kim"); err != nil || ok {
		t.Fatalf("second finish = %v, %v", ok, err)
	}
	if err := login("kim"); err != nil {
		t.Fatalf("legacy login after step 4: %v", err)
	}
	// A staged row an admin disabled is never finished.
	if err := srv.stageTeamAdmin(ctx, "noa", false, write); err != nil {
		t.Fatal(err)
	}
	if err := admins.SetAccountDisabled(ctx, "noa", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := srv.enableTeamAdmin(ctx, "noa"); err != nil || ok || !admins.rows["noa"].Disabled {
		t.Fatalf("finish after an admin disable = %v, %v", ok, err)
	}
}
