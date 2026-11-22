package shop

import (
	"context"

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

// Revalidator routes a revalidation by the team in ctx: the storefront team (and a ctx with no team)
// goes to the grbpwr storefront exactly as before; any other team's open shop is revalidated at its
// main address under its shop key; a team with no open shop is a no-op.
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
	if sf <= 0 {
		sf = tenant.TeamOneID
	}
	if !ok || t.ID == sf || r.Shop == nil || r.Target == nil {
		if ok && t.ID != sf {
			return nil // a team shop without the shop path wired: never the grbpwr storefront
		}
		return r.Base.RevalidateAll(ctx, data)
	}
	host, open, err := r.Target(ctx, t.ID)
	if err != nil || !open {
		return err
	}
	return r.Shop.RevalidateShop(ctx, host, Key(t.ID), data)
}
