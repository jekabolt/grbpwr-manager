package platformauth

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Profile field limits (P0 §1.1, §3.1).
const (
	// MaxPersonNameLen bounds a first or a last name, in runes (platform_user.first_name/last_name).
	MaxPersonNameLen = 60
	// MaxFullNameLen bounds the one-box full name of the sign-up, join and invite forms, in runes
	// after trimming.
	MaxFullNameLen = 80
)

var (
	errBadName     = errors.New("bad_name")
	errNameTooLong = errors.New("name_too_long")
	errBadUsername = errors.New("bad_username")
)

// usernamePattern is the @username rule (P0 §3.1): 2–30 characters, the handle's first-character rule.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,29}$`)

// CleanPersonName trims a first or a last name and collapses inner runs of spaces. Empty is allowed;
// longer than MaxPersonNameLen runes is errNameTooLong; invalid UTF-8 or a control character is errBadName.
func CleanPersonName(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errBadName
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errBadName
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxPersonNameLen {
		return "", errNameTooLong
	}
	return s, nil
}

// SplitFullName splits the one-box full name of the sign-up, join and invite forms at its first space:
// the first word is the first name, the rest the last name. Empty gives two empty names. Longer than
// MaxFullNameLen runes after trimming, either part longer than MaxPersonNameLen, invalid UTF-8 or a
// control character is errBadName.
func SplitFullName(s string) (first, last string, err error) {
	if !utf8.ValidString(s) {
		return "", "", errBadName
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", "", errBadName
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", "", nil
	}
	if utf8.RuneCountInString(s) > MaxFullNameLen {
		return "", "", errBadName
	}
	first, last, _ = strings.Cut(s, " ")
	if utf8.RuneCountInString(first) > MaxPersonNameLen || utf8.RuneCountInString(last) > MaxPersonNameLen {
		return "", "", errBadName
	}
	return first, last, nil
}

// NormalizePublicUsername lowercases a typed @username, strips one leading '@' and checks the rule:
// 2–30 of [a-z0-9._-] starting with a letter or digit, not a reserved actor name and not "me".
// errBadUsername otherwise.
func NormalizePublicUsername(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "@")
	if !usernamePattern.MatchString(s) || s == "me" || entity.AIActorIsReserved(s) {
		return "", errBadUsername
	}
	return s, nil
}
