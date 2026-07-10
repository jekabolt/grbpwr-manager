package entity

import "time"

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
}
