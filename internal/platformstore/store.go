// Package platformstore is the control plane of lekalo.studio: users, teams, membership, invites,
// sessions, feature overrides and outgoing platform mail, in their own database (prod `lekalo`,
// beta `lekalo_beta`).
//
// It has its own small pool (3 connections) with no tenant routing: the control plane is one
// database for the whole platform, and nothing here ever runs in a team's database. Its migrations
// live in sql/ and are tracked in platform_migrations, apart from the team databases' gorp_migrations.
package platformstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"
)

//go:embed sql/*.sql
var migrations embed.FS

const (
	poolSize        = 3
	connMaxLifetime = 5 * time.Minute
	pingTimeout     = 10 * time.Second
	migrateTimeout  = 2 * time.Minute
	// migrationsTable keeps the control plane's applied migrations apart from gorp_migrations.
	migrationsTable = "platform_migrations"
)

// Config is the platform section (env PLATFORM_* / TENANT_* / HTTP_ALLOWED_ORIGIN_REGEX, 02-DESIGN §14).
// DSN empty = teams mode off: the binary behaves exactly as the single-tenant one.
//
// It lives here and config.Platform aliases it: config imports the API servers, so the servers'
// SetPlatform cannot take a config type without an import cycle.
type Config struct {
	DSN string `mapstructure:"dsn"`
	// BaseDomain: lekalo.studio (prod) / beta.lekalo.studio (beta). Team hosts are <slug>.<BaseDomain>.
	BaseDomain string `mapstructure:"base_domain"`
	// LegacyAdminOrigins: comma-separated origins of today's admin (admin.grbpwr.com …) whose
	// tokens may still be legacy team-1 tokens.
	LegacyAdminOrigins string `mapstructure:"legacy_admin_origins"`
	// OperatorEmails: comma-separated emails of platform operators (root audience).
	OperatorEmails string `mapstructure:"operator_emails"`
	MailFrom       string `mapstructure:"mail_from"`
	MailerEnabled  bool   `mapstructure:"mailer_enabled"`
	// TokenPepper: HMAC key of email / invite token hashes.
	TokenPepper string `mapstructure:"token_pepper"`
	// DBPrefix: new team databases are DBPrefix + 8 [a-z0-9] (lk_t_ prod, lkb_t_ beta).
	DBPrefix string `mapstructure:"db_prefix"`
	// Team1DB: team #1's database; empty = the database of MYSQL_DSN. A mismatch refuses boot.
	Team1DB string `mapstructure:"team1_db"`
	// StorefrontTeam is TENANT_STOREFRONT_TEAM_ID as written: a team id or "none". config resolves
	// it into StorefrontTeamID / StorefrontNone; nothing else reads it.
	StorefrontTeam string `mapstructure:"storefront_team_id"`
	// StorefrontTeamID: the team the public storefront reads (1 = grbpwr). 0 when StorefrontNone.
	StorefrontTeamID int64 `mapstructure:"-"`
	// StorefrontNone: TENANT_STOREFRONT_TEAM_ID=none in teams mode — this installation has no shop.
	// Read it through NoShop.
	StorefrontNone bool `mapstructure:"-"`
	// Team1Profile: TENANT_TEAM1_PROFILE, the feature profile EnsureTeamOne gives team #1
	// (Team1ProfileAll, the default, or Team1ProfileProduction). Teams mode only.
	Team1Profile string `mapstructure:"team1_profile"`
	// Spares: how many ready spare databases the provisioner keeps.
	Spares int `mapstructure:"spares"`
	// ProvisionPerHour bounds new teams per hour.
	ProvisionPerHour int `mapstructure:"provision_per_hour"`
	// AllowedOriginRegex: CORS origins of team hosts (HTTP_ALLOWED_ORIGIN_REGEX).
	AllowedOriginRegex string `mapstructure:"allowed_origin_regex"`
	// SignupApproval (PLATFORM_SIGNUP_APPROVAL, §21): kept as text so an unreadable value never
	// stops a boot; read through SignupApprovalOn. Optional, read only in teams mode.
	SignupApproval string `mapstructure:"signup_approval"`
	// TermsVersion (PLATFORM_TERMS_VERSION): stored on every account sign-up or invite creates;
	// read through Terms. Optional, read only in teams mode.
	TermsVersion string `mapstructure:"terms_version"`
	// OAuth: provider sign-in settings (OAUTH_*). Optional; read only in teams mode.
	OAuth OAuthConfig `mapstructure:"oauth"`
}

