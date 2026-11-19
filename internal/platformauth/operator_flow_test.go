package platformauth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
)

// Operator entry end to end: an "op." code from the console becomes a team token with the op claim
// on the team host; a member's code path is untouched, the code is single use, a non-operator's
// code is refused.
func TestOperatorEntryExchange(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator) VALUES
 (501, 'op@x.io', 'opx', 'h', 'active', 1), (502, 'ann@x.io', 'annx', 'h', 'active', 0)`,
		`INSERT INTO team (id, slug, name, db_name, status) VALUES (50, 'opteam', 'Op Team', 'lkb_t_opteam01', 'active')`,
	} {
		if _, err := f.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	issue := func(uid int64) string {
		t.Helper()
		code, hash, err := NewOperatorCode()
		if err != nil || !strings.HasPrefix(code, OperatorCodePrefix) {
			t.Fatalf("code %q %v", code, err)
		}
		root := fmt.Sprintf("sroot%027d", uid)
		if _, err := f.st.DB().Exec(`INSERT IGNORE INTO user_session (sid, user_id, team_id, aud) VALUES (?, ?, NULL, 'console')`, root, uid); err != nil {
			t.Fatal(err)
		}
		if err := f.st.IssueHandoff(ctx, uid, root, platformstore.NewToken{TeamID: 50, TokenHash: hash, ExpiresAt: f.clock.Add(10 * time.Minute)}, f.clock); err != nil {
			t.Fatal(err)
		}
		return code
	}
	code := issue(501)
	ex, err := f.svc.ExchangeHandoff(ctxFrom("10.0.9.1", "opteam"), &auth.ExchangeHandoffRequest{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	c, err := jwt.VerifyTeamToken(f.svc.d.JWT, ex.GetToken(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Operator || !c.Super || c.Handle != OperatorActor || c.UserID != 501 || c.TeamID != 50 || c.Audience != jwt.AudTeam || c.SessionID == "" {
		t.Fatalf("claims %+v", c)
	}
	_, err = f.svc.ExchangeHandoff(ctxFrom("10.0.9.1", "opteam"), &auth.ExchangeHandoffRequest{Code: code})
	wantCode(t, err, codes.InvalidArgument, "operator code reused")
	_, err = f.svc.ExchangeHandoff(ctxFrom("10.0.9.1", "opteam"), &auth.ExchangeHandoffRequest{Code: issue(502)})
	wantCode(t, err, codes.InvalidArgument, "non-operator's op code")
	var n int
	if err := f.st.DB().Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE action = 'operator_signin' AND team_id = 50`); err != nil || n != 1 {
		t.Fatalf("operator_signin audit %d %v", n, err)
	}
}
