package platformstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Shops of teams (X18). Lock order: these tables stand after the team row (never before it) and are
// taken in the order team_shop → shop_host → team_stripe; no shop transaction locks anything of
// 2–6 (accounts, memberships, sessions, invites, tokens).

var (
	// ErrShopHostTaken: the host is attached to another team.
	ErrShopHostTaken = errors.New("platformstore: shop host taken")
	// ErrShopHostNotFound: the team has no such host.
	ErrShopHostNotFound = errors.New("platformstore: shop host not found")
	// ErrShopHostManaged: the platform address cannot be removed by hand.
	ErrShopHostManaged = errors.New("platformstore: shop host managed")
	// ErrShopHostNotLive: only a live host can be the main address.
	ErrShopHostNotLive = errors.New("platformstore: shop host not live")
	// ErrShopHostLimit: the team has as many custom hosts as it may.
	ErrShopHostLimit = errors.New("platformstore: shop host limit")
	// ErrShopHostRedirect: the host is (or would replace) one of the team's own hosts of the other
	// sort: a "www." redirect cannot be attached as a shop address, nor a shop address as a redirect.
	ErrShopHostRedirect = errors.New("platformstore: shop host redirect")
)

// ShopHostMax bounds the custom hosts of one team (a "www." redirect of one of them is not counted:
// it comes and goes with its domain).
const ShopHostMax = 10

// Shop host kinds and statuses (shop_host.kind / status).
const (
	ShopHostSubdomain = "subdomain"
	ShopHostCustom    = "custom"
	ShopHostLegacy    = "legacy"

	ShopHostRecordsNeeded = "records_needed"
	ShopHostVerifying     = "verifying"
	ShopHostLive          = "live"
	ShopHostFailed        = "failed"
)

// ShopRecord is one DNS record a custom host needs (stored as JSON on its row).
type ShopRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Found bool   `json:"found"`
}

// ShopHost is one host of a team's shop.
type ShopHost struct {
	Host       string
	TeamID     int64
	Kind       string
	Managed    bool
	Primary    bool
	Status     string
	RedirectTo string
	Records    []ShopRecord
	CreatedAt  time.Time
	VerifiedAt *time.Time
	CheckedAt  *time.Time
}

type shopHostRow struct {
	Host       string     `db:"host"`
	TeamID     int64      `db:"team_id"`
	Kind       string     `db:"kind"`
	Managed    bool       `db:"managed"`
	Primary    bool       `db:"is_primary"`
	Status     string     `db:"status"`
	RedirectTo string     `db:"redirect_to"`
	Records    []byte     `db:"records"`
	CreatedAt  time.Time  `db:"created_at"`
	VerifiedAt *time.Time `db:"verified_at"`
	CheckedAt  *time.Time `db:"checked_at"`
}

const shopHostColumns = `host, team_id, kind, managed, is_primary, status, redirect_to, records, created_at, verified_at, checked_at`

func (r shopHostRow) host() ShopHost {
	h := ShopHost{Host: r.Host, TeamID: r.TeamID, Kind: r.Kind, Managed: r.Managed, Primary: r.Primary, Status: r.Status,
		RedirectTo: r.RedirectTo, CreatedAt: r.CreatedAt, VerifiedAt: r.VerifiedAt, CheckedAt: r.CheckedAt}
	if len(r.Records) > 0 {
		_ = json.Unmarshal(r.Records, &h.Records) // a row we wrote; an unreadable one shows no records
	}
	return h
}

func recordsJSON(rs []ShopRecord) ([]byte, error) {
	if rs == nil {
		rs = []ShopRecord{}
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return nil, fmt.Errorf("platformstore: shop records: %w", err)
	}
	return b, nil
}

// TeamShop is a team's shop switch, the operator's close and its mail sender. A team without a row
// has the zero value (off). EnabledAt is when the shop last opened: the team switching it on, or the
// operator reopening a switched-on shop.
type TeamShop struct {
	TeamID           int64      `db:"team_id"`
	Enabled          bool       `db:"enabled"`
	EnabledAt        *time.Time `db:"enabled_at"`
	ClosedByOperator bool       `db:"closed_by_operator"`
	MailFromName     string     `db:"mail_from_name"`
	MailReplyTo      string     `db:"mail_reply_to"`
}

// TeamStripe is a team's Stripe setup as stored (X18-L3 writes it). The sealed keys stay sealed here.
type TeamStripe struct {
	TeamID              int64      `db:"team_id"`
	Kind                string     `db:"kind"`
	AccountID           *string    `db:"account_id"`
	AccountName         string     `db:"account_name"`
	Mode                string     `db:"mode"`
	ChargesEnabled      bool       `db:"charges_enabled"`
	PayoutsEnabled      bool       `db:"payouts_enabled"`
	DetailsSubmitted    bool       `db:"details_submitted"`
	FeeBps              int        `db:"fee_bps"`
	PublishableKey      string     `db:"publishable_key"`
	SecretKeySealed     []byte     `db:"secret_key_sealed"`
	WebhookSecretSealed []byte     `db:"webhook_secret_sealed"`
	KeyLast4            string     `db:"key_last4"`
	ConnectedAt         *time.Time `db:"connected_at"`
	DisconnectedAt      *time.Time `db:"disconnected_at"`
}

