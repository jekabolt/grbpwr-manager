package platformstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Writes of a team's Stripe (X18-L3). A team has one row: Stripe Connect (a connected Standard account,
// direct charges) or its own keys (the secret key and the webhook secret sealed by the caller). A
// disconnect keeps the row (name, last4, when) and frees the account id, so the account can be connected
// again here or by another team. Lock order: the team row FOR SHARE, then team_stripe rows.

// ErrStripeAccountTaken: another team holds that Stripe account.
var ErrStripeAccountTaken = errors.New("platformstore: stripe account taken")

// Team Stripe kinds (team_stripe.kind).
const (
	StripeKindConnect = "connect"
	StripeKindKeys    = "keys"
)

// StripeAccountState is a Stripe account as Stripe reports it.
type StripeAccountState struct {
	// AccountID is acct_…; "" only for own keys whose restricted key may not read the account.
	AccountID        string
	Name             string
	Mode             string // live | test
	ChargesEnabled   bool
	PayoutsEnabled   bool
	DetailsSubmitted bool
}

// TeamStripeKeys is a team's own keys as stored: the secrets arrive sealed.
type TeamStripeKeys struct {
	StripeAccountState
	PublishableKey      string
	SecretKeySealed     []byte
	WebhookSecretSealed []byte
	KeyLast4            string
}

func teamStripeCols() string {
	return `team_id, kind, account_id, account_name, mode, charges_enabled, payouts_enabled, details_submitted, fee_bps,
publishable_key, secret_key_sealed, webhook_secret_sealed, key_last4, connected_at, disconnected_at`
}

