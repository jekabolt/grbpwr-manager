package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// TestConvertPbEmployeeToEntity covers validation + a full field round-trip of the employee registry
// insert (gap-07 v2 A).
func TestConvertPbEmployeeToEntity(t *testing.T) {
	ok := &pb_admin.EmployeeInsert{
		FullName:           "  Мария Швея  ",
		Role:               "  seamstress ",
		EmploymentStart:    "2026-01-15",
		EmploymentEnd:      "2026-06-30",
		DefaultCurrency:    "eur",
		DefaultMonthlyCost: dec("1800.00"),
		Note:               " night shift ",
	}
	got, err := ConvertPbEmployeeToEntity(ok)
	require.NoError(t, err)
	require.Equal(t, "Мария Швея", got.FullName, "name trimmed")
	require.Equal(t, "seamstress", got.Role.String)
	require.True(t, got.EmploymentStart.Valid)
	require.Equal(t, 2026, got.EmploymentStart.Time.Year())
	require.Equal(t, 15, got.EmploymentStart.Time.Day(), "day preserved (not month-snapped)")
	require.True(t, got.EmploymentEnd.Valid)
	require.Equal(t, "EUR", got.DefaultCurrency.String, "currency upper-cased")
	require.True(t, got.DefaultMonthlyCost.Valid && got.DefaultMonthlyCost.Decimal.String() == "1800")
	require.Equal(t, "night shift", got.Note.String)

	// minimal: only a name; everything else NULL.
	min, err := ConvertPbEmployeeToEntity(&pb_admin.EmployeeInsert{FullName: "Иван"})
	require.NoError(t, err)
	require.False(t, min.Role.Valid)
	require.False(t, min.EmploymentStart.Valid)
	require.False(t, min.EmploymentEnd.Valid)
	require.False(t, min.DefaultCurrency.Valid)
	require.False(t, min.DefaultMonthlyCost.Valid)

	// failures (length/format guards mirror the columns — g25-09: clean InvalidArgument, not a DB 500).
	for name, in := range map[string]*pb_admin.EmployeeInsert{
		"empty name":       {FullName: "  "},
		"bad start date":   {FullName: "x", EmploymentStart: "2026-13-40"},
		"end before start": {FullName: "x", EmploymentStart: "2026-06-01", EmploymentEnd: "2026-01-01"},
		"negative cost":    {FullName: "x", DefaultMonthlyCost: dec("-1")},
		"name too long":    {FullName: strings.Repeat("x", maxVarchar191+1)},
		"role too long":    {FullName: "x", Role: strings.Repeat("x", maxVarchar64+1)},
		"note too long":    {FullName: "x", Note: strings.Repeat("x", maxVarchar255+1)},
		"bad currency":     {FullName: "x", DefaultCurrency: "EURO"},
	} {
		_, err := ConvertPbEmployeeToEntity(in)
		require.Error(t, err, name)
	}
	_, err = ConvertPbEmployeeToEntity(nil)
	require.Error(t, err, "nil employee rejected")
}

// TestEmployeeToPb round-trips a stored employee back to protobuf.
func TestEmployeeToPb(t *testing.T) {
	in, err := ConvertPbEmployeeToEntity(&pb_admin.EmployeeInsert{
		FullName: "Мария", Role: "seamstress", EmploymentStart: "2026-01-15",
		DefaultCurrency: "EUR", DefaultMonthlyCost: dec("1800"),
	})
	require.NoError(t, err)
	pb := EmployeeToPb(entity.Employee{Id: 7, EmployeeInsert: in, Archived: true})
	require.Equal(t, int32(7), pb.Id)
	require.True(t, pb.Archived)
	require.Equal(t, "Мария", pb.Employee.FullName)
	require.Equal(t, "2026-01-15", pb.Employee.EmploymentStart)
	require.Equal(t, "", pb.Employee.EmploymentEnd, "unset end stays empty")
	require.Equal(t, "EUR", pb.Employee.DefaultCurrency)
	require.Equal(t, "1800", pb.Employee.DefaultMonthlyCost.Value)
}

// TestOpexRecurringEmployeeLink verifies the salary→employee link survives the pb⇄entity round-trip
// and that 0 means "not linked".
func TestOpexRecurringEmployeeLink(t *testing.T) {
	linked, err := ConvertPbOpexRecurringToEntity(&pb_admin.OpexRecurringInsert{
		Label: "зарплата — Мария", Category: "salaries", Amount: dec("1800"),
		Currency: "EUR", ActiveFrom: "2026-01-01", EmployeeId: 7,
	})
	require.NoError(t, err)
	require.True(t, linked.EmployeeId.Valid && linked.EmployeeId.Int32 == 7)

	back := OpexRecurringToPb(entity.OpexRecurring{Id: 3, OpexRecurringInsert: linked})
	require.Equal(t, int32(7), back.Recurring.EmployeeId)

	// 0 → not linked.
	unlinked, err := ConvertPbOpexRecurringToEntity(&pb_admin.OpexRecurringInsert{
		Label: "rent", Category: "rent", Amount: dec("500"), Currency: "EUR", ActiveFrom: "2026-01-01",
	})
	require.NoError(t, err)
	require.False(t, unlinked.EmployeeId.Valid, "employee_id 0 → NULL")
	require.Equal(t, int32(0), OpexRecurringToPb(entity.OpexRecurring{OpexRecurringInsert: unlinked}).Recurring.EmployeeId)
}
