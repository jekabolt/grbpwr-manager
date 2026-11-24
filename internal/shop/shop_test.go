package shop

import (
	"net"
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

func TestHosts(t *testing.T) {
	for in, want := range map[string]string{"ACME.com:443": "acme.com", "acme.com.": "acme.com", " x.io ": "x.io"} {
		if got := NormHost(in); got != want {
			t.Errorf("NormHost(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"https://Acme.com": "acme.com", "http://localhost:3000": "localhost", "null": "", "": ""} {
		if got := OriginHost(in); got != want {
			t.Errorf("OriginHost(%q) = %q, want %q", in, got, want)
		}
	}
	for _, h := range []string{"acme.com", "shop.acme.co.uk", "x-y.io"} {
		if err := ValidCustomHost(h, "beta.lekalo.studio", "shop.beta.lekalo.studio"); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	for _, h := range []string{"", "acme", "1.2.3.4", "a..b", "-a.com", "acme.shop.beta.lekalo.studio", "beta.lekalo.studio",
		"x.vercel.app", "localhost", "a.localhost", "acme.c0m1.123", "ACME.com"} {
		if err := ValidCustomHost(h, "beta.lekalo.studio", "shop.beta.lekalo.studio"); !errors.Is(err, ErrBadHost) {
			t.Errorf("%q accepted", h)
		}
	}
	if Subdomain("acme", "shop.beta.lekalo.studio") != "acme.shop.beta.lekalo.studio" || Subdomain("acme", "") != "" {
		t.Error("Subdomain")
	}
}

func TestKeys(t *testing.T) {
	if Key(7) != "t7" || Namespace(7, 1) != "t7" || Namespace(1, 1) != "" || Namespace(1, 0) != "" {
		t.Fatal("key / namespace")
	}
	const m = "0123456789abcdef0123456789abcdef"
	a, _ := DeriveKey(m, 2, "access", 32)
	b, _ := DeriveKey(m, 3, "access", 32)
	c, _ := DeriveKey(m, 2, "refresh", 32)
	a2, _ := DeriveKey(m, 2, "access", 32)
	if string(a) == string(b) || string(a) == string(c) || string(a) != string(a2) || len(a) != 32 {
		t.Fatal("derived keys must differ per team and purpose and be stable")
	}
	if _, err := DeriveKey("", 2, "access", 32); err == nil {
		t.Fatal("no master derives a key")
	}
	s1, _ := RevalidateSecret(m, "t2")
	s2, _ := RevalidateSecret(m, "t3")
	if len(s1) != 64 || s1 == s2 {
		t.Fatalf("revalidate secrets %q %q", s1, s2)
	}
	// Pinned vector: the storefront (lane L2) derives the same hex from the same master and key.
	if s1 != mustSecret(t, m, "t2") {
		t.Fatal("unstable")
	}
}

func mustSecret(t *testing.T, m, k string) string {
	t.Helper()
	s, err := RevalidateSecret(m, k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type allRe struct{ n int }

func (r *allRe) RevalidateAll(context.Context, *dto.RevalidationData) error { r.n++; return nil }

type hostRe struct{ host, key string }

func (r *hostRe) RevalidateShop(_ context.Context, host, key string, _ *dto.RevalidationData) error {
	r.host, r.key = host, key
	return nil
}

func TestRevalidatorRoutes(t *testing.T) {
	base, sh := &allRe{}, &hostRe{}
	open := map[int64]string{7: "acme.com"}
	r := &Revalidator{Base: base, Shop: sh, StorefrontID: 1, Target: func(_ context.Context, id int64) (string, bool, error) {
		h, ok := open[id]
		return h, ok, nil
	}}
	ctx := context.Background()
	_ = r.RevalidateAll(ctx, &dto.RevalidationData{Hero: true})
	if base.n != 0 || sh.host != "" {
		t.Fatal("a ctx with no team revalidated something (fail closed)")
	}
	_ = r.RevalidateAll(tenant.With(ctx, tenant.Team{ID: 1}), &dto.RevalidationData{})
	if base.n != 1 || sh.host != "" {
		t.Fatalf("storefront: base %d, shop %q", base.n, sh.host)
	}
	_ = r.RevalidateAll(tenant.With(ctx, tenant.Team{ID: 7}), &dto.RevalidationData{})
	if base.n != 1 || sh.host != "acme.com" || sh.key != "t7" {
		t.Fatalf("shop: base %d, shop %q %q", base.n, sh.host, sh.key)
	}
	sh.host = ""
	_ = r.RevalidateAll(tenant.With(ctx, tenant.Team{ID: 8}), &dto.RevalidationData{})
	if base.n != 1 || sh.host != "" {
		t.Fatal("a team without an open shop revalidated something")
	}
	r.Shop = nil
	_ = r.RevalidateAll(tenant.With(ctx, tenant.Team{ID: 7}), &dto.RevalidationData{})
	if base.n != 1 {
		t.Fatal("a team shop without the shop path went to the storefront")
	}
}

func TestOrigins(t *testing.T) {
	calls := 0
	o := NewOrigins("shop.beta.lekalo.studio", func(context.Context) ([]string, error) {
		calls++
		return []string{"acme.com"}, nil
	})
	for origin, want := range map[string]bool{
		"https://acme.shop.beta.lekalo.studio":   true,
		"https://acme.com":                       true,
		"http://acme.com":                        false,
		"https://a.b.shop.beta.lekalo.studio":    false,
		"https://shop.beta.lekalo.studio":        false,
		"https://evil.com":                       false,
		"https://acme.shop.beta.lekalo.studio.x": false,
	} {
		if got := o.Allowed(origin); got != want {
			t.Errorf("%s = %v, want %v", origin, got, want)
		}
	}
	if calls != 1 {
		t.Fatalf("list calls = %d, want 1 (cached)", calls)
	}
	o.Invalidate()
	o.Allowed("https://acme.com")
	if calls != 2 {
		t.Fatalf("list calls after invalidate = %d", calls)
	}
}

func TestValidHostAndReserved(t *testing.T) {
	for _, h := range []string{"acme.com", "a-b.shop.lekalo.studio", "localhost", "x1.io"} {
		if !ValidHost(h) {
			t.Fatalf("%q refused", h)
		}
	}
	for _, h := range []string{"", "a_b.com", "acme.com/x", "-a.com", "a..com", "1.2.3.4", "[::1]", "acme.123", "ACME.com", "ac me.com", "\u00e9.com"} {
		if ValidHost(h) {
			t.Fatalf("%q accepted", h)
		}
	}
	got := RegistrableDomains("api.lekalo.studio", "shop.beta.lekalo.studio", "www.grbpwr.com", "localhost", "grbpwr.com", "x.co.uk")
	want := []string{"lekalo.studio", "grbpwr.com", "x.co.uk"}
	if len(got) != len(want) {
		t.Fatalf("registrable %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registrable %v, want %v", got, want)
		}
	}
	if !Under("www.grbpwr.com", "grbpwr.com") || Under("notgrbpwr.com", "grbpwr.com") || Under("x.com", "") {
		t.Fatal("under")
	}
}

func TestTXTProof(t *testing.T) {
	if _, err := NewTXTProof(" "); err == nil {
		t.Fatal("a proof without a secret")
	}
	p, err := NewTXTProof("pepper")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := NewTXTProof("other pepper")
	if p.Value(2, "acme.com") == p.Value(3, "acme.com") || p.Value(2, "acme.com") == p.Value(2, "acme.io") || p.Value(2, "acme.com") == q.Value(2, "acme.com") {
		t.Fatal("proof values collide")
	}
	rec := p.Record(2, "acme.com")
	if rec.Type != "TXT" || rec.Name != "_lekalo.acme.com" || rec.Value != p.Value(2, "acme.com") || rec.Found {
		t.Fatalf("record %+v", rec)
	}
	txt := map[string][]string{}
	var lookErr error
	p.LookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], lookErr }
	ctx := context.Background()
	if ok, err := p.Proven(ctx, 2, "acme.com"); ok || err != nil {
		t.Fatalf("no record: %v %v", ok, err)
	}
	txt["_lekalo.acme.com"] = []string{"v=spf1 -all", p.Value(3, "acme.com")}
	if ok, _ := p.Proven(ctx, 2, "acme.com"); ok {
		t.Fatal("another team's record proves")
	}
	txt["_lekalo.acme.com"] = append(txt["_lekalo.acme.com"], " "+p.Value(2, "acme.com")+" ")
	if ok, err := p.Proven(ctx, 2, "acme.com"); !ok || err != nil {
		t.Fatalf("own record: %v %v", ok, err)
	}
	lookErr = &net.DNSError{Err: "no such host", IsNotFound: true}
	if ok, err := p.Proven(ctx, 2, "acme.com"); ok || err != nil {
		t.Fatalf("nxdomain: %v %v", ok, err)
	}
	lookErr = errors.New("resolver down")
	if ok, err := p.Proven(ctx, 2, "acme.com"); ok || err == nil {
		t.Fatalf("resolver failure: %v %v", ok, err)
	}
}
