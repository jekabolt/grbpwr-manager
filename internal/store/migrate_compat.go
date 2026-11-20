package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/go-sql-driver/mysql"

	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// LoadSchemaSet reads the "-- compat:" declarations of the embedded team migrations (X3): the
// binary's Latest, Need and breaking list. Read once at boot, never per request. An error means a
// migration's name and header disagree, which the migration lint refuses before a build ships.
func LoadSchemaSet() (schemacompat.Set, error) {
	ms, err := teamMigrationSource().FindMigrations()
	if err != nil {
		return schemacompat.Set{}, fmt.Errorf("read embedded migrations: %w", err)
	}
	out := make([]schemacompat.Migration, 0, len(ms))
	for _, m := range ms {
		f, err := fs.Open("sql/" + m.Id)
		if err != nil {
			return schemacompat.Set{}, fmt.Errorf("open migration %s: %w", m.Id, err)
		}
		body, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return schemacompat.Set{}, fmt.Errorf("read migration %s: %w", m.Id, err)
		}
		h, err := schemacompat.ParseHeader(string(body))
		if err != nil {
			return schemacompat.Set{}, fmt.Errorf("migration %s: %w", m.Id, err)
		}
		out = append(out, schemacompat.Migration{ID: m.Id, Header: h})
	}
	return schemacompat.Build(out)
}

// SchemaGate is the edge predicate tenantbind.Binder.UseSchemaCheck takes, X3's compatibility
// window instead of SchemaBehind's equality: «this binary must not serve team t». Team #1 is always
// served (store.New migrates it before the server listens). Any other team is served while its
// version is at least set.Need and its min code (newest breaking migration applied to it) is at
// most set.Latest: a team behind only lazy migrations, or ahead only by additive ones, stays open.
func SchemaGate(set schemacompat.Set) func(tenant.Team) bool {
	return func(t tenant.Team) bool {
		if t.ID == tenant.TeamOneID {
			return false
		}
		return !set.Servable(t.SchemaVersion, t.SchemaMinCode)
	}
}

// AppliedVersion returns the newest migration recorded in database dbName's gorp_migrations, in
// sql-migrate order ("" when none): what a partly failed run really reached.
func AppliedVersion(ctx context.Context, baseDSN, dbName string) (string, error) {
	dsn, err := TeamDSN(baseDSN, dbName)
	if err != nil {
		return "", err
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse team dsn: %w", err)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return "", fmt.Errorf("applied version: connector: %w", err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(ctx, fingerprintTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT id FROM gorp_migrations`)
	if err != nil {
		return "", fmt.Errorf("applied version of %s: %w", dbName, err)
	}
	defer rows.Close()
	newest := ""
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		if newest == "" || schemacompat.Compare(id, newest) > 0 {
			newest = id
		}
	}
	return newest, rows.Err()
}
