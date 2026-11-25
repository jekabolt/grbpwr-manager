package shop

import (
	"context"
	"slices"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Store is what the shop service reads from the control plane (platformstore.Store).
type Store interface {
	TeamShop(ctx context.Context, teamID int64) (platformstore.TeamShop, error)
	ShopHosts(ctx context.Context, teamID int64) ([]platformstore.ShopHost, error)
	TeamBrand(ctx context.Context, teamID int64) (platformstore.TeamBrand, bool, error)
	TeamStripe(ctx context.Context, teamID int64) (platformstore.TeamStripe, bool, error)
}

// StorefrontPack is the code pack the shared storefront draws the storefront team (grbpwr) with.
const StorefrontPack = "grbpwr"

// Profile is what a storefront draws a shop with (frontend ShopProfile).
type Profile struct {
	ShopKey, Name, LogoURL, PrimaryHost               string
	Hosts                                             []string // live, primary first
	Open                                              bool
	LegalEntity, LegalAddress, ContactEmail, Website  string
	StripePublishableKey, StripeAccountID, StripeMode string
	// Pack is the storefront's code pack for this shop: StorefrontPack for the storefront team,
	// "" (the shared template alone) for every other team.
	Pack string
}

// Service answers shop questions over the control plane.
type Service struct {
	St           Store
	StorefrontID int64
	// LogoURL is the public logo URL of a team ("" = none).
	LogoURL func(ctx context.Context, teamID int64, slug string) string
	// StorefrontStripe answers the storefront team's publishable key and mode ("live" / "test")
	// from its env config, as its checkout runs now; nil or "" = no checkout key. The storefront
	// team never reads team_stripe.
	StorefrontStripe func(ctx context.Context) (publishable, mode string)
	// AdminHosts are the admin and API hosts among the storefront team's legacy hosts: never a shop
	// address, so a storefront never sees them (Profile leaves them out).
	AdminHosts []string
}

// storefront reports whether teamID is the storefront team.
func (s *Service) storefront(teamID int64) bool {
	return s.StorefrontID > 0 && teamID == s.StorefrontID
}

// PrimaryHost is a team's main address ("" when it has none).
func (s *Service) PrimaryHost(ctx context.Context, teamID int64) (string, error) {
	hosts, err := s.St.ShopHosts(ctx, teamID)
	if err != nil {
		return "", err
	}
	for _, h := range hosts {
		if h.Primary {
			return h.Host, nil
		}
	}
	return "", nil
}

// RevalidateTarget is the main address of a team's shop while it is on and not closed (Target).
// The storefront team has no shop switch (it is always on the shared storefront): its main address
// is the target whatever team_shop says.
func (s *Service) RevalidateTarget(ctx context.Context, teamID int64) (string, bool, error) {
	if s.storefront(teamID) {
		h, err := s.PrimaryHost(ctx, teamID)
		return h, h != "", err
	}
	ts, err := s.St.TeamShop(ctx, teamID)
	if err != nil {
		return "", false, err
	}
	if !ts.Enabled || ts.ClosedByOperator {
		return "", false, nil
	}
	h, err := s.PrimaryHost(ctx, teamID)
	return h, h != "", err
}

// Profile builds the storefront profile of team t's shop. open is the edge's answer (ShopOpen).
func (s *Service) Profile(ctx context.Context, t tenant.Team, open bool) (Profile, error) {
	p := Profile{ShopKey: Key(t.ID), Name: t.Name, Open: open}
	b, ok, err := s.St.TeamBrand(ctx, t.ID)
	if err != nil {
		return Profile{}, err
	}
	if ok {
		if b.BrandName != "" {
			p.Name = b.BrandName
		}
		p.LegalEntity, p.ContactEmail, p.Website = b.LegalEntity, b.ContactEmail, b.Website
		for i, l := range b.LegalAddress {
			if i > 0 {
				p.LegalAddress += ", "
			}
			p.LegalAddress += l
		}
	}
	hosts, err := s.St.ShopHosts(ctx, t.ID)
	if err != nil {
		return Profile{}, err
	}
	for _, h := range hosts {
		switch {
		case slices.Contains(s.AdminHosts, h.Host):
			// An admin or API host is never shown to a storefront.
		case h.Primary:
			p.PrimaryHost = h.Host
			p.Hosts = append([]string{h.Host}, p.Hosts...)
		case h.Status == platformstore.ShopHostLive && h.RedirectTo == "":
			// A redirect ("www." of a bare domain) never serves the shop itself.
			p.Hosts = append(p.Hosts, h.Host)
		}
	}
	if s.LogoURL != nil {
		p.LogoURL = s.LogoURL(ctx, t.ID, t.Slug)
	}
	if s.storefront(t.ID) {
		p.Pack = StorefrontPack
		if s.StorefrontStripe != nil {
			p.StripePublishableKey, p.StripeMode = s.StorefrontStripe(ctx)
			if p.StripePublishableKey == "" {
				p.StripeMode = ""
			}
		}
		return p, nil
	}
	st, ok, err := s.St.TeamStripe(ctx, t.ID)
	if err != nil {
		return Profile{}, err
	}
	if ok && st.DisconnectedAt == nil && st.ChargesEnabled {
		p.StripeMode = st.Mode
		switch st.Kind {
		case "connect":
			// Direct charges: Stripe.js loads with the platform key and the connected account (L3
			// fills the platform publishable key in the handler).
			if st.AccountID != nil {
				p.StripeAccountID = *st.AccountID
			}
		case "keys":
			p.StripePublishableKey = st.PublishableKey
		}
	}
	return p, nil
}
