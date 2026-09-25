package product

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// The age group (0366) on UpdateStyle's SQL side. Pure string checks over the SET clause builders —
// no database — so they pin the two shapes that make the compatibility promise true:
//   - masked, the column is written as sent (the write rule has refused "" and non-members first);
//   - unmasked, the column is written through COALESCE(NULLIF(:ageGroup, ''), age_group), so the
//     empty age group every old client's full replace carries keeps what is stored.

func TestStyleSetColumnsWritesAMaskedAgeGroupAsSent(t *testing.T) {
	for _, path := range []string{"age_group", "ageGroup", "agegroup"} {
		columns, seasonWritten := styleSetColumns([]string{path})
		if columns != "age_group = :ageGroup" {
			t.Errorf("mask %q: SET = %q, want exactly the masked age group assignment", path, columns)
		}
		if seasonWritten {
			t.Errorf("mask %q must not count as a season write (no SKU re-mint)", path)
		}
	}
}

func TestStyleSetColumnsWithoutTheAgeGroupPathLeavesItAlone(t *testing.T) {
	for _, fields := range [][]string{{"fit"}, {"targetGender"}, {"modelWearsHeightCm", "modelWearsSizeId"}, {"season"}} {
		columns, _ := styleSetColumns(fields)
		if strings.Contains(columns, "age_group") {
			t.Errorf("mask %v must not touch age_group: %s", fields, columns)
		}
	}
}

// The old-client guarantee on the SQL side: the unmasked full replace carries the keep-when-empty
// fragment exactly once, after a real separator, and never the bare assignment that would write "".
func TestUnmaskedStyleSetColumnsKeepsTheStoredAgeGroupWhenEmpty(t *testing.T) {
	columns, _ := styleSetColumns(nil)
	if got := strings.Count(columns, "age_group ="); got != 1 {
		t.Fatalf("unmasked SET must assign age_group exactly once, got %d\nSET: %s", got, columns)
	}
	if !strings.Contains(columns, styleAgeGroupFullReplaceFragment) {
		t.Errorf("unmasked SET must use the keep-when-empty fragment, got: %s", columns)
	}
	if strings.Contains(columns, "age_group = :ageGroup") {
		t.Errorf("unmasked SET must never write the bind as is — an old client's empty age group "+
			"would blank the stored one: %s", columns)
	}
	idx := strings.Index(columns, styleAgeGroupFullReplaceFragment)
	if before := strings.TrimRight(columns[:idx], " \t\n"); !strings.HasSuffix(before, ",") {
		t.Errorf("missing the comma before the age group fragment — a SQL syntax error at runtime: %s", columns)
	}
	if got := countCategoryIDAssignments(columns); got != 1 {
		t.Errorf("appending the age group must not disturb the category derivation, got %d assignments", got)
	}
	if !strings.HasPrefix(styleAgeGroupFullReplaceFragment, "age_group = COALESCE(NULLIF(:ageGroup, ''), age_group)") {
		t.Errorf("the fragment must fall back to the stored value on an empty bind: %s", styleAgeGroupFullReplaceFragment)
	}
}

// styleFieldsSet is shared with the colourway write paths, which bind from ColorwayBodyInsert and
// have no age group to send: listing the column there would make every colourway save a writer of
// a fact only UpdateStyle owns.
func TestStyleFieldsSetDoesNotWriteTheAgeGroup(t *testing.T) {
	if strings.Contains(styleFieldsSet, "age_group") {
		t.Errorf("styleFieldsSet must not name age_group: %s", styleFieldsSet)
	}
}

func TestStylePatchParamsBindsTheAgeGroupAsItsToken(t *testing.T) {
	got := stylePatchParams(entity.StylePatch{AgeGroup: entity.AgeGroupToddler})["ageGroup"]
	if got != "toddler" {
		t.Errorf(`ageGroup bind = %#v, want "toddler"`, got)
	}
	if got := stylePatchParams(entity.StylePatch{})["ageGroup"]; got != "" {
		t.Errorf(`an unset age group must bind "" (kept by the unmasked fragment), got %#v`, got)
	}
}

// The store applies the write rule itself, before any transaction, so a direct caller (a seeder, a
// script) cannot store an empty or unknown token under a mask that names the column. A zero Store
// proves "before any transaction": reaching the database would dereference nothing and panic.
func TestUpdateStyleRefusesABadMaskedAgeGroupBeforeTheTx(t *testing.T) {
	for _, ag := range []entity.AgeGroupEnum{"", "senior"} {
		_, err := (&Store{}).UpdateStyle(context.Background(), 1, 1, entity.StylePatch{AgeGroup: ag}, []string{"age_group"})
		var ve *entity.ValidationError
		if !errors.As(err, &ve) || ve.Field != "age_group" {
			t.Errorf("age group %q under an age_group mask: want a field-tagged refusal, got %v", ag, err)
		}
	}
	_, err := (&Store{}).UpdateStyle(context.Background(), 1, 1, entity.StylePatch{AgeGroup: "senior"}, nil)
	var ve *entity.ValidationError
	if !errors.As(err, &ve) || ve.Field != "age_group" {
		t.Errorf("a non-member on an unmasked full replace: want a field-tagged refusal, got %v", err)
	}
}
