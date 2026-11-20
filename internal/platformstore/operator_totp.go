package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Operator console second step (wave teams, gap 4): after the password an operator types a code from
// an authenticator app (TOTP). The rows live here; the crypto (sealing the secret, checking a code,
// hashing recovery codes) is platformauth's, handed in as a judge that runs inside the transaction.

// Kinds of an operator_second_step ticket.
const (
	SecondStepCode  = "code"  // the operator has an app: type its code (or a recovery code)
	SecondStepSetup = "setup" // no app yet: the mailed code first, then confirm a new app
)

// Events of an operator_alert letter.
const (
	OperatorEventSetup    = "setup"    // an operator set up an authenticator app
	OperatorEventReset    = "reset"    // an operator's app was removed (console or server reset)
	OperatorEventRecovery = "recovery" // an operator signed in with a recovery code
)

// Answers of the second-step store calls.
var (
	// ErrSecondStepLocked: too many wrong codes in a row; nothing is judged until LockedUntil.
	ErrSecondStepLocked = errors.New("platformstore: second step locked")
	// ErrNotOperator: the email names no active operator.
	ErrNotOperator = errors.New("platformstore: not an operator")
	// ErrCodeWrong: the code did not match (the counters were written).
	ErrCodeWrong = errors.New("platformstore: wrong code")
	// ErrAppUnreadable: the operator's sealed app secret no longer opens (the pepper was rotated).
	// Answered by the judge; JudgeSecondStep then drops the app and answers it back.
	ErrAppUnreadable = errors.New("platformstore: operator app unreadable")
)

// OperatorTOTP is an operator's confirmed authenticator.
type OperatorTOTP struct {
	UserID       int64        `db:"user_id"`
	SecretSealed []byte       `db:"secret_sealed"`
	ConfirmedAt  time.Time    `db:"confirmed_at"`
	LastStep     int64        `db:"last_step"`
	FailedCount  int          `db:"failed_count"`
	LockedUntil  sql.NullTime `db:"locked_until"`
}

// RecoveryCode is one unused recovery code row.
type RecoveryCode struct {
	ID       int64  `db:"id"`
	CodeHash string `db:"code_hash"`
}

// SecondStepTicket is the row behind a ticket.
type SecondStepTicket struct {
	TicketHash          string         `db:"ticket_hash"`
	UserID              int64          `db:"user_id"`
	CredGen             int64          `db:"cred_gen"`
	Kind                string         `db:"kind"`
	MailCodeHash        sql.NullString `db:"mail_code_hash"`
	MailOKAt            sql.NullTime   `db:"mail_ok_at"`
	PendingSecretSealed []byte         `db:"pending_secret_sealed"`
	Attempts            int            `db:"attempts"`
	ExpiresAt           time.Time      `db:"expires_at"`
	UsedAt              sql.NullTime   `db:"used_at"`
}

const secondStepColumns = `ticket_hash, user_id, cred_gen, kind, mail_code_hash, mail_ok_at, pending_secret_sealed,
attempts, expires_at, used_at`

// SecondStepPolicy bounds guessing.
type SecondStepPolicy struct {
	TicketTTL          time.Duration // life of a ticket (password → console)
	MaxTicketAttempts  int           // wrong codes on one ticket before it is burned
	MaxFailures        int           // wrong codes in a row on the account before the lock
	LockFor            time.Duration // the lock
	MaxSetupLettersPer int           // setup letters per account per hour
}

// StartSecondStep is the input of StartOperatorSecondStep.
type StartSecondStep struct {
	UserID     int64
	CredGen    int64
	TicketHash string
	// MailCodeHash and SetupLetter are used only when the operator has no app yet (kind setup):
	// the letter carrying the code goes to the account's stored address.
	MailCodeHash string
	SetupLetter  Letter
	Now          time.Time
}

// StartedSecondStep says what the ticket is.
type StartedSecondStep struct {
	Kind string
	// LetterQueued: kind setup and the hourly cap allowed a new letter (false: the code of an earlier
	// letter of this hour is not valid on this ticket, so the operator signs in again later).
	LetterQueued bool
}

