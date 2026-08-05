package entity

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// variant is a terse colour-variant fixture: only the facts the rule reads.
func variant(id int, code, colorName string, materialID int, materialName string) TechCardOutputVariant {
	v := TechCardOutputVariant{ColorName: colorName, MaterialName: materialName}
	v.Id = id
	v.ColorCode = code
	v.MaterialId = materialID
	v.Active = true
	return v
}

func retired(v TechCardOutputVariant) TechCardOutputVariant {
	v.Active = false
	return v
}

func archived(v TechCardOutputVariant) TechCardOutputVariant {
	v.MaterialArchived = true
	return v
}

func legacyOutput(id int, name string) AssemblyLegacyOutput {
	return AssemblyLegacyOutput{
		MaterialId:   sql.NullInt32{Int32: int32(id), Valid: id > 0},
		MaterialName: sql.NullString{String: name, Valid: name != ""},
	}
}

// The whole packing-spec promise in one table: "the black jacket ships the black dust bag", and when
// the server cannot say that honestly it says nothing at all — and says WHICH kind of nothing.
func TestResolveAssemblyOutput(t *testing.T) {
	black := variant(1, "BLK", "black", 101, "dust bag — black")
	white := variant(2, "WHT", "white", 102, "dust bag — white")
	unk := variant(3, "UNK", "unknown", 103, "dust bag — unknown")
	legacy := legacyOutput(900, "dust bag (legacy)")

	tests := []struct {
		name         string
		itemColor    string
		variants     []TechCardOutputVariant
		legacy       AssemblyLegacyOutput
		wantColor    string
		wantMaterial int
		wantName     string
		wantUnres    bool
		wantBasis    AssemblyResolutionBasis
	}{
		{
			name: "colour match wins over everything", itemColor: "BLK",
			variants: []TechCardOutputVariant{white, black}, legacy: legacy,
			wantColor: "BLK", wantMaterial: 101, wantName: "dust bag — black",
			wantBasis: AssemblyResolutionColorMatch,
		},
		{
			name: "colour match is case- and space-insensitive", itemColor: " blk ",
			variants:  []TechCardOutputVariant{white, black},
			wantColor: "BLK", wantMaterial: 101, wantName: "dust bag — black",
			wantBasis: AssemblyResolutionColorMatch,
		},
		{
			name: "an UNK item never matches the UNK variant", itemColor: "UNK",
			variants:  []TechCardOutputVariant{black, white, unk},
			wantUnres: true, wantBasis: AssemblyResolutionNoColorMatch,
		},
		{
			name: "an UNK variant is never the match for a real colour", itemColor: "GRN",
			variants:  []TechCardOutputVariant{unk, black},
			wantUnres: true, wantBasis: AssemblyResolutionNoColorMatch,
		},
		{
			name: "sole active variant ships regardless of colour", itemColor: "GRN",
			variants: []TechCardOutputVariant{white}, legacy: legacy,
			wantColor: "WHT", wantMaterial: 102, wantName: "dust bag — white",
			wantBasis: AssemblyResolutionSoleVariant,
		},
		{
			// (ii): an unloadable colourway leaves the code empty. One live bucket still means there is
			// no choice to get wrong, so the sole-variant branch fires — it is not a colour decision.
			name: "empty item colour still takes the sole active variant", itemColor: "",
			variants:  []TechCardOutputVariant{white},
			wantColor: "WHT", wantMaterial: 102, wantName: "dust bag — white",
			wantBasis: AssemblyResolutionSoleVariant,
		},
		{
			name: "a sole UNK variant still ships — one bucket, no choice to get wrong", itemColor: "BLK",
			variants:  []TechCardOutputVariant{unk},
			wantColor: "UNK", wantMaterial: 103, wantName: "dust bag — unknown",
			wantBasis: AssemblyResolutionSoleVariant,
		},
		{
			// (i) THE finding: the black bucket exists but is switched off. Substituting the only live
			// colour would put a white bag on a black jacket, confidently.
			name: "a RETIRED colour match blocks the sole-variant substitution", itemColor: "BLK",
			variants: []TechCardOutputVariant{retired(black), white}, legacy: legacy,
			wantUnres: true, wantBasis: AssemblyResolutionRetiredColor,
		},
		{
			name: "a retired colour match beats the legacy material too", itemColor: "BLK",
			variants: []TechCardOutputVariant{retired(black)}, legacy: legacy,
			wantUnres: true, wantBasis: AssemblyResolutionRetiredColor,
		},
		{
			name: "an UNK item does not 'match' a retired UNK colour", itemColor: "UNK",
			variants: []TechCardOutputVariant{retired(unk), white},
			// The retired UNK is not a match, so the sole live colour legitimately applies.
			wantColor: "WHT", wantMaterial: 102, wantName: "dust bag — white",
			wantBasis: AssemblyResolutionSoleVariant,
		},
		{
			name: "no variants at all falls back to the legacy single output", itemColor: "BLK",
			legacy:       legacy,
			wantMaterial: 900, wantName: "dust bag (legacy)", wantBasis: AssemblyResolutionLegacyOutput,
		},
		{
			// (iii): the card entered variant mode and every colour was later retired. The legacy
			// material is stale by construction — serving it unflagged was the second finding.
			name: "an ALL-RETIRED card does not fall back to the legacy material", itemColor: "GRN",
			variants: []TechCardOutputVariant{retired(black), retired(white)}, legacy: legacy,
			wantUnres: true, wantBasis: AssemblyResolutionNoColorMatch,
		},
		{
			name: "variant mode NEVER falls back to the stale legacy material", itemColor: "GRN",
			variants: []TechCardOutputVariant{black, white}, legacy: legacy,
			wantUnres: true, wantBasis: AssemblyResolutionNoColorMatch,
		},
		{
			name: "unknown item colour does not guess between several colours", itemColor: "",
			variants:  []TechCardOutputVariant{black, white},
			wantUnres: true, wantBasis: AssemblyResolutionNoColorMatch,
		},
		{
			// (iv): the colour is right, the bucket is withdrawn nomenclature. Don't send anyone there.
			name: "an ARCHIVED matched bucket is downgraded to unresolved", itemColor: "BLK",
			variants:  []TechCardOutputVariant{archived(black), white},
			wantUnres: true, wantBasis: AssemblyResolutionArchivedMaterial,
		},
		{
			name: "an ARCHIVED sole bucket is downgraded too", itemColor: "GRN",
			variants:  []TechCardOutputVariant{archived(white)},
			wantUnres: true, wantBasis: AssemblyResolutionArchivedMaterial,
		},
		{
			name: "an ARCHIVED legacy bucket is downgraded too", itemColor: "BLK",
			legacy:    AssemblyLegacyOutput{MaterialId: sql.NullInt32{Int32: 900, Valid: true}, Archived: true},
			wantUnres: true, wantBasis: AssemblyResolutionArchivedMaterial,
		},
		{
			name: "nothing at all — no colours, no output material", itemColor: "BLK",
			wantUnres: true, wantBasis: AssemblyResolutionNoOutput,
		},
		{
			name: "a variant pointing at no bucket is not an answer", itemColor: "BLK",
			variants:  []TechCardOutputVariant{variant(4, "BLK", "black", 0, "")},
			wantUnres: true, wantBasis: AssemblyResolutionNoColorMatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveAssemblyOutput(tt.itemColor, tt.variants, tt.legacy)
			require.Equal(t, tt.wantUnres, got.Unresolved, "unresolved flag")
			require.Equal(t, tt.wantBasis, got.Basis, "resolution basis")
			require.Equal(t, tt.wantColor, got.ResolvedColorCode, "resolved colour code")
			require.Equal(t, tt.wantMaterial, got.ResolvedMaterialId, "resolved material id")
			require.Equal(t, tt.wantName, got.ResolvedMaterialName, "resolved material name")
			if tt.wantUnres {
				// An unresolved line must carry NOTHING a packer could mistake for an answer.
				require.Zero(t, got.ResolvedMaterialId)
				require.Empty(t, got.ResolvedColorCode)
				require.Empty(t, got.ResolvedColorName)
				require.Empty(t, got.ResolvedMaterialName)
			}
			require.True(t, ValidAssemblyResolutionBases[got.Basis], "every outcome names a known basis")
			// The bool and the enum must never disagree — the client gates on one and explains with
			// the other.
			unresolvedBases := map[AssemblyResolutionBasis]bool{
				AssemblyResolutionRetiredColor: true, AssemblyResolutionNoColorMatch: true,
				AssemblyResolutionArchivedMaterial: true, AssemblyResolutionNoOutput: true,
			}
			require.Equal(t, unresolvedBases[got.Basis], got.Unresolved, "basis agrees with the unresolved flag")
		})
	}
}

