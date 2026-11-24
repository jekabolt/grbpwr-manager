package app

import (
	"context"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/config"
)

// The storefront's own hosts (legacy shop hosts, reserved for every team) come from its origins:
// no dev, preview or IP host, no duplicate.
func TestShopOwnHosts(t *testing.T) {
	got := shopOwnHosts(
		[]string{"https://www.grbpwr.com", "https://grbpwr.com/", "http://localhost:3000", "https://x.vercel.app", "http://127.0.0.1:8080", "https://WWW.grbpwr.com"},
		[]string{"https://beta.grbpwr.com", "nonsense"},
	)
	want := []string{"www.grbpwr.com", "grbpwr.com", "beta.grbpwr.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("own hosts %v, want %v", got, want)
	}
}

// One master for shop customer keys and shop revalidation secrets fails the boot.
func TestWireShopsRefusesOneMaster(t *testing.T) {
	c := &config.Config{}
	c.Revalidation.ShopMaster = "same-master-same-master-same-master"
	c.StorefrontAuth.ShopMaster = "same-master-same-master-same-master"
	a := &App{c: c}
	if err := a.wireShops(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("one master for both accepted")
	}
	c.Revalidation.ShopMaster = "another-master-another-master-xx"
	if err := a.wireShops(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("two masters: %v", err)
	}
}
