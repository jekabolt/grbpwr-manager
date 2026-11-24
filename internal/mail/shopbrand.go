package mail

import (
	"html"
	"html/template"
	netmail "net/mail"
	"strings"
)

// storefrontBrand is the word the catalog and the storefront templates use for the storefront's own
// name; a team shop's letter carries the shop's name in its place.
const storefrontBrand = "GRBPWR"

// shopBrand is how a team shop's letter presents itself: the checked display name, the shop's
// primary host (empty when it has none) and the sender line "<name> via lekalo" over the platform
// address.
type shopBrand struct {
	Name string
	Host string
	From string
}

// shopView is what a template sees through {{ shop }}: nil for a storefront letter.
type shopView struct {
	Name string
	Host string
	URL  string // https://<host>, empty without a host
}

// newShopBrand builds a shop's letter identity. The name passes shopDisplayName (reserved and
// look-alike names fall back to "shop"); the host passes cleanShopHost.
func newShopBrand(id ShopIdentity, platformAddr string) *shopBrand {
	name := shopDisplayName(id.Name, id.Taken)
	return &shopBrand{Name: name, Host: cleanShopHost(id.Host), From: shopSender(name, platformAddr)}
}

// shopSender is the From line of a shop letter: the display name with the fixed suffix over the
// platform's own address, quoted and encoded by net/mail so no name can break the header.
func shopSender(name, platformAddr string) string {
	return (&netmail.Address{Name: name + shopSenderSuffix, Address: platformAddr}).String()
}

// cleanShopHost keeps a host that is a plain DNS name (lower-case letters, digits, dots, hyphens,
// at least one dot); anything else is dropped and the letter carries no shop link.
func cleanShopHost(h string) string {
	h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "."))
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return ""
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return ""
		}
	}
	return h
}

func (b *shopBrand) view() *shopView {
	v := &shopView{Name: b.Name, Host: b.Host}
	if b.Host != "" {
		v.URL = "https://" + b.Host
	}
	return v
}

// rebrand puts the shop's name where a catalog string says GRBPWR, upper-cased when the string is
// upper-case copy (preheaders, sign-offs).
func (b *shopBrand) rebrand(s string) string {
	if !strings.Contains(s, storefrontBrand) {
		return s
	}
	name := b.Name
	if s == strings.ToUpper(s) {
		name = strings.ToUpper(name)
	}
	return strings.ReplaceAll(s, storefrontBrand, name)
}

// rebrandHTML is rebrand for a catalog string that carries markup: the name is escaped first.
func (b *shopBrand) rebrandHTML(s string) string {
	if !strings.Contains(s, storefrontBrand) {
		return s
	}
	name := b.Name
	if s == strings.ToUpper(s) {
		name = strings.ToUpper(name)
	}
	return strings.ReplaceAll(s, storefrontBrand, html.EscapeString(name))
}

// funcMap is the per-render override of a shop letter, attached after localeFuncMap: every catalog
// string comes back rebranded, and {{ shop }} answers the shop's view.
func (b *shopBrand) funcMap(loc *Loc) map[string]any {
	v := b.view()
	return map[string]any{
		"t": func(key string, kv ...any) string {
			return b.rebrand(loc.S(key, pairsToMap(kv...)))
		},
		"tHTML": func(key string, kv ...any) template.HTML {
			return template.HTML(b.rebrandHTML(loc.S(key, pairsToMap(kv...))))
		},
		"plural": func(key string, n int, kv ...any) string {
			return b.rebrand(loc.Plural(key, n, pairsToMap(kv...)))
		},
		"shop": func() *shopView { return v },
	}
}