// The colour dictionary stores codes upper-case CHAR(3); a variant row written before that was enforced,
// or a product code with stray whitespace, must still match the same colour.
func TestResolveAssemblyOutputMatchesAcrossStoredCase(t *testing.T) {
	lower := variant(1, "blk", "black", 101, "dust bag — black")
	other := variant(2, "WHT", "white", 102, "dust bag — white")
	got := ResolveAssemblyOutput("BLK", []TechCardOutputVariant{other, lower}, AssemblyLegacyOutput{})
	require.False(t, got.Unresolved)
	require.Equal(t, 101, got.ResolvedMaterialId)
	require.Equal(t, AssemblyResolutionColorMatch, got.Basis)
	// The code is echoed as STORED, not as normalised — the client renders the dictionary's own value.
	require.Equal(t, "blk", got.ResolvedColorCode)
}

// A retired row is matched on the same normalised terms as a live one: the gap must not disappear
// because someone typed the code in lower case.
func TestResolveAssemblyOutputRetiredMatchNormalises(t *testing.T) {
	got := ResolveAssemblyOutput(" blk ", []TechCardOutputVariant{
		retired(variant(1, "blk", "black", 101, "dust bag — black")),
		variant(2, "WHT", "white", 102, "dust bag — white"),
	}, legacyOutput(900, "dust bag (legacy)"))
	require.True(t, got.Unresolved)
	require.Equal(t, AssemblyResolutionRetiredColor, got.Basis)
}
