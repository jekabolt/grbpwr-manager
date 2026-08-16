package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func approvedSignoff(section entity.TechCardSignoffSection, digest string) entity.TechCardSignoff {
	return entity.TechCardSignoff{
		Section:      section,
		State:        entity.SignoffStateApproved,
		SignedDigest: sql.NullString{String: digest, Valid: digest != ""},
	}
}

// TestStampTechCardSignoffDigests pins the one rule the whole staleness mechanism rests on: the
// server must NOT re-stamp an approved section on an ordinary save. If it did, a save that edits the
// BOM would hand the materials sign-off a digest matching the new content and the section would
// silently re-bless itself — the exact failure the field exists to catch.
func TestStampTechCardSignoffDigests(t *testing.T) {
	card := func(bomName string, signoffs []entity.TechCardSignoff) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			BomItems: []entity.TechCardBomItem{{LineKey: "k1", Name: bomName}},
			Signoffs: signoffs,
		}
	}

	t.Run("an empty digest means approve now: the server fingerprints the payload", func(t *testing.T) {
		tc := card("wool", []entity.TechCardSignoff{approvedSignoff(entity.SignoffMaterials, "")})
		StampTechCardSignoffDigests(tc)
		got := tc.Signoffs[0].SignedDigest
		if !got.Valid || got.String == "" {
			t.Fatal("an approval with no digest must be stamped")
		}
		if want := TechCardSectionDigests(tc)[entity.SignoffMaterials]; got.String != want {
			t.Fatalf("stamped %s, want the section's own digest %s", got.String, want)
		}
	})

	t.Run("an echoed digest survives a save that changes the section", func(t *testing.T) {
		// Approve against "wool"...
		approved := card("wool", []entity.TechCardSignoff{approvedSignoff(entity.SignoffMaterials, "")})
		StampTechCardSignoffDigests(approved)
		stamped := approved.Signoffs[0].SignedDigest.String

		// ...then save again with a different BOM, echoing the digest back as a normal save does.
		edited := card("cotton", []entity.TechCardSignoff{approvedSignoff(entity.SignoffMaterials, stamped)})
		StampTechCardSignoffDigests(edited)

		if edited.Signoffs[0].SignedDigest.String != stamped {
			t.Fatal("an ordinary save must not move an existing approval's digest")
		}
		if TechCardSectionDigests(edited)[entity.SignoffMaterials] == stamped {
			t.Fatal("the section's current digest must have moved, or the test proves nothing")
		}
	})

	t.Run("a pending or rejected section carries no digest", func(t *testing.T) {
		for _, state := range []entity.TechCardSignoffState{entity.SignoffStatePending, entity.SignoffStateRejected} {
			tc := card("wool", []entity.TechCardSignoff{{
				Section:      entity.SignoffMaterials,
				State:        state,
				SignedDigest: sql.NullString{String: "stale", Valid: true},
			}})
			StampTechCardSignoffDigests(tc)
			if tc.Signoffs[0].SignedDigest.Valid {
				t.Fatalf("state %q must clear the digest", state)
			}
		}
	})
}

// TestTechCardSectionDigestsAreSectionScoped is the other half of the promise: sections are
// independent, so editing costing must not invalidate the design sign-off. A card-level lock_version
// comparison cannot express that, which is why this is a digest per section.
func TestTechCardSectionDigestsAreSectionScoped(t *testing.T) {
	base := &entity.TechCardInsert{
		Concept:  sql.NullString{String: "a coat", Valid: true},
		BomItems: []entity.TechCardBomItem{{LineKey: "k1", Name: "wool"}},
		Costing: &entity.TechCardCosting{
			CmtCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(10), Valid: true},
		},
	}
	before := TechCardSectionDigests(base)

	base.Costing.CmtCost = decimal.NullDecimal{Decimal: decimal.NewFromInt(11), Valid: true}
	after := TechCardSectionDigests(base)

	if after[entity.SignoffCosting] == before[entity.SignoffCosting] {
		t.Fatal("changing a cost article must move the costing digest")
	}
	for _, untouched := range []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffMaterials, entity.SignoffConstruction,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging,
	} {
		if after[untouched] != before[untouched] {
			t.Fatalf("a costing edit moved the %q digest; sections must be independent", untouched)
		}
	}
}

func TestConstructionDigestCoversOperationCostingInputs(t *testing.T) {
	// НОВАЯ ФОРМА ШАГА: «что делают» и «на чём» — два поля (0306). Фикстура переписана с
	// OpTypeLockstitch на пару (machine, lockstitch) не для красоты: канонизация в конверсии
	// proto→entity означает, что шага с сырым legacy-типом после Ф1 не существует, и фикстура,
	// описывающая несуществующее состояние, перестаёт что-либо доказывать. Отпечаток от этого не
	// двигается — компат-проекция хеширует пару ровно как прежнюю строку, что пинится эталонным
	// hex'ом в techcard_machine_digest_test.go.
	base := entity.TechCardInsert{Operations: []entity.TechCardOperation{{
		OperationType: entity.OpTypeMachine,
		MachineType:   sql.NullString{String: "lockstitch", Valid: true},
		Zone:          entity.TechCardGarmentZone("waist"),
		SMV:           decimal.NullDecimal{Decimal: decimal.RequireFromString("1.10"), Valid: true},
	}}}
	wantDifferent := func(t *testing.T, mutate func(*entity.TechCardOperation)) {
		t.Helper()
		before := TechCardSectionDigests(&base)[entity.SignoffConstruction]
		changed := base
		changed.Operations = append([]entity.TechCardOperation(nil), base.Operations...)
		mutate(&changed.Operations[0])
		after := TechCardSectionDigests(&changed)[entity.SignoffConstruction]
		if after == before {
			t.Fatal("costing-relevant operation change did not invalidate construction approval")
		}
	}

	t.Run("smv", func(t *testing.T) {
		wantDifferent(t, func(op *entity.TechCardOperation) {
			op.SMV = decimal.NullDecimal{Decimal: decimal.RequireFromString("1.15"), Valid: true}
		})
	})
	// `time norm` and `machine` used to be subtests here. Both columns went with the operations
	// break — time_norm as the legacy twin of smv, machine as a copy of operation_type written by a
	// preset — so what replaces them is the pair a step is now actually costed and placed by.
	//
	// `machine` вернулся, но уже не копией типа: 0306 сделал машинку ВТОРОЙ ОСЬЮ шага, и «этот шов
	// идёт на оверлоке, а не на прямострочке» — другое указание цеху при том же типе и той же SMV.
	t.Run("machine type", func(t *testing.T) {
		wantDifferent(t, func(op *entity.TechCardOperation) {
			op.MachineType = sql.NullString{String: "overlock", Valid: true}
		})
	})
	t.Run("zone", func(t *testing.T) {
		wantDifferent(t, func(op *entity.TechCardOperation) {
			op.Zone = entity.TechCardGarmentZone("hem")
		})
	})
	t.Run("seam allowance override", func(t *testing.T) {
		wantDifferent(t, func(op *entity.TechCardOperation) {
			op.SeamAllowanceMm = decimal.NullDecimal{Decimal: decimal.RequireFromString("12"), Valid: true}
		})
	})
	t.Run("operation type", func(t *testing.T) {
		wantDifferent(t, func(op *entity.TechCardOperation) {
			op.OperationType = entity.OpTypeHandwork
		})
	})
}