// OAuthConfig is sign in with Google / Apple (OAUTH_*). Parsed only: nothing set (or teams mode
// off) behaves exactly as without it. Its String redacts the secrets.
type OAuthConfig struct {
	// GoogleClientID / GoogleClientSecret: OAUTH_GOOGLE_CLIENT_ID / OAUTH_GOOGLE_CLIENT_SECRET.
	GoogleClientID     string `mapstructure:"google_client_id"`
	GoogleClientSecret string `mapstructure:"google_client_secret"`
	// AppleEnabled (OAUTH_APPLE_ENABLED): kept as text so an unreadable value never stops a boot;
	// read through AppleOn (default off).
	AppleEnabled string `mapstructure:"apple_enabled"`
	// AppleServicesID, AppleTeamID, AppleKeyID, ApplePrivateKey (the .p8 PEM):
	// OAUTH_APPLE_SERVICES_ID, OAUTH_APPLE_TEAM_ID, OAUTH_APPLE_KEY_ID, OAUTH_APPLE_PRIVATE_KEY.
	AppleServicesID string `mapstructure:"apple_services_id"`
	AppleTeamID     string `mapstructure:"apple_team_id"`
	AppleKeyID      string `mapstructure:"apple_key_id"`
	ApplePrivateKey string `mapstructure:"apple_private_key"`
	// CallbackBase (OAUTH_CALLBACK_BASE): the scheme and host of the provider callbacks; empty means
	// https://api.<PLATFORM_BASE_DOMAIN>. Set only for local development.
	CallbackBase string `mapstructure:"callback_base"`
}

// AppleOn reads AppleEnabled: off unless it parses as true.
func (o OAuthConfig) AppleOn() bool {
	b, err := strconv.ParseBool(strings.TrimSpace(o.AppleEnabled))
	return err == nil && b
}

// String names which settings are present and never prints a secret.
func (o OAuthConfig) String() string {
	set := func(s string) bool { return strings.TrimSpace(s) != "" }
	return fmt.Sprintf("platformstore.OAuthConfig{Google:%t GoogleSecret:%t Apple:%t AppleKey:%t CallbackBase:%s}",
		set(o.GoogleClientID), set(o.GoogleClientSecret), o.AppleOn(), set(o.ApplePrivateKey), o.CallbackBase)
}

// OAuthCallbackBase is OAuth.CallbackBase, or https://api.<BaseDomain> when it is empty.
func (c Config) OAuthCallbackBase() string {
	if v := strings.TrimRight(strings.TrimSpace(c.OAuth.CallbackBase), "/"); v != "" {
		return v
	}
	return "https://api." + strings.TrimSpace(c.BaseDomain)
}

// DefaultTermsVersion is the terms version stored when PLATFORM_TERMS_VERSION is not set.
const DefaultTermsVersion = "2026-11"

// maxTermsVersion is the width of platform_user.terms_version.
const maxTermsVersion = 32

