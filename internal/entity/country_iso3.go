package entity

import "strings"

// iso2ToISO3 maps ISO 3166-1 alpha-2 country codes to alpha-3. The rest of the system stores the
// buyer's country as a free-text string (often a name or an alpha-2); the resolvers below bridge
// that to a canonical alpha-2 (Sendcloud) or alpha-3 code. This is the full ISO 3166-1 set so any
// destination resolves.
var iso2ToISO3 = map[string]string{
	"AF": "AFG", "AX": "ALA", "AL": "ALB", "DZ": "DZA", "AS": "ASM", "AD": "AND", "AO": "AGO",
	"AI": "AIA", "AQ": "ATA", "AG": "ATG", "AR": "ARG", "AM": "ARM", "AW": "ABW", "AU": "AUS",
	"AT": "AUT", "AZ": "AZE", "BS": "BHS", "BH": "BHR", "BD": "BGD", "BB": "BRB", "BY": "BLR",
	"BE": "BEL", "BZ": "BLZ", "BJ": "BEN", "BM": "BMU", "BT": "BTN", "BO": "BOL", "BQ": "BES",
	"BA": "BIH", "BW": "BWA", "BV": "BVT", "BR": "BRA", "IO": "IOT", "BN": "BRN", "BG": "BGR",
	"BF": "BFA", "BI": "BDI", "CV": "CPV", "KH": "KHM", "CM": "CMR", "CA": "CAN", "KY": "CYM",
	"CF": "CAF", "TD": "TCD", "CL": "CHL", "CN": "CHN", "CX": "CXR", "CC": "CCK", "CO": "COL",
	"KM": "COM", "CG": "COG", "CD": "COD", "CK": "COK", "CR": "CRI", "CI": "CIV", "HR": "HRV",
	"CU": "CUB", "CW": "CUW", "CY": "CYP", "CZ": "CZE", "DK": "DNK", "DJ": "DJI", "DM": "DMA",
	"DO": "DOM", "EC": "ECU", "EG": "EGY", "SV": "SLV", "GQ": "GNQ", "ER": "ERI", "EE": "EST",
	"SZ": "SWZ", "ET": "ETH", "FK": "FLK", "FO": "FRO", "FJ": "FJI", "FI": "FIN", "FR": "FRA",
	"GF": "GUF", "PF": "PYF", "TF": "ATF", "GA": "GAB", "GM": "GMB", "GE": "GEO", "DE": "DEU",
	"GH": "GHA", "GI": "GIB", "GR": "GRC", "GL": "GRL", "GD": "GRD", "GP": "GLP", "GU": "GUM",
	"GT": "GTM", "GG": "GGY", "GN": "GIN", "GW": "GNB", "GY": "GUY", "HT": "HTI", "HM": "HMD",
	"VA": "VAT", "HN": "HND", "HK": "HKG", "HU": "HUN", "IS": "ISL", "IN": "IND", "ID": "IDN",
	"IR": "IRN", "IQ": "IRQ", "IE": "IRL", "IM": "IMN", "IL": "ISR", "IT": "ITA", "JM": "JAM",
	"JP": "JPN", "JE": "JEY", "JO": "JOR", "KZ": "KAZ", "KE": "KEN", "KI": "KIR", "KP": "PRK",
	"KR": "KOR", "KW": "KWT", "KG": "KGZ", "LA": "LAO", "LV": "LVA", "LB": "LBN", "LS": "LSO",
	"LR": "LBR", "LY": "LBY", "LI": "LIE", "LT": "LTU", "LU": "LUX", "MO": "MAC", "MG": "MDG",
	"MW": "MWI", "MY": "MYS", "MV": "MDV", "ML": "MLI", "MT": "MLT", "MH": "MHL", "MQ": "MTQ",
	"MR": "MRT", "MU": "MUS", "YT": "MYT", "MX": "MEX", "FM": "FSM", "MD": "MDA", "MC": "MCO",
	"MN": "MNG", "ME": "MNE", "MS": "MSR", "MA": "MAR", "MZ": "MOZ", "MM": "MMR", "NA": "NAM",
	"NR": "NRU", "NP": "NPL", "NL": "NLD", "NC": "NCL", "NZ": "NZL", "NI": "NIC", "NE": "NER",
	"NG": "NGA", "NU": "NIU", "NF": "NFK", "MK": "MKD", "MP": "MNP", "NO": "NOR", "OM": "OMN",
	"PK": "PAK", "PW": "PLW", "PS": "PSE", "PA": "PAN", "PG": "PNG", "PY": "PRY", "PE": "PER",
	"PH": "PHL", "PN": "PCN", "PL": "POL", "PT": "PRT", "PR": "PRI", "QA": "QAT", "RE": "REU",
	"RO": "ROU", "RU": "RUS", "RW": "RWA", "BL": "BLM", "SH": "SHN", "KN": "KNA", "LC": "LCA",
	"MF": "MAF", "PM": "SPM", "VC": "VCT", "WS": "WSM", "SM": "SMR", "ST": "STP", "SA": "SAU",
	"SN": "SEN", "RS": "SRB", "SC": "SYC", "SL": "SLE", "SG": "SGP", "SX": "SXM", "SK": "SVK",
	"SI": "SVN", "SB": "SLB", "SO": "SOM", "ZA": "ZAF", "GS": "SGS", "SS": "SSD", "ES": "ESP",
	"LK": "LKA", "SD": "SDN", "SR": "SUR", "SJ": "SJM", "SE": "SWE", "CH": "CHE", "SY": "SYR",
	"TW": "TWN", "TJ": "TJK", "TZ": "TZA", "TH": "THA", "TL": "TLS", "TG": "TGO", "TK": "TKL",
	"TO": "TON", "TT": "TTO", "TN": "TUN", "TR": "TUR", "TM": "TKM", "TC": "TCA", "TV": "TUV",
	"UG": "UGA", "UA": "UKR", "AE": "ARE", "GB": "GBR", "US": "USA", "UM": "UMI", "UY": "URY",
	"UZ": "UZB", "VU": "VUT", "VE": "VEN", "VN": "VNM", "VG": "VGB", "VI": "VIR", "WF": "WLF",
	"EH": "ESH", "YE": "YEM", "ZM": "ZMB", "ZW": "ZWE",
}

