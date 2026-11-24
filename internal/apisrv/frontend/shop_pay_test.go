package frontend

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type namedInvoicer struct {
	dependency.Invoicer
	name string
}

type fakeShopPayments struct {
	asked []int64
	procs map[int64]dependency.Invoicer
	pk    string
}

func (f *fakeShopPayments) ConnectPublishableKey() string { return f.pk }

func (f *fakeShopPayments) For(_ context.Context, t tenant.Team, _ entity.PaymentMethodName) (dependency.Invoicer, error) {
	f.asked = append(f.asked, t.ID)
	if p, ok := f.procs[t.ID]; ok {
		return p, nil
	}
	return nil, status.Error(codes.FailedPrecondition, MsgShopPaymentsNotReady)
}

// TestTeamShopPaysThroughItsOwnStripe: the storefront team keeps exactly its env processors and is
// never sent to team payments; every other team gets its own processor, never the storefront's.
func TestTeamShopPaysThroughItsOwnStripe(t *testing.T) {
	live, test := &namedInvoicer{name: "storefront live"}, &namedInvoicer{name: "storefront test"}
	team7 := &namedInvoicer{name: "team 7"}
	pay := &fakeShopPayments{procs: map[int64]dependency.Invoicer{7: team7}}
	s := shopAuthServer(t, testShopMaster)
	s.stripePayment, s.stripePaymentTest = live, test
	s.shops.d.Payments = pay
	at := func(id int64) context.Context { return tenant.With(context.Background(), tenant.Team{ID: id}) }

	for pm, want := range map[entity.PaymentMethodName]dependency.Invoicer{entity.CARD: live, entity.CARD_TEST: test} {
		h, err := s.getPaymentHandler(at(1), pm)
		if err != nil || h != want {
			t.Fatalf("storefront team %s: %v %v", pm, h, err)
		}
		if h, err := s.getPaymentHandler(context.Background(), pm); err != nil || h != want {
			t.Fatalf("no team %s: %v %v", pm, h, err)
		}
	}
	if len(pay.asked) != 0 {
		t.Fatalf("the storefront team asked team payments: %v", pay.asked)
	}
	if h, err := s.getPaymentHandler(at(7), entity.CARD_TEST); err != nil || h != team7 {
		t.Fatalf("team 7: %v %v", h, err)
	}
	if h, err := s.getPaymentHandler(at(8), entity.CARD); status.Convert(err).Message() != MsgShopPaymentsNotReady || h == live || h == test {
		t.Fatalf("team 8 without Stripe: %v %v", h, err)
	}
	s.shops.d.Payments = nil
	if _, err := s.getPaymentHandler(at(7), entity.CARD); status.Convert(err).Message() != MsgShopPaymentsNotReady {
		t.Fatalf("team payments unwired: %v", err)
	}
}

// TestShopProfileStripe: a connected shop loads Stripe.js with the platform key and its account, an
// own-keys shop with its own key; a shop that cannot take a payment here gets nothing.
func TestShopProfileStripe(t *testing.T) {
	pay := &fakeShopPayments{pk: "pk_test_platform"}
	connect := shop.Profile{StripeAccountID: "acct_1", StripeMode: "test"}
	keys := shop.Profile{StripePublishableKey: "pk_test_own", StripeMode: "test"}
	check := func(name string, p ShopPayments, prof shop.Profile, acct, pk, mode string) {
		t.Helper()
		a, k, m := shopStripeOf(p, prof)
		if a != acct || k != pk || m != mode {
			t.Fatalf("%s: %q %q %q", name, a, k, m)
		}
	}
	check("connect", pay, connect, "acct_1", "pk_test_platform", "test")
	check("keys", pay, keys, "", "pk_test_own", "test")
	check("no stripe", pay, shop.Profile{}, "", "", "")
	check("unwired", nil, keys, "", "", "")
	pay.pk = ""
	check("connect off", pay, connect, "", "", "")
	check("keys with connect off", pay, keys, "", "pk_test_own", "test")
}
