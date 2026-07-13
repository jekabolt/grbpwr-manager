// Package migrationlint holds static, database-free guards over the SQL
// migration files in ../sql. Unlike the store package's tests (which require a
// live MySQL via TestMain), these run anywhere — including plain `go test` and
// CI — so a bad new migration is caught before it reaches automigrate on beta or
// prod.
package migrationlint
