package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jmoiron/sqlx"
)

// SessionActive reports whether the session exists and is not revoked. An unknown sid is not
// active (a token whose session row is gone is refused).
func (s *Store) SessionActive(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	var n int
	if err := s.db.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM user_session WHERE sid = ? AND revoked_at IS NULL`, sid); err != nil {
		return false, fmt.Errorf("platformstore: session: %w", err)
	}
	return n > 0, nil
}

// SessionChain is the device chain of one of the user's sessions, COALESCE(parent_sid, sid), revoked
// or not (a sign-out of the device ends what is left of it). ok=false when the user has no such
// session.
func (s *Store) SessionChain(ctx context.Context, sid string, userID int64) (string, bool, error) {
	if sid == "" || userID <= 0 {
		return "", false, nil
	}
	var chains []string
	if err := s.db.SelectContext(ctx, &chains, `SELECT COALESCE(parent_sid, sid) FROM user_session WHERE sid = ? AND user_id = ?`,
		sid, userID); err != nil {
		return "", false, fmt.Errorf("platformstore: session chain: %w", err)
	}
	if len(chains) == 0 {
		return "", false, nil
	}
	return chains[0], true, nil
}

// Devices (P0 §3.3): a device is a session chain, COALESCE(parent_sid, sid) — a root session and the
// team sessions handed off from it, or a team session opened on its own.

const (
	// maxUserAgentRunes and maxSessionIP are the widths of user_session.user_agent and ip.
	maxUserAgentRunes = 255
	maxSessionIP      = 45
	// TouchEvery: a session's last_seen_at moves at most this often.
	TouchEvery = 5 * time.Minute
	// touchesMax bounds the in-process map of recent touches.
	touchesMax = 50000
	// ClientRetention: a session dead longer than this forgets its ip and user agent.
	ClientRetention = 30 * 24 * time.Hour
	// ForgetBatch bounds one ForgetSessionClients statement (fewer cleared: nothing is left).
	ForgetBatch = 1000
)

type sessionClientKey struct{}

// SessionClient is the device a request comes from: its User-Agent and its client IP.
type SessionClient struct {
	UserAgent string
	IP        string
}

// WithSessionClient carries the request's device into the store: a session opened with this ctx
// records it.
func WithSessionClient(ctx context.Context, c SessionClient) context.Context {
	return context.WithValue(ctx, sessionClientKey{}, c)
}

// sessionClientArgs are the user_agent and ip values of a new session (NULL for unknown). The user
// agent becomes valid UTF-8 without control characters, cut to 255 characters (a longer one, as
// in-app browsers send, would fail a strict INSERT and with it the sign-in); an ip longer than 45
// bytes, or the "unknown" of a request without one, is not stored.
func sessionClientArgs(ctx context.Context) (ua, ip any) {
	c, _ := ctx.Value(sessionClientKey{}).(SessionClient)
	if v := cleanUserAgent(c.UserAgent); v != "" {
		ua = v
	}
	if v := strings.TrimSpace(c.IP); v != "" && v != "unknown" && len(v) <= maxSessionIP {
		ip = v
	}
	return ua, ip
}

func cleanUserAgent(raw string) string {
	raw = strings.ToValidUTF8(raw, "")
	var b strings.Builder
	n := 0
	for _, r := range raw {
		if n == maxUserAgentRunes {
			break
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// sessionTouches remembers when this process last moved each session's last_seen_at, so a session
// costs one UPDATE per TouchEvery per instance rather than one per edge-cache miss.
type sessionTouches struct {
	mu sync.Mutex
	at map[string]time.Time
}

func newSessionTouches() *sessionTouches { return &sessionTouches{at: map[string]time.Time{}} }

// due reports whether sid was not touched within TouchEvery and marks it touched now.
func (t *sessionTouches) due(sid string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.at[sid]; ok && now.Sub(last) < TouchEvery && !now.Before(last) {
		return false
	}
	if len(t.at) >= touchesMax {
		for k, last := range t.at {
			if now.Sub(last) >= TouchEvery {
				delete(t.at, k)
			}
		}
		if len(t.at) >= touchesMax {
			t.at = map[string]time.Time{}
		}
	}
	t.at[sid] = now
	return true
}

// TouchSession moves a live session's last_seen_at to now when it is TouchEvery or more behind.
// The edge calls it after a cache miss found the session live; the root host calls it on its
// own reads. An unknown or revoked sid changes nothing.
func (s *Store) TouchSession(ctx context.Context, sid string, now time.Time) error {
	if sid == "" {
		return nil
	}
	now = now.UTC()
	if s.touch != nil && !s.touch.due(sid, now) {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE user_session SET last_seen_at = ?
WHERE sid = ? AND revoked_at IS NULL AND last_seen_at < ?`, now, sid, now.Add(-TouchEvery)); err != nil {
		return fmt.Errorf("platformstore: touch session: %w", err)
	}
	return nil
}

