package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestEmployeeRegistry exercises gap-07 v2 A: the employee registry CRUD, linking a salary
// OpexRecurring template to an employee via employee_id, the active/archived list filter, and the
// ON DELETE SET NULL guarantee that removing an employee never deletes booked salary history.
func TestEmployeeRegistry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	mtr := s.Metrics()

	var empID, recID int
	t.Cleanup(func() {
		// Fresh context: the test's ctx is already cancelled by its `defer cancel()` (defers run before
		// Cleanups), which would make these DELETEs no-ops — and a leaked ACTIVE salary template would
		// then inflate TestOpexV2's global materialise count.
		cctx := context.Background()
		if recID != 0 {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM opex_line WHERE recurring_id = ?", recID)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM opex_recurring WHERE id = ?", recID)
		}
		if empID != 0 {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM employee WHERE id = ?", empID)
		}
	})

	// --- create an employee ---
	empID, err = mtr.UpsertEmployee(ctx, entity.EmployeeInsert{
		FullName:           "NF-EMP Мария",
		Role:               sql.NullString{String: "seamstress", Valid: true},
		EmploymentStart:    sql.NullTime{Time: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), Valid: true},
		DefaultCurrency:    sql.NullString{String: "EUR", Valid: true},
		DefaultMonthlyCost: decimal.NullDecimal{Decimal: decimal.RequireFromString("1800"), Valid: true},
	}, 0)
	require.NoError(t, err)
	require.NotZero(t, empID)

	// read back via list.
	list, err := mtr.ListEmployees(ctx, false)
	require.NoError(t, err)
	var found *entity.Employee
	for i := range list {
		if list[i].Id == empID {
			found = &list[i]
		}
	}
	require.NotNil(t, found, "new employee is in the active list")
	require.Equal(t, "NF-EMP Мария", found.FullName)
	require.Equal(t, "seamstress", found.Role.String)
	require.Equal(t, 15, found.EmploymentStart.Time.Day(), "exact day stored")
	require.True(t, found.DefaultMonthlyCost.Valid && found.DefaultMonthlyCost.Decimal.Equal(decimal.RequireFromString("1800")))

	// --- update the employee (rename + set end) ---
	_, err = mtr.UpsertEmployee(ctx, entity.EmployeeInsert{
		FullName:      "NF-EMP Мария Иванова",
		Role:          sql.NullString{String: "senior seamstress", Valid: true},
		EmploymentEnd: sql.NullTime{Time: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Valid: true},
	}, empID)
	require.NoError(t, err)

	// --- link a salary recurring template to the employee ---
	recID, err = mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-EMP зарплата Мария", Category: "salaries",
		Amount: decimal.RequireFromString("1800"), Currency: "EUR",
		ActiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EmployeeId: sql.NullInt32{Int32: int32(empID), Valid: true},
	}, 0)
	require.NoError(t, err)

	recs, err := mtr.ListOpexRecurring(ctx, false)
	require.NoError(t, err)
	var rec *entity.OpexRecurring
	for i := range recs {
		if recs[i].Id == recID {
			rec = &recs[i]
		}
	}
	require.NotNil(t, rec)
	require.True(t, rec.EmployeeId.Valid && rec.EmployeeId.Int32 == int32(empID), "salary linked to the employee")

	// --- archive hides the employee from the active list but keeps the row + link ---
	require.NoError(t, mtr.ArchiveEmployee(ctx, empID))
	active, err := mtr.ListEmployees(ctx, false)
	require.NoError(t, err)
	for _, e := range active {
		require.NotEqual(t, empID, e.Id, "archived employee is hidden from the active list")
	}
	all, err := mtr.ListEmployees(ctx, true)
	require.NoError(t, err)
	var stillThere bool
	for _, e := range all {
		if e.Id == empID {
			stillThere = true
			require.True(t, e.Archived)
		}
	}
	require.True(t, stillThere, "archived employee still visible with includeArchived")

	// --- g25-08: update/archive of a nonexistent id is NotFound, not a silent success ---
	_, err = mtr.UpsertEmployee(ctx, entity.EmployeeInsert{FullName: "NF Ghost"}, 99999999)
	require.ErrorIs(t, err, sql.ErrNoRows, "updating a nonexistent employee is refused")
	require.ErrorIs(t, mtr.ArchiveEmployee(ctx, 99999999), sql.ErrNoRows, "archiving a nonexistent employee is refused")

	// --- deleting the employee SET NULLs the link, never deletes the salary history ---
	_, err = testDB.ExecContext(ctx, "DELETE FROM employee WHERE id = ?", empID)
	require.NoError(t, err)
	empID = 0 // cleaned up

	recs, err = mtr.ListOpexRecurring(ctx, false)
	require.NoError(t, err)
	var afterDelete *entity.OpexRecurring
	for i := range recs {
		if recs[i].Id == recID {
			afterDelete = &recs[i]
		}
	}
	require.NotNil(t, afterDelete, "the salary template survives employee deletion")
	require.False(t, afterDelete.EmployeeId.Valid, "employee_id is SET NULL on employee delete")
}
