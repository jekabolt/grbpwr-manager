package platformauth

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanPersonName(t *testing.T) {
	for _, c := range []struct {
		in, want string
		err      error
	}{
		{"", "", nil},
		{"  Anna  ", "Anna", nil},
		{"Anna   Maria", "Anna Maria", nil},
		{strings.Repeat("я", MaxPersonNameLen), strings.Repeat("я", MaxPersonNameLen), nil},
		{strings.Repeat("я", MaxPersonNameLen+1), "", errNameTooLong},
		{"An\x00na", "", errBadName},
		{"Anna\nB", "", errBadName},
		{"\xff\xfe", "", errBadName},
	} {
		got, err := CleanPersonName(c.in)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("CleanPersonName(%q) = %q, %v; want %q, %v", c.in, got, err, c.want, c.err)
		}
	}
}

func TestSplitFullName(t *testing.T) {
	for _, c := range []struct {
		in, first, last string
		bad             bool
	}{
		{"", "", "", false},
		{"   ", "", "", false},
		{"Anna", "Anna", "", false},
		{"  Anna   Karenina  Ivanovna ", "Anna", "Karenina Ivanovna", false},
		{"Анна Каренина", "Анна", "Каренина", false},
		{strings.Repeat("a", 40) + " " + strings.Repeat("b", 39), strings.Repeat("a", 40), strings.Repeat("b", 39), false},
		{strings.Repeat("a", 40) + " " + strings.Repeat("b", 40), "", "", true}, // 81 runes
		{strings.Repeat("a", MaxPersonNameLen+1), "", "", true},
		{"a " + strings.Repeat("b", MaxPersonNameLen+1), "", "", true},
		{"Anna\tKarenina", "", "", true},
		{"\xffAnna", "", "", true},
	} {
		first, last, err := SplitFullName(c.in)
		if c.bad {
			if !errors.Is(err, errBadName) {
				t.Errorf("SplitFullName(%q) = %q %q %v, want bad_name", c.in, first, last, err)
			}
			continue
		}
		if err != nil || first != c.first || last != c.last {
			t.Errorf("SplitFullName(%q) = %q %q %v, want %q %q", c.in, first, last, err, c.first, c.last)
		}
	}
}

func TestNormalizePublicUsername(t *testing.T) {
	for in, want := range map[string]string{
		"@Anna.K":                     "anna.k",
		"  bob ":                      "bob",
		"b0":                          "b0",
		"x_y-z.9":                     "x_y-z.9",
		strings.Repeat("a", 30):       strings.Repeat("a", 30),
		"@" + strings.Repeat("a", 30): strings.Repeat("a", 30),
	} {
		if got, err := NormalizePublicUsername(in); err != nil || got != want {
			t.Errorf("NormalizePublicUsername(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "@", "a", "-ab", ".ab", "_ab", "@@ab", "a b", "anna@x", "ann/a", "аня",
		strings.Repeat("a", 31), "me", "@ME", "system", "Unknown",
	} {
		if got, err := NormalizePublicUsername(in); !errors.Is(err, errBadUsername) {
			t.Errorf("NormalizePublicUsername(%q) = %q, %v; want bad_username", in, got, err)
		}
	}
}