// ShopHostTeam is the team a live host answers for; ok=false when no team has it live. Only live
// hosts route a storefront request: a custom host is live only after its team proved it owns the
// domain (ShopHostProof), a platform address and a legacy host are live by construction.
func (s *Store) ShopHostTeam(ctx context.Context, host string) (int64, bool, error) {
	var id int64
	if err := s.db.GetContext(ctx, &id, `SELECT team_id FROM shop_host WHERE host = ? AND status = 'live'`, host); err != nil {
		if isNoRows(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("platformstore: shop host team: %w", err)
	}
	return id, true, nil
}

// ShopHostPendingTTL is how long another team's custom host that never went live holds its name;
// after it the next team may attach the host (a pending attach cannot squat a domain).
const ShopHostPendingTTL = 7 * 24 * time.Hour

// stalePending: a custom host that never went live (never verified) within ShopHostPendingTTL. A
// host that went live once is its team's until the team removes it or is wiped, whatever its status
// now: a lapsed check never frees it for another team.
func stalePending(cur shopHostRow, now time.Time) bool {
	return cur.Kind == ShopHostCustom && cur.VerifiedAt == nil && cur.Status != ShopHostLive &&
		now.Sub(cur.CreatedAt) >= ShopHostPendingTTL
}

// ShopHostFree reports whether team teamID may attach host: no row, the team's own custom row, or
// another team's custom row that never went live (never verified) within ShopHostPendingTTL. Else ErrShopHostTaken
// (ErrShopHostManaged for the team's own platform or legacy host).
func (s *Store) ShopHostFree(ctx context.Context, teamID int64, host string, now time.Time) error {
	var cur shopHostRow
	err := s.db.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ?`, host)
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return fmt.Errorf("platformstore: shop host owner: %w", err)
	case cur.TeamID == teamID && cur.Kind != ShopHostCustom:
		return ErrShopHostManaged
	case cur.TeamID == teamID, stalePending(cur, now.UTC()):
		return nil
	}
	return ErrShopHostTaken
}

// ShopHosts lists a team's hosts: the platform address first, then by attach time.
func (s *Store) ShopHosts(ctx context.Context, teamID int64) ([]ShopHost, error) {
	var rows []shopHostRow
	if err := s.db.SelectContext(ctx, &rows, `SELECT `+shopHostColumns+` FROM shop_host WHERE team_id = ?
ORDER BY (kind = 'subdomain') DESC, created_at, host`, teamID); err != nil {
		return nil, fmt.Errorf("platformstore: shop hosts: %w", err)
	}
	out := make([]ShopHost, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.host())
	}
	return out, nil
}

// ShopHost reads one host of a team.
func (s *Store) ShopHost(ctx context.Context, teamID int64, host string) (ShopHost, error) {
	var r shopHostRow
	if err := s.db.GetContext(ctx, &r, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? AND team_id = ?`, host, teamID); err != nil {
		if isNoRows(err) {
			return ShopHost{}, ErrShopHostNotFound
		}
		return ShopHost{}, fmt.Errorf("platformstore: shop host: %w", err)
	}
	return r.host(), nil
}

// OpenShopHosts lists every live host of every shop that is on and not closed by the operator (the
// CORS allow-list of /api/frontend). Hosts of a team that is not active are left out.
func (s *Store) OpenShopHosts(ctx context.Context) ([]string, error) {
	var hosts []string
	if err := s.db.SelectContext(ctx, &hosts, `SELECT h.host FROM shop_host h
JOIN team_shop ts ON ts.team_id = h.team_id
JOIN team t ON t.id = h.team_id
WHERE ts.enabled = 1 AND ts.closed_by_operator = 0 AND t.status = 'active' AND h.status = 'live'
ORDER BY h.host`); err != nil {
		return nil, fmt.Errorf("platformstore: open shop hosts: %w", err)
	}
	return hosts, nil
}

// TeamShop reads a team's shop row; a team without one is off (zero value with TeamID set).
func (s *Store) TeamShop(ctx context.Context, teamID int64) (TeamShop, error) {
	var ts TeamShop
	err := s.db.GetContext(ctx, &ts, `SELECT team_id, enabled, enabled_at, closed_by_operator, mail_from_name, mail_reply_to
FROM team_shop WHERE team_id = ?`, teamID)
	if err != nil {
		if isNoRows(err) {
			return TeamShop{TeamID: teamID}, nil
		}
		return TeamShop{}, fmt.Errorf("platformstore: team shop: %w", err)
	}
	return ts, nil
}

// lockTeamShop makes sure the team's shop row exists and locks it (the first lock of every shop write).
func lockTeamShop(ctx context.Context, tx *sqlx.Tx, teamID int64) (TeamShop, error) {
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO team_shop (team_id) VALUES (?)`, teamID); err != nil {
		return TeamShop{}, fmt.Errorf("platformstore: team shop row: %w", err)
	}
	var ts TeamShop
	if err := tx.GetContext(ctx, &ts, `SELECT team_id, enabled, enabled_at, closed_by_operator, mail_from_name, mail_reply_to
FROM team_shop WHERE team_id = ? FOR UPDATE`, teamID); err != nil {
		return TeamShop{}, fmt.Errorf("platformstore: lock team shop: %w", err)
	}
	return ts, nil
}

// SetShopEnabled turns a team's shop on or off. Turning it on puts its platform address subdomain
// (a live, managed host) in place: a platform address of an old slug is replaced, and it becomes the
// main address when the team has no other. Turning it off keeps every host (they stop answering).
func (s *Store) SetShopEnabled(ctx context.Context, teamID int64, enabled bool, subdomain string, by int64, now time.Time) error {
	now = now.UTC()
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		ts, err := lockTeamShop(ctx, tx, teamID)
		if err != nil {
			return err
		}
		if enabled {
			if err := ensureSubdomainTx(ctx, tx, teamID, subdomain, now); err != nil {
				return err
			}
		}
		enabledAt := ts.EnabledAt
		if enabled && !ts.Enabled {
			enabledAt = &now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_shop SET enabled = ?, enabled_at = ?, updated_by = ?, updated_at = ?
WHERE team_id = ?`, enabled, enabledAt, nullID(by), now, teamID); err != nil {
			return fmt.Errorf("platformstore: set shop enabled: %w", err)
		}
		return nil
	})
}

