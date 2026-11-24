// Package shop holds what the storefront edge, the admin shop screen and the storefront API share
// about team shops (X18): host names, the platform address, the cache namespace, per-shop secrets,
// the domain provider seam (Vercel, lane L5) and the shop-aware revalidator.
package shop

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"golang.org/x/net/publicsuffix"
)

// ErrBadHost: not a host name a shop can answer on.
var ErrBadHost = errors.New("shop: bad host")

// NormHost lower-cases a host and drops a port and a trailing dot. "" when s is empty.
func NormHost(s string) string {
	h := strings.ToLower(strings.TrimSpace(s))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(h, ".")
}

// OriginHost is the host of an origin ("https://brand.com" → "brand.com"); "" when o is not one.
func OriginHost(o string) string {
	o = strings.TrimSpace(o)
	i := strings.Index(o, "://")
	if i <= 0 {
		return ""
	}
	scheme := strings.ToLower(o[:i])
	if scheme != "https" && scheme != "http" {
		return ""
	}
	rest := strings.TrimSuffix(o[i+3:], "/")
	if rest == "" || strings.ContainsAny(rest, "/?#@ ") {
		return ""
	}
	return NormHost(rest)
}

// ValidCustomHost checks a domain a team attaches: a lower-case DNS name of 2+ labels, letters,
// digits and hyphens, no IP, not under the platform's own domains (base and shop zone) nor a dev
// or preview domain (localhost, vercel.app).
func ValidCustomHost(h string, platformDomains ...string) error {
	if !ValidHost(h) || strings.Count(h, ".") < 1 {
		return ErrBadHost
	}
	// Dev and preview hosts stay outside shop_host: a storefront preview's Origin must never name a shop.
	for _, d := range append([]string{"localhost", "vercel.app", "vercel.sh"}, platformDomains...) {
		if Under(h, d) {
			return ErrBadHost
		}
	}
	return nil
}

// ValidHost reports whether h is a normalized DNS host name a shop could answer on: lower-case
// labels of letters, digits and hyphens (no leading or trailing hyphen), at most 253 bytes, no IP,
// the last label not numeric. Checked before any host reaches the control plane.
func ValidHost(h string) bool {
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	return tld[0] < '0' || tld[0] > '9'
}

// Under reports whether host h is domain d or under it ("" d: false).
func Under(h, d string) bool {
	d = NormHost(d)
	return d != "" && (h == d || strings.HasSuffix(h, "."+d))
}

// RegistrableDomains answers the registrable domains (eTLD+1, the public suffix list) of hosts:
// "api.lekalo.studio" and "shop.beta.lekalo.studio" → "lekalo.studio". Every host under one is the
// platform's or the storefront's and is never a team's custom host. Hosts that are not DNS names
// (localhost, IPs) and duplicates are left out.
func RegistrableDomains(hosts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hosts {
		h = NormHost(h)
		if !ValidHost(h) || !strings.Contains(h, ".") {
			continue
		}
		d, err := publicsuffix.EffectiveTLDPlusOne(h)
		if err != nil || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// Zone is the shop zone: zone when set, else "shop." + base ("" when both are empty).
func Zone(zone, base string) string {
	if z := NormHost(zone); z != "" {
		return z
	}
	if b := NormHost(base); b != "" {
		return "shop." + b
	}
	return ""
}

// Subdomain is a team's platform address: "{slug}.{zone}"; "" without a zone or slug.
func Subdomain(slug, zone string) string {
	slug, zone = strings.ToLower(strings.TrimSpace(slug)), NormHost(zone)
	if slug == "" || zone == "" {
		return ""
	}
	return slug + "." + zone
}

// Namespace is the cache namespace of a team's shop: "" for the storefront team (grbpwr keeps its
// cache tags as they are) and for teams mode off, "t{id}" for every other team.
func Namespace(teamID, storefrontID int64) string {
	if storefrontID <= 0 {
		storefrontID = tenant.TeamOneID
	}
	if teamID <= 0 || teamID == storefrontID {
		return ""
	}
	return "t" + strconv.FormatInt(teamID, 10)
}

// DeriveKey derives a team's secret for one purpose from a master secret (HKDF-SHA256, info
// "shop:{purpose}:{team id}"). Each team, each purpose gets its own key; the master never leaves the
// API. n is the key length in bytes.
func DeriveKey(master string, teamID int64, purpose string, n int) ([]byte, error) {
	if master == "" {
		return nil, errors.New("shop: no master secret")
	}
	return hkdf.Key(sha256.New, []byte(master), nil, "shop:"+purpose+":"+strconv.FormatInt(teamID, 10), n)
}

// Key is a team's shop key ("t{id}"): the storefront's name for a shop in revalidation secrets.
func Key(teamID int64) string { return "t" + strconv.FormatInt(teamID, 10) }

// RevalidateSecret is the secret a revalidation post to a shop carries:
// hex(HKDF-SHA256(master, salt empty, info "revalidate:"+shopKey, 32 bytes)), as the storefront
// derives it from the same master (SHOP_REVALIDATE_MASTER).
func RevalidateSecret(master, shopKey string) (string, error) {
	if master == "" {
		return "", errors.New("shop: no revalidate master")
	}
	k, err := hkdf.Key(sha256.New, []byte(master), nil, "revalidate:"+shopKey, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k), nil
}
