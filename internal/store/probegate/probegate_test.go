package probegate

import (
	"strings"
	"testing"
)

// env is a fake environment: a disposable container database, which Check must pass, and which each
// case below breaks in exactly one way.
func env(over map[string]string) func(string) string {
	base := map[string]string{
		"CI":             "true",
		EnvDisposableDB:  "grbpwr_test",
		"MYSQL_DATABASE": "grbpwr_test",
		"MYSQL_HOST":     "127.0.0.1",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestCheckPassesADisposableContainerDatabase(t *testing.T) {
	for _, over := range []map[string]string{
		nil,
		{"MYSQL_HOST": "localhost"},
		{"MYSQL_HOST": "mysql"},
		{"MYSQL_HOST": "db"},
		{"MYSQL_HOST": "MySQL"},
		{"MYSQL_HOST": "127.0.0.2"},
		{"MYSQL_HOST": "::1"},
		{"MYSQL_HOST": "[::1]"},
		{EnvDisposableDB: "probe", "MYSQL_DATABASE": "probe"},
		{EnvDisposableDB: "grbpwr_ci_42", "MYSQL_DATABASE": "grbpwr_ci_42"},
		{EnvDisposableDB: "t45-scratch", "MYSQL_DATABASE": "t45-scratch"},
	} {
		if err := Check(env(over)); err != nil {
			t.Errorf("%v: refused a disposable container database: %v", over, err)
		}
	}
}

func TestCheckRefusesAnythingElse(t *testing.T) {
	for _, tc := range []struct {
		name string
		over map[string]string
		want string
	}{
		{"no CI: TestMain would read config.toml", map[string]string{"CI": ""}, "CI is not set"},
		{"nothing declared disposable", map[string]string{EnvDisposableDB: ""}, EnvDisposableDB + " is not set"},
		{"declaration and target disagree", map[string]string{EnvDisposableDB: "grbpwr_test", "MYSQL_DATABASE": "grbpwr_test2"}, "must name the same database"},
		{"the production name", map[string]string{EnvDisposableDB: "grbpwr", "MYSQL_DATABASE": "grbpwr"}, "real base"},
		{"the beta name", map[string]string{EnvDisposableDB: "grbpwr_beta", "MYSQL_DATABASE": "grbpwr_beta"}, "real base"},
		{"the managed default", map[string]string{EnvDisposableDB: "defaultdb", "MYSQL_DATABASE": "defaultdb"}, "real base"},
		{"a production word inside", map[string]string{EnvDisposableDB: "grbpwr_prod_test", "MYSQL_DATABASE": "grbpwr_prod_test"}, `contains "prod"`},
		{"a beta word inside", map[string]string{EnvDisposableDB: "test_beta", "MYSQL_DATABASE": "test_beta"}, `contains "beta"`},
		{"no disposable marker", map[string]string{EnvDisposableDB: "grbpwr2", "MYSQL_DATABASE": "grbpwr2"}, "does not say it is disposable"},
		{"a marker only as a substring", map[string]string{EnvDisposableDB: "contest", "MYSQL_DATABASE": "contest"}, "does not say it is disposable"},
		{"a managed host", map[string]string{"MYSQL_HOST": "db-mysql-fra1-12345-do-user-1-0.b.db.ondigitalocean.com"}, "is not local"},
		{"a remote address", map[string]string{"MYSQL_HOST": "10.0.0.5"}, "is not local"},
		{"no host", map[string]string{"MYSQL_HOST": ""}, "is not local"},
		// A bare name is not local: the resolver may complete it with a search domain
		// (REVIEW-T45-codex-2). Only the named CI services pass.
		{"a bare name outside the list", map[string]string{"MYSQL_HOST": "prod-db"}, "is not local"},
		{"a bare name that starts like a service", map[string]string{"MYSQL_HOST": "mysql2"}, "is not local"},
		{"a bare name that ends like a service", map[string]string{"MYSQL_HOST": "grbpwr-db"}, "is not local"},
		{"a service name under a domain", map[string]string{"MYSQL_HOST": "mysql.internal"}, "is not local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(env(tc.over))
			if err == nil {
				t.Fatalf("passed: %v", tc.over)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused for the wrong reason: %v (want %q)", err, tc.want)
			}
		})
	}
}
