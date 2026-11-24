package app

import (
	"context"
	"log/slog"
	"time"

	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/shoppay"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// shopPayStopTimeout bounds how long Stop waits for the team processors' payment monitors.
const shopPayStopTimeout = 15 * time.Second

// newShopPay builds the team shops' Stripe (X18-L3): Stripe Connect and a team's own keys, its
// callback, webhooks and per-team processors. nil without a storefront API (no-shop installation):
// no team can take a payment there, so the owner sees stripe_not_ready. The storefront team keeps
// the env processors (StripePayment / StripePaymentTest) and never goes through this.
func (a *App) newShopPay(ctx context.Context, b *tenantbind.Binder, adminS *admin.Server, storefrontServed bool) *shoppay.Service {
	if a.platform == nil || !storefrontServed {
		return nil
	}
	cfg := a.c.Platform
	fd := shoppay.StripeFactoryDeps{Repo: a.db, Mailer: a.ma, InvoiceTTL: a.c.StripePayment.InvoiceExpiration}
	if a.rm != nil {
		// Assigned only when non-nil: a nil *Manager inside the interface would pass nil checks.
		fd.Reservations = a.rm
	}
	svc := shoppay.New(ctx, shoppay.Deps{
		Config:     a.c.ShopStripe,
		APIBase:    cfg.APIBase(),
		BaseDomain: cfg.BaseDomain,
		Store:      a.platform,
		Teams:      b,
		Factory:    shoppay.NewStripeFactory(fd),
		Changed:    adminS.ShopPaymentsChanged,
	})
	switch {
	case a.c.ShopStripe.Master == "":
		slog.Default().WarnContext(ctx, "shops: SHOP_STRIPE_MASTER is empty; team shops cannot take payments")
	case !a.c.ShopStripe.ConnectReady():
		slog.Default().InfoContext(ctx, "shops: Stripe Connect is not configured; teams can use their own keys only")
	}
	a.hs.SetShopStripeHandlers(httpapi.ShopStripeHandlers{Callback: svc.Callback(), Connect: svc.ConnectWebhook(), Team: svc.TeamWebhook()})
	a.shopPay = svc
	return svc
}

// startShopPay warms the live teams' processors (unpaid orders' monitors, account sync). Called once
// the admin server has the control plane: a sync redraws a shop through it.
func (a *App) startShopPay(ctx context.Context) {
	if a.shopPay != nil {
		a.shopPay.Start(ctx)
	}
}

// stopShopPay stops the team processors' payment monitors (before the databases close).
func (a *App) stopShopPay(ctx context.Context) {
	if a.shopPay == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, shopPayStopTimeout)
	defer cancel()
	a.shopPay.Stop(sctx)
}

// shopPayments and frontendShopPayments keep a nil service a nil interface (a typed nil would pass
// the servers' nil checks).
func shopPayments(svc *shoppay.Service) admin.ShopPayments {
	if svc == nil {
		return nil
	}
	return svc
}

func frontendShopPayments(svc *shoppay.Service) frontend.ShopPayments {
	if svc == nil {
		return nil
	}
	return svc
}
