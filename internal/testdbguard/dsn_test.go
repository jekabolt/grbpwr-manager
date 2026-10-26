package testdbguard

import (
	"strings"
	"testing"
)

func TestCheckMySQLDSN(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		wantErr string
	}{
		{name: "localhost", dsn: "user:pass@tcp(localhost:3306)/grbpwr_test"},
		{name: "IPv4 loopback", dsn: "user:pass@tcp(127.0.0.1:3306)/orders_test"},
		{name: "IPv6 loopback", dsn: "user:pass@tcp([::1]:3306)/orders_test"},
		{name: "docker mysql service", dsn: "user:pass@tcp(mysql:3306)/orders_test"},
		{name: "docker db service", dsn: "user:pass@tcp(db:3306)/orders_test"},
		{name: "implicit local address", dsn: "user:pass@/orders_test"},
		{name: "DigitalOcean", dsn: "user:pass@tcp(db-mysql-fra1-123-do-user-1-0.b.db.ondigitalocean.com:25060)/orders_test", wantErr: "DigitalOcean"},
		{name: "remote IPv4", dsn: "user:pass@tcp(10.0.0.5:3306)/orders_test", wantErr: "not allowed"},
		{name: "other loopback", dsn: "user:pass@tcp(127.0.0.2:3306)/orders_test", wantErr: "not allowed"},
		{name: "unknown service", dsn: "user:pass@tcp(database:3306)/orders_test", wantErr: "not allowed"},
		{name: "production database", dsn: "user:pass@tcp(localhost:3306)/grbpwr", wantErr: "protected"},
		{name: "beta database", dsn: "user:pass@tcp(localhost:3306)/grbpwr_beta", wantErr: "protected"},
		{name: "numbered beta database", dsn: "user:pass@tcp(localhost:3306)/grbpwr_beta_02", wantErr: "protected"},
		{name: "numbered beta database uppercase", dsn: "user:pass@tcp(localhost:3306)/GRBPWR_BETA_99", wantErr: "protected"},
		{name: "missing test suffix", dsn: "user:pass@tcp(localhost:3306)/orders", wantErr: "must end with _test"},
		{name: "similar suffix", dsn: "user:pass@tcp(localhost:3306)/orders_testing", wantErr: "must end with _test"},
		{name: "empty database", dsn: "user:pass@tcp(localhost:3306)/", wantErr: "must end with _test"},
		{name: "unix socket", dsn: "user:pass@unix(/tmp/mysql.sock)/orders_test", wantErr: "must use TCP"},
		{name: "malformed", dsn: "not-a-dsn", wantErr: "invalid MySQL DSN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckMySQLDSN(tt.dsn)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckMySQLDSN() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckMySQLDSN() unexpectedly allowed %q", tt.dsn)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CheckMySQLDSN() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
