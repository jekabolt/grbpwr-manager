// Package stylenumber holds the canonical, machine-readable style-number (Артикул) contract
// (PLM-rework Q1, tmp/plm-rework/01-DOMAIN-MODEL.md §2.1): the server-proposed shape and the strict
// manual-override grammar. style-number-contract-v1.json is the single source of truth — the Go
// golden tests in this package read it, and any other language's tests should read the same file
// rather than re-typing the rules.
//
// Two responsibilities:
//   - Generate proposes {SEASON}{YY}-{SEQ} from facts present at creation (season code + year + a
//     per-season sequence supplied by the store). model_no is allocated lazily at first SKU mint,
//     so it is intentionally NOT a token here.
//   - ValidateManual enforces the strict override grammar so a hand-typed style number is well
//     formed; the global UNIQUE(style_number) index is the authority on collisions.
package stylenumber

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

//go:embed style-number-contract-v1.json
var contractFS embed.FS

// ContractFileName is the canonical fixture file name, exposed so tooling (e.g. a script that
// copies this file into a sibling TypeScript repo) does not hardcode it twice.
const ContractFileName = "style-number-contract-v1.json"

// Contract is the decoded shape of style-number-contract-v1.json.
type Contract struct {
	Version        string              `json:"version"`
	Description    string              `json:"description"`
	Template       string              `json:"template"`
	SeqWidth       int                 `json:"seq_width"`
	SegmentSep     string              `json:"segment_separator"`
	Season         SeasonContract      `json:"season"`
	ManualOverride ManualOverride      `json:"manual_override"`
	GoldenGenerate []GenerateVector    `json:"golden_generate"`
	NegativeGen    []NegativeGenVector `json:"negative_generate"`
	GoldenManual   []string            `json:"golden_manual"`
	NegativeManual []NegativeManVector `json:"negative_manual"`
}

// SeasonContract is the valid season-code set and year window.
type SeasonContract struct {
	ValidCodes []string `json:"valid_codes"`
	YearMin    int      `json:"year_min"`
	YearMax    int      `json:"year_max"`
}

// ManualOverride is the strict grammar a hand-entered style number must match.
type ManualOverride struct {
	Pattern     string `json:"pattern"`
	MaxLength   int    `json:"max_length"`
	Description string `json:"description"`
}

// GenerateVector is one golden (season, year, seq) -> style number example.
type GenerateVector struct {
	Season string `json:"season"`
	Year   int    `json:"year"`
	Seq    int    `json:"seq"`
	Want   string `json:"want"`
	Note   string `json:"note,omitempty"`
}

// NegativeGenVector is a Generate input that MUST be rejected, tagged with the reason.
type NegativeGenVector struct {
	Season string `json:"season"`
	Year   int    `json:"year"`
	Seq    int    `json:"seq"`
	Reason string `json:"reason"`
}

// NegativeManVector is a manual-override candidate that MUST be rejected, tagged with the reason.
type NegativeManVector struct {
	Candidate string `json:"candidate"`
	Reason    string `json:"reason"`
}

// Reason codes for a rejected manual override (stable, machine-readable; surfaced as the Reason on a
// field-tagged entity.ValidationError so a client can bind the failure to style_number).
const (
	ReasonEmpty         = "empty"
	ReasonTooLong       = "too_long"
	ReasonFormatInvalid = "format_invalid"
)

var (
	loadOnce  sync.Once
	loaded    *Contract
	loadErr   error
	seasonSet map[string]struct{}
	manualRxp *regexp.Regexp
)

// Load decodes and caches the embedded fixture, compiling the manual-override regexp once.
func Load() (*Contract, error) {
	loadOnce.Do(func() {
		data, err := contractFS.ReadFile(ContractFileName)
		if err != nil {
			loadErr = fmt.Errorf("stylenumber: read contract fixture: %w", err)
			return
		}
		var c Contract
		if err := json.Unmarshal(data, &c); err != nil {
			loadErr = fmt.Errorf("stylenumber: decode contract fixture: %w", err)
			return
		}
		rx, err := regexp.Compile(c.ManualOverride.Pattern)
		if err != nil {
			loadErr = fmt.Errorf("stylenumber: compile manual pattern: %w", err)
			return
		}
		seasonSet = make(map[string]struct{}, len(c.Season.ValidCodes))
		for _, s := range c.Season.ValidCodes {
			seasonSet[s] = struct{}{}
		}
		loaded, manualRxp = &c, rx
	})
	return loaded, loadErr
}

// ContractVersion returns the canonical contract version (e.g. "grbpwr-style-number-v1"), or ""
// only if the embedded fixture fails to decode (contract_test.go guards that it does not).
func ContractVersion() string {
	if c, err := Load(); err == nil {
		return c.Version
	}
	return ""
}

// Prefix returns the season prefix a generated style number carries, e.g. ("SS", 2026) -> "SS26-".
// It is what the store scans for to find the next free sequence. Season code is upper-cased and
// validated; the year must be in the contract window.
func Prefix(seasonCode string, year int) (string, error) {
	c, err := Load()
	if err != nil {
		return "", err
	}
	code := strings.ToUpper(strings.TrimSpace(seasonCode))
	if code == "" {
		return "", fmt.Errorf("season code is required")
	}
	if _, ok := seasonSet[code]; !ok {
		return "", fmt.Errorf("season code %q is not one of %v", code, c.Season.ValidCodes)
	}
	if year < c.Season.YearMin || year > c.Season.YearMax {
		return "", fmt.Errorf("season year %d out of range [%d, %d]", year, c.Season.YearMin, c.Season.YearMax)
	}
	return fmt.Sprintf("%s%02d%s", code, year%100, c.SegmentSep), nil
}

// Generate builds a proposed style number {SEASON}{YY}-{SEQ} from a season, year and a per-season
// sequence (>= 1). seq_width is the MINIMUM zero-padding; a larger sequence grows past it. The
// result is globally unique by construction (season+year+seq) and is a valid manual candidate.
func Generate(seasonCode string, year, seq int) (string, error) {
	c, err := Load()
	if err != nil {
		return "", err
	}
	prefix, err := Prefix(seasonCode, year)
	if err != nil {
		return "", err
	}
	if seq < 1 {
		return "", fmt.Errorf("sequence must be >= 1, got %d", seq)
	}
	return fmt.Sprintf("%s%0*d", prefix, c.SeqWidth, seq), nil
}

// ValidateManual reports whether a hand-entered style number is well formed against the strict
// override grammar. It returns "" when valid, else a stable reason code (ReasonEmpty / ReasonTooLong
// / ReasonFormatInvalid). Whitespace is trimmed first; call NormalizeManual to get the stored value.
func ValidateManual(candidate string) string {
	c, err := Load()
	if err != nil {
		return ReasonFormatInvalid
	}
	v := strings.TrimSpace(candidate)
	if v == "" {
		return ReasonEmpty
	}
	if len(v) > c.ManualOverride.MaxLength {
		return ReasonTooLong
	}
	if !manualRxp.MatchString(v) {
		return ReasonFormatInvalid
	}
	return ""
}

// NormalizeManual trims a manual candidate to its stored form (whitespace-trimmed). It does not
// validate — call ValidateManual for that.
func NormalizeManual(candidate string) string { return strings.TrimSpace(candidate) }

// ManualHint returns the human-readable grammar description, used as the how-to-fix on a
// field-tagged rejection.
func ManualHint() string {
	if c, err := Load(); err == nil {
		return c.ManualOverride.Description
	}
	return "uppercase alphanumeric segments separated by single hyphens"
}