// iso3Set is the set of valid alpha-3 codes, derived from iso2ToISO3, so ResolveCountryISO3 can
// accept an address that already stores an alpha-3 code.
var iso3Set = func() map[string]bool {
	s := make(map[string]bool, len(iso2ToISO3))
	for _, v := range iso2ToISO3 {
		s[v] = true
	}
	return s
}()

// iso3ToISO2 is the reverse of iso2ToISO3, so an address that already stores an alpha-3 code
// resolves back to the alpha-2 Sendcloud expects.
var iso3ToISO2 = func() map[string]string {
	m := make(map[string]string, len(iso2ToISO3))
	for k, v := range iso2ToISO3 {
		m[v] = k
	}
	return m
}()

// ResolveCountryISO3 normalizes a free-text country value (an alpha-3 code, an alpha-2 code, or a
// country name as GA4/checkout reports it) to an ISO 3166-1 alpha-3 code. Returns ("", false) when
// the value cannot be resolved, so the caller rejects the request rather than sending a bad address.
func ResolveCountryISO3(country string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(country))
	if c == "" {
		return "", false
	}
	if len(c) == 3 && iso3Set[c] {
		return c, true
	}
	if len(c) == 2 {
		if iso3, ok := iso2ToISO3[c]; ok {
			return iso3, true
		}
	}
	// Fall back to name resolution (reuses the GA4 country-name table): name -> alpha-2 -> alpha-3.
	if iso2, ok := CountryNameToISO2(country); ok {
		if iso3, ok := iso2ToISO3[iso2]; ok {
			return iso3, true
		}
	}
	return "", false
}

// ResolveCountryISO2 normalizes a free-text country value (an alpha-2 code, an alpha-3 code, or a
// country name as GA4/checkout reports it) to an ISO 3166-1 alpha-2 code — the format Sendcloud
// requires on label addresses and customs origin_country. Returns ("", false) when the value cannot
// be resolved, so the caller rejects the request before calling the carrier.
func ResolveCountryISO2(country string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(country))
	if c == "" {
		return "", false
	}
	if len(c) == 2 {
		if _, ok := iso2ToISO3[c]; ok {
			return c, true
		}
	}
	if len(c) == 3 {
		if iso2, ok := iso3ToISO2[c]; ok {
			return iso2, true
		}
	}
	// Fall back to name resolution (reuses the GA4 country-name table): name -> alpha-2.
	if iso2, ok := CountryNameToISO2(country); ok {
		return iso2, true
	}
	return "", false
}
