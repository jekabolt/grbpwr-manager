package tenanttest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/platform"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/store/tenantdb"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	baseDomain = "tenanttest.lekalo.test"
	slugB      = "bravo"
	uidB       = int64(201)
	uidOp      = int64(301)
	sidA       = "salpha00000000000000000000000000"
	sidB       = "sbravo00000000000000000000000000"
	sidOp      = "soper000000000000000000000000000"
)

// harness is one control plane, team #1 (A, slug grbpwr) and team B (slug bravo) wired exactly
// like production: binder.Unary → auth.UnaryAdminAuthInterceptor → generated handler.
type harness struct {
	root  *sql.DB
	ctl   *platformstore.Store
	st    *store.MYSQLStore
	b     *tenantbind.Binder
	auth  *authsrv.Server
	adm   *admin.Server
	plat  *platform.Server
	uidA  int64
	teamB tenant.Team

	tokA, tokB, tokBAsOne, tokOp string
}

var (
	h     *harness
	hOnce sync.Once
	hErr  error
)

func (x *harness) close() {
	if x.st != nil {
		x.st.Close()
	}
	if x.ctl != nil {
		_ = x.ctl.Close()
	}
	if x.root != nil {
		_ = x.root.Close()
	}
}

func setup(t *testing.T) *harness {
	t.Helper()
	if os.Getenv("MYSQL_HOST") == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI set but MYSQL_HOST empty")
		}
		t.Skip("no test MySQL (run through test-db.sh)")
	}
	hOnce.Do(func() {
		x := &harness{}
		hErr = x.build(t)
		h = x
	})
	if hErr != nil {
		t.Fatal(hErr)
	}
	return h
}

