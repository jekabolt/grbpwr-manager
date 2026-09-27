// Package probegate decides whether a DATABASE PROBE of internal/store may run at all, and it
// decides it before a single connection is opened.
//
// Why it exists. internal/store's TestMain connects to whatever its configuration names — the MYSQL_*
// environment when CI is set, ../../config/config.toml otherwise — migrates it, runs, and then DROPS
// EVERY TABLE AND VIEW of that database. On a developer machine that configuration has pointed at a
// real base. A probe file that relies on «runs only in CI» written in a comment is one ordinary
// `go test` away from destroying one (REVIEW-T45-codex, finding 7).
//
// So a probe file is built only with `-tags integration` AND its init() — which runs before TestMain —
// calls Check and exits the test binary on a refusal. Check passes only an environment that NAMES a
// disposable database and in which everything TestMain will connect to agrees with that name:
//
//   - CI is set, so TestMain builds its DSN from MYSQL_* and never reads config.toml;
//   - GRBPWR_DISPOSABLE_DB names the database, and it is exactly MYSQL_DATABASE — the declaration and
//     the target cannot drift apart;
//   - the name carries a disposable marker (test, probe, ci, scratch, tmp, disposable) and is not one
//     of the names a real base goes by;
//   - MYSQL_HOST is local: loopback (localhost, 127.0.0.0/8, ::1) or one of the CI service names in
//     ciServiceHosts — an explicit list, never a pattern: a bare name like «prod-db» goes through the
//     machine's DNS search domains and can resolve to a remote server (REVIEW-T45-codex-2).
//
// The package imports no driver and opens nothing; its own tests run without a database.
package probegate

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// EnvDisposableDB is the variable a probe run must set to the name of the database it may destroy.
const EnvDisposableDB = "GRBPWR_DISPOSABLE_DB"

// productionNames are database names a real base of this project goes by (or a server's own
// schemas). Refused whatever else the environment says.
var productionNames = map[string]bool{
	"grbpwr": true, "grbpwr_beta": true, "grbpwr_prod": true, "grbpwr_production": true,
	"grbpwr_manager": true, "defaultdb": true,
	"mysql": true, "sys": true, "information_schema": true, "performance_schema": true,
}

// productionWords refuse a name that merely CONTAINS them («grbpwr_prod_copy» is not disposable
// because somebody called it a copy).
var productionWords = []string{"prod", "beta", "live", "master", "main"}

// disposableMarker is what a disposable database's name must say about itself, as a whole word
// between separators.
var disposableMarker = regexp.MustCompile(`(^|[_\-.])(test|tests|probe|probes|ci|scratch|tmp|disposable)([_\-.]|\d|$)`)

// ciServiceHosts are the container service names a CI job reaches its throwaway MySQL by (the label
// of a docker-compose or GitHub Actions service). Named one by one: any other bare host name is
// refused, because the resolver may complete it with a search domain and land on a real server.
var ciServiceHosts = map[string]bool{"mysql": true, "db": true}

// Check returns nil when the environment names a disposable database the probes may migrate, write
// and drop, and a refusal saying what is missing otherwise. getenv is os.Getenv in a probe file.
func Check(getenv func(string) string) error {
	if strings.TrimSpace(getenv("CI")) == "" {
		return fmt.Errorf("CI is not set: without it the store's TestMain reads ../../config/config.toml, " +
			"which is a real database, and drops every table of it at the end")
	}
	declared := strings.TrimSpace(getenv(EnvDisposableDB))
	if declared == "" {
		return fmt.Errorf("%s is not set: name the throwaway database these probes may migrate and drop", EnvDisposableDB)
	}
	target := strings.TrimSpace(getenv("MYSQL_DATABASE"))
	if declared != target {
		return fmt.Errorf("%s=%q but MYSQL_DATABASE=%q: the probes connect to MYSQL_DATABASE, so the two must "+
			"name the same database", EnvDisposableDB, declared, target)
	}
	name := strings.ToLower(target)
	if productionNames[name] {
		return fmt.Errorf("database %q is the name of a real base, never a disposable one", target)
	}
	for _, w := range productionWords {
		if strings.Contains(name, w) {
			return fmt.Errorf("database %q contains %q: a disposable database's name must not look like a real one", target, w)
		}
	}
	if !disposableMarker.MatchString(name) {
		return fmt.Errorf("database %q does not say it is disposable: its name must carry one of test, probe, "+
			"ci, scratch, tmp or disposable as a word (e.g. grbpwr_test)", target)
	}
	host := strings.ToLower(strings.TrimSpace(getenv("MYSQL_HOST")))
	if !localHost(host) {
		return fmt.Errorf("MYSQL_HOST=%q is not local: the probes run against a container reached by loopback "+
			"(localhost, 127.0.0.1, ::1) or by a CI service name (mysql, db), never any other host", host)
	}
	return nil
}

// localHost accepts loopback (by name or address) and the CI service names in ciServiceHosts —
// nothing else, however local a bare name looks.
func localHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return ciServiceHosts[host]
}
