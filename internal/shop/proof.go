package shop

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// Ownership proof of a custom host (X18). The domain provider saying «live» only means the DNS points
// at the shared storefront deployment; it does not say which team owns the domain (a domain a team let
// go of keeps pointing there). A custom host goes live only once its team publishes a TXT record at
// ProofName(host) with the value Proof.Value(team, host): an HMAC of the team and the host, so the
// record of one team never proves another team's attach.

// ProofLabel is the label the proof record sits under: "_lekalo.<host>".
const ProofLabel = "_lekalo"

// proofPrefix starts the TXT value.
const proofPrefix = "lekalo-shop-verify="

// proofTimeout bounds one DNS lookup.
const proofTimeout = 5 * time.Second

// ProofName is the DNS name of a host's proof record.
func ProofName(host string) string { return ProofLabel + "." + host }

// TXTProof is the DNS-TXT ownership proof (platformstore.ShopHostProof).
type TXTProof struct {
	key []byte
	// LookupTXT answers the TXT values of a name (net.Resolver.LookupTXT when nil).
	LookupTXT func(ctx context.Context, name string) ([]string, error)
}

// NewTXTProof derives the proof key from a server secret (TOKEN_PEPPER): HKDF-SHA256, info
// "shop-host-proof". An empty secret is an error: without a key no custom host can go live.
func NewTXTProof(secret string) (*TXTProof, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("shop: no secret for the host proof")
	}
	k, err := hkdf.Key(sha256.New, []byte(secret), nil, "shop-host-proof", 32)
	if err != nil {
		return nil, err
	}
	return &TXTProof{key: k}, nil
}

// Value is the TXT value team teamID publishes for host.
func (p *TXTProof) Value(teamID int64, host string) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(strconv.FormatInt(teamID, 10) + ":" + host))
	return proofPrefix + hex.EncodeToString(m.Sum(nil)[:16])
}

// Record is the record the shop screen shows the team (found=false; Proven decides).
func (p *TXTProof) Record(teamID int64, host string) platformstore.ShopRecord {
	return platformstore.ShopRecord{Type: "TXT", Name: ProofName(host), Value: p.Value(teamID, host)}
}

// Proven reports whether host's proof record carries team teamID's value. A name with no TXT record
// is false, nil; a resolver failure is an error (the caller keeps the host off live).
func (p *TXTProof) Proven(ctx context.Context, teamID int64, host string) (bool, error) {
	lookup := p.LookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	ctx, cancel := context.WithTimeout(ctx, proofTimeout)
	defer cancel()
	vals, err := lookup(ctx, ProofName(host))
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return false, nil
		}
		return false, err
	}
	want := p.Value(teamID, host)
	for _, v := range vals {
		if hmac.Equal([]byte(strings.TrimSpace(v)), []byte(want)) {
			return true, nil
		}
	}
	return false, nil
}