// EnsureShopSubdomain puts the team's platform address in place (a rename moved its slug) without
// touching the switch. A no-op when it is already there.
func (s *Store) EnsureShopSubdomain(ctx context.Context, teamID int64, subdomain string, now time.Time) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		return ensureSubdomainTx(ctx, tx, teamID, subdomain, now.UTC())
	})
}

func ensureSubdomainTx(ctx context.Context, tx *sqlx.Tx, teamID int64, subdomain string, now time.Time) error {
	if subdomain == "" {
		return errors.New("platformstore: shop subdomain is empty")
	}
	var hosts []shopHostRow
	if err := tx.SelectContext(ctx, &hosts, `SELECT `+shopHostColumns+` FROM shop_host WHERE team_id = ? FOR UPDATE`, teamID); err != nil {
		return fmt.Errorf("platformstore: shop hosts: %w", err)
	}
	have, primary, oldPrimary := false, false, false
	for _, h := range hosts {
		if h.Host == subdomain {
			have = true
			continue
		}
		if h.Primary {
			primary = true
			if h.Kind == ShopHostSubdomain {
				oldPrimary = true
			}
		}
	}
	// A platform address of an old slug goes (the new one replaces it).
	if _, err := tx.ExecContext(ctx, `DELETE FROM shop_host WHERE team_id = ? AND kind = 'subdomain' AND host <> ?`, teamID, subdomain); err != nil {
		return fmt.Errorf("platformstore: old shop subdomain: %w", err)
	}
	if oldPrimary {
		primary = false
	}
	if !have {
		var owner int64
		err := tx.GetContext(ctx, &owner, `SELECT team_id FROM shop_host WHERE host = ? FOR UPDATE`, subdomain)
		switch {
		case err == nil:
			return ErrShopHostTaken
		case !isNoRows(err):
			return fmt.Errorf("platformstore: shop subdomain owner: %w", err)
		}
		recs, err := recordsJSON(nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO shop_host (host, team_id, kind, managed, is_primary, status, records, created_at, verified_at)
VALUES (?, ?, 'subdomain', 1, 0, 'live', ?, ?, ?)`, subdomain, teamID, recs, now, now); err != nil {
			if dupKey(err, "PRIMARY") {
				return ErrShopHostTaken
			}
			return fmt.Errorf("platformstore: add shop subdomain: %w", err)
		}
	}
	if !primary {
		if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET is_primary = 1 WHERE host = ? AND team_id = ?`, subdomain, teamID); err != nil {
			return fmt.Errorf("platformstore: shop subdomain primary: %w", err)
		}
	}
	return nil
}

