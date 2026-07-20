// Package jpk builds Polish JPK_V7M (JPK_VAT) filings from the accounting ledger. This first slice is
// the taxpayer identity that heads every filing (Naglowek/KodUrzedu + Podmiot1) — legal-registry values
// with no source in the app, supplied by the operator (config.JPKConfig maps onto Taxpayer at the call
// site, so this package stays free of the config dependency). The declaration (P_ fields) and the
// evidence rows (SprzedazWiersz / ZakupWiersz) are built in the following slices.
package jpk

import (
	"fmt"
	"regexp"
	"strings"
)

// Taxpayer is the JPK_V7M header identity. Every field except Phone is required for a schema-valid
// file; Validate enforces the formats so a mistyped config value is caught before a filing is produced
// rather than rejected by the tax authority.
type Taxpayer struct {
	NIP       string // 10-digit taxpayer NIP (Podmiot1/OsobaNiefizyczna/NIP)
	FullName  string // full legal name (Podmiot1/OsobaNiefizyczna/PelnaNazwa)
	Email     string // contact email (Podmiot1/OsobaNiefizyczna/Email)
	Phone     string // optional contact phone (Podmiot1/OsobaNiefizyczna/Telefon)
	TaxOffice string // 4-digit destination tax-office code (Naglowek/KodUrzedu)
}

var (
	nipDigits       = regexp.MustCompile(`^[0-9]{10}$`)
	taxOfficeDigits = regexp.MustCompile(`^[0-9]{4}$`)
	emailShape      = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// Configured reports whether a taxpayer identity has been supplied at all. An empty NIP means the JPK
// export is not configured on this environment and the RPC should refuse cleanly rather than emit a
// file with a blank taxpayer.
func (t Taxpayer) Configured() bool { return strings.TrimSpace(t.NIP) != "" }

// Validate checks the identity is well-formed for JPK_V7M. It verifies the NIP checksum (the last
// digit is a weighted mod-11 check of the first nine), the 4-digit tax-office code, a full name, and a
// plausible email — the four values the schema requires.
func (t Taxpayer) Validate() error {
	nip := strings.TrimSpace(t.NIP)
	if !nipDigits.MatchString(nip) {
		return fmt.Errorf("jpk: NIP must be exactly 10 digits, got %q", t.NIP)
	}
	if !validNIPChecksum(nip) {
		return fmt.Errorf("jpk: NIP %q fails its check-digit — likely a typo", nip)
	}
	if strings.TrimSpace(t.FullName) == "" {
		return fmt.Errorf("jpk: taxpayer full name (PelnaNazwa) is required")
	}
	if !emailShape.MatchString(strings.TrimSpace(t.Email)) {
		return fmt.Errorf("jpk: a contact email is required, got %q", t.Email)
	}
	if !taxOfficeDigits.MatchString(strings.TrimSpace(t.TaxOffice)) {
		return fmt.Errorf("jpk: tax-office code (KodUrzedu) must be exactly 4 digits, got %q", t.TaxOffice)
	}
	return nil
}

// validNIPChecksum verifies the Polish NIP control digit: the first nine digits are weighted by
// {6,5,7,2,3,4,5,6,7}, summed mod 11, and that must equal the tenth digit (a remainder of 10 is never
// issued, so it is treated as invalid).
func validNIPChecksum(nip string) bool {
	weights := [9]int{6, 5, 7, 2, 3, 4, 5, 6, 7}
	sum := 0
	for i := range 9 {
		sum += int(nip[i]-'0') * weights[i]
	}
	check := sum % 11
	if check == 10 {
		return false
	}
	return check == int(nip[9]-'0')
}
