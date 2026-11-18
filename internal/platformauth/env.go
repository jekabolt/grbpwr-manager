package platformauth

import (
	"os"
	"strconv"
	"strings"
)

// DefaultTermsVersion is the terms version stored when PLATFORM_TERMS_VERSION is not set.
const DefaultTermsVersion = "2026-11"

// SignupApprovalFromEnv reads PLATFORM_SIGNUP_APPROVAL: on unless it parses as false ("false", "0",
// "off", "no"). Read only in teams mode; an unset or unreadable value keeps approval on.
func SignupApprovalFromEnv() bool {
	return signupApproval(os.Getenv("PLATFORM_SIGNUP_APPROVAL"))
}

func signupApproval(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "off", "no":
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return true
}

// TermsVersionFromEnv reads PLATFORM_TERMS_VERSION (optional; DefaultTermsVersion when empty).
func TermsVersionFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("PLATFORM_TERMS_VERSION")); v != "" && len(v) <= 32 {
		return v
	}
	return DefaultTermsVersion
}
