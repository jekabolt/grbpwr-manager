package mail

import (
	netmail "net/mail"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShopDisplayNameRefusesReservedNames(t *testing.T) {
	for _, name := range []string{
		"GRBPWR", "grbpwr", "Grbpwr Store", "GRB PWR", "g.r.b.p.w.r", "grbpvvr",
		"Gгbрwг",       // Cyrillic г, р
		"ＧＲＢＰＷＲ",       // full-width
		"GRBPWR\u200b", // zero-width space
		"LEKALO", "lekalo shop", "1ekalo", "lekaIo", "lekal0", "l e k a l o", "Lеkаlо", "lekaio",
		"", "   ", "\u202e\u202d",
	} {
		require.Equal(t, shopFallbackName, shopDisplayName(name, nil), "%q", name)
	}
}

func TestShopDisplayNameRefusesOtherTeamsNames(t *testing.T) {
	taken := []string{"Acme", "NORTH studio"}
	for _, name := range []string{"Acme", "ACME", "acme!", "Αcme", "Acrne", "North Studio", "N0RTH STUDI0"} {
		require.Equal(t, shopFallbackName, shopDisplayName(name, taken), "%q", name)
	}
	require.Equal(t, "Acme Two", shopDisplayName("Acme Two", taken))
	require.Equal(t, "Café Noir", shopDisplayName("Café Noir", taken))
}

func TestCleanShopName(t *testing.T) {
	cases := map[string]string{
		"  Acme   Studio ":                  "Acme Studio",
		`Evil" <admin@x.test>`:              "Evil admin x.test",
		"Shop\r\nBcc: victim@x.test":        "Shop Bcc victim x.test",
		"\u202eAcme\u202c":                  "Acme",
		"Rock & Roll":                       "Rock & Roll",
		"O'Neil-Studio":                     "O'Neil-Studio",
		strings.Repeat("a", 60):             strings.Repeat("a", shopNameMaxRunes),
		"Ателье Лето":                       "Ателье Лето",
		"<script>alert(1)</script> Studios": "script alert 1 script Studios",
	}
	for in, want := range cases {
		require.Equal(t, want, cleanShopName(in), "%q", in)
	}
}

func TestShopSenderIsTheNeutralIdentity(t *testing.T) {
	for _, name := range []string{"Acme Studio", "Ателье Лето", "Rock & Roll", "O'Neil-Studio", shopFallbackName} {
		from := shopSender(name, "no-reply@lekalo.test")
		a, err := netmail.ParseAddress(from)
		require.NoError(t, err, from)
		require.Equal(t, "no-reply@lekalo.test", a.Address)
		require.Equal(t, name+" via lekalo", a.Name)
	}
}

func TestNewShopBrand(t *testing.T) {
	b := newShopBrand(ShopIdentity{Name: "GRBPWR", Host: "Acme.Lekalo.test."}, "no-reply@lekalo.test")
	require.Equal(t, shopFallbackName, b.Name)
	require.Equal(t, "acme.lekalo.test", b.Host)
	require.Equal(t, "https://acme.lekalo.test", b.view().URL)

	for _, bad := range []string{"localhost", "acme.test/evil", "javascript:alert(1)", "a b.test", "acme.test\"onload=x"} {
		require.Empty(t, newShopBrand(ShopIdentity{Name: "Acme", Host: bad}, "x@y.test").Host, "%q", bad)
	}
}

func TestShopBrandRebrand(t *testing.T) {
	b := &shopBrand{Name: "Acme & Co"}
	require.Equal(t, "Your Acme & Co sign-in code", b.rebrand("Your GRBPWR sign-in code"))
	require.Equal(t, "WELCOME TO ACME & CO", b.rebrand("WELCOME TO GRBPWR"))
	require.Equal(t, "Thanks from <b>Acme &amp; Co</b>", b.rebrandHTML("Thanks from <b>GRBPWR</b>"))
	require.Equal(t, "no brand here", b.rebrand("no brand here"))
}