// SessionLiveness is when an unrevoked session stops being live: a root session created at or
// before RootNotBefore (its absolute lifetime), a team session at or before TeamNotBefore (its
// token's lifetime; a team token is never renewed). Console sessions are never devices.
type SessionLiveness struct {
	RootNotBefore time.Time
	TeamNotBefore time.Time
}

func (l SessionLiveness) live(aud string, created time.Time) bool {
	switch aud {
	case SessionRoot:
		return created.After(l.RootNotBefore)
	case SessionTeam:
		return created.After(l.TeamNotBefore)
	default:
		return false
	}
}

// LiveSession is one live session of a device.
type LiveSession struct {
	SID        string
	Chain      string
	Aud        string
	CreatedAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	IP         string
}

type liveSessionRow struct {
	SID        string         `db:"sid"`
	Chain      string         `db:"chain"`
	Aud        string         `db:"aud"`
	CreatedAt  time.Time      `db:"created_at"`
	LastSeenAt time.Time      `db:"last_seen_at"`
	UserAgent  sql.NullString `db:"user_agent"`
	IP         sql.NullString `db:"ip"`
}

// LiveSessions lists the user's live root and team sessions (unrevoked, within l), oldest first.
func (s *Store) LiveSessions(ctx context.Context, userID int64, l SessionLiveness) ([]LiveSession, error) {
	if userID <= 0 {
		return nil, nil
	}
	var rows []liveSessionRow
	if err := s.db.SelectContext(ctx, &rows, `SELECT sid, COALESCE(parent_sid, sid) AS chain, aud, created_at, last_seen_at,
user_agent, ip FROM user_session
WHERE user_id = ? AND revoked_at IS NULL
  AND ((aud = 'root' AND created_at > ?) OR (aud = 'team' AND created_at > ?))
ORDER BY created_at, sid`, userID, l.RootNotBefore.UTC(), l.TeamNotBefore.UTC()); err != nil {
		return nil, fmt.Errorf("platformstore: live sessions: %w", err)
	}
	out := make([]LiveSession, 0, len(rows))
	for _, r := range rows {
		out = append(out, LiveSession{SID: r.SID, Chain: r.Chain, Aud: r.Aud, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt,
			UserAgent: r.UserAgent.String, IP: r.IP.String})
	}
	return out, nil
}

// DevicesSignedOut is a committed sign-out of devices: the ended sessions (for the edge cache) and
// how many live devices they were.
type DevicesSignedOut struct {
	SIDs    []string
	Devices int
}