// AttachShopHost adds a custom host of the team with the records it needs. Attaching a host the team
// already has refreshes its records and status; another team's host is ErrShopHostTaken, unless it
// never went live within ShopHostPendingTTL (then this team takes the name over). The status is
// gated by the ownership proof (gateShopHost): live only when the team proved the domain.
func (s *Store) AttachShopHost(ctx context.Context, teamID int64, host, status string, records []ShopRecord, now time.Time) error {
	now = now.UTC()
	status, records, unproven := s.gateShopHost(ctx, teamID, host, host, status, records)
	recs, err := recordsJSON(records)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		var cur shopHostRow
		err := tx.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? FOR UPDATE`, host)
		switch {
		case err == nil && cur.TeamID != teamID && stalePending(cur, now):
			// Never live (so never a main address): the name goes to the team that attaches it now.
			if _, err := tx.ExecContext(ctx, `DELETE FROM shop_host WHERE host = ? AND team_id = ?`, host, cur.TeamID); err != nil {
				return fmt.Errorf("platformstore: stale shop host: %w", err)
			}
		case err == nil && cur.TeamID != teamID:
			return ErrShopHostTaken
		case err == nil && cur.Kind != ShopHostCustom:
			return ErrShopHostManaged
		case err == nil && cur.RedirectTo != "":
			// The team's own "www." redirect: it stays one until the team removes it.
			return ErrShopHostRedirect
		case err == nil:
			status = keepVerified(cur, status, unproven)
			verified := cur.VerifiedAt
			if status == ShopHostLive && cur.Status != ShopHostLive {
				verified = &now
			}
			_, err = tx.ExecContext(ctx, `UPDATE shop_host SET status = ?, records = ?, checked_at = ?, verified_at = ? WHERE host = ?`,
				status, recs, now, verified, host)
			if err != nil {
				return fmt.Errorf("platformstore: refresh shop host: %w", err)
			}
			if cur.Primary && status != ShopHostLive {
				return primaryToSubdomainTx(ctx, tx, teamID, host)
			}
			return nil
		case !isNoRows(err):
			return fmt.Errorf("platformstore: shop host owner: %w", err)
		}
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM shop_host WHERE team_id = ? AND kind = 'custom' AND redirect_to = ''`, teamID); err != nil {
			return fmt.Errorf("platformstore: count shop hosts: %w", err)
		}
		if n >= ShopHostMax {
			return ErrShopHostLimit
		}
		var verified *time.Time
		if status == ShopHostLive {
			verified = &now
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO shop_host (host, team_id, kind, managed, is_primary, status, records, created_at, verified_at, checked_at)
VALUES (?, ?, 'custom', 0, 0, ?, ?, ?, ?, ?)`, host, teamID, status, recs, now, verified, now); err != nil {
			if dupKey(err, "PRIMARY") {
				return ErrShopHostTaken
			}
			return fmt.Errorf("platformstore: attach shop host: %w", err)
		}
		// Attached again: the provider must keep it.
		if _, err := tx.ExecContext(ctx, `DELETE FROM shop_domain_drop WHERE host = ?`, host); err != nil {
			return fmt.Errorf("platformstore: cancel shop domain drop: %w", err)
		}
		return nil
	})
}

// SetShopHostStatus stores a check's answer for a custom host of the team, gated by the ownership
// proof (gateShopHost): the provider's «live» counts only once the team proved the domain. A host
// that stops being live loses the main address to the platform address. It returns the status it
// stored, which may differ from the one asked for.
func (s *Store) SetShopHostStatus(ctx context.Context, teamID int64, host, status string, records []ShopRecord, now time.Time) (string, error) {
	now = now.UTC()
	// A redirect is proven by its target's proof (read before the DNS lookup, outside the transaction;
	// a row's target never changes while it stands).
	proofHost := host
	var target string
	if err := s.db.GetContext(ctx, &target, `SELECT redirect_to FROM shop_host WHERE host = ? AND team_id = ?`, host, teamID); err != nil && !isNoRows(err) {
		return "", fmt.Errorf("platformstore: shop host target: %w", err)
	}
	if target != "" {
		proofHost = target
	}
	status, records, unproven := s.gateShopHost(ctx, teamID, host, proofHost, status, records)
	recs, err := recordsJSON(records)
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		var cur shopHostRow
		if err := tx.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? AND team_id = ? FOR UPDATE`, host, teamID); err != nil {
			if isNoRows(err) {
				return ErrShopHostNotFound
			}
			return fmt.Errorf("platformstore: lock shop host: %w", err)
		}
		if cur.Kind != ShopHostCustom {
			return ErrShopHostManaged
		}
		if cur.RedirectTo != target {
			return ErrShopHostNotFound // removed and attached again meanwhile: the next check sees it
		}
		status = keepVerified(cur, status, unproven)
		verified := cur.VerifiedAt
		if status == ShopHostLive && cur.Status != ShopHostLive {
			verified = &now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET status = ?, records = ?, checked_at = ?, verified_at = ? WHERE host = ?`,
			status, recs, now, verified, host); err != nil {
			return fmt.Errorf("platformstore: shop host status: %w", err)
		}
		if cur.Primary && status != ShopHostLive {
			return primaryToSubdomainTx(ctx, tx, teamID, host)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

// RemoveShopHost deletes a custom host of the team and queues its removal at the domain provider
// (shop_domain_drop), with the "www." redirects that point at it. When it was the main address, the
// platform address takes over.
func (s *Store) RemoveShopHost(ctx context.Context, teamID int64, host string) error {
	_, err := s.RemoveShopHostAll(ctx, teamID, host)
	return err
}

// RemoveShopHostAll is RemoveShopHost answering every host it removed (host first, then its
// redirects), so the caller can drop each at the provider at once.
func (s *Store) RemoveShopHostAll(ctx context.Context, teamID int64, host string) ([]string, error) {
	var gone []string
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		gone = gone[:0]
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		var cur shopHostRow
		if err := tx.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? AND team_id = ? FOR UPDATE`, host, teamID); err != nil {
			if isNoRows(err) {
				return ErrShopHostNotFound
			}
			return fmt.Errorf("platformstore: lock shop host: %w", err)
		}
		if cur.Managed || cur.Kind != ShopHostCustom {
			return ErrShopHostManaged
		}
		gone = append(gone, host)
		if cur.RedirectTo == "" {
			var redirects []string
			if err := tx.SelectContext(ctx, &redirects, `SELECT host FROM shop_host WHERE team_id = ? AND redirect_to = ? ORDER BY host FOR UPDATE`, teamID, host); err != nil {
				return fmt.Errorf("platformstore: shop host redirects: %w", err)
			}
			gone = append(gone, redirects...)
		}
		for _, h := range gone {
			if _, err := tx.ExecContext(ctx, `DELETE FROM shop_host WHERE host = ? AND team_id = ?`, h, teamID); err != nil {
				return fmt.Errorf("platformstore: remove shop host: %w", err)
			}
		}
		if err := enqueueDomainDropTx(ctx, tx, gone, time.Now().UTC()); err != nil {
			return err
		}
		if cur.Primary {
			return primaryToSubdomainTx(ctx, tx, teamID, host)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return gone, nil
}

