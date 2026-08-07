package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// TestGenderEnumNoDrift asserts every non-UNKNOWN proto GenderEnum value has an entity mapping to a
// valid gender, and that the entity/proto sets are the same size. It fails if a value is added to
// the proto enum (or entity set) without updating the other — the "edit in 3-4 places" drift the
// enum single-sourcing (PR5-F) is meant to catch.
func TestGenderEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.GenderEnum_name {
		if pb_common.GenderEnum(v) == pb_common.GenderEnum_GENDER_ENUM_UNKNOWN {
			continue
		}
		protoValues++
		g, ok := genderPbEntityMap[pb_common.GenderEnum(v)]
		if !ok {
			t.Errorf("proto GenderEnum %s has no entity mapping", name)
			continue
		}
		if !entity.IsValidTargetGender(g) {
			t.Errorf("proto GenderEnum %s maps to invalid entity gender %q", name, g)
		}
	}
	if protoValues != len(genderPbEntityMap) {
		t.Errorf("proto gender values (%d) != entity mapping size (%d)", protoValues, len(genderPbEntityMap))
	}
	if protoValues != len(entity.ValidProductTargetGenders) {
		t.Errorf("proto gender values (%d) != entity.ValidProductTargetGenders (%d)", protoValues, len(entity.ValidProductTargetGenders))
	}
}

// TestSeasonEnumNoDrift is the same guard for SeasonEnum.
func TestSeasonEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.SeasonEnum_name {
		if pb_common.SeasonEnum(v) == pb_common.SeasonEnum_SEASON_ENUM_UNKNOWN {
			continue
		}
		protoValues++
		s, ok := seasonPbEntityMap[pb_common.SeasonEnum(v)]
		if !ok {
			t.Errorf("proto SeasonEnum %s has no entity mapping", name)
			continue
		}
		if !entity.IsValidSeason(s) {
			t.Errorf("proto SeasonEnum %s maps to invalid entity season %q", name, s)
		}
	}
	if protoValues != len(seasonPbEntityMap) {
		t.Errorf("proto season values (%d) != entity mapping size (%d)", protoValues, len(seasonPbEntityMap))
	}
	if protoValues != len(entity.ValidSeasons) {
		t.Errorf("proto season values (%d) != entity.ValidSeasons (%d)", protoValues, len(entity.ValidSeasons))
	}
}

// TestMaterialClassEnumNoDrift is the same guard for the S15 MaterialClass enum: every non-UNKNOWN
// proto value maps (via materialClassPbToEntity) to a valid entity class and the sets match in size.
func TestMaterialClassEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.MaterialClass_name {
		if pb_common.MaterialClass(v) == pb_common.MaterialClass_MATERIAL_CLASS_UNKNOWN {
			continue
		}
		protoValues++
		c, ok := materialClassPbToEntity[pb_common.MaterialClass(v)]
		if !ok {
			t.Errorf("proto MaterialClass %s has no entity mapping", name)
			continue
		}
		if !entity.ValidMaterialClasses[c] {
			t.Errorf("proto MaterialClass %s maps to invalid entity class %q", name, c)
		}
	}
	if protoValues != len(materialClassPbToEntity) {
		t.Errorf("proto material class values (%d) != entity mapping size (%d)", protoValues, len(materialClassPbToEntity))
	}
	if protoValues != len(entity.ValidMaterialClasses) {
		t.Errorf("proto material class values (%d) != entity.ValidMaterialClasses (%d)", protoValues, len(entity.ValidMaterialClasses))
	}
}

// TestTechCardPurposeEnumNoDrift is the same guard for the R6 TechCardPurpose enum: every non-UNKNOWN
// proto value maps (via techCardPurposeFromPb) to a valid entity purpose and the sets match in size.
// Closes the entity<->proto leg the T-C purpose drift work left for T-B (handoff item 3).
func TestTechCardPurposeEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.TechCardPurpose_name {
		if pb_common.TechCardPurpose(v) == pb_common.TechCardPurpose_TECH_CARD_PURPOSE_UNKNOWN {
			continue
		}
		protoValues++
		p := techCardPurposeFromPb(pb_common.TechCardPurpose(v))
		if !entity.ValidTechCardPurposes[p] {
			t.Errorf("proto TechCardPurpose %s maps to invalid entity purpose %q", name, p)
		}
	}
	if protoValues != len(entity.ValidTechCardPurposes) {
		t.Errorf("proto purpose values (%d) != entity.ValidTechCardPurposes (%d)", protoValues, len(entity.ValidTechCardPurposes))
	}
}

// TestTechCardAuxSubtypeEnumNoDrift is the entity<->proto leg (WS7) for the aux_subtype enum: every
// non-UNKNOWN proto value maps (via techCardAuxSubtypeFromPb) to a valid entity sub-type, and the three
// sizes (proto values, mapping table, entity Valid set) all agree. The entity<->DB leg is in
// internal/store/migrationlint (TestTechCardAuxSubtypeDBCheckNoDrift).
func TestTechCardAuxSubtypeEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.TechCardAuxSubtype_name {
		if pb_common.TechCardAuxSubtype(v) == pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_UNKNOWN {
			continue
		}
		protoValues++
		s := techCardAuxSubtypeFromPb(pb_common.TechCardAuxSubtype(v))
		if !entity.ValidTechCardAuxSubtypes[s] {
			t.Errorf("proto TechCardAuxSubtype %s maps to invalid entity sub-type %q", name, s)
		}
	}
	if protoValues != len(auxSubtypePbByEntity) {
		t.Errorf("proto aux_subtype values (%d) != mapping table size (%d)", protoValues, len(auxSubtypePbByEntity))
	}
	if protoValues != len(entity.ValidTechCardAuxSubtypes) {
		t.Errorf("proto aux_subtype values (%d) != entity.ValidTechCardAuxSubtypes (%d)", protoValues, len(entity.ValidTechCardAuxSubtypes))
	}
}

