package tenanttest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// Databases of this package only: the store tests own grbpwr_test and drop its tables, the
// tenantdb tests own tenantdb_one_test / tdbt_*.
const (
	dbOne     = "tnt_one_test"
	dbPrefix  = "tntt_"
	dbTwo     = dbPrefix + "b0000001"
	dbControl = "tntt_control"
)

func TestMain(m *testing.M) {
	os.Exit(runPackage(m))
}

func rootDSN(db string) string {
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
		os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), os.Getenv("MYSQL_HOST"), port, db)
}

// runPackage holds the server-wide schema lock shared with the tenantdb / teamsync tests (the
// disposable test MySQL keeps its data in memory; two packages migrating full team schemas at once
// can exhaust it), then drops this package's databases.
func runPackage(m *testing.M) int {
	if os.Getenv("MYSQL_HOST") == "" {
		return m.Run()
	}
	r, err := sql.Open("mysql", rootDSN(""))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer r.Close()
	ctx := context.Background()
	conn, err := r.Conn(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('teams_schema_tests', 900)`).Scan(&got); err != nil || got.Int64 != 1 {
		fmt.Fprintf(os.Stderr, "test schema lock: %v %v\n", got, err)
		return 1
	}
	defer conn.ExecContext(ctx, `SELECT RELEASE_LOCK('teams_schema_tests')`)
	code := m.Run()
	if h != nil {
		h.close()
	}
	for _, db := range []string{dbOne, dbTwo, dbControl} {
		_, _ = r.Exec("DROP DATABASE IF EXISTS `" + db + "`")
	}
	return code
}
