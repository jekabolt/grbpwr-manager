package jpk

import "testing"

func TestTaxpayerValidate(t *testing.T) {
	// 1234563218: first nine digits 1,2,3,4,5,6,3,2,1 weighted 6,5,7,2,3,4,5,6,7 sum to 118; 118%11=8,
	// which matches the tenth digit — a valid NIP used purely as a checksum fixture.
	valid := Taxpayer{NIP: "1234563218", FullName: "GRBPWR sp. z o.o.", Email: "vat@grbpwr.com", TaxOffice: "1471"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid taxpayer rejected: %v", err)
	}

	cases := []struct {
		name string
		tp   Taxpayer
	}{
		{"bad nip checksum", Taxpayer{NIP: "1234563210", FullName: "X", Email: "a@b.co", TaxOffice: "1471"}},
		{"nip not 10 digits", Taxpayer{NIP: "12345", FullName: "X", Email: "a@b.co", TaxOffice: "1471"}},
		{"missing name", Taxpayer{NIP: "1234563218", Email: "a@b.co", TaxOffice: "1471"}},
		{"bad email", Taxpayer{NIP: "1234563218", FullName: "X", Email: "nope", TaxOffice: "1471"}},
		{"tax office not 4 digits", Taxpayer{NIP: "1234563218", FullName: "X", Email: "a@b.co", TaxOffice: "14"}},
	}
	for _, c := range cases {
		if err := c.tp.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}

	if (Taxpayer{}).Configured() {
		t.Error("empty taxpayer should report not configured")
	}
	if !valid.Configured() {
		t.Error("valid taxpayer should report configured")
	}
}
