package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// UpsertEmployee inserts an employee-registry row when id == 0, otherwise updates that row, returning
// its id (gap-07 v2 A). The registry is the counterpart to salary OpexRecurring templates; it never
// itself books cost.
func (s *Store) UpsertEmployee(ctx context.Context, ins entity.EmployeeInsert, id int) (int, error) {
	params := map[string]any{
		"full_name":            strings.TrimSpace(ins.FullName),
		"role":                 ins.Role,
		"employment_start":     ins.EmploymentStart,
		"employment_end":       ins.EmploymentEnd,
		"default_currency":     nullUpper(ins.DefaultCurrency),
		"default_monthly_cost": ins.DefaultMonthlyCost,
		"note":                 ins.Note,
	}
	if id == 0 {
		newID, err := storeutil.ExecNamedLastId(ctx, s.DB, `
			INSERT INTO employee (full_name, role, employment_start, employment_end, default_currency,
			                      default_monthly_cost, note)
			VALUES (:full_name, :role, :employment_start, :employment_end, :default_currency,
			        :default_monthly_cost, :note)`, params)
		if err != nil {
			return 0, fmt.Errorf("insert employee: %w", err)
		}
		return newID, nil
	}
	// Existence is checked explicitly: a no-op UPDATE also affects 0 rows, so rows-affected can't
	// tell a typo'd id from an unchanged row — and silently "updating" nothing must not read as
	// success (g25-08).
	if err := s.checkEmployeeExists(ctx, id); err != nil {
		return 0, err
	}
	params["id"] = id
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE employee
		SET full_name = :full_name, role = :role, employment_start = :employment_start,
		    employment_end = :employment_end, default_currency = :default_currency,
		    default_monthly_cost = :default_monthly_cost, note = :note
		WHERE id = :id`, params); err != nil {
		return 0, fmt.Errorf("update employee %d: %w", id, err)
	}
	return id, nil
}

// checkEmployeeExists returns sql.ErrNoRows when no employee row has this id (g25-08).
func (s *Store) checkEmployeeExists(ctx context.Context, id int) error {
	n, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM employee WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("check employee %d exists: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// nullUpper upper-cases a NullString (currency codes) when set, leaving NULL as NULL.
func nullUpper(s sql.NullString) sql.NullString {
	if !s.Valid {
		return s
	}
	return sql.NullString{String: strings.ToUpper(strings.TrimSpace(s.String)), Valid: true}
}

// ArchiveEmployee marks an employee archived. Linked salary OpexRecurring templates keep their
// employee_id (the FK is ON DELETE SET NULL only, archive is a soft flag) so history stays
// attributed. Returns sql.ErrNoRows for a nonexistent id (g25-08).
func (s *Store) ArchiveEmployee(ctx context.Context, id int) error {
	if err := s.checkEmployeeExists(ctx, id); err != nil {
		return err
	}
	return storeutil.ExecNamed(ctx, s.DB,
		`UPDATE employee SET archived = TRUE WHERE id = :id`, map[string]any{"id": id})
}

// ListEmployees returns registry rows, active-only unless includeArchived, by name.
func (s *Store) ListEmployees(ctx context.Context, includeArchived bool) ([]entity.Employee, error) {
	where := "WHERE archived = FALSE"
	if includeArchived {
		where = ""
	}
	rows, err := storeutil.QueryListNamed[entity.Employee](ctx, s.DB, `
		SELECT id, full_name, role, employment_start, employment_end, default_currency,
		       default_monthly_cost, note, archived, created_at, updated_at
		FROM employee `+where+`
		ORDER BY archived, full_name, id`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list employees: %w", err)
	}
	return rows, nil
}