// TestAssemblyResolutionBasisEnumNoDrift is the same guard for the packing spec's resolution
// discriminator: every non-UNKNOWN proto value maps back to a valid entity basis, and the three sizes
// (proto values, mapping table, entity Valid set) agree. This enum has no DB leg — it is computed at
// read time and never stored — so this is the only place it can drift.
func TestAssemblyResolutionBasisEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.AssemblyResolutionBasis_name {
		if pb_common.AssemblyResolutionBasis(v) == pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_UNKNOWN {
			continue
		}
		protoValues++
		b := assemblyResolutionBasisFromPb(pb_common.AssemblyResolutionBasis(v))
		if !entity.ValidAssemblyResolutionBases[b] {
			t.Errorf("proto AssemblyResolutionBasis %s maps to invalid entity basis %q", name, b)
		}
	}
	if protoValues != len(assemblyResolutionBasisPbByEntity) {
		t.Errorf("proto resolution basis values (%d) != mapping table size (%d)", protoValues, len(assemblyResolutionBasisPbByEntity))
	}
	if protoValues != len(entity.ValidAssemblyResolutionBases) {
		t.Errorf("proto resolution basis values (%d) != entity.ValidAssemblyResolutionBases (%d)", protoValues, len(entity.ValidAssemblyResolutionBases))
	}
}

// TestBomPurposeEnumNoDrift is the entity<->proto leg for НАЗНАЧЕНИЕ (0265): every non-UNSET proto
// value maps to a valid entity purpose, and the three sizes (proto values, mapping table, entity
// Valid set) agree. The entity<->DB leg is TestBomPurposeDBCheckNoDrift in internal/store/migrationlint.
//
// UNSET is skipped rather than mapped: it is not a value but the absence of one ("not sorted yet"),
// and it must stay absent from the mapping table so it can only ever become a NULL column.
func TestBomPurposeEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.TechCardBomPurpose_name {
		if pb_common.TechCardBomPurpose(v) == pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET {
			continue
		}
		protoValues++
		p, ok := techCardBomPurposePbToEntity[pb_common.TechCardBomPurpose(v)]
		if !ok || !entity.ValidTechCardBomPurposes[p] {
			t.Errorf("proto TechCardBomPurpose %s maps to invalid entity purpose %q", name, p)
		}
	}
	if protoValues != len(techCardBomPurposePbToEntity) {
		t.Errorf("proto purpose values (%d) != mapping table size (%d)", protoValues, len(techCardBomPurposePbToEntity))
	}
	if protoValues != len(entity.ValidTechCardBomPurposes) {
		t.Errorf("proto purpose values (%d) != entity.ValidTechCardBomPurposes (%d)", protoValues, len(entity.ValidTechCardBomPurposes))
	}
}

// TestPieceCutSymmetryEnumNoDrift is the entity<->proto leg for КАК КРОИТСЯ (0275): every non-UNKNOWN
// proto value maps to a valid entity symmetry, and the three sizes (proto values, mapping table,
// entity Valid set) agree. The entity<->DB leg is TestPieceCutSymmetryDBCheckNoDrift in
// internal/store/migrationlint.
//
// UNKNOWN is skipped rather than mapped, on the same rule as TechCardBomPurpose's UNSET: it is not a
// value but the absence of one («не размечено»), and it must stay out of the mapping table so it can
// only ever become a NULL column. A future contributor who "completes" the map by adding it would
// turn every stale save into a write of the string "unknown".
func TestPieceCutSymmetryEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.TechCardPieceCutSymmetry_name {
		if pb_common.TechCardPieceCutSymmetry(v) == pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN {
			continue
		}
		protoValues++
		s, ok := techCardPieceCutSymmetryPbToEntity[pb_common.TechCardPieceCutSymmetry(v)]
		if !ok || !entity.ValidTechCardPieceCutSymmetries[s] {
			t.Errorf("proto TechCardPieceCutSymmetry %s maps to invalid entity symmetry %q", name, s)
		}
	}
	if protoValues != len(techCardPieceCutSymmetryPbToEntity) {
		t.Errorf("proto cut symmetry values (%d) != mapping table size (%d)", protoValues, len(techCardPieceCutSymmetryPbToEntity))
	}
	if protoValues != len(entity.ValidTechCardPieceCutSymmetries) {
		t.Errorf("proto cut symmetry values (%d) != entity.ValidTechCardPieceCutSymmetries (%d)", protoValues, len(entity.ValidTechCardPieceCutSymmetries))
	}
	// The reverse table must be a true inverse: PieceCutSymmetryToPb reads it on every card read, and a
	// missing entry there degrades a MARKED piece to «не размечено» silently on the wire.
	for pb, ent := range techCardPieceCutSymmetryPbToEntity {
		if got := techCardPieceCutSymmetryEntityToPb[ent]; got != pb {
			t.Errorf("entity %q maps back to %s, want %s", ent, got, pb)
		}
	}
}
