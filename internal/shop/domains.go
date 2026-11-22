package shop

import (
	"context"
	"strings"
)

// Record is one DNS record a custom domain needs.
type Record struct {
	Type, Name, Value string
	Found             bool
}

// DomainState is the provider's answer for one domain.
type DomainState struct {
	// Status: "records_needed", "verifying", "live" or "failed" (platformstore.ShopHost*).
	Status string
	// Records are what the team adds at its DNS provider, each with whether it is seen.
	Records []Record
}

// DomainProvider attaches custom domains to the one multi-shop storefront deployment. Lane L5
// implements it over the Vercel API; FakeDomains stands in until then (and in tests). Every method
// is idempotent: attaching a domain the project already has answers its state.
type DomainProvider interface {
	Attach(ctx context.Context, host string) (DomainState, error)
	Check(ctx context.Context, host string) (DomainState, error)
	Remove(ctx context.Context, host string) error
}

// FakeDomains is a DomainProvider that talks to nobody: it answers the records a Vercel project asks
// for (an A record for an apex, a CNAME for a subdomain) and «records needed» until Live names the
// host. Safe for beta; nothing outside the process changes.
type FakeDomains struct {
	// Live lists hosts Check reports as live (tests).
	Live map[string]bool
}

const (
	fakeApexIP = "76.76.21.21"
	fakeCNAME  = "cname.vercel-dns.com"
)

func fakeRecords(host string, found bool) []Record {
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return []Record{{Type: "A", Name: "@", Value: fakeApexIP, Found: found}}
	}
	return []Record{{Type: "CNAME", Name: strings.Join(labels[:len(labels)-2], "."), Value: fakeCNAME, Found: found}}
}

func (f *FakeDomains) state(host string) DomainState {
	if f != nil && f.Live[host] {
		return DomainState{Status: "live", Records: fakeRecords(host, true)}
	}
	return DomainState{Status: "records_needed", Records: fakeRecords(host, false)}
}

// Attach answers the records for host.
func (f *FakeDomains) Attach(_ context.Context, host string) (DomainState, error) {
	return f.state(host), nil
}

// Check answers host's state.
func (f *FakeDomains) Check(_ context.Context, host string) (DomainState, error) {
	return f.state(host), nil
}

// Remove does nothing.
func (f *FakeDomains) Remove(context.Context, string) error { return nil }