// AttachShopRedirect adds host as a "www." redirect of target, a custom host of the team without a
// redirect of its own: a custom, unmanaged row with redirect_to set, not counted against ShopHostMax,
// proven by target's proof (its records leave that proof record out: the target's row shows it) and
// removed with target. Attaching it again refreshes its status; a non-redirect host of the team is
// ErrShopHostRedirect, another team's host ErrShopHostTaken (unless it never went live within
// ShopHostPendingTTL), a target the team does not have ErrShopHostNotFound.
func (s *Store) AttachShopRedirect(ctx context.Context, teamID int64, host, target, status string, records []ShopRecord, now time.Time) error {
	now = now.UTC()
	if host == target {
		return ErrShopHostRedirect
	}
	status, records, unproven := s.gateShopHost(ctx, teamID, host, target, status, records)
	recs, err := recordsJSON(records)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		var tgt shopHostRow
		if err := tx.GetContext(ctx, &tgt, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? AND team_id = ? FOR UPDATE`, target, teamID); err != nil {
			if isNoRows(err) {
				return ErrShopHostNotFound
			}
			return fmt.Errorf("platformstore: redirect target: %w", err)
		}
		if tgt.Kind != ShopHostCustom || tgt.RedirectTo != "" {
			return ErrShopHostNotFound
		}
		var cur shopHostRow
		err := tx.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? FOR UPDATE`, host)
		switch {
		case err == nil && cur.TeamID != teamID && stalePending(cur, now):
			if _, err := tx.ExecContext(ctx, `DELETE FROM shop_host WHERE host = ? AND team_id = ?`, host, cur.TeamID); err != nil {
				return fmt.Errorf("platformstore: stale shop host: %w", err)
			}
		case err == nil && cur.TeamID != teamID:
			return ErrShopHostTaken
		case err == nil && (cur.Kind != ShopHostCustom || cur.RedirectTo != target):
			return ErrShopHostRedirect
		case err == nil:
			status = keepVerified(cur, status, unproven)
			verified := cur.VerifiedAt
			if status == ShopHostLive && cur.Status != ShopHostLive {
				verified = &now
			}
			if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET status = ?, records = ?, checked_at = ?, verified_at = ? WHERE host = ?`,
				status, recs, now, verified, host); err != nil {
				return fmt.Errorf("platformstore: refresh shop redirect: %w", err)
			}
			return nil
		case !isNoRows(err):
			return fmt.Errorf("platformstore: shop host owner: %w", err)
		}
		var verified *time.Time
		if status == ShopHostLive {
			verified = &now
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO shop_host (host, team_id, kind, managed, is_primary, status, redirect_to, records, created_at, verified_at, checked_at)
VALUES (?, ?, 'custom', 0, 0, ?, ?, ?, ?, ?, ?)`, host, teamID, status, target, recs, now, verified, now); err != nil {
			if dupKey(err, "PRIMARY") {
				return ErrShopHostTaken
			}
			return fmt.Errorf("platformstore: attach shop redirect: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shop_domain_drop WHERE host = ?`, host); err != nil {
			return fmt.Errorf("platformstore: cancel shop domain drop: %w", err)
		}
		return nil
	})
}

func primaryToSubdomainTx(ctx context.Context, tx *sqlx.Tx, teamID int64, from string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET is_primary = 0 WHERE host = ?`, from); err != nil {
		return fmt.Errorf("platformstore: drop shop primary: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET is_primary = 1 WHERE team_id = ? AND kind = 'subdomain'`, teamID); err != nil {
		return fmt.Errorf("platformstore: shop primary to subdomain: %w", err)
	}
	return nil
}

// ClearShopPrimary leaves the team without a main address (the storefront team only: every other
// team's shop keeps its platform address as the main one). A no-op without one.
func (s *Store) ClearShopPrimary(ctx context.Context, teamID int64) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET is_primary = 0 WHERE team_id = ? AND is_primary = 1`, teamID); err != nil {
			return fmt.Errorf("platformstore: clear shop primary: %w", err)
		}
		return nil
	})
}

// SetShopPrimary makes a live host of the team its main address (a legacy host too: the storefront
// team's main address is one of them once grbpwr.com moves to the shared storefront).
func (s *Store) SetShopPrimary(ctx context.Context, teamID int64, host string) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		var cur shopHostRow
		if err := tx.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? AND team_id = ? FOR UPDATE`, host, teamID); err != nil {
			if isNoRows(err) {
				return ErrShopHostNotFound
			}
			return fmt.Errorf("platformstore: lock shop host: %w", err)
		}
		if cur.Status != ShopHostLive || cur.RedirectTo != "" {
			return ErrShopHostNotLive
		}
		if cur.Primary {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET is_primary = 0 WHERE team_id = ? AND is_primary = 1`, teamID); err != nil {
			return fmt.Errorf("platformstore: drop shop primary: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shop_host SET is_primary = 1 WHERE host = ?`, host); err != nil {
			return fmt.Errorf("platformstore: set shop primary: %w", err)
		}
		return nil
	})
}

