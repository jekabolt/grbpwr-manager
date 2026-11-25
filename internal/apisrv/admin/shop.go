package admin

import (
	"context"
	"errors"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"log/slog"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The shop screen (X18): a team turns its shop on (it gets {slug}.<shop zone>), attaches its own
// domains, picks the main address, sets the sender of its letters. Payments (Stripe Connect or own
// keys) are in shop_stripe.go (lane X18-L3). Teams mode only; every
// change is an owner's (or an entered operator's).

// ShopWiring is what the shop screen needs beside the control plane.
type ShopWiring struct {
	// Domains attaches custom domains to the multi-shop storefront (Vercel, lane L5; a fake until then).
	Domains shop.DomainProvider
	// Invalidate drops this instance's cached answers about a team's shop (edge, CORS).
	Invalidate func(teamID int64)
	// Revalidator redraws a team's own shop (shop.Revalidator); nil = team shops are not revalidated.
	Revalidator dependency.RevalidationService
	// Reserved are domains no team may attach, nor any host under them: the storefront's own and
	// legacy hosts and their registrable domains (grbpwr.com, lekalo.studio, ...). The base domain,
	// the shop zone and the API host are refused with their registrable domains anyway.
	Reserved []string
	// Payments is the team shops' Stripe (shoppay.Service, lane X18-L3); nil = Stripe RPCs answer
	// stripe_not_ready and team-shop card payments are refused.
	Payments ShopPayments
	// NoPrimary are legacy hosts of the storefront team that are never its main address (admin and
	// API hosts): the shared storefront would send grbpwr's customers there.
	NoPrimary []string
	// StorefrontHosts are the storefront team's configured shop hosts (TENANT_STOREFRONT_HOSTS): the
	// only hosts its shop screen lists and the only ones it may make its main address.
	StorefrontHosts []string
}

// SetShops wires the shop screen (teams mode; call before serving).
func (s *Server) SetShops(w ShopWiring) { s.shopW = &w }

// Shop statuses (Shop.status, PlatformTeam.shop).
const (
	shopOff         = "off"
	shopOpen        = "open"
	shopClosed      = "closed"
	shopCheckoutOff = "checkout_off"
)

func (s *Server) storefrontID() int64 {
	if id := s.platform.cfg.StorefrontTeamID; id > 0 {
		return id
	}
	if s.platform.cfg.NoShop() {
		return 0
	}
	return tenant.TeamOneID
}

// shopCaller resolves the caller's team for the shop screen; owner=true also demands an owner.
func (s *Server) shopCaller(ctx context.Context, owner bool) (peopleCtx, error) {
	if !s.teamsOn() || s.shopW == nil {
		return peopleCtx{}, errTeamsOff()
	}
	if owner {
		return s.changer(ctx)
	}
	return s.people(ctx)
}

func shopErr(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, platformstore.ErrShopHostTaken), errors.Is(err, shop.ErrDomainTaken):
		return status.Error(codes.AlreadyExists, "host_taken")
	case errors.Is(err, platformstore.ErrShopHostNotFound):
		return status.Error(codes.NotFound, "host_not_found")
	case errors.Is(err, platformstore.ErrShopHostManaged):
		return status.Error(codes.FailedPrecondition, "host_managed")
	case errors.Is(err, platformstore.ErrShopHostNotLive):
		return status.Error(codes.FailedPrecondition, "host_not_live")
	case errors.Is(err, platformstore.ErrShopHostLimit):
		return status.Error(codes.ResourceExhausted, "host_limit")
	case errors.Is(err, platformstore.ErrShopHostRedirect):
		return status.Error(codes.FailedPrecondition, "host_redirect")
	case errors.Is(err, shop.ErrBadHost):
		return status.Error(codes.InvalidArgument, "bad_host")
	}
	if _, ok := status.FromError(err); ok && err != nil {
		return err
	}
	slog.Default().ErrorContext(ctx, "shop: "+op, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "shop")
}

// shopChanged runs after every change: this instance's caches, and a profile redraw of the shop.
func (s *Server) shopChanged(ctx context.Context, teamID int64) {
	if s.shopW.Invalidate != nil {
		s.shopW.Invalidate(teamID)
	}
	s.revalidateAsync(ctx, &dto.RevalidationData{Profile: true})
}

func (s *Server) shopZone() string { return s.platform.cfg.ShopZoneHost() }

