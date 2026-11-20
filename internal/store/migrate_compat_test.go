package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// The embedded migrations parse, and the gate's set matches what the fleet migrates to.
func TestLoadSchemaSetOnEmbeddedMigrations(t *testing.T) {
	set, err := LoadSchemaSet()
	require.NoError(t, err)
	assert.Equal(t, LatestMigrationID(), set.Latest)
	assert.NotEmpty(t, set.Need)
	assert.Empty(t, set.Breaking, "no breaking team migration has shipped yet")
}

func TestSchemaGateWindow(t *testing.T) {
	set := schemacompat.Set{Latest: "0487_breaking_x.sql", Need: "0486_a.sql", Breaking: []string{"0487_breaking_x.sql"}}
	closed := SchemaGate(set)
	assert.False(t, closed(tenant.Team{ID: tenant.TeamOneID}), "team #1 is always served")
	assert.False(t, closed(tenant.Team{ID: 2, SchemaVersion: "0486_a.sql"}), "behind only the breaking file, which code does not need")
	assert.True(t, closed(tenant.Team{ID: 2, SchemaVersion: "0485_a.sql"}), "behind a needed migration")
	assert.False(t, closed(tenant.Team{ID: 2, SchemaVersion: "0490_a.sql", SchemaMinCode: "0487_breaking_x.sql"}), "ahead by additive files")
	assert.True(t, closed(tenant.Team{ID: 2, SchemaVersion: "0491_breaking_y.sql", SchemaMinCode: "0491_breaking_y.sql"}), "past a breaking file this code predates")
	assert.True(t, SchemaGate(schemacompat.Set{})(tenant.Team{ID: 2, SchemaVersion: "0486_a.sql"}), "an unreadable set fails closed")
}

// A full team migration killed mid-way (deploy restart, timeout) resumes on the next run and
// ends with exactly the schema of a clean run.
func TestTeamMigrationKilledMidwayResumes(t *testing.T) {
	const db = "x3resume_test"
	ctx := context.Background()
	drop := func() { _, _ = testDB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+db+"`") }
	drop()
	t.Cleanup(drop)
	_, err := testDB.ExecContext(ctx, "CREATE DATABASE `"+db+"` CHARACTER SET utf8mb4")
	require.NoError(t, err)

	killCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	_, err = MigrateTeam(killCtx, testCfg.DSN, db)
	cancel()
	require.Error(t, err, "the run must be cut before it finishes")
	partial, err := AppliedVersion(ctx, testCfg.DSN, db)
	require.NoError(t, err)
	require.NotEmpty(t, partial, "some migrations applied before the kill")
	require.NotEqual(t, LatestMigrationID(), partial)

	version, err := MigrateTeam(ctx, testCfg.DSN, db)
	require.NoError(t, err)
	assert.Equal(t, LatestMigrationID(), version)
	applied, err := AppliedVersion(ctx, testCfg.DSN, db)
	require.NoError(t, err)
	assert.Equal(t, LatestMigrationID(), applied)

	clean, err := DSNDatabase(testCfg.DSN)
	require.NoError(t, err)
	want, err := SchemaLines(ctx, testCfg.DSN, clean)
	require.NoError(t, err)
	got, err := SchemaLines(ctx, testCfg.DSN, db)
	require.NoError(t, err)
	assert.Equal(t, want, got, "a resumed database must equal a clean one")
}

// Owner 20.11: code N starts on grbpwr's own database at N+1 (a rollback over an additive
// release); a diverged history or a newer breaking migration still refuses.
func TestTeamOneBootsOnSchemaAheadAndRefusesDiverged(t *testing.T) {
	ctx := context.Background()
	add := func(id string) {
		_, err := testDB.ExecContext(ctx, `INSERT INTO gorp_migrations (id, applied_at) VALUES (?, NOW())`, id)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM gorp_migrations WHERE id = ?`, id) })
	}
	del := func(id string) {
		_, err := testDB.ExecContext(ctx, `DELETE FROM gorp_migrations WHERE id = ?`, id)
		require.NoError(t, err)
	}

	add("9999_from_a_newer_release.sql")
	require.NoError(t, MigrateDSN(ctx, testCfg.DSN), "code N must start on schema N+1")

	add("0001_zz_diverged.sql")
	require.Error(t, MigrateDSN(ctx, testCfg.DSN), "a diverged history must still refuse")
	del("0001_zz_diverged.sql")

	add("9999_breaking_drop_x.sql")
	require.Error(t, MigrateDSN(ctx, testCfg.DSN), "a newer breaking migration must still refuse")
}