// SetShopMail stores the sender name and reply-to of the team's letters (already normalised).
func (s *Store) SetShopMail(ctx context.Context, teamID int64, fromName, replyTo string, by int64) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeamShop(ctx, tx, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_shop SET mail_from_name = ?, mail_reply_to = ?, updated_by = ?, updated_at = UTC_TIMESTAMP(3)
WHERE team_id = ?`, fromName, replyTo, nullID(by), teamID); err != nil {
			return fmt.Errorf("platformstore: set shop mail: %w", err)
		}
		return nil
	})
}

// SetShopClosed closes (or reopens) a team's shop by the operator; the team's own switch stays. A
// reopen of a switched-on shop is a fresh opening: enabled_at moves to now, so shop mail queued
// before it (while the shop was closed) is never sent.
func (s *Store) SetShopClosed(ctx context.Context, teamID int64, closed bool) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		ts, err := lockTeamShop(ctx, tx, teamID)
		if err != nil {
			return err
		}
		if !closed && ts.ClosedByOperator && ts.Enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE team_shop SET closed_by_operator = FALSE, enabled_at = UTC_TIMESTAMP(3),
updated_at = UTC_TIMESTAMP(3) WHERE team_id = ?`, teamID); err != nil {
				return fmt.Errorf("platformstore: reopen shop: %w", err)
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_shop SET closed_by_operator = ?, updated_at = UTC_TIMESTAMP(3) WHERE team_id = ?`,
			closed, teamID); err != nil {
			return fmt.Errorf("platformstore: set shop closed: %w", err)
		}
		return nil
	})
}

// TeamStripe reads a team's Stripe row; ok=false when it has none.
func (s *Store) TeamStripe(ctx context.Context, teamID int64) (TeamStripe, bool, error) {
	var st TeamStripe
	err := s.db.GetContext(ctx, &st, `SELECT team_id, kind, account_id, account_name, mode, charges_enabled, payouts_enabled,
details_submitted, fee_bps, publishable_key, secret_key_sealed, webhook_secret_sealed, key_last4, connected_at, disconnected_at
FROM team_stripe WHERE team_id = ?`, teamID)
	if err != nil {
		if isNoRows(err) {
			return TeamStripe{}, false, nil
		}
		return TeamStripe{}, false, fmt.Errorf("platformstore: team stripe: %w", err)
	}
	return st, true, nil
}

// RecordShopMailOwner remembers which team sent the shop letter Resend accepted as providerID (its
// outbox row mailID in the team database), for the bounce route. Recording an id again keeps the
// first owner.
func (s *Store) RecordShopMailOwner(ctx context.Context, providerID string, teamID, mailID int64) error {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" || len(providerID) > 64 || teamID <= 0 || mailID <= 0 {
		return errors.New("platformstore: shop mail owner: bad id")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO shop_mail_owner (provider_id, team_id, mail_id) VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE team_id = team_id`, providerID, teamID, mailID); err != nil {
		return fmt.Errorf("platformstore: shop mail owner: %w", err)
	}
	return nil
}

// ShopMailOwner answers the team and outbox row of a shop letter by Resend's email id; ok=false when
// no shop letter carries it.
func (s *Store) ShopMailOwner(ctx context.Context, providerID string) (teamID, mailID int64, ok bool, err error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" || len(providerID) > 64 {
		return 0, 0, false, nil
	}
	var row struct {
		TeamID int64 `db:"team_id"`
		MailID int64 `db:"mail_id"`
	}
	if err := s.db.GetContext(ctx, &row, `SELECT team_id, mail_id FROM shop_mail_owner WHERE provider_id = ?`, providerID); err != nil {
		if isNoRows(err) {
			return 0, 0, false, nil
		}
		return 0, 0, false, fmt.Errorf("platformstore: shop mail owner: %w", err)
	}
	return row.TeamID, row.MailID, true, nil
}

// shopTables are a team's shop rows in the control plane.
func shopTables() []string {
	return []string{"team_shop", "shop_host", "team_stripe", "shop_mail_owner"}
}