// shopView builds the screen's Shop of team t (eff = its effective features).
func (s *Server) shopView(ctx context.Context, t tenant.Team, eff []feature.Key) (*pb_admin.Shop, error) {
	st := s.platform.store
	out := &pb_admin.Shop{Mail: &pb_admin.ShopMail{}, Payments: &pb_admin.ShopPayments{Method: "none", Status: "none"}}
	if t.ID == s.storefrontID() {
		// The storefront team (grbpwr): always on, its env keys and mail; its hosts are the legacy ones,
		// one of which the owner may make its main address (grbpwr.com on the shared storefront).
		// Only its configured storefront hosts are offered, never an admin or API host (NoPrimary).
		out.Enabled, out.Status = true, shopOpen
		out.Payments.Method, out.Payments.Status = "platform", "active"
		hosts, err := st.ShopHosts(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		var hide, offer []string
		if s.shopW != nil {
			hide, offer = s.shopW.NoPrimary, s.shopW.StorefrontHosts
		}
		for _, h := range hosts {
			if h.Primary {
				out.PrimaryHost = h.Host
			} else if slices.Contains(hide, h.Host) || !slices.Contains(offer, h.Host) {
				continue
			}
			out.Domains = append(out.Domains, shopDomainPb(h))
		}
		return out, nil
	}
	ts, err := st.TeamShop(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	hosts, err := st.ShopHosts(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	sub := shop.Subdomain(t.Slug, s.shopZone())
	if ts.Enabled && sub != "" && !slices.ContainsFunc(hosts, func(h platformstore.ShopHost) bool { return h.Host == sub }) {
		// A rename moved the slug: the platform address follows it.
		if err := st.EnsureShopSubdomain(ctx, t.ID, sub, time.Now()); err != nil {
			return nil, err
		}
		if s.shopW.Invalidate != nil {
			s.shopW.Invalidate(t.ID)
		}
		if hosts, err = st.ShopHosts(ctx, t.ID); err != nil {
			return nil, err
		}
	}
	out.Enabled, out.ClosedByOperator, out.Address = ts.Enabled, ts.ClosedByOperator, sub
	out.Mail.FromName, out.Mail.ReplyTo = ts.MailFromName, ts.MailReplyTo
	for _, h := range hosts {
		d := shopDomainPb(h)
		if h.Primary {
			out.PrimaryHost = h.Host
		}
		out.Domains = append(out.Domains, d)
	}
	for _, k := range []feature.Key{feature.Products, feature.Orders} {
		if !slices.Contains(eff, k) {
			out.MissingFeatures = append(out.MissingFeatures, string(k))
		}
	}
	stripe, ok, err := st.TeamStripe(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	payable := false
	if ok {
		out.Payments = shopPaymentsPb(stripe)
		payable = out.Payments.Status == "active"
	}
	s.shopWebhookURL(t, out.Payments, stripe, ok)
	switch {
	case !ts.Enabled:
		out.Status = shopOff
	case ts.ClosedByOperator || len(out.MissingFeatures) > 0 || t.Status != tenant.StatusActive:
		out.Status = shopClosed
	case !payable:
		out.Status = shopCheckoutOff
	default:
		out.Status = shopOpen
	}
	return out, nil
}

func tsPb(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func shopDomainPb(h platformstore.ShopHost) *pb_admin.ShopDomain {
	d := &pb_admin.ShopDomain{Host: h.Host, Kind: h.Kind, Status: h.Status, Primary: h.Primary, Managed: h.Managed,
		RedirectTo: h.RedirectTo, VerifiedAt: tsPb(h.VerifiedAt), CheckedAt: tsPb(h.CheckedAt)}
	for _, r := range h.Records {
		d.Records = append(d.Records, &pb_admin.ShopDnsRecord{Type: r.Type, Name: r.Name, Value: r.Value, Found: r.Found})
	}
	return d
}

func shopPaymentsPb(st platformstore.TeamStripe) *pb_admin.ShopPayments {
	p := &pb_admin.ShopPayments{Method: st.Kind, AccountName: st.AccountName, ChargesEnabled: st.ChargesEnabled,
		PayoutsEnabled: st.PayoutsEnabled, DetailsSubmitted: st.DetailsSubmitted, Mode: st.Mode, KeyLast4: st.KeyLast4}
	switch {
	case st.DisconnectedAt != nil:
		p.Status = "disconnected"
	case st.ChargesEnabled:
		p.Status = "active"
	case st.DetailsSubmitted:
		p.Status = "restricted"
	default:
		p.Status = "onboarding"
	}
	return p
}

func (s *Server) shopOf(ctx context.Context, p peopleCtx) (*pb_admin.Shop, error) {
	out, err := s.shopView(ctx, p.w.Team, p.w.Features)
	if err != nil {
		return nil, shopErr(ctx, "view", err)
	}
	return out, nil
}

// GetShop answers the caller's team's shop.
func (s *Server) GetShop(ctx context.Context, _ *pb_admin.GetShopRequest) (*pb_admin.GetShopResponse, error) {
	p, err := s.shopCaller(ctx, false)
	if err != nil {
		return nil, err
	}
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.GetShopResponse{Shop: out}, nil
}

// legacyShop refuses changes to the storefront team's shop (configured by env): its switch, domains,
// payments and mail. Its main address is the one thing the owner picks (SetShopPrimaryHost).
func (s *Server) legacyShop(p peopleCtx) error {
	if p.w.Team.ID == s.storefrontID() {
		return status.Error(codes.FailedPrecondition, "shop_legacy")
	}
	return nil
}

// SetShopEnabled turns the team's shop on (its platform address appears) or off.
func (s *Server) SetShopEnabled(ctx context.Context, req *pb_admin.SetShopEnabledRequest) (*pb_admin.SetShopEnabledResponse, error) {
	p, err := s.shopCaller(ctx, true)
	if err != nil {
		return nil, err
	}
	if err := s.legacyShop(p); err != nil {
		return nil, err
	}
	sub := shop.Subdomain(p.w.Team.Slug, s.shopZone())
	if req.GetEnabled() && sub == "" {
		return nil, status.Error(codes.FailedPrecondition, "shop_zone_unset")
	}
	if err := s.platform.store.SetShopEnabled(ctx, p.w.Team.ID, req.GetEnabled(), sub, p.userID, time.Now()); err != nil {
		return nil, shopErr(ctx, "enable", err)
	}
	action := "shop.disabled"
	if req.GetEnabled() {
		action = "shop.enabled"
	}
	s.peopleAudit(ctx, p, action, map[string]string{"address": sub})
	s.shopChanged(ctx, p.w.Team.ID)
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetShopEnabledResponse{Shop: out}, nil
}

// customHost normalizes a domain a team attaches and refuses one that is the platform's or the
// storefront's: the base domain, the shop zone, the API host, every reserved domain, and every host
// under the registrable domain of any of them (www.grbpwr.com, api.lekalo.studio, ...).
func (s *Server) customHost(raw string) (string, error) {
	h := shop.NormHost(raw)
	cfg := s.platform.cfg
	own := []string{cfg.BaseDomain, s.shopZone(), shop.OriginHost(cfg.APIBase())}
	if s.shopW != nil {
		own = append(own, s.shopW.Reserved...)
	}
	refused := append(append([]string{}, own...), shop.RegistrableDomains(own...)...)
	if err := shop.ValidCustomHost(h, refused...); err != nil {
		return "", err
	}
	return h, nil
}

func shopRecords(rs []shop.Record) []platformstore.ShopRecord {
	out := make([]platformstore.ShopRecord, 0, len(rs))
	for _, r := range rs {
		out = append(out, platformstore.ShopRecord{Type: r.Type, Name: r.Name, Value: r.Value, Found: r.Found})
	}
	return out
}

func validShopStatus(st string) string {
	switch st {
	case platformstore.ShopHostRecordsNeeded, platformstore.ShopHostVerifying, platformstore.ShopHostLive, platformstore.ShopHostFailed:
		return st
	}
	return platformstore.ShopHostFailed
}

func (s *Server) domainOf(ctx context.Context, teamID int64, host string) (*pb_admin.ShopDomain, error) {
	h, err := s.platform.store.ShopHost(ctx, teamID, host)
	if err != nil {
		return nil, shopErr(ctx, "domain", err)
	}
	return shopDomainPb(h), nil
}

// AttachShopDomain adds the team's own domain and answers the DNS records it needs.
func (s *Server) AttachShopDomain(ctx context.Context, req *pb_admin.AttachShopDomainRequest) (*pb_admin.AttachShopDomainResponse, error) {
	p, err := s.shopCaller(ctx, true)
	if err != nil {
		return nil, err
	}
	if err := s.legacyShop(p); err != nil {
		return nil, err
	}
	host, err := s.customHost(req.GetHost())
	if err != nil {
		return nil, shopErr(ctx, "attach", err)
	}
	// The host must be free (in any status: a pending attach holds its name for a while) before the
	// provider hears of it. A queued provider removal of it stays queued until the store attach
	// commits (AttachShopHost drops it), so a failed attach never loses it.
	if err := s.platform.store.ShopHostFree(ctx, p.w.Team.ID, host, time.Now()); err != nil {
		return nil, shopErr(ctx, "attach", err)
	}
	if cur, err := s.platform.store.ShopHost(ctx, p.w.Team.ID, host); err == nil && cur.RedirectTo != "" {
		return nil, shopErr(ctx, "attach", platformstore.ErrShopHostRedirect)
	}
	state, err := s.shopW.Domains.Attach(ctx, host)
	if errors.Is(err, shop.ErrDomainTaken) || errors.Is(err, shop.ErrBadHost) {
		return nil, shopErr(ctx, "attach", err)
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "shop: domain provider attach", slog.String("err", err.Error()))
		return nil, status.Error(codes.Unavailable, "domain_provider")
	}
	if err := s.platform.store.AttachShopHost(ctx, p.w.Team.ID, host, validShopStatus(state.Status), shopRecords(state.Records), time.Now()); err != nil {
		return nil, shopErr(ctx, "attach", err)
	}
	s.peopleAudit(ctx, p, "shop.domain_attached", map[string]string{"host": host})
	s.attachWWW(ctx, p, host)
	s.shopChanged(ctx, p.w.Team.ID)
	d, err := s.domainOf(ctx, p.w.Team.ID, host)
	if err != nil {
		return nil, err
	}
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.AttachShopDomainResponse{Shop: out, Domain: d}, nil
}

// attachWWW gives a bare domain its "www." sibling redirecting to it, when that host is free (no
// other team's, not the team's own shop address). Best effort: a failure leaves the domain without it,
// and attaching the domain again tries again.
func (s *Server) attachWWW(ctx context.Context, p peopleCtx, apex string) {
	if reg := shop.RegistrableDomains(apex); len(reg) != 1 || reg[0] != apex {
		return // a subdomain ("shop.brand.com") gets no sibling
	}
	www, err := s.customHost("www." + apex)
	if err != nil {
		return
	}
	st, teamID, now := s.platform.store, p.w.Team.ID, time.Now()
	if cur, err := st.ShopHost(ctx, teamID, www); err == nil && cur.RedirectTo != apex {
		return // the team's own shop address: left alone
	}
	if err := st.ShopHostFree(ctx, teamID, www, now); err != nil {
		return // another team's
	}
	state, err := s.shopW.Domains.AttachRedirect(ctx, www, apex)
	if err != nil {
		slog.Default().WarnContext(ctx, "shop: www redirect at the domain provider", slog.String("host", www), slog.String("err", err.Error()))
		return
	}
	if err := st.AttachShopRedirect(ctx, teamID, www, apex, validShopStatus(state.Status), shopRecords(state.Records), now); err != nil {
		slog.Default().WarnContext(ctx, "shop: www redirect", slog.String("host", www), slog.String("err", err.Error()))
		return
	}
	s.peopleAudit(ctx, p, "shop.domain_attached", map[string]string{"host": www, "redirect_to": apex})
}

// CheckShopDomain asks the provider again whether the domain's records are in place.
func (s *Server) CheckShopDomain(ctx context.Context, req *pb_admin.CheckShopDomainRequest) (*pb_admin.CheckShopDomainResponse, error) {
	p, err := s.shopCaller(ctx, true)
	if err != nil {
		return nil, err
	}
	host := shop.NormHost(req.GetHost())
	cur, err := s.platform.store.ShopHost(ctx, p.w.Team.ID, host)
	if err != nil {
		return nil, shopErr(ctx, "check", err)
	}
	if cur.Kind == platformstore.ShopHostCustom {
		state, err := s.shopW.Domains.Check(ctx, host)
		if err != nil {
			slog.Default().ErrorContext(ctx, "shop: domain provider check", slog.String("err", err.Error()))
			return nil, status.Error(codes.Unavailable, "domain_provider")
		}
		if _, err := s.platform.store.SetShopHostStatus(ctx, p.w.Team.ID, host, validShopStatus(state.Status), shopRecords(state.Records), time.Now()); err != nil {
			return nil, shopErr(ctx, "check", err)
		}
		if state.Status != cur.Status {
			s.shopChanged(ctx, p.w.Team.ID)
		}
	}
	d, err := s.domainOf(ctx, p.w.Team.ID, host)
	if err != nil {
		return nil, err
	}
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.CheckShopDomainResponse{Shop: out, Domain: d}, nil
}

// RemoveShopDomain detaches the team's own domain (the platform address stays).
func (s *Server) RemoveShopDomain(ctx context.Context, req *pb_admin.RemoveShopDomainRequest) (*pb_admin.RemoveShopDomainResponse, error) {
	p, err := s.shopCaller(ctx, true)
	if err != nil {
		return nil, err
	}
	host := shop.NormHost(req.GetHost())
	gone, err := s.platform.store.RemoveShopHostAll(ctx, p.w.Team.ID, host)
	if err != nil {
		return nil, shopErr(ctx, "remove", err)
	}
	// The removals are queued (shop_domain_drop) with the rows (the domain and its "www." redirect);
	// try them now, the drain retries a failure.
	for _, h := range gone {
		if _, err := s.platform.store.DropShopDomainNow(ctx, h, time.Now(), s.shopW.Domains.Remove); err != nil {
			slog.Default().WarnContext(ctx, "shop: domain provider remove; queued for retry", slog.String("err", err.Error()))
		}
	}
	s.peopleAudit(ctx, p, "shop.domain_removed", map[string]string{"host": host})
	s.shopChanged(ctx, p.w.Team.ID)
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.RemoveShopDomainResponse{Shop: out}, nil
}

// SetShopPrimaryHost makes a live address the shop's main one. The storefront team picks one of its
// configured storefront hosts (never an admin or API host, never a CORS-only origin), or clears it
// with an empty host: grbpwr then revalidates only the old storefront.
func (s *Server) SetShopPrimaryHost(ctx context.Context, req *pb_admin.SetShopPrimaryHostRequest) (*pb_admin.SetShopPrimaryHostResponse, error) {
	p, err := s.shopCaller(ctx, true)
	if err != nil {
		return nil, err
	}
	host := shop.NormHost(req.GetHost())
	sf := p.w.Team.ID == s.storefrontID()
	action := "shop.primary_set"
	switch {
	case sf && host == "":
		action, err = "shop.primary_cleared", s.platform.store.ClearShopPrimary(ctx, p.w.Team.ID)
	case sf && slices.Contains(s.shopW.NoPrimary, host):
		return nil, status.Error(codes.FailedPrecondition, "host_admin")
	case sf && !slices.Contains(s.shopW.StorefrontHosts, host):
		return nil, status.Error(codes.FailedPrecondition, "host_not_storefront")
	default:
		err = s.platform.store.SetShopPrimary(ctx, p.w.Team.ID, host)
	}
	if err != nil {
		return nil, shopErr(ctx, "primary", err)
	}
	s.peopleAudit(ctx, p, action, map[string]string{"host": host})
	s.shopChanged(ctx, p.w.Team.ID)
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetShopPrimaryHostResponse{Shop: out}, nil
}

const (
	shopFromNameMax = 64
	shopReplyToMax  = 254
)

// SetShopMail stores the sender name and reply-to address of the shop's letters.
func (s *Server) SetShopMail(ctx context.Context, req *pb_admin.SetShopMailRequest) (*pb_admin.SetShopMailResponse, error) {
	p, err := s.shopCaller(ctx, true)
	if err != nil {
		return nil, err
	}
	if err := s.legacyShop(p); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetFromName())
	if len([]rune(name)) > shopFromNameMax || strings.ContainsAny(name, "\r\n<>\"") {
		return nil, status.Error(codes.InvalidArgument, "bad_from_name")
	}
	reply := strings.ToLower(strings.TrimSpace(req.GetReplyTo()))
	if reply != "" {
		a, err := mail.ParseAddress(reply)
		if err != nil || a.Address != reply || len(reply) > shopReplyToMax || !strings.Contains(reply[strings.LastIndex(reply, "@")+1:], ".") {
			return nil, status.Error(codes.InvalidArgument, "bad_reply_to")
		}
	}
	if err := s.platform.store.SetShopMail(ctx, p.w.Team.ID, name, reply, p.userID); err != nil {
		return nil, shopErr(ctx, "mail", err)
	}
	s.peopleAudit(ctx, p, "shop.mail_set", map[string]string{})
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetShopMailResponse{Shop: out}, nil
}
