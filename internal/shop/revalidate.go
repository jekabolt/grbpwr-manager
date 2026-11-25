package shop

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// AllRevalidator is the storefront revalidator (dependency.RevalidationService).
type AllRevalidator interface {
	RevalidateAll(ctx context.Context, data *dto.RevalidationData) error
}

// HostRevalidator revalidates one shop at its main address (revalidation.Revalidator.RevalidateShop).
type HostRevalidator interface {
	RevalidateShop(ctx context.Context, host, shopKey string, data *dto.RevalidationData) error
}

// Target answers a team's shop main address when its shop is open (ok=false: nothing to revalidate).
type Target func(ctx context.Context, teamID int64) (host string, ok bool, err error)

// Revalidator routes a revalidation by the team in ctx: the storefront team goes to the shared
// storefront at its main address under its shop key once it has one (the owner sets grbpwr's main
// address when grbpwr.com moves there), and to the old grbpwr storefront (Base) until then; any other
// team's open shop is revalidated at its main address under its shop key; a team with no open shop
// is a no-op. It fails closed: a ctx with no team revalidates
// nothing (never the grbpwr storefront on a guess) and logs it. StorefrontID is the resolved
// storefront team (wireShops); <= 0 means the installation has none (TENANT_STOREFRONT_TEAM_ID=none),
// and then Base is never called.
type Revalidator struct {
	Base         AllRevalidator
	Shop         HostRevalidator
	Target       Target
	StorefrontID int64
}

// RevalidateAll implements dependency.RevalidationService.
func (r *Revalidator) RevalidateAll(ctx context.Context, data *dto.RevalidationData) error {
	t, ok := tenant.From(ctx)
	sf := r.StorefrontID
	switch {
	case !ok:
		slog.Default().WarnContext(ctx, "shop revalidate: no team in ctx; skipped")
		return nil
	case sf > 0 && t.ID == sf:
		return r.storefront(ctx, sf, data)
	case r.Shop == nil || r.Target == nil:
		return nil // a team shop without the shop path wired: never the grbpwr storefront
	}
	host, open, err := r.Target(ctx, t.ID)
	if err != nil || !open {
		return err
	}
	return r.Shop.RevalidateShop(ctx, host, Key(t.ID), data)
}

// storefront revalidates the storefront team: its main address on the shared storefront, or Base
// while it has none (or the shop path is not wired). A failed lookup falls back to Base, as before.
func (r *Revalidator) storefront(ctx context.Context, sf int64, data *dto.RevalidationData) error {
	if r.Shop != nil && r.Target != nil {
		host, ok, err := r.Target(ctx, sf)
		switch {
		case err != nil:
			slog.Default().WarnContext(ctx, "shop revalidate: storefront main address", slog.String("err", err.Error()))
		case ok:
			return r.Shop.RevalidateShop(ctx, host, Key(sf), data)
		}
	}
	if r.Base == nil {
		return nil
	}
	return r.Base.RevalidateAll(ctx, data)
}
