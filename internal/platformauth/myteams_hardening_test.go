package platformauth

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
)

const (
	logoOwnerSID  = "logoowner00000000000000000000000"
	logoMemberSID = "logomember0000000000000000000000"
)

func seedLogoService(t *testing.T, f *flow) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES
 (201, 'logo-owner@x.test', 'logo-owner', '!x', 'active'),
 (202, 'logo-member@x.test', 'logo-member', '!x', 'active')`,
		`INSERT INTO team (id, slug, name, db_name, status) VALUES
 (20, 'logo-alpha', 'Alpha', 'lk_t_logoalpha', 'active'),
 (21, 'logo-beta', 'Beta', 'lk_t_logobeta', 'active')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES
 (20, 201, 'owner', 'active'), (20, 202, 'member', 'active')`,
		`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES
 ('` + logoOwnerSID + `', 201, 20, 'team'), ('` + logoMemberSID + `', 202, 20, 'team')`,
		`INSERT INTO team_logo (team_id, png, sha256) VALUES (20, 'old', REPEAT('a', 64))`,
	} {
		if _, err := f.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func logoToken(t *testing.T, f *flow, userID int64, handle, sid string) string {
	t.Helper()
	tok, err := jwt.NewTeamTokenAt(f.svc.d.JWT, time.Hour, jwt.TeamClaims{
		Handle: handle, UserID: userID, TeamID: 20, SessionID: sid, Audience: jwt.AudTeam,
	}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestSetTeamLogoRechecksAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
		call func(t *testing.T, f *flow) error
	}{
		{
			name: "non-owner", code: codes.PermissionDenied,
			call: func(t *testing.T, f *flow) error {
				return logoSetError(f, ctxFrom("10.8.0.1", "logo-alpha"), logoToken(t, f, 202, "logo-member", logoMemberSID))
			},
		},
		{
			name: "wrong team host", code: codes.PermissionDenied,
			call: func(t *testing.T, f *flow) error {
				return logoSetError(f, ctxFrom("10.8.0.2", "logo-beta"), logoToken(t, f, 201, "logo-owner", logoOwnerSID))
			},
		},
		{
			name: "revoked session", code: codes.Unauthenticated,
			call: func(t *testing.T, f *flow) error {
				if _, err := f.st.DB().Exec(`UPDATE user_session SET revoked_at = ? WHERE sid = ?`, f.clock, logoOwnerSID); err != nil {
					t.Fatal(err)
				}
				return logoSetError(f, ctxFrom("10.8.0.3", "logo-alpha"), logoToken(t, f, 201, "logo-owner", logoOwnerSID))
			},
		},
		{
			name: "suspended team", code: codes.FailedPrecondition,
			call: func(t *testing.T, f *flow) error {
				if _, err := f.st.DB().Exec(`UPDATE team SET status = 'suspended' WHERE id = 20`); err != nil {
					t.Fatal(err)
				}
				f.st.Directory().Invalidate(20)
				return logoSetError(f, ctxFrom("10.8.0.4", "logo-alpha"), logoToken(t, f, 201, "logo-owner", logoOwnerSID))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlow(t)
			seedLogoService(t, f)
			wantCode(t, tc.call(t, f), tc.code, tc.name)
			var png string
			if err := f.st.DB().Get(&png, `SELECT png FROM team_logo WHERE team_id = 20`); err != nil || png != "old" {
				t.Fatalf("refused logo write changed bytes to %q: %v", png, err)
			}
		})
	}
}

func logoSetError(f *flow, ctx context.Context, token string) error {
	_, err := f.svc.SetTeamLogo(ctx, token, &auth.SetTeamLogoRequest{})
	return err
}