// SignOutOtherDevices ends every session of the user outside the device chain keep, in one
// transaction: the account FOR UPDATE first (as SignOutRoot, so a hand-off or sign-in exchange,
// which holds it FOR SHARE, either commits before and its session is ended here, or starts after
// and finds its code spent), then revokeOtherDevicesTx. credential_gen does not move.
func (s *Store) SignOutOtherDevices(ctx context.Context, userID int64, keep string, l SessionLiveness, now time.Time) (DevicesSignedOut, error) {
	if userID <= 0 || strings.TrimSpace(keep) == "" {
		return DevicesSignedOut{}, errors.New("platformstore: signing out other devices needs the account and its device")
	}
	var out DevicesSignedOut
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockUserByID(ctx, tx, userID); err != nil {
			if errors.Is(err, ErrUserNotFound) {
				out = DevicesSignedOut{}
				return nil
			}
			return err
		}
		var err error
		out, err = revokeOtherDevicesTx(ctx, tx, userID, keep, l, now)
		return err
	})
	if err != nil {
		return DevicesSignedOut{}, err
	}
	return out, nil
}

// revokeOtherDevicesTx revokes every unrevoked session of the user whose chain is not keep (any
// audience, expired ones too), expires the unused hand-off and sign-in codes their root sessions
// issued, and ends every Google connect in flight (no session is recorded on one). Devices counts
// the chains that had a live session (l). Inside the caller's transaction, which holds the account
// FOR UPDATE.
func revokeOtherDevicesTx(ctx context.Context, tx *sqlx.Tx, userID int64, keep string, l SessionLiveness, now time.Time) (DevicesSignedOut, error) {
	now = now.UTC()
	var rows []struct {
		SID       string    `db:"sid"`
		Chain     string    `db:"chain"`
		Aud       string    `db:"aud"`
		CreatedAt time.Time `db:"created_at"`
	}
	if err := tx.SelectContext(ctx, &rows, `SELECT sid, COALESCE(parent_sid, sid) AS chain, aud, created_at FROM user_session
WHERE user_id = ? AND revoked_at IS NULL AND COALESCE(parent_sid, sid) <> ? FOR UPDATE`, userID, keep); err != nil {
		return DevicesSignedOut{}, fmt.Errorf("platformstore: other devices: %w", err)
	}
	var out DevicesSignedOut
	liveChains := map[string]bool{}
	for _, r := range rows {
		out.SIDs = append(out.SIDs, r.SID)
		if l.live(r.Aud, r.CreatedAt) {
			liveChains[r.Chain] = true
		}
	}
	out.Devices = len(liveChains)
	if len(rows) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE user_session SET revoked_at = ?
WHERE user_id = ? AND revoked_at IS NULL AND COALESCE(parent_sid, sid) <> ?`, now, userID, keep); err != nil {
			return DevicesSignedOut{}, fmt.Errorf("platformstore: revoke other devices: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ?
WHERE user_id = ? AND purpose IN ('handoff', 'signin') AND used_at IS NULL AND parent_sid IS NOT NULL AND parent_sid <> ?`,
		now, userID, keep); err != nil {
		return DevicesSignedOut{}, fmt.Errorf("platformstore: other devices: codes: %w", err)
	}
	if err := expireLinkFlowsTx(ctx, tx, userID, now); err != nil {
		return DevicesSignedOut{}, err
	}
	return out, nil
}

// ForgetSessionClients clears ip and user_agent of sessions dead for more than ClientRetention:
// revoked before now - ClientRetention, or never revoked and expired by then (a root session after
// rootTTL; team and console tokens live hours, so their creation counts). At most ForgetBatch rows
// per call; returns how many it cleared.
func (s *Store) ForgetSessionClients(ctx context.Context, now time.Time, rootTTL time.Duration) (int64, error) {
	cut := now.UTC().Add(-ClientRetention)
	r, err := s.db.ExecContext(ctx, `UPDATE user_session SET ip = NULL, user_agent = NULL
WHERE (ip IS NOT NULL OR user_agent IS NOT NULL)
  AND ((revoked_at IS NOT NULL AND revoked_at < ?)
    OR (revoked_at IS NULL AND ((aud = 'root' AND created_at <= ?) OR (aud <> 'root' AND created_at <= ?))))
LIMIT `+fmt.Sprint(ForgetBatch), cut, cut.Add(-rootTTL), cut)
	if err != nil {
		return 0, fmt.Errorf("platformstore: forget session clients: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("platformstore: forget session clients: %w", err)
	}
	return n, nil
}
