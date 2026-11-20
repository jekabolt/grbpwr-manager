package platformauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Device RPCs of the calling person (P0 design §3.3). A device is a session chain: a root session
// and the team sessions handed off from it, or a team session opened on its own.

const (
	// MaxSessionReadsPerMin bounds ListMySessions per account.
	MaxSessionReadsPerMin = 60
	// MaxSessionWritesPerHr bounds RevokeMySession and RevokeMyOtherSessions per account.
	MaxSessionWritesPerHr = 30
	// maxDeviceKeyLen bounds RevokeMySession's key (16 hex characters are issued).
	maxDeviceKeyLen = 64
	// userAgentHeader is the browser's User-Agent as the gateway forwards it to the gRPC handler.
	userAgentHeader = "grpcgateway-user-agent"
)

// Device answers. Built per call: the gate counts package-level values.
func errThisDevice() error     { return status.Error(codes.FailedPrecondition, "this_device") }
func errDeviceNotFound() error { return status.Error(codes.NotFound, "not_found") }

// withSessionClient puts the request's device on ctx, so a session opened with it records the
// browser's User-Agent and the client IP (the same IP the audit takes).
func withSessionClient(ctx context.Context) context.Context {
	c := platformstore.SessionClient{IP: middleware.GetClientIP(ctx)}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(userAgentHeader); len(v) > 0 {
			c.UserAgent = v[0]
		}
	}
	return platformstore.WithSessionClient(ctx, c)
}

// liveness is when a session stops counting as a live device: a root session after
// RootSessionTTL, a team session after its token's lifetime (team tokens are never renewed).
func (s *Service) liveness(now time.Time) platformstore.SessionLiveness {
	return platformstore.SessionLiveness{RootNotBefore: now.Add(-RootSessionTTL), TeamNotBefore: now.Add(-s.d.TeamTTL)}
}

// touch moves the session's last seen time (at most every platformstore.TouchEvery); a failure
// only costs the device list some freshness.
func (s *Service) touch(ctx context.Context, sid string) {
	if err := s.d.Store.TouchSession(ctx, sid, s.now()); err != nil {
		s.log(ctx, "touch session", err)
	}
}

// deviceKey is the opaque key of a device chain (P0 §3.3: the first 16 hex of sha256 of the chain's
// sid), recomputed on every call: it names the chain without carrying its sid.
func deviceKey(chain string) string {
	sum := sha256.Sum256([]byte(chain))
	return hex.EncodeToString(sum[:8])
}

// UserAgentBrowser names the browser of a User-Agent: chrome, safari, firefox, edge, opera, or
// browser when it is none of them (or unknown).
func UserAgentBrowser(ua string) string {
	switch {
	case containsAny(ua, "Edg/", "EdgA/", "EdgiOS/", "Edge/"):
		return "edge"
	case containsAny(ua, "OPR/", "OPiOS/", "Opera"):
		return "opera"
	case containsAny(ua, "Firefox/", "FxiOS/"):
		return "firefox"
	case containsAny(ua, "CriOS/", "Chrome/", "Chromium/"):
		return "chrome"
	case strings.Contains(ua, "Safari/"):
		return "safari"
	default:
		return "browser"
	}
}

