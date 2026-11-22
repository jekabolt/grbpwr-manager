package storefront

// Config holds storefront customer account (passwordless login + JWT) settings.
type Config struct {
	AccessJWTSecret            string `mapstructure:"access_jwt_secret"`
	AccessJWTIssuer            string `mapstructure:"access_jwt_issuer"`
	AccessJWTAudience          string `mapstructure:"access_jwt_audience"`
	AccessJtiRevocationEnabled bool   `mapstructure:"access_jti_revocation_enabled"`
	AccessJWTTTL               string `mapstructure:"access_jwt_ttl"`
	RefreshTTL                 string `mapstructure:"refresh_ttl"`
	LoginChallengeTTL          string `mapstructure:"login_challenge_ttl"`
	LoginPepper                string `mapstructure:"login_pepper"`
	RefreshPepper              string `mapstructure:"refresh_pepper"`
	MagicLinkBaseURL           string `mapstructure:"magic_link_base_url"`
	// ShopMaster (STOREFRONT_AUTH_SHOP_MASTER, X18): the master secret every team shop's own access
	// key and peppers are derived from. The storefront team keeps the keys above. Empty = customer
	// accounts are off on team shops (grbpwr unchanged).
	ShopMaster string `mapstructure:"shop_master"`
}
