package stylenumber

import (
	"strings"
	"testing"
)

func TestFixtureLoads(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Version == "" || c.SeqWidth <= 0 || c.ManualOverride.Pattern == "" {
		t.Fatalf("fixture missing required fields: %+v", c)
	}
	if ContractVersion() != c.Version {
		t.Errorf("ContractVersion = %q, want %q", ContractVersion(), c.Version)
	}
}

// TestGenerateGolden pins the generator against every golden vector in the fixture.
func TestGenerateGolden(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, v := range c.GoldenGenerate {
		got, err := Generate(v.Season, v.Year, v.Seq)
		if err != nil {
			t.Errorf("Generate(%q,%d,%d) unexpected error: %v (%s)", v.Season, v.Year, v.Seq, err, v.Note)
			continue
		}
		if got != v.Want {
			t.Errorf("Generate(%q,%d,%d) = %q, want %q (%s)", v.Season, v.Year, v.Seq, got, v.Want, v.Note)
		}
		// A generated number must itself pass the strict manual validator (it is a valid override).
		if r := ValidateManual(got); r != "" {
			t.Errorf("generated %q fails ValidateManual: %s", got, r)
		}
	}
}

// TestGenerateNegative pins that every rejected generator input in the fixture errors.
func TestGenerateNegative(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, v := range c.NegativeGen {
		if _, err := Generate(v.Season, v.Year, v.Seq); err == nil {
			t.Errorf("Generate(%q,%d,%d) should be rejected (%s), got no error", v.Season, v.Year, v.Seq, v.Reason)
		}
	}
}

// TestValidateManualGolden pins accepted overrides; TestValidateManualNegative pins rejects with
// their exact reason code.
func TestValidateManualGolden(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, cand := range c.GoldenManual {
		if r := ValidateManual(cand); r != "" {
			t.Errorf("ValidateManual(%q) = %q, want accepted", cand, r)
		}
	}
}

func TestValidateManualNegative(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, v := range c.NegativeManual {
		if r := ValidateManual(v.Candidate); r != v.Reason {
			t.Errorf("ValidateManual(%q) = %q, want %q", v.Candidate, r, v.Reason)
		}
	}
}

func TestValidateManualTooLong(t *testing.T) {
	c, _ := Load()
	long := strings.Repeat("A", c.ManualOverride.MaxLength+1)
	if r := ValidateManual(long); r != ReasonTooLong {
		t.Errorf("ValidateManual(len %d) = %q, want too_long", len(long), r)
	}
}

func TestPrefix(t *testing.T) {
	got, err := Prefix("ss", 2026)
	if err != nil || got != "SS26-" {
		t.Fatalf("Prefix(ss,2026) = %q, %v; want SS26-", got, err)
	}
	if _, err := Prefix("XX", 2026); err == nil {
		t.Error("Prefix with unknown season should error")
	}
}
