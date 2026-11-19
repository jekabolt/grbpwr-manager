package platformauth

import (
	"errors"
	"strings"
	"testing"
)

// CheckOperatorPassword (teams D1, the console's delete confirmation): the caller's own password,
// operators only, under the sign-in limiters, and the password never in an error text.
func TestCheckOperatorPassword(t *testing.T) {
	f := newFlow(t)
	const pw = "Op-Delete-Pass-4417"
	hash, err := f.svc.d.Hasher.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator) VALUES
 (601, 'Op6@x.io', 'op6', ?, 'active', 1), (602, 'ann6@x.io', 'ann6', ?, 'active', 0)`, hash, hash); err != nil {
		t.Fatal(err)
	}
	ctx := ctxFrom("10.0.6.1", "")
	check := func(uid int64, p string) error { return f.svc.CheckOperatorPassword(ctx, uid, p) }

	if err := check(601, pw); err != nil {
		t.Fatalf("right password: %v", err)
	}
	for _, p := range []string{"", "wrong", strings.Repeat("x", MaxPasswordLen+1)} {
		if err := check(601, p); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("password of %d chars: %v", len(p), err)
		}
	}
	if err := check(602, pw); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("not an operator: %v", err)
	}
	if err := check(999, pw); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("unknown user: %v", err)
	}
	// Five attempts a minute per login (case-insensitive email): the sixth is refused, even right.
	if err := check(601, "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("fifth attempt: %v", err)
	}
	err = check(601, pw)
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("sixth attempt: %v", err)
	}
	if strings.Contains(err.Error(), pw) {
		t.Fatal("password in the error text")
	}
}
