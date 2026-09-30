package dto

import (
	"testing"

	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestUpdateMaskIsCountryOnly(t *testing.T) {
	mask := func(paths ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: paths} }
	tests := []struct {
		name        string
		mask        *fieldmaskpb.FieldMask
		countryOnly bool
		devOnly     bool
	}{
		{"nil mask", nil, false, false},
		{"empty mask", mask(), false, false},
		{"exactly country_code", mask("country_code"), true, false},
		{"json spelling", mask("countryCode"), true, false},
		{"padded and repeated", mask(" Country_Code ", "country_code"), true, false},
		{"country and merchandising", mask("country_code", "merchandising"), false, false},
		{"country and development", mask("country_code", "development.name"), false, false},
		{"development only", mask("development.colours", "development.nameI18n"), false, true},
		{"bare development", mask("development"), false, true},
		{"merchandising only", mask("merchandising"), false, false},
		{"a nested country path is not the field", mask("country_code.name"), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UpdateMaskIsCountryOnly(tt.mask); got != tt.countryOnly {
				t.Errorf("UpdateMaskIsCountryOnly(%v) = %v, want %v", tt.mask.GetPaths(), got, tt.countryOnly)
			}
			if got := UpdateMaskIsDevelopmentOnly(tt.mask); got != tt.devOnly {
				t.Errorf("UpdateMaskIsDevelopmentOnly(%v) = %v, want %v", tt.mask.GetPaths(), got, tt.devOnly)
			}
		})
	}
}
