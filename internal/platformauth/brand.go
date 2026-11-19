package platformauth

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// Brand limits (runes), as the team_brand columns.
const (
	BrandNameMax   = 64
	LegalEntityMax = 96
	AddressLineMax = 64
	WebsiteMax     = 128
)

// Brand refusal keys (InvalidArgument), the same for every brand write: the workspace screen and a
// team's creation (sign-up, create team).
const (
	KeyBadText    = "bad_text"
	KeyBadAddress = "bad_address"
	KeyBadEmail   = "bad_email"
	KeyBadWebsite = "bad_website"
)

// BrandFields is what a brand write carries: admin.SetTeamBrandRequest and admin.TeamBrand both have
// these generated getters, which are nil-safe.
type BrandFields interface {
	GetBrandName() string
	GetLegalEntity() string
	GetLegalAddress() []string
	GetContactEmail() string
	GetWebsite() string
}

// cleanBrandText trims, refuses control characters and line breaks inside, collapses whitespace
// runs into one space and checks the rune limit. Any script is accepted; case is kept as typed.
func cleanBrandText(s string, max int) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", false
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		return "", false
	}
	return s, true
}

// cleanWebsite: empty, or the address without its http(s) scheme and one trailing slash, lower-case,
// with a dot and nothing that is a space or a control character.
func cleanWebsite(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", true
	}
	if rest, ok := strings.CutPrefix(s, "https://"); ok {
		s = rest
	} else if rest, ok := strings.CutPrefix(s, "http://"); ok {
		s = rest
	}
	s = strings.TrimSuffix(s, "/")
	host, _, _ := strings.Cut(s, "/")
	if !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") ||
		strings.Contains(s, "://") || utf8.RuneCountInString(s) > WebsiteMax {
		return "", false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

// NormalizeTeamBrand is the one normalisation of a brand write (teams W §1). Refusals (InvalidArgument):
// bad_text (a control character or line break, or over the limit: name 64, entity 96, address line 64),
// bad_address (more than three non-blank lines; blank lines are dropped and the rest move up),
// bad_email (not empty and not an address NormalizeEmail accepts), bad_website. A nil message is all
// empty.
func NormalizeTeamBrand(in BrandFields) (platformstore.TeamBrand, error) {
	bad := func(key string) (platformstore.TeamBrand, error) {
		return platformstore.TeamBrand{}, errBad(key)
	}
	var b platformstore.TeamBrand
	var ok bool
	if b.BrandName, ok = cleanBrandText(in.GetBrandName(), BrandNameMax); !ok {
		return bad(KeyBadText)
	}
	if b.LegalEntity, ok = cleanBrandText(in.GetLegalEntity(), LegalEntityMax); !ok {
		return bad(KeyBadText)
	}
	for _, raw := range in.GetLegalAddress() {
		line, ok := cleanBrandText(raw, AddressLineMax)
		if !ok {
			return bad(KeyBadText)
		}
		if line == "" {
			continue
		}
		if len(b.LegalAddress) == platformstore.TeamBrandAddressLines {
			return bad(KeyBadAddress)
		}
		b.LegalAddress = append(b.LegalAddress, line)
	}
	if email := strings.TrimSpace(in.GetContactEmail()); email != "" {
		norm, err := NormalizeEmail(email)
		if err != nil {
			return bad(KeyBadEmail)
		}
		b.ContactEmail = norm
	}
	if b.Website, ok = cleanWebsite(in.GetWebsite()); !ok {
		return bad(KeyBadWebsite)
	}
	return b, nil
}

// creationBrand is the brand a team's creation (sign-up, create team) carries, normalised by
// NormalizeTeamBrand: nil when it carries none or only empty values (no row is written then).
func creationBrand(in *pb_admin.TeamBrand) (*platformstore.TeamBrand, error) {
	b, err := NormalizeTeamBrand(in)
	if err != nil {
		return nil, err
	}
	if b.Empty() {
		return nil, nil
	}
	return &b, nil
}