// DeleteTeamShopTx removes every shop row of a team (the day-30 wipe's finish calls it beside the
// team's other control-plane rows) and answers the rows deleted, by table. Its custom hosts are queued
// for removal at the domain provider (shop_domain_drop); its hosts are free again.
func DeleteTeamShopTx(ctx context.Context, tx *sqlx.Tx, teamID int64) (map[string]int64, error) {
	out := map[string]int64{}
	var custom []string
	if err := tx.SelectContext(ctx, &custom, `SELECT host FROM shop_host WHERE team_id = ? AND kind = 'custom'`, teamID); err != nil {
		return nil, fmt.Errorf("platformstore: delete team shop: hosts: %w", err)
	}
	if err := enqueueDomainDropTx(ctx, tx, custom, time.Now().UTC()); err != nil {
		return nil, err
	}
	for _, table := range shopTables() {
		r, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE team_id = ?`, teamID)
		if err != nil {
			return nil, fmt.Errorf("platformstore: delete team shop: %s: %w", table, err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			out[table] = n
		}
	}
	return out, nil
}

// ShopSummary is a team's shop as the console's team row shows it.
type ShopSummary struct {
	Enabled     bool   `db:"enabled"`
	Closed      bool   `db:"closed_by_operator"`
	PrimaryHost string `db:"primary_host"`
	Payable     bool   `db:"payable"`
}

// ShopSummaries reads the shops of teams in one query; a team without a shop row is missing.
func (s *Store) ShopSummaries(ctx context.Context, teamIDs []int64) (map[int64]ShopSummary, error) {
	out := make(map[int64]ShopSummary, len(teamIDs))
	if len(teamIDs) == 0 {
		return out, nil
	}
	q, args, err := sqlx.In(`SELECT ts.team_id, ts.enabled, ts.closed_by_operator, COALESCE(h.host, '') AS primary_host,
(st.team_id IS NOT NULL AND st.charges_enabled = 1 AND st.disconnected_at IS NULL) AS payable
FROM team_shop ts
LEFT JOIN shop_host h ON h.primary_team = ts.team_id
LEFT JOIN team_stripe st ON st.team_id = ts.team_id
WHERE ts.team_id IN (?)`, teamIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		TeamID int64 `db:"team_id"`
		ShopSummary
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
		return nil, fmt.Errorf("platformstore: shop summaries: %w", err)
	}
	for _, r := range rows {
		out[r.TeamID] = r.ShopSummary
	}
	return out, nil
}

// ShopHostProof decides whether a team owns a custom host (X18): the DNS record it must publish, and
// whether that record is in place. internal/shop.TXTProof in prod.
type ShopHostProof interface {
	Record(teamID int64, host string) ShopRecord
	Proven(ctx context.Context, teamID int64, host string) (bool, error)
}

type shopProofHolder struct{ p ShopHostProof }

// SetShopHostProof installs the ownership proof every custom host's status goes through. Without one
// no custom host ever goes live (fail closed).
func (s *Store) SetShopHostProof(p ShopHostProof) { s.proof.Store(&shopProofHolder{p: p}) }

// gateShopHost is the one «may this custom host go live» decision (every path that stores a custom
// host's status calls it: the shop screen and the domains worker). It puts the proof record first in
// the records (found or not) and turns a provider «live» into «verifying» until the team's proof is
// in its DNS; unproven reports that demotion (keepVerified undoes it for a host verified before). No
// proof installed: never live. proofHost is the host whose proof counts: host itself, or for a
// redirect its target (then the target's proof record is not repeated in host's records).
func (s *Store) gateShopHost(ctx context.Context, teamID int64, host, proofHost, status string, records []ShopRecord) (string, []ShopRecord, bool) {
	h := s.proof.Load()
	if h == nil || h.p == nil {
		if status == ShopHostLive {
			return ShopHostVerifying, records, true
		}
		return status, records, false
	}
	ok, err := h.p.Proven(ctx, teamID, proofHost)
	if err != nil {
		ok = false
	}
	if proofHost != host {
		if status == ShopHostLive && !ok {
			return ShopHostVerifying, records, true
		}
		return status, records, false
	}
	rec := h.p.Record(teamID, host)
	rec.Found = ok
	out := make([]ShopRecord, 0, len(records)+1)
	out = append(out, rec)
	for _, r := range records {
		if r.Type == rec.Type && r.Name == rec.Name {
			continue
		}
		out = append(out, r)
	}
	if status == ShopHostLive && !ok {
		return ShopHostVerifying, out, true
	}
	return status, out, false
}

// keepVerified: a host its team proved once (verified_at set) keeps its current status when the
// provider says live but the proof is not seen now (TXT removed, resolver failure): the proof
// decides ownership once; the provider alone demotes a verified host.
func keepVerified(cur shopHostRow, status string, unproven bool) string {
	if unproven && cur.VerifiedAt != nil {
		return cur.Status
	}
	return status
}

// ShopDomainDropMax bounds one drain pass.
const ShopDomainDropMax = 50

func enqueueDomainDropTx(ctx context.Context, tx *sqlx.Tx, hosts []string, now time.Time) error {
	for _, h := range hosts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO shop_domain_drop (host, created_at, next_attempt_at) VALUES (?, ?, ?)
AS n ON DUPLICATE KEY UPDATE next_attempt_at = n.next_attempt_at`, h, now, now); err != nil {
			return fmt.Errorf("platformstore: queue shop domain drop: %w", err)
		}
	}
	return nil
}

// dropBackoff is the wait after n failed removals: 1 m doubling, at most 6 h.
func dropBackoff(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < 6*time.Hour; i++ {
		d *= 2
	}
	return min(d, 6*time.Hour)
}

