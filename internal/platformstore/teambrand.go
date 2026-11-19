package platformstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
)

// ErrOwnerTarget: a member-rights change named an owner (an owner has full access, no section rights).
var ErrOwnerTarget = errors.New("platformstore: target is an owner")

// TeamBrandAddressLines is how many legal address lines a team's brand holds.
const TeamBrandAddressLines = 3

// Team #1's brand as today's papers print it (teams W §1, A3): written once by EnsureTeamOneProfile.
const (
	teamOneBrandName    = "GRBPWR"
	teamOneLegalEntity  = "GRBPWR LIMITED"
	teamOneAddress1     = "167-169 GREAT PORTLAND STREET"
	teamOneAddress2     = "5TH FLOOR, LONDON, W1W 5PF"
	teamOneContactEmail = "customercare@grbpwr.com"
	teamOneWebsite      = "grbpwr.com"
)

// TeamBrand is a team's brand data for its papers (teams W §1), already normalised by the server:
// LegalAddress holds up to TeamBrandAddressLines lines, none blank. Empty values print nothing.
type TeamBrand struct {
	BrandName    string
	LegalEntity  string
	LegalAddress []string
	ContactEmail string
	Website      string
}

// Empty is a brand with no value at all: a team's creation writes no row for it.
func (b TeamBrand) Empty() bool {
	return b.BrandName == "" && b.LegalEntity == "" && len(b.LegalAddress) == 0 && b.ContactEmail == "" && b.Website == ""
}

type teamBrandRow struct {
	BrandName    string `db:"brand_name"`
	LegalEntity  string `db:"legal_entity"`
	Address1     string `db:"legal_address_1"`
	Address2     string `db:"legal_address_2"`
	Address3     string `db:"legal_address_3"`
	ContactEmail string `db:"contact_email"`
	Website      string `db:"website"`
}

func (r teamBrandRow) brand() TeamBrand {
	b := TeamBrand{BrandName: r.BrandName, LegalEntity: r.LegalEntity, ContactEmail: r.ContactEmail, Website: r.Website}
	for _, l := range []string{r.Address1, r.Address2, r.Address3} {
		if l != "" {
			b.LegalAddress = append(b.LegalAddress, l)
		}
	}
	return b
}

func brandRow(b TeamBrand) (teamBrandRow, error) {
	if len(b.LegalAddress) > TeamBrandAddressLines {
		return teamBrandRow{}, fmt.Errorf("platformstore: team brand: %d address lines", len(b.LegalAddress))
	}
	var lines [TeamBrandAddressLines]string
	copy(lines[:], b.LegalAddress)
	return teamBrandRow{BrandName: b.BrandName, LegalEntity: b.LegalEntity, Address1: lines[0], Address2: lines[1],
		Address3: lines[2], ContactEmail: b.ContactEmail, Website: b.Website}, nil
}

const teamBrandColumns = `brand_name, legal_entity, legal_address_1, legal_address_2, legal_address_3, contact_email, website`

// teamBrandUpsert writes a team's whole brand: a new row, or every field of the existing one.
const teamBrandUpsert = `INSERT INTO team_brand
  (team_id, ` + teamBrandColumns + `, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(3)) AS nv
ON DUPLICATE KEY UPDATE brand_name = nv.brand_name, legal_entity = nv.legal_entity,
  legal_address_1 = nv.legal_address_1, legal_address_2 = nv.legal_address_2, legal_address_3 = nv.legal_address_3,
  contact_email = nv.contact_email, website = nv.website, updated_by = nv.updated_by, updated_at = nv.updated_at`

func execBrandUpsert(ctx context.Context, tx *sqlx.Tx, teamID int64, r teamBrandRow, by int64) error {
	if _, err := tx.ExecContext(ctx, teamBrandUpsert, teamID, r.BrandName, r.LegalEntity, r.Address1, r.Address2, r.Address3,
		r.ContactEmail, r.Website, by); err != nil {
		return fmt.Errorf("platformstore: put team brand: %w", err)
	}
	return nil
}

// putTeamBrandTx writes the brand of a team the caller's transaction has just inserted (sign-up,
// create team): lock order 1b, right after putTeamLogoTx. by is the creating person (updated_by).
// nil or an empty brand writes no row.
func putTeamBrandTx(ctx context.Context, tx *sqlx.Tx, teamID int64, b *TeamBrand, by int64) error {
	if b == nil || b.Empty() {
		return nil
	}
	r, err := brandRow(*b)
	if err != nil {
		return err
	}
	return execBrandUpsert(ctx, tx, teamID, r, by)
}

// TeamBrand reads a team's brand. ok=false when the team has none (a new team: empty values).
func (s *Store) TeamBrand(ctx context.Context, teamID int64) (TeamBrand, bool, error) {
	var rows []teamBrandRow
	if err := s.db.SelectContext(ctx, &rows, `SELECT `+teamBrandColumns+` FROM team_brand WHERE team_id = ?`, teamID); err != nil {
		return TeamBrand{}, false, fmt.Errorf("platformstore: team brand: %w", err)
	}
	if len(rows) == 0 {
		return TeamBrand{}, false, nil
	}
	return rows[0].brand(), true, nil
}