func nullAccount(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func clipName(s string) string {
	r := []rune(s)
	if len(r) > 128 {
		return string(r[:128])
	}
	return s
}

// claimStripeTx locks the team (active only) and its row, and refuses an account another team holds.
// It reports whether the team already has a row.
func claimStripeTx(ctx context.Context, tx *sqlx.Tx, teamID int64, accountID string) (bool, error) {
	var st string
	if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR SHARE`, teamID); err != nil {
		if isNoRows(err) {
			return false, ErrWrongTeamStatus
		}
		return false, fmt.Errorf("platformstore: stripe team: %w", err)
	}
	if st != "active" {
		return false, ErrWrongTeamStatus
	}
	var have int
	if err := tx.GetContext(ctx, &have, `SELECT COUNT(*) FROM team_stripe WHERE team_id = ? FOR UPDATE`, teamID); err != nil {
		return false, fmt.Errorf("platformstore: stripe row: %w", err)
	}
	if accountID != "" {
		var holders []int64
		if err := tx.SelectContext(ctx, &holders, `SELECT team_id FROM team_stripe WHERE account_id = ? FOR UPDATE`, accountID); err != nil {
			return false, fmt.Errorf("platformstore: stripe account: %w", err)
		}
		for _, h := range holders {
			if h != teamID {
				return false, ErrStripeAccountTaken
			}
		}
	}
	return have > 0, nil
}

func stripeWriteErr(op string, err error) error {
	if dupKey(err, "uq_team_stripe_account") {
		return ErrStripeAccountTaken
	}
	if errors.Is(err, ErrStripeAccountTaken) || errors.Is(err, ErrWrongTeamStatus) {
		return err
	}
	return fmt.Errorf("platformstore: %s: %w", op, err)
}

// ConnectTeamStripe stores a finished Connect: kind connect, any own keys wiped, the fee kept.
func (s *Store) ConnectTeamStripe(ctx context.Context, teamID int64, a StripeAccountState, now time.Time) error {
	if a.AccountID == "" {
		return errors.New("platformstore: connect needs an account")
	}
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		have, err := claimStripeTx(ctx, tx, teamID, a.AccountID)
		if err != nil {
			return err
		}
		args := []any{StripeKindConnect, a.AccountID, clipName(a.Name), a.Mode, a.ChargesEnabled, a.PayoutsEnabled, a.DetailsSubmitted, now, now}
		if have {
			_, err = tx.ExecContext(ctx, `UPDATE team_stripe SET kind = ?, account_id = ?, account_name = ?, mode = ?,
charges_enabled = ?, payouts_enabled = ?, details_submitted = ?, publishable_key = '', secret_key_sealed = NULL,
webhook_secret_sealed = NULL, key_last4 = '', connected_at = ?, disconnected_at = NULL, updated_at = ? WHERE team_id = ?`,
				append(args, teamID)...)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO team_stripe (kind, account_id, account_name, mode, charges_enabled,
payouts_enabled, details_submitted, connected_at, updated_at, team_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				append(args, teamID)...)
		}
		return err
	})
	if err != nil {
		return stripeWriteErr("connect stripe", err)
	}
	return nil
}

// SetTeamStripeKeys stores a team's own keys: kind keys, the sealed secrets, the fee kept.
func (s *Store) SetTeamStripeKeys(ctx context.Context, teamID int64, k TeamStripeKeys, now time.Time) error {
	if len(k.SecretKeySealed) == 0 || len(k.WebhookSecretSealed) == 0 {
		return errors.New("platformstore: keys need both sealed secrets")
	}
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		have, err := claimStripeTx(ctx, tx, teamID, k.AccountID)
		if err != nil {
			return err
		}
		args := []any{StripeKindKeys, nullAccount(k.AccountID), clipName(k.Name), k.Mode, k.ChargesEnabled, k.PayoutsEnabled,
			k.DetailsSubmitted, k.PublishableKey, k.SecretKeySealed, k.WebhookSecretSealed, k.KeyLast4, now, now, teamID}
		if have {
			_, err = tx.ExecContext(ctx, `UPDATE team_stripe SET kind = ?, account_id = ?, account_name = ?, mode = ?,
charges_enabled = ?, payouts_enabled = ?, details_submitted = ?, publishable_key = ?, secret_key_sealed = ?,
webhook_secret_sealed = ?, key_last4 = ?, connected_at = ?, disconnected_at = NULL, updated_at = ? WHERE team_id = ?`, args...)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO team_stripe (kind, account_id, account_name, mode, charges_enabled,
payouts_enabled, details_submitted, publishable_key, secret_key_sealed, webhook_secret_sealed, key_last4, connected_at,
updated_at, team_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		}
		return err
	})
	if err != nil {
		return stripeWriteErr("set stripe keys", err)
	}
	return nil
}

// SyncTeamStripe refreshes the name and flags of a team's live Stripe row, only while it still holds
// that account (accountID "" = a row without one). ok=false when nothing matched (disconnected, or the
// team moved to another account since).
func (s *Store) SyncTeamStripe(ctx context.Context, teamID int64, a StripeAccountState) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE team_stripe SET account_name = ?, charges_enabled = ?, payouts_enabled = ?,
details_submitted = ?, updated_at = UTC_TIMESTAMP(3)
WHERE team_id = ? AND disconnected_at IS NULL AND COALESCE(account_id, '') = ?`,
		clipName(a.Name), a.ChargesEnabled, a.PayoutsEnabled, a.DetailsSubmitted, teamID, a.AccountID)
	if err != nil {
		return false, fmt.Errorf("platformstore: sync stripe: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("platformstore: sync stripe: %w", err)
	}
	return n > 0, nil
}

// DisconnectTeamStripe marks a team's Stripe disconnected: the sealed secrets are wiped, the flags
// cleared and the account id freed; the name and last4 stay for the screen. onlyAccount != "" acts only
// while the row still holds that account (a late deauthorization of an older account changes nothing).
// ok=false when nothing was live.
func (s *Store) DisconnectTeamStripe(ctx context.Context, teamID int64, onlyAccount string, now time.Time) (bool, error) {
	q := `UPDATE team_stripe SET account_id = NULL, charges_enabled = 0, payouts_enabled = 0, details_submitted = 0,
publishable_key = '', secret_key_sealed = NULL, webhook_secret_sealed = NULL, disconnected_at = ?, updated_at = ?
WHERE team_id = ? AND disconnected_at IS NULL`
	args := []any{now, now, teamID}
	if onlyAccount != "" {
		q += ` AND account_id = ?`
		args = append(args, onlyAccount)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, fmt.Errorf("platformstore: disconnect stripe: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("platformstore: disconnect stripe: %w", err)
	}
	return n > 0, nil
}

// TeamStripeByAccount reads the live Connect row holding a Stripe account; ok=false when none does.
func (s *Store) TeamStripeByAccount(ctx context.Context, accountID string) (TeamStripe, bool, error) {
	if accountID == "" {
		return TeamStripe{}, false, nil
	}
	var st TeamStripe
	err := s.db.GetContext(ctx, &st, `SELECT `+teamStripeCols()+` FROM team_stripe
WHERE account_id = ? AND kind = 'connect' AND disconnected_at IS NULL`, accountID)
	if err != nil {
		if isNoRows(err) {
			return TeamStripe{}, false, nil
		}
		return TeamStripe{}, false, fmt.Errorf("platformstore: team stripe by account: %w", err)
	}
	return st, true, nil
}

// LiveStripeTeams lists the teams whose Stripe is live (not disconnected); kind "" = both kinds.
func (s *Store) LiveStripeTeams(ctx context.Context, kind string) ([]int64, error) {
	q := `SELECT team_id FROM team_stripe WHERE disconnected_at IS NULL`
	var args []any
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	var out []int64
	if err := s.db.SelectContext(ctx, &out, q+` ORDER BY team_id`, args...); err != nil {
		return nil, fmt.Errorf("platformstore: live stripe teams: %w", err)
	}
	return out, nil
}
