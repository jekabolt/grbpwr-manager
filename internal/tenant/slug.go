package tenant

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"google.golang.org/grpc/metadata"
)

// slugPattern: 3–40 chars, lowercase latin letters, digits and inner hyphens.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

var (
	errSlugShape    = errors.New("address must be 3-40 latin letters, digits or hyphens, starting and ending with a letter or digit")
	errSlugHyphens  = errors.New("address must not contain two hyphens in a row")
	errSlugReserved = errors.New("address is reserved")
)

// IsReservedSlug reports whether s is a name the platform keeps for itself (hosts, mail CNAMEs,
// product names). send / rsend are live Resend CNAMEs on lekalo.studio; shop is the shop zone
// (shop.lekalo.studio, X18).
func IsReservedSlug(s string) bool {
	switch s {
	case "www", "api", "app", "admin", "console", "beta", "staging", "dev", "vpn",
		"mail", "smtp", "imap", "send", "rsend", "bounce",
		"files", "cdn", "static", "assets", "status", "docs", "help", "support",
		"auth", "login", "signup", "register", "billing", "backend", "shop",
		"grbpwr", "lekalo", "zoho", "resend":
		return true
	}
	return false
}

// ValidSlug checks a workspace address: shape, no "--", not reserved. Uniqueness against team and
// team_slug_history is the control plane's job. Team #1's own slug "grbpwr" is reserved on purpose:
// nobody can register it; team #1 holds it from EnsureTeamOne.
func ValidSlug(s string) error {
	if !slugPattern.MatchString(s) {
		return errSlugShape
	}
	if strings.Contains(s, "--") {
		return errSlugHyphens
	}
	if IsReservedSlug(s) {
		return errSlugReserved
	}
	return nil
}

// headerTeam is the gRPC metadata key of the Grpc-Metadata-X-Team header.
const headerTeam = "x-team"

// HeaderSlug returns the team slug the caller named in metadata x-team, lowercased and trimmed.
// "" means none: the call comes from the root host. It does not validate or resolve the slug.
func HeaderSlug(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(headerTeam)
	if len(vals) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(vals[0]))
}
