package patternaccess

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// roPresigner is a file store with read-only hosts (lane B8): PresignPatternObject signs OUR bucket,
// the ReadOnlyPatterns side answers by host. Both count their calls.
type roPresigner struct {
	fakePresigner
	hosts   map[string]bool
	roCalls int
}

func (p *roPresigner) PatternObjectSource(_ context.Context, rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", true
	}
	if h := strings.ToLower(u.Host); p.hosts[h] {
		return h, true
	}
	return "", true
}

func (p *roPresigner) ReadOnlyPatternURL(_ context.Context, host, key string) (string, time.Time, error) {
	p.roCalls++
	return "https://" + host + "/" + key, time.Now().Add(time.Hour), nil
}

// The same key in our bucket and on two read-only hosts is three objects. A token minted for a
// copied row resolves to the host that row came from — never to our bucket's object with the same
// key, never to the first configured host — and our own row keeps its presigned url.
func TestLegacyTokenResolvesToRecordedHost(t *testing.T) {
	const key = "grbpwr-com/tech-card-patterns/2024/may/p.pdf"
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{}}
	pres := &roPresigner{hosts: map[string]bool{"files.grbpwr.com": true, "files2.grbpwr.com": true}}
	svc, err := New(objects, &fakeCards{}, pres, "test-pepper", testViewerBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)

	ps := []*pb_common.TechCardSizePattern{
		{Url: "https://cdn.lekalo.test/" + key},
		{Url: "https://files.grbpwr.com/" + key},
		{Url: "https://files2.grbpwr.com/" + key},
	}
	svc.FillTechCardPatternURLs(context.Background(), testViewerBaseURL, ps)
	if len(objects.rows) != 3 {
		t.Fatalf("same key on three hosts must be three access rows, got %d", len(objects.rows))
	}
	tokenOf := func(p *pb_common.TechCardSizePattern) string {
		return strings.TrimPrefix(p.ViewUrl, testViewerBaseURL)
	}
	if tokenOf(ps[0]) == tokenOf(ps[1]) || tokenOf(ps[1]) == tokenOf(ps[2]) {
		t.Fatal("rows on different hosts must mint different tokens")
	}

	w := serve(svc, tokenOf(ps[2]))
	if got := w.Header().Get("Location"); got != "https://files2.grbpwr.com/"+key {
		t.Fatalf("a row from the second read-only host must link there, got %q (code %d)", got, w.Code)
	}
	w = serve(svc, tokenOf(ps[1]))
	if got := w.Header().Get("Location"); got != "https://files.grbpwr.com/"+key {
		t.Fatalf("a row from the first read-only host must link there, got %q", got)
	}
	if pres.calls != 0 {
		t.Fatalf("a legacy token must never reach our bucket's presign, got %d calls", pres.calls)
	}

	w = serve(svc, tokenOf(ps[0]))
	if got := w.Header().Get("Location"); !strings.HasPrefix(got, "https://origin.example/"+key) {
		t.Fatalf("our own row keeps its presigned url, got %q", got)
	}
	if pres.calls != 1 || pres.roCalls != 2 {
		t.Fatalf("calls: presign %d (want 1), read-only %d (want 2)", pres.calls, pres.roCalls)
	}
}
