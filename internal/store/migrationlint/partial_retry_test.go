package migrationlint

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// partialRetryEnforceAfter is the last migration that existed when the startup lock wait was
// bounded. Applied files are immutable and already survived their start.
const partialRetryEnforceAfter = 446

var (
	retryLineCommentRe = regexp.MustCompile(`(?m)--.*$`)
	retryBlockComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	retryQuotedRe      = regexp.MustCompile(`'(?:[^'\\]|\\.|'')*'`)
	retryDDLRe         = regexp.MustCompile(`(?im)^[ \t]*(ALTER\s+TABLE|RENAME\s+TABLE|CREATE\s+((UNIQUE|FULLTEXT|SPATIAL)\s+)?INDEX|DROP\s+INDEX|(CREATE|DROP)\s+TABLE|(CREATE(\s+OR\s+REPLACE)?|DROP)\s+VIEW|(CREATE|DROP)\s+(TRIGGER|PROCEDURE|FUNCTION|EVENT)|EXECUTE)\b`)
	retryCreateObjRe   = regexp.MustCompile(`(?i)^CREATE\s+(TRIGGER|PROCEDURE|FUNCTION|EVENT)\s+(\S+?)[\s(]`)
)

// A lock-wait timeout or a canceled start can stop a migration at any DDL, or right after one
// committed and before its gorp_migrations row. DDL auto-commits, so the next start runs the file
// from the top and a raw DDL that already succeeded fails forever on "duplicate column". Every DDL
// therefore has to re-run safely, the file's only or last one included.
func TestNewMigrationsSurvivePartialRetry(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", migrationsDir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := migrationPrefixRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil || num <= partialRetryEnforceAfter {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if stmt := firstUnsafeRetryDDL(string(body)); stmt != "" {
			t.Errorf("%s: %q fails when the file is re-run; guard it with information_schema + PREPARE", name, stmt)
		}
	}
}

func firstUnsafeRetryDDL(body string) string {
	up, _, _ := strings.Cut(body, "-- +migrate Down")
	up = retryLineCommentRe.ReplaceAllString(up, "")
	up = retryBlockComment.ReplaceAllString(up, "")
	up = retryQuotedRe.ReplaceAllString(up, "''")

	guardFrom := 0
	for _, loc := range retryDDLRe.FindAllStringIndex(up, -1) {
		stmt, _, _ := strings.Cut(up[loc[0]:], ";")
		stmt = strings.Join(strings.Fields(stmt), " ")
		upper := strings.ToUpper(stmt)
		switch {
		case strings.HasPrefix(upper, "EXECUTE"):
			// The prepared text sits in a quoted string, so only the guard around it can be seen.
			if !strings.Contains(strings.ToUpper(up[guardFrom:loc[0]]), "INFORMATION_SCHEMA") {
				return stmt
			}
			guardFrom = loc[1]
		case strings.Contains(upper, "IF NOT EXISTS"), strings.Contains(upper, "IF EXISTS"),
			strings.HasPrefix(upper, "CREATE OR REPLACE"):
		default:
			if obj := retryCreateObjRe.FindStringSubmatch(stmt); obj != nil &&
				droppedBefore(up[:loc[0]], obj[1], obj[2]) {
				continue
			}
			return stmt
		}
	}
	return ""
}

func droppedBefore(text, kind, name string) bool {
	re := regexp.MustCompile(`(?i)\bDROP\s+` + kind + `\s+IF\s+EXISTS\s+` + regexp.QuoteMeta(name) + `\b`)
	return re.MatchString(text)
}

func TestFirstUnsafeRetryDDL(t *testing.T) {
	const guard = "SET @ddl = (SELECT IF(COUNT(*) = 0, 'ALTER TABLE a ADD x INT', 'SELECT 1') FROM information_schema.COLUMNS);\nPREPARE stmt FROM @ddl;\nEXECUTE stmt;\nDEALLOCATE PREPARE stmt;\n"
	cases := map[string]struct {
		body string
		bad  bool
	}{
		"single raw":          {"-- +migrate Up\nALTER TABLE a ADD COLUMN x INT;\n", true},
		"guarded then raw":    {"-- +migrate Up\n" + guard + "ALTER TABLE a ADD COLUMN x INT;\n", true},
		"two guarded":         {"-- +migrate Up\n" + guard + guard, false},
		"dynamic unguarded":   {"-- +migrate Up\nSET @ddl = 'ALTER TABLE a ADD x INT';\nPREPARE stmt FROM @ddl;\nEXECUTE stmt;\n", true},
		"second not guarded":  {"-- +migrate Up\n" + guard + "SET @ddl = 'ALTER TABLE a ADD y INT';\nPREPARE stmt FROM @ddl;\nEXECUTE stmt;\n", true},
		"idempotent create":   {"-- +migrate Up\nCREATE TABLE IF NOT EXISTS a (id INT);\nDROP TABLE IF EXISTS b;\n", false},
		"multiline if exists": {"-- +migrate Up\nCREATE TABLE\n  IF NOT EXISTS a (id INT);\n", false},
		"raw create table":    {"-- +migrate Up\nCREATE TABLE a (id INT);\n", true},
		"fulltext index":      {"-- +migrate Up\nCREATE FULLTEXT INDEX i ON a (x);\n", true},
		"raw view":            {"-- +migrate Up\nCREATE VIEW v AS SELECT 1;\n", true},
		"replace view":        {"-- +migrate Up\nCREATE OR REPLACE VIEW v AS SELECT 1;\n", false},
		"raw trigger":         {"-- +migrate Up\nCREATE TRIGGER t BEFORE INSERT ON a FOR EACH ROW SET @x = 1;\n", true},
		"dropped first":       {"-- +migrate Up\nDROP PROCEDURE IF EXISTS p;\nCREATE PROCEDURE p() SELECT 1;\n", false},
		"other name dropped":  {"-- +migrate Up\nDROP PROCEDURE IF EXISTS q;\nCREATE PROCEDURE p() SELECT 1;\n", true},
		"down is ignored":     {"-- +migrate Up\n" + guard + "-- +migrate Down\nALTER TABLE a DROP COLUMN x;\n", false},
		"text in comment":     {"-- +migrate Up\n-- ALTER TABLE z ADD y INT;\n" + guard, false},
		"data only":           {"-- +migrate Up\nUPDATE a SET x = 1;\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := firstUnsafeRetryDDL(tc.body); (got != "") != tc.bad {
				t.Fatalf("got %q, want bad=%v", got, tc.bad)
			}
		})
	}
}
