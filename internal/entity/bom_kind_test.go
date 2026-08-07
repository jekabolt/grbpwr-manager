package entity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ValidTechCardBomKinds is DERIVED from bomKindHomeSection, which is the point: the vocabulary and
// the kind↔section pairing are physically one list, so a kind cannot be added to one and forgotten
// in the other. This asserts the derivation actually holds — a future edit that "optimises" the set
// into a second literal map would pass every other test in the repo and fail here.
func TestValidBomKindsAreDerivedFromTheHomeSectionTable(t *testing.T) {
	require.Len(t, ValidTechCardBomKinds, len(bomKindHomeSection))
	for k := range bomKindHomeSection {
		require.True(t, ValidTechCardBomKinds[k], "kind %q has a home section but is not in the valid set", k)
	}
	for k := range ValidTechCardBomKinds {
		_, ok := bomKindHomeSection[k]
		require.True(t, ok, "kind %q is valid but has no home section", k)
	}
}

// Every stored value must be byte-identical to its own lower-case form and spelled from [a-z_] only.
//
// This is not tidiness. chk_bom_item_kind closes the vocabulary against CASE as well as spelling
// (STRCMP over a BINARY cast), because REGEXP inherits the column's case-insensitive collation — so
// a constant declared "Zipper" here would compile, pass the enum drift tests, and then be rejected
// by MySQL at INSERT time on the card-save path, with nothing between the two to notice. The
// character-set half matters for the same reason from the other end: the drift test extracts the
// migration's alternation with a [a-zA-Z_|] regexp, so a value carrying a digit or a dash would make
// it fail to FIND the list rather than compare it.
func TestBomKindValuesAreLowerSnakeCase(t *testing.T) {
	for k := range ValidTechCardBomKinds {
		s := string(k)
		require.NotEmpty(t, s)
		require.Equal(t, strings.ToLower(s), s, "kind %q is not byte-identical to its lower-case form", k)
		for _, r := range s {
			require.True(t, (r >= 'a' && r <= 'z') || r == '_', "kind %q carries an unsupported rune %q", k, r)
		}
	}
}

// The wildcard sentinel must stay unmistakable for a section AND unmistakable for a kind: it is the
// empty string precisely so it can be neither. `other` is a real value on BOTH axes (BomKindOther and
// BomSectionOther), which is exactly the confusion this guards.
func TestBomKindAnySectionIsNotAValueOnEitherAxis(t *testing.T) {
	require.False(t, ValidTechCardBomSections[BomKindAnySection])
	require.False(t, ValidTechCardBomKinds[TechCardBomKind(BomKindAnySection)])
	require.NotEqual(t, TechCardBomSection(BomKindOther), BomKindAnySection)

	home, ok := BomKindHomeSection(BomKindOther)
	require.True(t, ok)
	require.Equal(t, BomKindAnySection, home, "`other` is the escape hatch of every eligible family, so it has no single home")

	_, ok = BomKindHomeSection("zip")
	require.False(t, ok, "an unknown kind must report itself as unknown, not fall back to a section")
}

// purpose and kind classify DISJOINT halves of the BOM and must never be conflated, but they share
// one spelling by design: `other`, the escape hatch of both, so an operator learns the pattern once.
// Nothing else may overlap — a value in both vocabularies would be a coin flip in any code that
// resolved a bare string against "the classification", which is how the two axes would start to
// merge back together.
func TestBomKindAndPurposeVocabulariesOnlyShareOther(t *testing.T) {
	shared := make([]string, 0, 1)
	for k := range ValidTechCardBomKinds {
		if ValidTechCardBomPurposes[TechCardBomPurpose(k)] {
			shared = append(shared, string(k))
		}
	}
	require.Equal(t, []string{string(BomKindOther)}, shared)
}
