package entity

import "strings"

// countryNameToISO2 maps a normalized (lowercased, space-collapsed) country name to its ISO 3166-1
// alpha-2 code. Keys are the ISO 3166-1 English short names GA4's `country` dimension reports, plus
// common aliases (GA4/vernacular variants). Used to join GA4 country-keyed sessions to address.country
// (ISO-2) for per-country demand metrics (analytics-v2 task 09).
//
// It is deliberately NOT the full 249-entry ISO table: it covers all of Europe and the major world
// markets this store actually sees traffic and orders from. Any name it doesn't know resolves to
// (false), and the caller buckets that traffic under "(unmatched)" so it stays visible rather than
// being silently dropped — extend this map when a real, recurring country shows up there.
var countryNameToISO2 = map[string]string{
	// --- Europe (EU / EEA / neighbours) ---
	"albania":                "AL",
	"andorra":                "AD",
	"austria":                "AT",
	"belarus":                "BY",
	"belgium":                "BE",
	"bosnia and herzegovina": "BA",
	"bulgaria":               "BG",
	"croatia":                "HR",
	"cyprus":                 "CY",
	"czechia":                "CZ",
	"czech republic":         "CZ",
	"denmark":                "DK",
	"estonia":                "EE",
	"faroe islands":          "FO",
	"finland":                "FI",
	"france":                 "FR",
	"germany":                "DE",
	"gibraltar":              "GI",
	"greece":                 "GR",
	"hungary":                "HU",
	"iceland":                "IS",
	"ireland":                "IE",
	"italy":                  "IT",
	"kosovo":                 "XK",
	"latvia":                 "LV",
	"liechtenstein":          "LI",
	"lithuania":              "LT",
	"luxembourg":             "LU",
	"malta":                  "MT",
	"moldova":                "MD",
	"republic of moldova":    "MD",
	"monaco":                 "MC",
	"montenegro":             "ME",
	"netherlands":            "NL",
	"the netherlands":        "NL",
	"north macedonia":        "MK",
	"macedonia":              "MK",
	"norway":                 "NO",
	"poland":                 "PL",
	"portugal":               "PT",
	"romania":                "RO",
	"russia":                 "RU",
	"russian federation":     "RU",
	"san marino":             "SM",
	"serbia":                 "RS",
	"slovakia":               "SK",
	"slovenia":               "SI",
	"spain":                  "ES",
	"sweden":                 "SE",
	"switzerland":            "CH",
	"ukraine":                "UA",
	"united kingdom":         "GB",
	"uk":                     "GB",
	"great britain":          "GB",
	"vatican":                "VA",
	"vatican city":           "VA",
	"holy see":               "VA",

	// --- Americas ---
	"argentina":                "AR",
	"bolivia":                  "BO",
	"brazil":                   "BR",
	"canada":                   "CA",
	"chile":                    "CL",
	"colombia":                 "CO",
	"costa rica":               "CR",
	"dominican republic":       "DO",
	"ecuador":                  "EC",
	"el salvador":              "SV",
	"guatemala":                "GT",
	"honduras":                 "HN",
	"mexico":                   "MX",
	"panama":                   "PA",
	"paraguay":                 "PY",
	"peru":                     "PE",
	"puerto rico":              "PR",
	"united states":            "US",
	"united states of america": "US",
	"usa":                      "US",
	"uruguay":                  "UY",
	"venezuela":                "VE",

	// --- Middle East & Central Asia ---
	"armenia":              "AM",
	"azerbaijan":           "AZ",
	"bahrain":              "BH",
	"georgia":              "GE",
	"iran":                 "IR",
	"iraq":                 "IQ",
	"israel":               "IL",
	"jordan":               "JO",
	"kazakhstan":           "KZ",
	"kuwait":               "KW",
	"lebanon":              "LB",
	"oman":                 "OM",
	"qatar":                "QA",
	"saudi arabia":         "SA",
	"turkey":               "TR",
	"türkiye":              "TR",
	"turkiye":              "TR",
	"united arab emirates": "AE",
	"uae":                  "AE",
	"uzbekistan":           "UZ",

	// --- Asia-Pacific ---
	"australia":    "AU",
	"bangladesh":   "BD",
	"cambodia":     "KH",
	"china":        "CN",
	"hong kong":    "HK",
	"india":        "IN",
	"indonesia":    "ID",
	"japan":        "JP",
	"macao":        "MO",
	"macau":        "MO",
	"malaysia":     "MY",
	"new zealand":  "NZ",
	"pakistan":     "PK",
	"philippines":  "PH",
	"singapore":    "SG",
	"south korea":  "KR",
	"korea":        "KR",
	"sri lanka":    "LK",
	"taiwan":       "TW",
	"thailand":     "TH",
	"vietnam":      "VN",
	"viet nam":     "VN",

	// --- Africa ---
	"algeria":      "DZ",
	"egypt":        "EG",
	"ghana":        "GH",
	"kenya":        "KE",
	"morocco":      "MA",
	"nigeria":      "NG",
	"south africa": "ZA",
	"tunisia":      "TN",
}

// CountryNameToISO2 resolves a country name (any casing/spacing, e.g. a GA4 "country" value) to its
// ISO 3166-1 alpha-2 code. ok is false when the name is unrecognized — the caller buckets it as
// "(unmatched)" so unmapped GA4 traffic is visible rather than silently dropped.
func CountryNameToISO2(name string) (string, bool) {
	key := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(name))), " ")
	code, ok := countryNameToISO2[key]
	return code, ok
}
