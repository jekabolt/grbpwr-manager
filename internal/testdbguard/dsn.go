// Package testdbguard validates that integration-test DSNs cannot target a
// production or otherwise remote MySQL database. It parses configuration only;
// it never opens a database connection.
package testdbguard

import (
	"fmt"
	"net"
	"strings"

	"github.com/go-sql-driver/mysql"
)

var dockerHosts = map[string]bool{
	"db":    true,
	"mysql": true,
}

// CheckMySQLDSN permits only a local/container TCP host and a database whose
// name ends in _test.
func CheckMySQLDSN(dsn string) error {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("invalid MySQL DSN: %w", err)
	}

	database := cfg.DBName
	databaseLower := strings.ToLower(database)
	if databaseLower == "grbpwr" || databaseLower == "grbpwr_beta" {
		return fmt.Errorf("database %q is a protected grbpwr database", database)
	}
	if !strings.HasSuffix(database, "_test") {
		return fmt.Errorf("database %q is not a test database: its name must end with _test", database)
	}
	if cfg.Net != "tcp" {
		return fmt.Errorf("MySQL network %q is not allowed: test databases must use TCP", cfg.Net)
	}

	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return fmt.Errorf("invalid MySQL TCP address %q: %w", cfg.Addr, err)
	}
	host = strings.ToLower(host)
	if strings.Contains(host, "ondigitalocean") {
		return fmt.Errorf("MySQL host %q is a DigitalOcean host and is never allowed in tests", host)
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || dockerHosts[host] {
		return nil
	}
	return fmt.Errorf("MySQL host %q is not allowed: use localhost, 127.0.0.1, ::1, mysql, or db", host)
}
