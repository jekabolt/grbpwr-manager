package platformauth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

const testPepper = "0123456789abcdef0123456789abcdef-pepper"

func TestTransliterateAndSuggestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Ателье Жук":   "atele-zhuk",
		"Acme Studio!": "acme-studio",
		"  --  ":       "",
	} {
		if got := Transliterate(in); got != want {
			t.Errorf("Transliterate(%q) = %q, want %q", in, got, want)
		}
	}
	held := map[string]bool{"acme-studio": true, "acme-studio-2": true}
	got, err := SuggestSlug("Acme Studio", func(s string) (bool, error) { return held[s], nil })
	if err != nil || got != "acme-studio-3" {
		t.Fatalf("SuggestSlug = %q, %v", got, err)
	}
	got, err = SuggestSlug("!!!", func(string) (bool, error) { return false, nil })
	if err != nil || !strings.HasPrefix(got, "team-") || tenant.ValidSlug(got) != nil {
		t.Fatalf("empty name slug = %q, %v", got, err)
	}
	long := strings.Repeat("a", 60)
	got, err = SuggestSlug(long, func(s string) (bool, error) { return len(s) == maxSlugLen && !strings.Contains(s, "-"), nil })
	if err != nil || len(got) > maxSlugLen || !strings.HasSuffix(got, "-2") {
		t.Fatalf("long slug = %q, %v", got, err)
	}
	boom := errors.New("db down")
	if _, err := SuggestSlug("x y", func(string) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatalf("taken error not returned: %v", err)
	}
}

