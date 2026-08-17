package entity

import (
	"errors"
	"time"
)

// Admin represents a row of the admins table.
type Admin struct {
	Id           int       `db:"id"`
	Username     string    `db:"username"`
	PasswordHash string    `db:"password_hash"`
	IsSuper      bool      `db:"is_super"`
	Disabled     bool      `db:"disabled"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// AccessLevel is the level of access an account has to an admin-panel section.
// write implies read.
type AccessLevel string

const (
	// AccessRead permits the section's read-only RPCs.
	AccessRead AccessLevel = "read"
	// AccessWrite permits the section's mutating RPCs (and, implicitly, its reads).
	AccessWrite AccessLevel = "write"
)

// Valid reports whether l is a recognized access level.
func (l AccessLevel) Valid() bool {
	return l == AccessRead || l == AccessWrite
}

// Covers reports whether an account holding access level l may perform an RPC
// that requires level need. write covers read; read covers only read.
func (l AccessLevel) Covers(need AccessLevel) bool {
	if l == AccessWrite {
		return true
	}
	return l == AccessRead && need == AccessRead
}

// AdminPermission grants an account a level of access to one section.
type AdminPermission struct {
	Section string      `db:"section"`
	Access  AccessLevel `db:"access"`
}

// AdminAccount is an admin account with its resolved permission set.
type AdminAccount struct {
	Admin
	Permissions []AdminPermission
	// Specialties is what the person says they do. It grants nothing — see
	// AdminSpecialty — and is resolved for the whole page in one grouped query.
	Specialties []string
}

// AdminRef is a person as every picker in the panel shows them. Deliberately
// narrower than AdminAccount: no permissions, no timestamps, nothing that would
// make a picker response a description of who can do what.
type AdminRef struct {
	Id       int    `db:"id"`
	Username string `db:"username"`
	IsSuper  bool   `db:"is_super"`
	// Specialties is the «kirill · конструктор» byline and the second thing the
	// picker's search matches on, after the username.
	Specialties []string `db:"-"`
}

// MaxAdminSpecialtyVocabulary caps the SHARED specialty dictionary. A specialty
// is NOT a permission and never becomes one: rights live in admin_permission, and
// nothing in the codebase may branch on a specialty. But anybody authenticated may
// mint a new entry (решение Р1), nobody may delete one (there is no such RPC, and
// the link FK is RESTRICT), and the whole vocabulary rides on every people-picker
// response — so an unbounded dictionary is a one-way inflation of a panel-wide
// payload. The bound catches a loop, not a person.
const MaxAdminSpecialtyVocabulary = 200

// ErrAdminSpecialtyVocabularyFull is returned instead of silently dropping the
// new name: «сохранено» on a write that stored nothing is the worse answer.
var ErrAdminSpecialtyVocabularyFull = errors.New("the specialty vocabulary is full")