func (x *harness) build(t *testing.T) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	dsnOne := rootDSN(dbOne)
	if err := testdbguard.CheckMySQLDSN(dsnOne); err != nil {
		return err
	}
	var err error
	if x.root, err = sql.Open("mysql", rootDSN("")); err != nil {
		return err
	}
	for _, db := range []string{dbOne, dbTwo, dbControl} {
		for _, q := range []string{"DROP DATABASE IF EXISTS `" + db + "`", "CREATE DATABASE `" + db + "` CHARACTER SET utf8mb4"} {
			if _, err := x.root.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
	}

	// Team A = team #1, fully migrated. Team B gets the same tables (structure only, no
	// migration seed rows): the in-memory test MySQL does not hold two full migration runs next to
	// other lanes' containers, and the isolation question is which database a statement lands in.
	began := time.Now()
	if _, err := store.MigrateTeam(ctx, dsnOne, dbOne); err != nil {
		return fmt.Errorf("migrate %s: %w", dbOne, err)
	}
	t.Logf("migrated %s in %s", dbOne, time.Since(began).Round(time.Second))
	var tables []string
	rows, err := x.root.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES
WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`, dbOne)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		if _, err := x.root.ExecContext(ctx, "CREATE TABLE `"+dbTwo+"`.`"+tb+"` LIKE `"+dbOne+"`.`"+tb+"`"); err != nil {
			return fmt.Errorf("copy %s: %w", tb, err)
		}
	}
	t.Logf("team B: %d tables (structure only)", len(tables))
	for db, name := range map[string]string{dbOne: "alpha", dbTwo: "bravo"} {
		for _, q := range []string{
			"DELETE FROM `" + db + "`.admins",
			"INSERT INTO `" + db + "`.admins (username, password_hash, is_super) VALUES ('" + name + "', 'x', 1)",
		} {
			if _, err := x.root.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("seed admins %s: %w", db, err)
			}
		}
	}

	// Control plane.
	if x.ctl, err = platformstore.Open(ctx, rootDSN(dbControl)); err != nil {
		return err
	}
	if err := x.ctl.EnsureTeamOne(ctx, dbOne, []platformstore.TeamOneAdmin{{Username: "alpha", PasswordHash: "h", Super: true}}); err != nil {
		return fmt.Errorf("team one: %w", err)
	}
	if err := x.ctl.DB().GetContext(ctx, &x.uidA, `SELECT id FROM platform_user WHERE handle = 'alpha'`); err != nil {
		return fmt.Errorf("alpha user: %w", err)
	}
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES (%d, 'bravo@x.test', 'bravo', 'h', 'active')`, uidB),
		fmt.Sprintf(`INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator) VALUES (%d, 'op@x.test', 'oper', 'h', 'active', 1)`, uidOp),
		`INSERT INTO team (id, slug, name, db_name, profile, status) VALUES (2, '` + slugB + `', 'bravo', '` + dbTwo + `', 'standard', 'active')`,
		fmt.Sprintf(`INSERT INTO team_member (team_id, user_id, role, status) VALUES (2, %d, 'owner', 'active')`, uidB),
		fmt.Sprintf(`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('%s', %d, 1, 'team'), ('%s', %d, 2, 'team'), ('%s', %d, NULL, 'console')`,
			sidA, x.uidA, sidB, uidB, sidOp, uidOp),
	} {
		if _, err := x.ctl.DB().ExecContext(ctx, q); err != nil {
			return fmt.Errorf("control seed: %w (%s)", err, q)
		}
	}
	// Every feature on for B, so every method reaches B's handler (team #1 is profile all).
	for _, info := range feature.Catalog() {
		if err := x.ctl.SetFeature(ctx, 2, string(info.Key), true, uidOp); err != nil {
			return fmt.Errorf("feature %s: %w", info.Key, err)
		}
	}
	if x.teamB, err = x.ctl.Directory().ByID(ctx, 2); err != nil {
		return fmt.Errorf("team B: %w", err)
	}

	// Store on the shared tenant pool. Strict off: a statement without a team is counted
	// (MissingCtx) instead of refused, so a context dropped anywhere shows up as a number.
	// Two connections: A and B calls share and swap them.
	if x.st, err = store.NewForTest(context.Background(), store.Config{ // the store closes its pool when this ctx ends
		DSN: dsnOne, MaxOpenConnections: 2, MaxIdleConnections: 2,
		Tenant: tenantdb.Options{Team1: tenant.TeamOne(dbOne), Strict: false, DBPrefix: dbPrefix},
	}); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if x.b, err = tenantbind.New(tenant.TeamOne(dbOne)); err != nil {
		return err
	}
	x.b.UsePlatform(x.ctl, platformstore.Config{BaseDomain: baseDomain, DBPrefix: dbPrefix, Team1DB: dbOne, StorefrontTeamID: 1})
	if x.auth, err = authsrv.New(&authsrv.Config{
		JWTSecret:              strings.Repeat("k7", 24),
		MasterPassword:         "tenanttest-master-password",
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "1h",
	}, x.st.Admin()); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	x.auth.SetEdge(x.b)
	if x.adm, err = admin.New(x.st, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		entity.LabelAddress{}, "", "", jpk.Taxpayer{}, decimal.Zero); err != nil {
		return fmt.Errorf("admin: %w", err)
	}
	x.plat = platform.New(x.ctl)
	x.plat.SetInvalidator(x.b.InvalidateTeam)

	mint := func(c jwt.TeamClaims) (string, error) {
		return jwt.NewTeamTokenAt(x.auth.JwtAuth, time.Hour, c, time.Now())
	}
	if x.tokA, err = mint(jwt.TeamClaims{Handle: "alpha", UserID: x.uidA, TeamID: 1, SessionID: sidA, Audience: jwt.AudTeam, Super: true}); err != nil {
		return err
	}
	if x.tokB, err = mint(jwt.TeamClaims{Handle: "bravo", UserID: uidB, TeamID: 2, SessionID: sidB, Audience: jwt.AudTeam, Super: true}); err != nil {
		return err
	}
	// B's real user and session, but a token naming team #1 (as if minted for a team B is not in).
	if x.tokBAsOne, err = mint(jwt.TeamClaims{Handle: "bravo", UserID: uidB, TeamID: 1, SessionID: sidB, Audience: jwt.AudTeam, Super: true}); err != nil {
		return err
	}
	if x.tokOp, err = mint(jwt.TeamClaims{Handle: "oper", UserID: uidOp, SessionID: sidOp, Audience: jwt.AudConsole}); err != nil {
		return err
	}
	return nil
}