func TestCodes(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		c, err := NewCode()
		if err != nil || len(c) != 6 {
			t.Fatalf("NewCode = %q, %v", c, err)
		}
		if _, ok := NormalizeCode(c); !ok {
			t.Fatalf("code %q not six digits", c)
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Fatalf("codes repeat too often: %d distinct of 200", len(seen))
	}
	h := CodeHash(testPepper, PurposeVerify, 7, "482913")
	if !CodeMatches(testPepper, PurposeVerify, 7, "482913", h) {
		t.Fatal("code does not match its own hash")
	}
	for _, c := range []struct {
		purpose string
		uid     int64
		code    string
		pepper  string
	}{{PurposeReset, 7, "482913", testPepper}, {PurposeVerify, 8, "482913", testPepper}, {PurposeVerify, 7, "482914", testPepper},
		{PurposeVerify, 7, "482913", testPepper + "x"}} {
		if CodeMatches(c.pepper, c.purpose, c.uid, c.code, h) {
			t.Fatalf("hash matched a different input %+v", c)
		}
	}
	if strings.Contains(h, "482913") {
		t.Fatal("hash contains the code")
	}
	for in, want := range map[string]string{"482 913": "482913", "482-913": "482913", " 482913 ": "482913"} {
		if got, ok := NormalizeCode(in); !ok || got != want {
			t.Errorf("NormalizeCode(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"48291", "4829130", "48a913", ""} {
		if _, ok := NormalizeCode(bad); ok {
			t.Errorf("NormalizeCode(%q) accepted", bad)
		}
	}
	if SpacedCode("482913") != "482 913" {
		t.Fatal("SpacedCode")
	}
}

func TestTokens(t *testing.T) {
	tok, hash, err := NewToken()
	if err != nil || len(tok) < 40 || HashToken(tok) != hash || hash == tok || len(hash) != 64 {
		t.Fatalf("NewToken = %q %q %v", tok, hash, err)
	}
	tok2, _, _ := NewToken()
	if tok2 == tok {
		t.Fatal("tokens repeat")
	}
	ph, err := pwhash.New(16, 1000)
	if err != nil {
		t.Fatal(err)
	}
	u, err := UnusableHash(ph)()
	if err != nil || !wellFormedHash(u) || ph.Validate("", u) == nil {
		t.Fatalf("UnusableHash = %q %v", u, err)
	}
	if u2, _ := UnusableHash(ph)(); u2 == u {
		t.Fatal("unusable hashes repeat")
	}
	for _, bad := range []string{"", "!unusable$00ff", "PBKDF2$sha512$1000$salt", "x$y$z$w$v"} {
		if wellFormedHash(bad) {
			t.Fatalf("wellFormedHash(%q) = true", bad)
		}
	}
	if CheckPepper("short") == nil || CheckPepper(testPepper) != nil {
		t.Fatal("CheckPepper")
	}
	sid, err := NewSessionID()
	if err != nil || len(sid) != 32 {
		t.Fatalf("sid %q %v", sid, err)
	}
}

func TestCodeStateCaps(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	var c CodeState
	for i := 1; i < MaxUserCodeFailures; i++ {
		c = c.AfterFailure(t0.Add(time.Duration(i) * time.Minute))
		if c.Locked(t0.Add(time.Duration(i) * time.Minute)) {
			t.Fatalf("locked after %d failures", i)
		}
	}
	tenth := t0.Add(2 * time.Hour)
	c = c.AfterFailure(tenth)
	if c.Failures != MaxUserCodeFailures || !c.Locked(tenth) {
		t.Fatalf("not locked after the 10th failure: %+v", c)
	}
	// Locked until the window from the first failure ends.
	end := t0.Add(time.Minute).Add(CodeFailureWindow)
	if !c.Locked(end.Add(-time.Second)) || c.Locked(end) {
		t.Fatalf("lock ends at %v, want %v", *c.LockedUntil, end)
	}
	// A failure after the window starts a new count.
	c = c.AfterFailure(end.Add(time.Hour))
	if c.Failures != 1 || c.Locked(end.Add(time.Hour)) {
		t.Fatalf("window did not roll: %+v", c)
	}
	if RowCodeDead(MaxRowCodeAttempts-1) || !RowCodeDead(MaxRowCodeAttempts) {
		t.Fatal("RowCodeDead")
	}
}

func TestValidation(t *testing.T) {
	if e, err := NormalizeEmail("  Anna@Example.COM "); err != nil || e != "anna@example.com" {
		t.Fatalf("NormalizeEmail = %q %v", e, err)
	}
	for _, bad := range []string{"", "no-at", "a@", "@b.c", "a b@c.d"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("NormalizeEmail(%q) accepted", bad)
		}
	}
	if CheckPassword("short") == nil || CheckPassword(strings.Repeat("x", MaxPasswordLen+1)) == nil || CheckPassword("long enough") != nil {
		t.Fatal("CheckPassword")
	}
	if n, err := CleanTeamName("  Atelier   Zhuk "); err != nil || n != "Atelier Zhuk" {
		t.Fatalf("CleanTeamName = %q %v", n, err)
	}
	if _, err := CleanTeamName("   "); err == nil {
		t.Fatal("empty team name accepted")
	}
	if CheckHandle("anna.k") != nil || CheckHandle("A") == nil || CheckHandle("-anna") == nil {
		t.Fatal("CheckHandle")
	}
	for in, want := range map[string]string{"anna.k+work@x.io": "anna.k", "__@x.io": "user", "Ivan_P@x.io": "ivan_p"} {
		if got := HandleBase(in); got != want {
			t.Errorf("HandleBase(%q) = %q, want %q", in, got, want)
		}
	}
	if HandleCandidate("anna", 1) != "anna" || HandleCandidate("anna", 12) != "anna-12" {
		t.Fatal("HandleCandidate")
	}
	emails, invalid := ParseOperatorEmails(" A@x.io, ,b@y.io,a@x.io, nope ")
	if strings.Join(emails, ",") != "a@x.io,b@y.io" || len(invalid) != 1 {
		t.Fatalf("ParseOperatorEmails = %v %v", emails, invalid)
	}
}

func TestInvitePermissions(t *testing.T) {
	super, perms, err := InvitePermissions([]byte(`[{"section":"products","access":"write"}]`), "owner")
	if err != nil || !super || perms != nil {
		t.Fatalf("owner = %v %v %v", super, perms, err)
	}
	super, perms, err = InvitePermissions([]byte(`[{"section":"products","access":"write"},{"section":"nope","access":"read"},{"section":"orders","access":"admin"}]`), "member")
	if err != nil || super || len(perms) != 1 || perms[0].Section != "products" {
		t.Fatalf("member = %v %v %v", super, perms, err)
	}
	if _, _, err := InvitePermissions([]byte(`{`), "member"); err == nil {
		t.Fatal("bad json accepted")
	}
}

func TestSignupApprovalEnv(t *testing.T) {
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "junk": true, "false": false, "0": false, " OFF ": false, "no": false} {
		if got := signupApproval(v); got != want {
			t.Fatalf("signupApproval(%q) = %v, want %v", v, got, want)
		}
	}
}