// UserAgentOS names the system of a User-Agent: macos, windows, ios, android, linux, or "" when
// unknown.
func UserAgentOS(ua string) string {
	switch {
	case containsAny(ua, "iPhone", "iPad", "iPod"):
		return "ios"
	case strings.Contains(ua, "Android"):
		return "android"
	case strings.Contains(ua, "Windows"):
		return "windows"
	case containsAny(ua, "Macintosh", "Mac OS X"):
		return "macos"
	case containsAny(ua, "Linux", "X11", "CrOS"):
		return "linux"
	default:
		return ""
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// device is one chain being folded from its live sessions.
type device struct {
	chain    string
	ua       string
	uaRoot   bool // ua came from the chain's own first session
	ip       string
	lastSeen time.Time
}

// devices folds live sessions (oldest first) into devices: the browser and system from the chain's
// first session (else its oldest session that has one), the ip of its newest session that has one,
// last seen the latest of all. The caller's chain comes first, then the most recently seen.
func devices(rows []platformstore.LiveSession, thisChain string) []*auth.MySession {
	byChain := map[string]*device{}
	var order []*device
	for _, r := range rows {
		d := byChain[r.Chain]
		if d == nil {
			d = &device{chain: r.Chain}
			byChain[r.Chain] = d
			order = append(order, d)
		}
		if r.UserAgent != "" && (r.SID == r.Chain || (d.ua == "" && !d.uaRoot)) {
			d.ua, d.uaRoot = r.UserAgent, r.SID == r.Chain
		}
		if r.IP != "" {
			d.ip = r.IP
		}
		if r.LastSeenAt.After(d.lastSeen) {
			d.lastSeen = r.LastSeenAt
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if (a.chain == thisChain) != (b.chain == thisChain) {
			return a.chain == thisChain
		}
		if !a.lastSeen.Equal(b.lastSeen) {
			return a.lastSeen.After(b.lastSeen)
		}
		return deviceKey(a.chain) < deviceKey(b.chain)
	})
	out := make([]*auth.MySession, 0, len(order))
	for _, d := range order {
		out = append(out, &auth.MySession{
			Key:        deviceKey(d.chain),
			Browser:    UserAgentBrowser(d.ua),
			Os:         UserAgentOS(d.ua),
			Ip:         d.ip,
			LastSeenAt: timestamppb.New(d.lastSeen),
			ThisDevice: d.chain == thisChain,
		})
	}
	return out
}

// ListMySessions lists the calling person's live devices, this one first.
func (s *Service) ListMySessions(ctx context.Context, token string, _ *auth.ListMySessionsRequest) (*auth.ListMySessionsResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if !s.sessionRead.Allow(c.key()) {
		return nil, errBusy()
	}
	rows, err := s.d.Store.LiveSessions(ctx, c.UserID, s.liveness(s.now()))
	if err != nil {
		s.log(ctx, "list sessions", err)
		return nil, errInternal()
	}
	return &auth.ListMySessionsResponse{Sessions: devices(rows, c.Chain)}, nil
}

// RevokeMySession signs one other device out: every session of its chain ends, with the codes its
// root session issued. this_device for the caller's own device (log out is SignOut with device),
// not_found for a key that names no live device of the caller.
func (s *Service) RevokeMySession(ctx context.Context, token string, req *auth.RevokeMySessionRequest) (*auth.RevokeMySessionResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetKey())
	if key == "" || len(key) > maxDeviceKeyLen {
		return nil, errDeviceNotFound()
	}
	if key == deviceKey(c.Chain) {
		return nil, errThisDevice()
	}
	if !s.sessionWrite.Allow(c.key()) {
		return nil, errBusy()
	}
	now := s.now()
	rows, err := s.d.Store.LiveSessions(ctx, c.UserID, s.liveness(now))
	if err != nil {
		s.log(ctx, "list sessions", err)
		return nil, errInternal()
	}
	chain := ""
	for _, r := range rows {
		if r.Chain != c.Chain && deviceKey(r.Chain) == key {
			chain = r.Chain
			break
		}
	}
	if chain == "" {
		return nil, errDeviceNotFound()
	}
	sids, err := s.d.Store.SignOutRoot(ctx, chain, c.UserID, now)
	if err != nil {
		s.log(ctx, "revoke device", err)
		return nil, errInternal()
	}
	s.revoked(ctx, sids)
	// A Google connect is bound to the account, not to a device: one the ended device started must
	// not finish (the caller's own, if any, starts again).
	if err := s.d.Store.ExpireLinkFlows(ctx, c.UserID, now); err != nil {
		s.log(ctx, "revoke device: link flows", err)
	}
	s.audit(ctx, c.UserID, c.TeamID, "device_signout", nil)
	return &auth.RevokeMySessionResponse{}, nil
}

// RevokeMyOtherSessions signs out every device but the caller's; signed_out counts the live ones.
func (s *Service) RevokeMyOtherSessions(ctx context.Context, token string, _ *auth.RevokeMyOtherSessionsRequest) (*auth.RevokeMyOtherSessionsResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if !s.sessionWrite.Allow(c.key()) {
		return nil, errBusy()
	}
	now := s.now()
	out, err := s.d.Store.SignOutOtherDevices(ctx, c.UserID, c.Chain, s.liveness(now), now)
	if err != nil {
		s.log(ctx, "revoke other devices", err)
		return nil, errInternal()
	}
	s.revoked(ctx, out.SIDs)
	s.audit(ctx, c.UserID, c.TeamID, "devices_signout_others", map[string]string{"devices": strconv.Itoa(out.Devices)})
	return &auth.RevokeMyOtherSessionsResponse{SignedOut: int32(out.Devices)}, nil
}

// signOutDevice ends the whole chain of a team token's session (the profile's log out): the root
// session it was handed off from and every team session of that chain. false when the session is
// not the user's (the caller falls back to ending the one session).
func (s *Service) signOutDevice(ctx context.Context, sid string, userID int64, now time.Time) bool {
	chain, ok, err := s.d.Store.SessionChain(ctx, sid, userID)
	if err != nil {
		s.log(ctx, "sign-out chain", err)
		return false
	}
	if !ok {
		return false
	}
	sids, err := s.d.Store.SignOutRoot(ctx, chain, userID, now)
	if err != nil {
		s.log(ctx, "sign-out device", err)
		return false
	}
	s.revoked(ctx, sids)
	return true
}