// --- calling through the production chain ---------------------------------------------------

// caller is who presents a call: its token, the X-Team it names, its browser origin.
type caller struct {
	token, xteam, origin string
}

func (x *harness) asA() caller {
	return caller{token: x.tokA, xteam: tenant.TeamOneSlug, origin: "https://" + tenant.TeamOneSlug + "." + baseDomain}
}
func (x *harness) asB() caller {
	return caller{token: x.tokB, xteam: slugB, origin: "https://" + slugB + "." + baseDomain}
}

// outcome of one call.
type outcome struct {
	reached bool        // the handler (or the spy in its place) ran
	team    tenant.Team // the team bound in ctx when it ran
	resp    any
	err     error
	panic   string
}

type method struct {
	name, full string
	read       bool
	handler    grpc.MethodHandler
}

func adminMethods() []method {
	var out []method
	for _, m := range pb_admin.AdminService_ServiceDesc.Methods {
		full := "/" + pb_admin.AdminService_ServiceDesc.ServiceName + "/" + m.MethodName
		lvl, _ := rbac.Access(full)
		out = append(out, method{name: m.MethodName, full: full, read: lvl == entity.AccessRead, handler: m.Handler})
	}
	return out
}

// fill sets request fields that look like ids to 1 (top level only), so a get / list by id
// reaches the database instead of stopping at validation.
func fill(msg proto.Message) {
	r := msg.ProtoReflect()
	fs := r.Descriptor().Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.IsList() || f.IsMap() {
			continue
		}
		n := string(f.Name())
		if n != "id" && !strings.HasSuffix(n, "_id") {
			continue
		}
		switch f.Kind() {
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			r.Set(f, protoreflect.ValueOfInt32(1))
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			r.Set(f, protoreflect.ValueOfInt64(1))
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
			r.Set(f, protoreflect.ValueOfUint32(1))
		case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			r.Set(f, protoreflect.ValueOfUint64(1))
		}
	}
}

// invoke runs one AdminService (or PlatformService) method through binder → auth chain → the
// generated handler. spy=true replaces the business handler with a recorder (nothing is written).
// body, when not empty, is the request as protojson; otherwise ids are filled with 1.
func (x *harness) invoke(m method, srv any, c caller, spy bool, body string) outcome {
	var o outcome
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
		strings.ToLower(authsrv.AuthMetadataKey), "Bearer "+c.token,
		"x-team", c.xteam,
		"grpcgateway-origin", c.origin,
	))
	dec := func(in any) error {
		msg := in.(proto.Message)
		if body != "" {
			return protojson.Unmarshal([]byte(body), msg)
		}
		fill(msg)
		return nil
	}
	authI := x.auth.UnaryAdminAuthInterceptor()
	bindI := x.b.Unary()
	chain := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return bindI(ctx, req, info, func(ctx context.Context, req any) (any, error) {
			return authI(ctx, req, info, func(ctx context.Context, req any) (any, error) {
				o.reached = true
				o.team, _ = tenant.From(ctx)
				if spy {
					return nil, nil
				}
				return handler(ctx, req)
			})
		})
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				o.panic = fmt.Sprint(r)
			}
		}()
		o.resp, o.err = m.handler(srv, ctx, dec, chain)
	}()
	return o
}

func (x *harness) adminCall(name string, c caller, body string) outcome {
	for _, m := range adminMethods() {
		if m.name == name {
			return x.invoke(m, x.adm, c, false, body)
		}
	}
	return outcome{err: errors.New("no method " + name)}
}

func respJSON(v any) string {
	m, ok := v.(proto.Message)
	if !ok || m == nil {
		return ""
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}
