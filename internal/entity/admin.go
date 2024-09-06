package entity

// Admins represents the admins table
type Admin struct {
	Id           int    `db:"id"`
	Username     string `db:"username"`
	PasswordHash string `db:"password_hash"`
}
