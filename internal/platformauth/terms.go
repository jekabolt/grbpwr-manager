package platformauth

import "github.com/jekabolt/grbpwr-manager/internal/platformstore"

// DefaultTermsVersion is the terms version stored when PLATFORM_TERMS_VERSION is not set
// (config.Platform.TermsVersion; read through platformstore.Config.Terms).
const DefaultTermsVersion = platformstore.DefaultTermsVersion
