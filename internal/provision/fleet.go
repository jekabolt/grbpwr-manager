package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/store"
)

// MigrateFleetOnce brings every active / suspended team and every ready spare whose schema_version
// is not the binary's latest forward, one database at a time, then checks its fingerprint against
// the reference (G8). Team #1 is never migrated here (store.New does it at boot); it is only
// fingerprinted and compared. A failure leaves that database behind (its calls answer UNAVAILABLE)
// and is retried on the next pass.
func (w *Worker) MigrateFleetOnce(ctx context.Context) error {
	latest := w.schema.Latest()
	if latest == "" {
		return errors.New("provision: no embedded migrations")
	}
	var errs []error

	spares, err := w.pf.ReadySpares(ctx)
	if err != nil {
		w.log.ErrorContext(ctx, "provision: list ready spares", slog.String("err", err.Error()))
		errs = append(errs, err)
	}
	for _, sp := range spares {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sp.SchemaVersion.String == latest {
			continue
		}
		if err := w.migrateSpare(ctx, sp.DBName); err != nil {
			errs = append(errs, err)
		}
	}

	teams, err := w.pf.SchemaTeams(ctx)
	if err != nil {
		w.log.ErrorContext(ctx, "provision: list teams", slog.String("err", err.Error()))
		return errors.Join(append(errs, err)...)
	}
	for _, t := range teams {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if t.SchemaVersion.String == latest {
			continue
		}
		if err := w.migrateTeam(ctx, t.ID, t.Slug, t.DBName); err != nil {
			errs = append(errs, err)
		}
	}

	w.checkTeamOne(ctx, latest)
	return errors.Join(errs...)
}

func (w *Worker) migrateSpare(ctx context.Context, dbName string) error {
	if err := w.allowed(dbName); err != nil {
		w.log.ErrorContext(ctx, "provision: spare name refused", slog.String("db", dbName), slog.String("err", err.Error()))
		return err
	}
	version, err := w.schema.Migrate(ctx, dbName)
	if err != nil {
		w.log.ErrorContext(ctx, "spare_migrate_failed", slog.String("db", dbName), slog.String("err", err.Error()))
		return err
	}
	lines, err := w.schema.Lines(ctx, dbName)
	if err != nil {
		return fmt.Errorf("provision: fingerprint spare %s: %w", dbName, err)
	}
	return w.pf.SetSpareSchema(ctx, dbName, version, store.FingerprintOf(lines))
}

func (w *Worker) migrateTeam(ctx context.Context, id int64, slug, dbName string) error {
	if err := w.allowed(dbName); err != nil {
		w.log.ErrorContext(ctx, "team_migrate_failed",
			slog.Int64("tid", id), slog.String("slug", slug), slog.String("err", "database name refused: "+err.Error()))
		return err
	}
	version, err := w.schema.Migrate(ctx, dbName)
	if err != nil {
		w.log.ErrorContext(ctx, "team_migrate_failed",
			slog.Int64("tid", id), slog.String("slug", slug), slog.String("version", w.schema.Latest()),
			slog.String("err", err.Error()))
		return err
	}
	lines, err := w.schema.Lines(ctx, dbName)
	if err != nil {
		// Migrated but not fingerprinted: record the version (the team serves again) without a
		// fingerprint; the next pass of the console shows it unknown.
		w.log.ErrorContext(ctx, "provision: fingerprint team", slog.String("slug", slug), slog.String("err", err.Error()))
		_ = w.pf.SetTeamSchema(ctx, id, version, "")
		return err
	}
	if err := w.pf.SetTeamSchema(ctx, id, version, store.FingerprintOf(lines)); err != nil {
		return err
	}
	w.log.InfoContext(ctx, "provision: team migrated", slog.String("slug", slug), slog.String("version", version))
	w.compare(ctx, slug, version, lines)
	return nil
}

// checkTeamOne fingerprints team #1 and compares it with the reference (warn only, never fatal).
func (w *Worker) checkTeamOne(ctx context.Context, latest string) {
	if w.cfg.Team1DB == "" {
		return
	}
	lines, err := w.schema.Lines(ctx, w.cfg.Team1DB)
	if err != nil {
		w.log.WarnContext(ctx, "provision: fingerprint team #1", slog.String("err", err.Error()))
		return
	}
	w.compare(ctx, "grbpwr", latest, lines)
}

// compare is G8: the database's schema lines against the reference spare at that version. A
// mismatch is a WARN schema_drift with the first differing lines; no reference yet = no verdict.
func (w *Worker) compare(ctx context.Context, slug, version string, lines []string) {
	refDB, refFP, ok, err := w.pf.ReferenceSpare(ctx, version)
	if err != nil {
		w.log.WarnContext(ctx, "provision: reference fingerprint", slog.String("err", err.Error()))
		return
	}
	if !ok || refFP == store.FingerprintOf(lines) {
		return
	}
	attrs := []any{slog.String("slug", slug), slog.String("version", version), slog.String("reference_db", refDB)}
	if err := w.allowed(refDB); err == nil {
		if want, err := w.schema.Lines(ctx, refDB); err == nil {
			attrs = append(attrs, slog.Any("first_diff", store.FirstSchemaDiff(want, lines, driftLinesShown)))
		}
	}
	w.log.WarnContext(ctx, "schema_drift", attrs...)
}
