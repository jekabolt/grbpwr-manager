// Package migrationlint holds static, database-free guards over the SQL
// migration files in ../sql. Unlike the store package's tests (which require a
// live MySQL via TestMain), these run anywhere with plain `go test`. The
// Migration safety workflow runs this package before database replay, so a bad
// new migration is caught before it reaches automigrate on beta or prod.
package migrationlint