// SetTeamBrandParams is one full replace of a team's brand by an owner or an entered operator.
type SetTeamBrandParams struct {
	TeamID int64
	Actor  Actor
	Brand  TeamBrand
}

// SetTeamBrand replaces a writable team's brand (all fields; a new row for a team without one) and
// returns the names of the fields that changed. One transaction locks and re-checks in the global
// order, as SetTeamLogo: the team row (FOR UPDATE), its brand row (1b), then the actor (checkActorTx).
// A suspension or the actor's demotion either commits first and refuses the write or waits for it.
// ErrTeamReadOnly is a suspended team, ErrWrongTeamStatus any other unusable status, ErrNotOwner an
// actor who is no longer an active owner or operator.
func (s *Store) SetTeamBrand(ctx context.Context, p SetTeamBrandParams) ([]string, error) {
	next, err := brandRow(p.Brand)
	if err != nil {
		return nil, err
	}
	var changed []string
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		changed = nil
		var st string
		if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR UPDATE`, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		if st != "active" {
			return ErrWrongTeamStatus
		}
		var cur []teamBrandRow
		if err := tx.SelectContext(ctx, &cur, `SELECT `+teamBrandColumns+` FROM team_brand WHERE team_id = ? FOR UPDATE`,
			p.TeamID); err != nil {
			return fmt.Errorf("platformstore: lock team brand: %w", err)
		}
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		var prev teamBrandRow
		if len(cur) > 0 {
			prev = cur[0]
		}
		changed = brandChanges(prev, next)
		return execBrandUpsert(ctx, tx, p.TeamID, next, p.Actor.UserID)
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// brandChanges names the fields that differ (the audit carries names only, never values).
func brandChanges(a, b teamBrandRow) []string {
	var out []string
	if a.BrandName != b.BrandName {
		out = append(out, "brand_name")
	}
	if a.LegalEntity != b.LegalEntity {
		out = append(out, "legal_entity")
	}
	if a.Address1 != b.Address1 || a.Address2 != b.Address2 || a.Address3 != b.Address3 {
		out = append(out, "legal_address")
	}
	if a.ContactEmail != b.ContactEmail {
		out = append(out, "contact_email")
	}
	if a.Website != b.Website {
		out = append(out, "website")
	}
	return out
}

// seedTeamOneBrandTx writes team #1's brand inside EnsureTeamOneProfile's transaction, which holds
// the team row. A no-op on an existing row: a boot never overwrites an owner's edit. Not INSERT
// IGNORE, which would hide every other error too.
func seedTeamOneBrandTx(ctx context.Context, tx execer) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_brand
  (team_id, `+teamBrandColumns+`, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, '', ?, ?, NULL, UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE team_id = team_id`,
		tenant.TeamOneID, teamOneBrandName, teamOneLegalEntity, teamOneAddress1, teamOneAddress2,
		teamOneContactEmail, teamOneWebsite); err != nil {
		return fmt.Errorf("platformstore: ensure team #1: brand: %w", err)
	}
	return nil
}

// deleteTeamBrandTx removes a team's brand inside the caller's transaction, which already holds the
// team row. Every hard delete of a team row calls it beside deleteTeamLogoTx (DeleteTeam is soft and
// keeps it for RestoreTeam; removing a logo never touches it).
func deleteTeamBrandTx(ctx context.Context, tx *sqlx.Tx, teamID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM team_brand WHERE team_id = ?`, teamID); err != nil {
		return fmt.Errorf("platformstore: delete team brand: %w", err)
	}
	return nil
}

// RevokeMemberSessions ends a member's sessions in the team after their section rights changed (the
// caller already wrote the team database's admins row), so the next call is refused and re-entry
// mints the new claims. One transaction in the global order: the team row (FOR UPDATE), the actor
// (checkActorTx), the target's membership, their sessions. ErrTeamReadOnly is a suspended team,
// ErrWrongTeamStatus any other unusable status, ErrNotOwner an actor who may no longer change people,
// ErrNotMember a target who is no longer an active member, ErrOwnerTarget a target who is an owner
// now. Idempotent: a retry revokes what is live by then.
func (s *Store) RevokeMemberSessions(ctx context.Context, p MemberChangeParams) (MemberChange, error) {
	var out MemberChange
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		out = MemberChange{}
		var st string
		if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR UPDATE`, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		if st != "active" {
			return ErrWrongTeamStatus
		}
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		var m struct {
			Role   string `db:"role"`
			Handle string `db:"handle"`
		}
		if err := tx.GetContext(ctx, &m, `SELECT m.role, u.handle FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND m.user_id = ? AND m.status = 'active' FOR UPDATE OF m`, p.TeamID, p.UserID); err != nil {
			if isNoRows(err) {
				return ErrNotMember
			}
			return fmt.Errorf("platformstore: lock member: %w", err)
		}
		if m.Role != "member" {
			return ErrOwnerTarget
		}
		revoked, err := revokeTeamSessionsTx(ctx, tx, p.UserID, p.TeamID, p.Now)
		if err != nil {
			return err
		}
		out = MemberChange{Handle: m.Handle, RevokedSessions: revoked}
		return nil
	})
	return out, err
}
