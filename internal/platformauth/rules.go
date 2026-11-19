package platformauth

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Lifetimes (02-DESIGN §4, §5, §8).
const (
	VerifyTTL         = 24 * time.Hour
	ResetTTL          = time.Hour
	HandoffTTL        = 60 * time.Second
	HandoffLongTTL    = 10 * time.Minute // issued by VerifyEmail / AcceptInvite
	RootTokenTTL      = 10 * time.Minute
	PendingTeamTTL    = 48 * time.Hour
	InviteLetterTTL   = 7 * 24 * time.Hour
	CodeFailureWindow = 24 * time.Hour
)

// Caps (02-DESIGN §5, §8).
const (
	MaxRowCodeAttempts   = 5  // wrong codes on one letter before its code is dead (its link still works)
	MaxUserCodeFailures  = 10 // wrong codes per user in 24 h before the code path locks (links unaffected)
	MaxCodeTriesPerIPHr  = 30
	MaxLettersPerEmailHr = 3
	MaxSignUpsPerIPHr    = 5
	MaxOwnedTeams        = 3
)

// Field limits.
const (
	MinPasswordLen = 8
	// MaxPasswordLen bounds the PBKDF2 input: a megabyte "password" costs the server, not the guesser.
	MaxPasswordLen = 256
	MaxTeamNameLen = 120
	maxEmailLen    = 254
	maxHandleLen   = 63
)

var (
	errNoSlug       = errors.New("platformauth: no free address found")
	errBadEmail     = errors.New("email is not valid")
	errBadPassword  = errors.New("password must be 8 or more characters")
	errLongPassword = errors.New("password is too long")
	errBadTeamName  = errors.New("team name must be 1-120 characters")
	errBadHandle    = errors.New("name must be 2-63 latin letters, digits, dots, dashes or underscores")
)

// handlePattern: what a person may choose as a handle (the join form's «name»).
var handlePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

// NormalizeEmail lowercases and validates an address; only a bare local@domain is accepted.
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > maxEmailLen {
		return "", errBadEmail
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return "", errBadEmail
	}
	at := strings.LastIndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || !strings.Contains(s[at+1:], ".") {
		return "", errBadEmail
	}
	return s, nil
}

// CheckPassword applies the sheet's rule: 8 or more characters (runes), at most MaxPasswordLen bytes.
func CheckPassword(p string) error {
	if len(p) > MaxPasswordLen {
		return errLongPassword
	}
	if utf8.RuneCountInString(p) < MinPasswordLen {
		return errBadPassword
	}
	return nil
}

// CleanTeamName trims a team name, collapses inner runs of spaces and checks it: 1–120 runes, any script, no control characters.
func CleanTeamName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", errBadTeamName
	}
	n := utf8.RuneCountInString(s)
	if n < 1 || n > MaxTeamNameLen {
		return "", errBadTeamName
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", errBadTeamName
		}
	}
	return strings.Join(strings.Fields(s), " "), nil
}

// CheckHandle validates a handle a person chose.
func CheckHandle(h string) error {
	if !handlePattern.MatchString(h) {
		return errBadHandle
	}
	return nil
}

// HandleBase derives a handle from an email's local part: lowercase, only [a-z0-9._-], starting
// with a letter or digit, 2–63 characters; "user" when nothing usable is left.
func HandleBase(email string) string {
	local := email
	if at := strings.LastIndexByte(email, '@'); at >= 0 {
		local = email[:at]
	}
	if plus := strings.IndexByte(local, '+'); plus > 0 {
		local = local[:plus]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(local) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	h := strings.TrimLeft(b.String(), "._-")
	if len(h) > maxHandleLen-4 { // room for "-NNN"
		h = h[:maxHandleLen-4]
	}
	if CheckHandle(h) != nil {
		return "user"
	}
	return h
}

// HandleCandidate is the n-th candidate for a base: base, base-2, base-3 …
func HandleCandidate(base string, n int) string {
	if n <= 1 {
		return base
	}
	return base + "-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ParseOperatorEmails reads PLATFORM_OPERATOR_EMAILS: a comma-separated list, trimmed, empties
// dropped, lowercased, deduplicated, invalid addresses dropped (returned apart so boot can log them).
func ParseOperatorEmails(s string) (emails, invalid []string) {
	seen := map[string]bool{}
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		e, err := NormalizeEmail(p)
		if err != nil {
			invalid = append(invalid, p)
			continue
		}
		if !seen[e] {
			seen[e] = true
			emails = append(emails, e)
		}
	}
	return emails, invalid
}

// CodeState is a user's code-failure counter (platform_user.code_failures*, code_locked_until).
type CodeState struct {
	Failures    int
	Since       *time.Time
	LockedUntil *time.Time
}

// Locked reports whether the user's code path is locked at now.
func (c CodeState) Locked(now time.Time) bool {
	return c.LockedUntil != nil && now.Before(*c.LockedUntil)
}

// AfterFailure is the state after one more wrong code at now: a rolling 24 h window from its first
// failure; the 10th failure in the window locks the code path until that window ends.
func (c CodeState) AfterFailure(now time.Time) CodeState {
	if c.Since == nil || !now.Before(c.Since.Add(CodeFailureWindow)) {
		since := now
		c = CodeState{Failures: 1, Since: &since}
	} else {
		c.Failures++
	}
	if c.Failures >= MaxUserCodeFailures {
		until := c.Since.Add(CodeFailureWindow)
		c.LockedUntil = &until
	}
	return c
}

// RowCodeDead reports whether a letter's code is spent after attempts wrong tries.
func RowCodeDead(attempts int) bool { return attempts >= MaxRowCodeAttempts }
