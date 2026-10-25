package migreplay

import (
	"context"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

func TestRunRejectsUnsafeDSNsBeforeConnecting(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "remote host",
			dsn:  "user:pass@tcp(db.example.com:3306)/migration_replay_test",
			want: "not allowed",
		},
		{
			name: "production database name",
			dsn:  "user:pass@tcp(127.0.0.1:3306)/grbpwr",
			want: "protected",
		},
		{
			name: "multi statements",
			dsn:  "user:pass@tcp(127.0.0.1:3306)/migration_replay_test?multiStatements=true",
			want: "multiStatements=true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(context.Background(), Config{DSN: tt.dsn})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestFormatMigrationErrorNamesFileAndMySQLCode(t *testing.T) {
	err := &migrate.TxError{
		Migration: &migrate.Migration{Id: "0433_bad_check.sql"},
		Err:       &mysql.MySQLError{Number: 3819, Message: "Check constraint is violated"},
	}
	message := formatMigrationError("new migration", err).Error()
	for _, want := range []string{"0433_bad_check.sql", "MySQL 3819", "Check constraint is violated"} {
		if !strings.Contains(message, want) {
			t.Errorf("formatted error %q does not contain %q", message, want)
		}
	}
}