// DrainShopDomainDrops removes due queued hosts at the domain provider (remove), one row lock each,
// held across the provider call: an attach of the same host waits for it (AttachShopHost deletes the
// queued row) and so commits after the removal, never before. A host attached again meanwhile is not removed. A
// failure backs off (1 m doubling, at most 6 h). It answers how many hosts were removed.
func (s *Store) DrainShopDomainDrops(ctx context.Context, now time.Time, remove func(ctx context.Context, host string) error) (int, error) {
	now = now.UTC()
	var hosts []string
	if err := s.db.SelectContext(ctx, &hosts, `SELECT host FROM shop_domain_drop WHERE next_attempt_at <= ? ORDER BY next_attempt_at LIMIT ?`,
		now, ShopDomainDropMax); err != nil {
		return 0, fmt.Errorf("platformstore: due shop domain drops: %w", err)
	}
	done := 0
	var firstErr error
	for _, host := range hosts {
		ok, err := s.dropShopDomain(ctx, host, now, true, remove)
		if ok {
			done++
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return done, firstErr
}

// DropShopDomainNow runs host's queued removal at once (the shop screen right after a removal),
// whatever its backoff. ok=false with a nil error: nothing queued, or the host is attached again.
func (s *Store) DropShopDomainNow(ctx context.Context, host string, now time.Time, remove func(ctx context.Context, host string) error) (bool, error) {
	return s.dropShopDomain(ctx, host, now.UTC(), false, remove)
}

func (s *Store) dropShopDomain(ctx context.Context, host string, now time.Time, dueOnly bool, remove func(ctx context.Context, host string) error) (bool, error) {
	removed := false
	var rerr error
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		q := `SELECT attempts FROM shop_domain_drop WHERE host = ? FOR UPDATE SKIP LOCKED`
		args := []any{host}
		if dueOnly {
			q = `SELECT attempts FROM shop_domain_drop WHERE host = ? AND next_attempt_at <= ? FOR UPDATE SKIP LOCKED`
			args = append(args, now)
		}
		var attempts int
		if err := tx.GetContext(ctx, &attempts, q, args...); err != nil {
			if isNoRows(err) {
				return nil // cancelled, or another instance holds it
			}
			return err
		}
		var held int
		if err := tx.GetContext(ctx, &held, `SELECT COUNT(*) FROM shop_host WHERE host = ?`, host); err != nil {
			return err
		}
		if held == 0 {
			if rerr = remove(ctx, host); rerr != nil {
				msg := rerr.Error()
				if len(msg) > 255 {
					msg = msg[:255]
				}
				_, err := tx.ExecContext(ctx, `UPDATE shop_domain_drop SET attempts = ?, last_error = ?, next_attempt_at = ? WHERE host = ?`,
					attempts+1, msg, now.Add(dropBackoff(attempts+1)), host)
				return err
			}
			removed = true
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM shop_domain_drop WHERE host = ?`, host)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("platformstore: drop shop domain: %w", err)
	}
	if rerr != nil {
		return false, fmt.Errorf("platformstore: drop shop domain at the provider: %w", rerr)
	}
	return removed, nil
}

// EnsureLegacyShopHosts makes hosts the storefront team's (teamID) legacy hosts: live, managed, never
// a main address on their own (the owner may pick one, SetShopPrimary; a boot keeps that pick).
// Idempotent (boot). A host another team holds is taken back: legacy hosts are the
// storefront's own, and its customers must never reach another team. It answers the hosts taken back.
func (s *Store) EnsureLegacyShopHosts(ctx context.Context, teamID int64, hosts []string, now time.Time) ([]string, error) {
	now = now.UTC()
	var taken []string
	recs, err := recordsJSON(nil)
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		var err error
		for try := 0; try < 2; try++ { // a second try when another instance seeded it at the same time
			err = s.ensureLegacyShopHost(ctx, teamID, host, recs, now, &taken)
			if err == nil || !dupKey(err, "PRIMARY") {
				break
			}
		}
		if err != nil {
			return taken, fmt.Errorf("platformstore: legacy shop host %s: %w", host, err)
		}
	}
	return taken, nil
}

func (s *Store) ensureLegacyShopHost(ctx context.Context, teamID int64, host string, recs []byte, now time.Time, taken *[]string) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		var cur shopHostRow
		err := tx.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ?`, host)
		switch {
		case isNoRows(err):
		case err != nil:
			return err
		case cur.TeamID == teamID && cur.Kind == ShopHostLegacy && cur.Status == ShopHostLive:
			return nil
		case cur.Kind == ShopHostSubdomain:
			// A team's platform address is never a legacy host: the configuration is wrong (boot fails).
			return fmt.Errorf("%w: %s is a platform address of a team", ErrShopHostManaged, host)
		default:
			// Lock order: the holder's team_shop row, then the host.
			if _, err := lockTeamShop(ctx, tx, cur.TeamID); err != nil {
				return err
			}
			if err := tx.GetContext(ctx, &cur, `SELECT `+shopHostColumns+` FROM shop_host WHERE host = ? FOR UPDATE`, host); err != nil && !isNoRows(err) {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM shop_host WHERE host = ?`, host); err != nil {
				return err
			}
			if cur.TeamID != teamID {
				*taken = append(*taken, host)
			}
			if cur.Primary && cur.TeamID != teamID {
				if err := primaryToSubdomainTx(ctx, tx, cur.TeamID, host); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO shop_host (host, team_id, kind, managed, is_primary, status, records, created_at, verified_at)
VALUES (?, ?, 'legacy', 1, 0, 'live', ?, ?, ?)`, host, teamID, recs, now, now)
		return err
	})
}

// TeamMailName is one team's name and brand name as the team-shop mail lane sees them: no shop's
// letters may read as another team.
type TeamMailName struct {
	TeamID    int64  `db:"team_id"`
	Name      string `db:"name"`
	BrandName string `db:"brand_name"`
}

// TeamMailNames lists every team that is not deleted, with its brand name ("" without a brand row).
func (s *Store) TeamMailNames(ctx context.Context) ([]TeamMailName, error) {
	var out []TeamMailName
	if err := s.db.SelectContext(ctx, &out, `SELECT team.id AS team_id, team.name AS name,
COALESCE(team_brand.brand_name, '') AS brand_name
FROM team LEFT JOIN team_brand ON team_brand.team_id = team.id
WHERE team.status <> 'deleted' ORDER BY team.id`); err != nil {
		return nil, fmt.Errorf("platformstore: team mail names: %w", err)
	}
	return out, nil
}