// StartOperatorSecondStep opens a ticket for an operator whose password was just checked. Every older
// unused ticket of the account is burned (one live ticket per account: guesses never spread over
// several). The kind is decided under the operator_totp row lock.
func (s *Store) StartOperatorSecondStep(ctx context.Context, p StartSecondStep, pol SecondStepPolicy) (StartedSecondStep, error) {
	var res StartedSecondStep
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res = StartedSecondStep{}
		u, err := shareUserByID(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if u.Status != UserActive || !u.IsOperator || u.CredGen != p.CredGen {
			return ErrTokenInvalid
		}
		has := 0
		if err := tx.GetContext(ctx, &has, `SELECT COUNT(*) FROM operator_totp WHERE user_id = ? FOR UPDATE`, u.ID); err != nil {
			return fmt.Errorf("platformstore: operator totp: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operator_second_step SET used_at = ? WHERE user_id = ? AND used_at IS NULL`,
			p.Now, u.ID); err != nil {
			return fmt.Errorf("platformstore: burn tickets: %w", err)
		}
		kind := SecondStepCode
		var mailHash any
		if has == 0 {
			kind = SecondStepSetup
			if !u.Email.Valid || u.Email.String == "" {
				return ErrTokenInvalid
			}
			n, err := setupLettersSinceTx(ctx, tx, u.Email.String, p.Now.Add(-time.Hour))
			if err != nil {
				return err
			}
			if n < pol.MaxSetupLettersPer {
				l := p.SetupLetter
				l.To = u.Email.String
				l.Template = "operator_setup"
				if err := insertLetterTx(ctx, tx, l, p.Now); err != nil {
					return err
				}
				mailHash = p.MailCodeHash
				res.LetterQueued = true
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO operator_second_step
(ticket_hash, user_id, cred_gen, kind, mail_code_hash, attempts, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, 0, ?, ?)`, p.TicketHash, u.ID, u.CredGen, kind, mailHash, p.Now, p.Now.Add(pol.TicketTTL)); err != nil {
			return fmt.Errorf("platformstore: insert ticket: %w", err)
		}
		res.Kind = kind
		return nil
	})
	return res, err
}

func setupLettersSinceTx(ctx context.Context, tx *sqlx.Tx, email string, since time.Time) (int, error) {
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM platform_email
WHERE to_email = ? AND template = 'operator_setup' AND created_at > ?`, email, since); err != nil {
		return 0, fmt.Errorf("platformstore: count setup letters: %w", err)
	}
	return n, nil
}

// SecondStepState is what a judge sees: the locked ticket, the operator, the app (nil: none yet)
// and the unused recovery codes.
type SecondStepState struct {
	Ticket   SecondStepTicket
	User     User
	TOTP     *OperatorTOTP
	Recovery []RecoveryCode
	Now      time.Time
}

// SecondStepVerdict is what the judge decided; the store writes it in the same transaction.
type SecondStepVerdict struct {
	// Wrong: the code did not match. The ticket's attempts and the account's failures grow; at the
	// limits the ticket is burned and the account locked.
	Wrong bool

	// MailOK (setup, stage 1): the mailed code matched; PendingSecretSealed is the new app secret.
	MailOK              bool
	PendingSecretSealed []byte

	// Enroll (setup, stage 2): the pending secret is confirmed by an app code of step EnrollStep;
	// RecoveryHashes replace every older recovery code.
	Enroll         bool
	EnrollStep     int64
	RecoveryHashes []string

	// AcceptStep (code): the app code of this step was accepted (> last_step, checked here again).
	AcceptStep int64
	// UseRecoveryID (code): this recovery code was used.
	UseRecoveryID int64

	// SessionSID: on success the console session to open (with Enroll, AcceptStep or UseRecoveryID).
	SessionSID string
}

// SecondStepOutcome is what happened.
type SecondStepOutcome struct {
	User User
	// RecoveryLeft: unused recovery codes after this call (meaningful on a session).
	RecoveryLeft int
	// Burned: this wrong code burned the ticket (the operator signs in again).
	Burned bool
}

// JudgeSecondStep locks the ticket (FOR UPDATE), the operator (FOR SHARE) and the app row, refuses a
// used, expired, foreign or stale ticket with ErrTokenInvalid and a locked account with
// ErrSecondStepLocked, then lets judge decide and writes its verdict. A Wrong verdict commits (the
// counters must survive) and the call returns ErrCodeWrong. A judge answering ErrAppUnreadable on a
// code ticket drops the app (committed) and the call returns ErrAppUnreadable.
func (s *Store) JudgeSecondStep(ctx context.Context, ticketHash, wantKind string, now time.Time, pol SecondStepPolicy,
	judge func(SecondStepState) (SecondStepVerdict, error)) (SecondStepOutcome, error) {
	var out SecondStepOutcome
	wrong, unreadable := false, false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		out, wrong, unreadable = SecondStepOutcome{}, false, false
		var t SecondStepTicket
		if err := tx.GetContext(ctx, &t, `SELECT `+secondStepColumns+` FROM operator_second_step WHERE ticket_hash = ? FOR UPDATE`,
			ticketHash); err != nil {
			if isNoRows(err) {
				return ErrTokenInvalid
			}
			return fmt.Errorf("platformstore: lock ticket: %w", err)
		}
		if t.UsedAt.Valid || !t.ExpiresAt.After(now) || t.Kind != wantKind || t.Attempts >= pol.MaxTicketAttempts {
			return ErrTokenInvalid
		}
		u, err := shareUserByID(ctx, tx, t.UserID)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if u.Status != UserActive || !u.IsOperator || u.CredGen != t.CredGen {
			return ErrTokenInvalid
		}
		st := SecondStepState{Ticket: t, User: u, Now: now}
		var row OperatorTOTP
		err = tx.GetContext(ctx, &row, `SELECT user_id, secret_sealed, confirmed_at, last_step, failed_count, locked_until
FROM operator_totp WHERE user_id = ? FOR UPDATE`, u.ID)
		switch {
		case err == nil:
			st.TOTP = &row
		case !isNoRows(err):
			return fmt.Errorf("platformstore: lock operator totp: %w", err)
		}
		// A code ticket needs an app; a setup ticket must not find one (another tab finished first).
		if (t.Kind == SecondStepCode) != (st.TOTP != nil) {
			return ErrTokenInvalid
		}
		if st.TOTP != nil && st.TOTP.LockedUntil.Valid && st.TOTP.LockedUntil.Time.After(now) {
			return ErrSecondStepLocked
		}
		if err := tx.SelectContext(ctx, &st.Recovery, `SELECT id, code_hash FROM operator_recovery_code
WHERE user_id = ? AND used_at IS NULL ORDER BY id`, u.ID); err != nil {
			return fmt.Errorf("platformstore: recovery codes: %w", err)
		}
		v, err := judge(st)
		if errors.Is(err, ErrAppUnreadable) && st.TOTP != nil {
			// The sealed secret no longer opens (the pepper was rotated): the app counts as never
			// set up. The drop commits; the next sign-in is a first setup behind a mailed code.
			out.User, unreadable = u, true
			return dropOperatorAppTx(ctx, tx, u.ID, now)
		}
		if err != nil {
			return err
		}
		out.User = u
		if v.Wrong {
			wrong = true
			return s.secondStepWrongTx(ctx, tx, &t, st.TOTP, now, pol, &out)
		}
		return s.secondStepApplyTx(ctx, tx, st, v, now, &out)
	})
	if err == nil && wrong {
		return out, ErrCodeWrong
	}
	if err == nil && unreadable {
		return out, ErrAppUnreadable
	}
	return out, err
}

func (s *Store) secondStepWrongTx(ctx context.Context, tx *sqlx.Tx, t *SecondStepTicket, app *OperatorTOTP, now time.Time,
	pol SecondStepPolicy, out *SecondStepOutcome) error {
	attempts := t.Attempts + 1
	var used any
	if attempts >= pol.MaxTicketAttempts {
		used = now
		out.Burned = true
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operator_second_step SET attempts = ?, used_at = COALESCE(used_at, ?)
WHERE ticket_hash = ?`, attempts, used, t.TicketHash); err != nil {
		return fmt.Errorf("platformstore: ticket attempt: %w", err)
	}
	if app == nil {
		return nil
	}
	fails := app.FailedCount + 1
	var lock any
	if fails >= pol.MaxFailures {
		lock, fails = now.Add(pol.LockFor), 0
		// The lock burns the ticket as well: after it the operator starts from the password.
		if _, err := tx.ExecContext(ctx, `UPDATE operator_second_step SET used_at = COALESCE(used_at, ?) WHERE user_id = ?`,
			now, app.UserID); err != nil {
			return fmt.Errorf("platformstore: burn tickets: %w", err)
		}
		out.Burned = true
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operator_totp SET failed_count = ?, locked_until = COALESCE(?, locked_until)
WHERE user_id = ?`, fails, lock, app.UserID); err != nil {
		return fmt.Errorf("platformstore: operator failures: %w", err)
	}
	return nil
}

func (s *Store) secondStepApplyTx(ctx context.Context, tx *sqlx.Tx, st SecondStepState, v SecondStepVerdict, now time.Time,
	out *SecondStepOutcome) error {
	t, u := st.Ticket, st.User
	switch {
	case v.MailOK:
		if t.Kind != SecondStepSetup || t.MailOKAt.Valid || len(v.PendingSecretSealed) == 0 {
			return ErrTokenInvalid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operator_second_step SET mail_ok_at = ?, pending_secret_sealed = ?, attempts = 0
WHERE ticket_hash = ?`, now, v.PendingSecretSealed, t.TicketHash); err != nil {
			return fmt.Errorf("platformstore: setup mail ok: %w", err)
		}
		return nil
	case v.Enroll:
		if t.Kind != SecondStepSetup || !t.MailOKAt.Valid || len(t.PendingSecretSealed) == 0 || len(v.RecoveryHashes) == 0 {
			return ErrTokenInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO operator_totp (user_id, secret_sealed, confirmed_at, last_step, failed_count)
VALUES (?, ?, ?, ?, 0)`, u.ID, t.PendingSecretSealed, now, v.EnrollStep); err != nil {
			return fmt.Errorf("platformstore: enroll operator totp: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM operator_recovery_code WHERE user_id = ?`, u.ID); err != nil {
			return fmt.Errorf("platformstore: drop recovery codes: %w", err)
		}
		for _, h := range v.RecoveryHashes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO operator_recovery_code (user_id, code_hash, created_at) VALUES (?, ?, ?)`,
				u.ID, h, now); err != nil {
				return fmt.Errorf("platformstore: recovery code: %w", err)
			}
		}
		out.RecoveryLeft = len(v.RecoveryHashes)
		if err := operatorAlertTx(ctx, tx, OperatorEventSetup, u.Email.String, "", now); err != nil {
			return err
		}
	case v.AcceptStep > 0:
		if st.TOTP == nil || v.AcceptStep <= st.TOTP.LastStep {
			return ErrCodeWrong
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operator_totp SET last_step = ?, failed_count = 0, locked_until = NULL
WHERE user_id = ? AND last_step < ?`, v.AcceptStep, u.ID, v.AcceptStep); err != nil {
			return fmt.Errorf("platformstore: accept step: %w", err)
		}
		out.RecoveryLeft = len(st.Recovery)
	case v.UseRecoveryID > 0:
		r, err := tx.ExecContext(ctx, `UPDATE operator_recovery_code SET used_at = ? WHERE id = ? AND user_id = ? AND used_at IS NULL`,
			now, v.UseRecoveryID, u.ID)
		if err != nil {
			return fmt.Errorf("platformstore: use recovery code: %w", err)
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrCodeWrong
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operator_totp SET failed_count = 0, locked_until = NULL WHERE user_id = ?`, u.ID); err != nil {
			return fmt.Errorf("platformstore: clear failures: %w", err)
		}
		out.RecoveryLeft = len(st.Recovery) - 1
		if err := operatorAlertTx(ctx, tx, OperatorEventRecovery, u.Email.String, "", now); err != nil {
			return err
		}
	default:
		return ErrTokenInvalid
	}
	if v.SessionSID == "" {
		return ErrTokenInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operator_second_step SET used_at = ? WHERE ticket_hash = ?`, now, t.TicketHash); err != nil {
		return fmt.Errorf("platformstore: consume ticket: %w", err)
	}
	return createSessionTx(ctx, tx, v.SessionSID, u.ID, 0, SessionConsole, "", u.CredGen, now)
}

// operatorAlertPayload is the operator_alert letter: what happened (event), to whose app (email) and,
// for a console reset, which operator did it (by).
type operatorAlertPayload struct {
	Event string `json:"event"`
	Email string `json:"email"`
	By    string `json:"from,omitempty"`
}

// operatorAlertTx queues one operator_alert letter to every active operator address.
func operatorAlertTx(ctx context.Context, tx *sqlx.Tx, event, whose, by string, now time.Time) error {
	var to []string
	if err := tx.SelectContext(ctx, &to, `SELECT email FROM platform_user
WHERE is_operator = 1 AND status = 'active' AND email IS NOT NULL ORDER BY id`); err != nil {
		return fmt.Errorf("platformstore: operators: %w", err)
	}
	b, err := json.Marshal(operatorAlertPayload{Event: event, Email: whose, By: by})
	if err != nil {
		return fmt.Errorf("platformstore: alert payload: %w", err)
	}
	for _, addr := range to {
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_email (to_email, template, payload, status, created_at)
VALUES (?, 'operator_alert', ?, 'pending', ?)`, addr, string(b), now); err != nil {
			return fmt.Errorf("platformstore: enqueue operator alert: %w", err)
		}
	}
	return nil
}

// OperatorSecondStepRow is one operator as the console lists them.
type OperatorSecondStepRow struct {
	UserID       int64          `db:"id"`
	Email        sql.NullString `db:"email"`
	ConfirmedAt  sql.NullTime   `db:"confirmed_at"`
	RecoveryLeft int            `db:"recovery_left"`
}

// ListOperatorSecondSteps lists active operators with their app state.
func (s *Store) ListOperatorSecondSteps(ctx context.Context) ([]OperatorSecondStepRow, error) {
	var rows []OperatorSecondStepRow
	if err := s.db.SelectContext(ctx, &rows, `SELECT u.id, u.email, t.confirmed_at,
 (SELECT COUNT(*) FROM operator_recovery_code r WHERE r.user_id = u.id AND r.used_at IS NULL) AS recovery_left
FROM platform_user u LEFT JOIN operator_totp t ON t.user_id = u.id
WHERE u.is_operator = 1 AND u.status = 'active' ORDER BY u.email`); err != nil {
		return nil, fmt.Errorf("platformstore: list operators: %w", err)
	}
	return rows, nil
}

// ResetOperatorSecondStep removes the app and recovery codes of the operator with this email, burns
// their tickets, revokes their console and team sessions (returned, for the edge cache) and burns
// their unused hand-off codes. by is the email
// of the operator who did it ("" = the server-side reset). Every operator gets an alert letter. The
// next sign-in of that operator sets an app up again behind a mailed code. ErrNotOperator when the
// email names no active operator.
func (s *Store) ResetOperatorSecondStep(ctx context.Context, email, by string, now time.Time) ([]string, error) {
	var sids []string
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		sids = nil
		u, err := lockUserByEmail(ctx, tx, email)
		if errors.Is(err, ErrUserNotFound) {
			return ErrNotOperator
		}
		if err != nil {
			return err
		}
		if u.Status != UserActive || !u.IsOperator {
			return ErrNotOperator
		}
		if err := dropOperatorAppTx(ctx, tx, u.ID, now); err != nil {
			return err
		}
		// Every console and team session of the operator (an operator has no memberships, so each
		// team session is a team entry) and every unused hand-off code: nothing the lost phone helped
		// open outlives the reset.
		if sids, err = endOperatorAccessTx(ctx, tx, []int64{u.ID}, now); err != nil {
			return err
		}
		return operatorAlertTx(ctx, tx, OperatorEventReset, u.Email.String, by, now)
	})
	return sids, err
}

// endOperatorAccessTx revokes every live console and team session of these users and burns their
// unused hand-off codes. It returns the revoked session ids (for the edge cache).
func endOperatorAccessTx(ctx context.Context, tx *sqlx.Tx, userIDs []int64, now time.Time) ([]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var sids []string
	q, args, err := sqlx.In(`SELECT sid FROM user_session
WHERE user_id IN (?) AND revoked_at IS NULL AND aud IN ('console', 'team') FOR UPDATE`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("platformstore: operator sessions: %w", err)
	}
	if err := tx.SelectContext(ctx, &sids, q, args...); err != nil {
		return nil, fmt.Errorf("platformstore: operator sessions: %w", err)
	}
	if len(sids) > 0 {
		q, args, err := sqlx.In(`UPDATE user_session SET revoked_at = ? WHERE sid IN (?)`, now, sids)
		if err != nil {
			return nil, fmt.Errorf("platformstore: revoke operator sessions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return nil, fmt.Errorf("platformstore: revoke operator sessions: %w", err)
		}
	}
	q, args, err = sqlx.In(`UPDATE email_token SET used_at = ?
WHERE user_id IN (?) AND purpose = 'handoff' AND used_at IS NULL`, now, userIDs)
	if err != nil {
		return nil, fmt.Errorf("platformstore: burn hand-off codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return nil, fmt.Errorf("platformstore: burn hand-off codes: %w", err)
	}
	return sids, nil
}

// secondStepFlag is the platform_flag row that remembers the last PLATFORM_OPERATOR_SECOND_STEP a
// boot applied.
const secondStepFlag = "operator_second_step"

// ApplyOperatorSecondStepSwitch records PLATFORM_OPERATOR_SECOND_STEP at boot. When it turns on (the
// stored value is not "on" yet) every console session, every team session and unused hand-off code
// of an operator, and every unused second-step ticket end in the same transaction: nothing issued
// without the second step survives the switch. Off only records "off", so a later switch-on ends them
// again. It returns the revoked session ids and whether this call turned the switch on.
func (s *Store) ApplyOperatorSecondStepSwitch(ctx context.Context, on bool, now time.Time) ([]string, bool, error) {
	var sids []string
	turnedOn := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		sids, turnedOn = nil, false
		var cur []string
		if err := tx.SelectContext(ctx, &cur, `SELECT value FROM platform_flag WHERE name = ? FOR UPDATE`, secondStepFlag); err != nil {
			return fmt.Errorf("platformstore: second step flag: %w", err)
		}
		want := "off"
		if on {
			want = "on"
		}
		if len(cur) == 1 && cur[0] == want {
			return nil
		}
		if on {
			var ids []int64
			if err := tx.SelectContext(ctx, &ids, `SELECT id FROM platform_user WHERE is_operator <> 0
UNION SELECT DISTINCT user_id FROM user_session WHERE aud = 'console' AND revoked_at IS NULL`); err != nil {
				return fmt.Errorf("platformstore: operators: %w", err)
			}
			var err error
			if sids, err = endOperatorAccessTx(ctx, tx, ids, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operator_second_step SET used_at = ? WHERE used_at IS NULL`, now); err != nil {
				return fmt.Errorf("platformstore: burn tickets: %w", err)
			}
			turnedOn = true
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_flag (name, value, updated_at) VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = VALUES(updated_at)`, secondStepFlag, want, now); err != nil {
			return fmt.Errorf("platformstore: second step flag: %w", err)
		}
		return nil
	})
	return sids, turnedOn, err
}

// dropOperatorAppTx removes the app, recovery codes and live tickets of one operator (the next
// sign-in sets an app up again behind a mailed code).
func dropOperatorAppTx(ctx context.Context, tx *sqlx.Tx, userID int64, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM operator_totp WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("platformstore: drop operator totp: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM operator_recovery_code WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("platformstore: drop recovery codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operator_second_step SET used_at = ? WHERE user_id = ? AND used_at IS NULL`,
		now, userID); err != nil {
		return fmt.Errorf("platformstore: burn tickets: %w", err)
	}
	return nil
}