// SignupApprovalOn reads SignupApproval: on unless it parses as false ("false", "0", "off", "no");
// unset or unreadable keeps approval on.
func (c Config) SignupApprovalOn() bool {
	v := strings.ToLower(strings.TrimSpace(c.SignupApproval))
	switch v {
	case "off", "no":
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return true
}

// Terms reads TermsVersion: DefaultTermsVersion when empty or wider than the column.
func (c Config) Terms() string {
	if v := strings.TrimSpace(c.TermsVersion); v != "" && len(v) <= maxTermsVersion {
		return v
	}
	return DefaultTermsVersion
}

// Enabled reports teams mode: a control-plane DSN is configured.
func (c Config) Enabled() bool { return strings.TrimSpace(c.DSN) != "" }

// NoShop reports an installation without a storefront: teams mode with TENANT_STOREFRONT_TEAM_ID=none.
// It is the one predicate every no-shop decision reads (mounts, workers, side effects).
func (c Config) NoShop() bool { return c.Enabled() && c.StorefrontNone }

// LegacyOrigins splits LegacyAdminOrigins.
func (c Config) LegacyOrigins() []string { return splitList(c.LegacyAdminOrigins) }

// Operators splits OperatorEmails, lowercased.
func (c Config) Operators() []string {
	out := splitList(c.OperatorEmails)
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return out
}

// String redacts the DSN and the pepper.
func (c Config) String() string {
	red := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return ""
		}
		return "***REDACTED***"
	}
	return fmt.Sprintf("platformstore.Config{DSN:%s BaseDomain:%s DBPrefix:%s Team1DB:%s StorefrontTeamID:%d NoShop:%t Team1Profile:%s MailerEnabled:%t TokenPepper:%s}",
		red(c.DSN), c.BaseDomain, c.DBPrefix, c.Team1DB, c.StorefrontTeamID, c.NoShop(), c.Team1Profile, c.MailerEnabled, red(c.TokenPepper))
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Store is the control-plane repository.
type Store struct {
	db  *sqlx.DB
	dir *directory
}

// ErrUserNotFound: no platform user with that handle.
var ErrUserNotFound = errors.New("platformstore: user not found")

// ErrPasswordMismatch: the current password does not match the platform user's hash.
var ErrPasswordMismatch = errors.New("platformstore: current password mismatch")

// Open connects to the control plane, applies its migrations and returns the store.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("platformstore: parse dsn: %w", err)
	}
	if cfg.DBName == "" {
		return nil, errors.New("platformstore: the dsn names no database")
	}
	// Scanned DATETIME columns come back as time.Time in UTC.
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, err := sqlx.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("platformstore: open: %w", err)
	}
	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(poolSize)
	db.SetConnMaxLifetime(connMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("platformstore: ping: %w", err)
	}
	if err := migrateUp(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, dir: newDirectory(db)}, nil
}

func migrateUp(ctx context.Context, db *sqlx.DB) error {
	src := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrations, Root: "sql"}
	set := migrate.MigrationSet{TableName: migrationsTable}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := set.Exec(db.DB, "mysql", src, migrate.Up)
		done <- result{n, err}
	}()
	timer := time.NewTimer(migrateTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			return fmt.Errorf("platformstore: migrations: %w", r.err)
		}
		slog.Default().InfoContext(ctx, "platformstore: applied control-plane migrations", slog.Int("count", r.n))
		return nil
	case <-ctx.Done():
		return fmt.Errorf("platformstore: migrations: %w", ctx.Err())
	case <-timer.C:
		return errors.New("platformstore: migrations did not finish in time")
	}
}

// DB is the control-plane pool.
func (s *Store) DB() *sqlx.DB { return s.db }

// Directory resolves teams from the control plane (15 s cache per team).
func (s *Store) Directory() tenant.Directory { return s.dir }

// Close closes the pool.
func (s *Store) Close() error { return s.db.Close() }

// SetPasswordHashByHandle mirrors a password change of a team-1 admin onto its platform user, found
// through team #1 membership: a platform user who only shares the handle is ErrUserNotFound.
func (s *Store) SetPasswordHashByHandle(ctx context.Context, handle, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return errors.New("platformstore: empty password hash")
	}
	var id int64
	err := s.db.GetContext(ctx, &id, `SELECT u.id FROM platform_user u
JOIN team_member m ON m.user_id = u.id AND m.team_id = 1 WHERE u.handle = ?`, handle)
	if err != nil {
		if isNoRows(err) {
			return ErrUserNotFound
		}
		return fmt.Errorf("platformstore: find user: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE platform_user SET password_hash = ?, credential_gen = credential_gen + 1 WHERE id = ?`, hash, id); err != nil {
		return fmt.Errorf("platformstore: set password: %w", err)
	}
	return nil
}
