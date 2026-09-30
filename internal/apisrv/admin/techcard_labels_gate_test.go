package admin

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func labelsApproval() []entity.TechCardSignoff {
	return []entity.TechCardSignoff{{Section: entity.SignoffLabels, State: entity.SignoffStateApproved}}
}

// D-04: a fresh LABELS approval is refused while a garment label has no mockup, and the refusal names
// the labels; with every mockup in place the approval passes.
func TestLabelsMockupSignGate(t *testing.T) {
	fresh := map[entity.TechCardSignoffSection]bool{entity.SignoffLabels: true}

	tc := &entity.TechCardInsert{
		GarmentLabels: []entity.TechCardGarmentLabel{{Key: "brand", MediaIds: []int{1}}, {Key: "hangtag"}},
		Signoffs:      labelsApproval(),
	}
	ve := validateLabelsMockupSignGate(tc, fresh)
	require.NotNil(t, ve)
	require.Equal(t, "signoffs[0].section", ve.Field)
	require.Contains(t, ve.Message, "hangtag (#2)")
	require.NotContains(t, ve.Message, "brand")

	tc.GarmentLabels[1].MediaIds = []int{2}
	require.Nil(t, validateLabelsMockupSignGate(tc, fresh))

	// No garment labels at all: nothing lacks a mockup.
	require.Nil(t, validateLabelsMockupSignGate(&entity.TechCardInsert{Signoffs: labelsApproval()}, fresh))
}

// Saving is never blocked: without a fresh LABELS approval (pending, carried, another section) the
// gate stays silent however many labels lack a mockup.
func TestLabelsMockupSignGateOnlyJudgesAFreshLabelsApproval(t *testing.T) {
	bare := []entity.TechCardGarmentLabel{{Key: "hangtag"}}

	carried := &entity.TechCardInsert{GarmentLabels: bare, Signoffs: labelsApproval()}
	require.Nil(t, validateLabelsMockupSignGate(carried, map[entity.TechCardSignoffSection]bool{}))

	pending := &entity.TechCardInsert{GarmentLabels: bare,
		Signoffs: []entity.TechCardSignoff{{Section: entity.SignoffLabels, State: entity.SignoffStatePending}}}
	require.Nil(t, validateLabelsMockupSignGate(pending, map[entity.TechCardSignoffSection]bool{entity.SignoffLabels: true}))

	other := &entity.TechCardInsert{GarmentLabels: bare,
		Signoffs: []entity.TechCardSignoff{{Section: entity.SignoffPackaging, State: entity.SignoffStateApproved}}}
	require.Nil(t, validateLabelsMockupSignGate(other, map[entity.TechCardSignoffSection]bool{entity.SignoffPackaging: true}))
}

// The gate sits on the create path too: prepareCreateTechCardSignoffs marks every approval fresh, so a
// card created with LABELS approved over a mockup-less label is refused.
func TestLabelsMockupSignGateOnCreate(t *testing.T) {
	tc := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "flag"}}, Signoffs: labelsApproval()}
	fresh := prepareCreateTechCardSignoffs(tc, "alice", time.Now())
	require.NotNil(t, validateLabelsMockupSignGate(tc, fresh))
}

// The presence belt: a fresh approval over a payload that would KEEP stored labels / items / the
// composition record is refused; the same payload on a card with nothing stored is fine.
func TestFreshLabelsSectionsCarried(t *testing.T) {
	freshLabels := map[entity.TechCardSignoffSection]bool{entity.SignoffLabels: true}
	freshPkg := map[entity.TechCardSignoffSection]bool{entity.SignoffPackaging: true}
	pkgApproval := []entity.TechCardSignoff{{Section: entity.SignoffPackaging, State: entity.SignoffStateApproved}}

	storedFull := &entity.TechCardInsert{
		GarmentLabels:  []entity.TechCardGarmentLabel{{Key: "brand"}},
		PackagingItems: []entity.TechCardPackagingItem{{Key: "polybag"}},
		CareLabel:      &entity.TechCardCareLabel{},
	}
	storedEmpty := &entity.TechCardInsert{}

	// Old bundle (no labels_aware, no care_label) over stored content → refused, both sections.
	old := &entity.TechCardInsert{Signoffs: labelsApproval()}
	require.NotNil(t, validateFreshLabelsSectionsCarried(old, storedFull, freshLabels))
	oldPkg := &entity.TechCardInsert{Packaging: &entity.TechCardPackaging{}, Signoffs: pkgApproval}
	require.NotNil(t, validateFreshLabelsSectionsCarried(oldPkg, storedFull, freshPkg))

	// Same old payload over a card with nothing stored → allowed (the digest is right either way).
	require.Nil(t, validateFreshLabelsSectionsCarried(old, storedEmpty, freshLabels))
	require.Nil(t, validateFreshLabelsSectionsCarried(oldPkg, storedEmpty, freshPkg))

	// New bundle carrying everything → allowed.
	cur := &entity.TechCardInsert{LabelsAware: true, CareLabel: &entity.TechCardCareLabel{}, Signoffs: labelsApproval()}
	require.Nil(t, validateFreshLabelsSectionsCarried(cur, storedFull, freshLabels))

	// Aware but omitting the composition record while one is stored → refused.
	noCare := &entity.TechCardInsert{LabelsAware: true, Signoffs: labelsApproval()}
	require.NotNil(t, validateFreshLabelsSectionsCarried(noCare, storedFull, freshLabels))
}

// The season clone re-addresses label BOM links by line key and drops colourway overrides (the clone
// creates no colourways), keeping the card-level composition overrides.
func TestCarryCloneLabels(t *testing.T) {
	source := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{{Id: 11, LineKey: "K11"}, {Id: 12, LineKey: "K12"}},
	}}
	insert := &entity.TechCardInsert{
		GarmentLabels:  []entity.TechCardGarmentLabel{{Key: "brand", BomItemId: ni32t(11)}, {Key: "size"}},
		PackagingItems: []entity.TechCardPackagingItem{{Key: "polybag", BomItemId: ni32t(12)}},
		CareLabel: &entity.TechCardCareLabel{QRPreset: "fixed",
			Colorways: []entity.TechCardCareLabelColorway{{ColorwayId: 5}}},
	}
	carryCloneLabels(source, insert)
	require.Equal(t, "K11", insert.GarmentLabels[0].BomLineKey)
	require.False(t, insert.GarmentLabels[0].BomItemId.Valid)
	require.Equal(t, "", insert.GarmentLabels[1].BomLineKey)
	require.Equal(t, "K12", insert.PackagingItems[0].BomLineKey)
	require.False(t, insert.PackagingItems[0].BomItemId.Valid)
	require.Nil(t, insert.CareLabel.Colorways)
	require.Equal(t, "fixed", insert.CareLabel.QRPreset)
}

func ni32t(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
