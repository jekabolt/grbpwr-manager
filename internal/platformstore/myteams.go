package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

// ErrTryAgain: the transaction lost a lock race (InnoDB chose it as a deadlock victim or its lock
// wait timed out); nothing was written and the same request may simply be sent again.
var ErrTryAgain = errors.New("platformstore: lock conflict, try again")

// lockConflict maps InnoDB's deadlock (1213) and lock-wait timeout (1205) to ErrTryAgain.
func lockConflict(err error) error {
	var me *mysql.MySQLError
	if errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205) {
		return fmt.Errorf("%w: %w", ErrTryAgain, err)
	}
	return err
}

// CreateTeamParams is a `new team` from a root session (teams O §7, §12). The caller checked the
// root session, validated the name and the address, and processed the logo.
type CreateTeamParams struct {
	UserID     int64
	Name, Slug string
	Now        time.Time
	// Review is the approval switch: on → the team waits in review and every active operator gets a
	// team_request letter; off → it goes straight to provisioning.
	Review Review
	// TermsVersion is the current terms version; TermsAccepted is the request's terms_accepted. While
	// the account has not accepted TermsVersion the request must carry it (ErrTermsRequired), and it
	// is stored then.
	TermsVersion  string
	TermsAccepted bool
	// Logo is the new team's logo, already processed (nil = none).
	Logo *TeamLogo
}

// CreatedTeam is a committed CreateTeam.
type CreatedTeam struct {
	TeamID             int64
	Slug, Name, Status string
}

// CreateTeam creates one more team owned by an active account, in one transaction: the address
// first, as SignUp does (an expired pending team that holds it goes, with its logo; a held address
// is ErrSlugTaken whoever holds it), then the account FOR UPDATE (not active or gone:
// ErrTokenInvalid; terms not accepted and not carried: ErrTermsRequired), then the new team row
// (review while approval is on, else provisioning — the verified sign-up's transition), the owner
// membership, the logo, the accepted terms, and under review the operators' team_request letters.
// No letter goes to the person. There is no cap on own teams. A lost lock race is ErrTryAgain.
//
// It locks no team row other than the expired pending one it clears and the row it inserts, and no
// session, invite or token: a DeleteTeam of another team of the same person never waits for it and
// it never waits for one (lock order at DeleteTeam).
func (s *Store) CreateTeam(ctx context.Context, p CreateTeamParams) (CreatedTeam, error) {
	p.Now = p.Now.UTC()
	if p.UserID <= 0 || p.Name == "" || p.Slug == "" {
		return CreatedTeam{}, errors.New("platformstore: create team needs an account, a name and an address")
	}
	var res CreatedTeam
	var swept []int64
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res = CreatedTeam{}
		var err error
		swept, err = deletePendingTeamsTx(ctx, tx, `slug = ? AND status = 'pending' AND pending_expires_at < ?`, p.Slug, p.Now)
		if err != nil {
			return fmt.Errorf("platformstore: drop expired pending team: %w", err)
		}
		var held int
		if err := tx.GetContext(ctx, &held, `SELECT
 (SELECT COUNT(*) FROM team WHERE slug = ?) + (SELECT COUNT(*) FROM team_slug_history WHERE slug = ? AND redirect_until > ?)`,
			p.Slug, p.Slug, p.Now); err != nil {
			return fmt.Errorf("platformstore: slug held: %w", err)
		}
		if held > 0 {
			return ErrSlugTaken
		}
		u, err := lockUserByID(ctx, tx, p.UserID)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if u.Status != UserActive {
			return ErrTokenInvalid
		}
		var accepted sql.NullString
		if err := tx.GetContext(ctx, &accepted, `SELECT terms_version FROM platform_user WHERE id = ?`, u.ID); err != nil {
			return fmt.Errorf("platformstore: read terms: %w", err)
		}
		needTerms := p.TermsVersion != "" && accepted.String != p.TermsVersion
		if needTerms && !p.TermsAccepted {
			return ErrTermsRequired
		}
		next := "provisioning"
		if p.Review.On {
			next = "review"
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO team (slug, name, status, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
			p.Slug, p.Name, next, u.ID, p.Now)
		if err != nil {
			if dupKey(err, "uq_team_slug") {
				return ErrSlugTaken
			}
			return fmt.Errorf("platformstore: insert team: %w", err)
		}
		tid, err := r.LastInsertId()
		if err != nil {
			return fmt.Errorf("platformstore: team id: %w", err)
		}
		if err := putTeamLogoTx(ctx, tx, tid, p.Logo); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO team_member (team_id, user_id, role, status, created_at)
VALUES (?, ?, 'owner', 'active', ?)`, tid, u.ID, p.Now); err != nil {
			return fmt.Errorf("platformstore: owner membership: %w", err)
		}
		if needTerms {
			termsVersion, termsAt := termsColumns(p.TermsVersion, p.Now)
			if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET terms_version = ?, terms_accepted_at = ? WHERE id = ?`,
				termsVersion, termsAt, u.ID); err != nil {
				return fmt.Errorf("platformstore: store terms: %w", err)
			}
		}
		if p.Review.On {
			var operators []string
			if err := tx.SelectContext(ctx, &operators, `SELECT email FROM platform_user
WHERE is_operator = 1 AND status = 'active' AND email IS NOT NULL ORDER BY id`); err != nil {
				return fmt.Errorf("platformstore: operators: %w", err)
			}
			for _, to := range operators {
				if err := insertTeamLetterTx(ctx, tx, to, "team_request", teamLetterPayload{
					Link: p.Review.ConsoleLink, TeamName: p.Name, Slug: p.Slug, Email: u.Email.String,
				}, p.Now); err != nil {
					return err
				}
			}
		}
		res = CreatedTeam{TeamID: tid, Slug: p.Slug, Name: p.Name, Status: next}
		return nil
	})
	for _, id := range swept {
		s.dir.Invalidate(id)
	}
	if err != nil {
		return CreatedTeam{}, lockConflict(err)
	}
	s.dir.Invalidate(res.TeamID)
	return res, nil
}

// MyTeams is what the root list needs about an account.
type MyTeams struct {
	Email  string
	Status string
	// IsOperator: an operator's account.
	IsOperator bool
	// TermsVersion is the terms version the account accepted ("" = none).
	TermsVersion string
	// Teams are MemberTeams: review, provisioning, active and suspended teams with an active
	// membership, each with its role and logo; deleted, pending and removed memberships are absent.
	Teams []MemberTeam
}

// ListMyTeams reads the account and its teams for the root list. ErrUserNotFound when the account
// is gone.
func (s *Store) ListMyTeams(ctx context.Context, userID int64) (MyTeams, error) {
	var row struct {
		Email        sql.NullString `db:"email"`
		Status       string         `db:"status"`
		IsOperator   bool           `db:"is_operator"`
		TermsVersion sql.NullString `db:"terms_version"`
	}
	if err := s.db.GetContext(ctx, &row, `SELECT email, status, is_operator, terms_version FROM platform_user WHERE id = ?`,
		userID); err != nil {
		if isNoRows(err) {
			return MyTeams{}, ErrUserNotFound
		}
		return MyTeams{}, fmt.Errorf("platformstore: read account: %w", err)
	}
	teams, err := s.MemberTeams(ctx, userID)
	if err != nil {
		return MyTeams{}, err
	}
	return MyTeams{Email: row.Email.String, Status: row.Status, IsOperator: row.IsOperator,
		TermsVersion: row.TermsVersion.String, Teams: teams}, nil
}
