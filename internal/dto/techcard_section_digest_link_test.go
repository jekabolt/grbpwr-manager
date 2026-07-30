package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// linkedWritePayload is what the admin client actually sends for a BOM line linked to a catalog
// material: the link, and nothing the read path would resolve anyway.
func linkedWritePayload() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{{
			LineKey:    "k1",
			Section:    entity.BomSectionFabric,
			MaterialId: sql.NullInt64{Int64: 42, Valid: true},
		}},
	}
}

// enrichedReadModel is the same line as the BOM read query returns it: identity resolved through the
// link (internal/store/techcard/materials.go).
func enrichedReadModel() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{{
			LineKey:     "k1",
			Section:     entity.BomSectionFabric,
			MaterialId:  sql.NullInt64{Int64: 42, Valid: true},
			Name:        "wool twill 320",
			Supplier:    sql.NullString{String: "Mill SpA", Valid: true},
			SupplierRef: sql.NullString{String: "WT-320", Valid: true},
			Composition: sql.NullString{String: "100% wool", Valid: true},
			Spec:        sql.NullString{String: "brushed", Valid: true},
			Unit:        sql.NullString{String: "m", Valid: true},
		}},
	}
}

func materialCatalog() map[int64]BomMaterialIdentity {
	return map[int64]BomMaterialIdentity{42: {
		Name:        "wool twill 320",
		Supplier:    "Mill SpA",
		SupplierRef: "WT-320",
		Composition: "100% wool",
		Spec:        "brushed",
		Unit:        "m",
	}}
}

// TestMaterialsDigestSurvivesTheLink is the regression that matters: a MATERIALS approval is stamped on
// the WRITE payload and checked against the READ model, and for a linked BOM line those two carry
// different identity fields. Without resolving the link on the write side the digests could never match
// and every materials sign-off read "changed since sign-off" the moment it was made.
func TestMaterialsDigestSurvivesTheLink(t *testing.T) {
	catalog := materialCatalog()

	t.Run("the raw payload does NOT match the read model", func(t *testing.T) {
		// Guards the premise. If this ever stops holding, the write side no longer needs the catalog.
		if TechCardSectionDigests(linkedWritePayload())[entity.SignoffMaterials] ==
			TechCardSectionDigests(enrichedReadModel())[entity.SignoffMaterials] {
			t.Fatal("write and read agree without resolving the link; this test proves nothing")
		}
	})

	t.Run("resolving the link makes them agree", func(t *testing.T) {
		got := TechCardSectionDigestsAsRead(linkedWritePayload(), catalog)[entity.SignoffMaterials]
		want := TechCardSectionDigests(enrichedReadModel())[entity.SignoffMaterials]
		if got != want {
			t.Fatalf("stamped %s, but the next read reports %s", got, want)
		}
	})

	t.Run("a client-typed name on a linked line is ignored, as the read query ignores it", func(t *testing.T) {
		payload := linkedWritePayload()
		payload.BomItems[0].Name = "whatever the user typed"
		payload.BomItems[0].Unit = sql.NullString{String: "kg", Valid: true}
		got := TechCardSectionDigestsAsRead(payload, catalog)[entity.SignoffMaterials]
		want := TechCardSectionDigests(enrichedReadModel())[entity.SignoffMaterials]
		if got != want {
			t.Fatal("the catalog value must win over the line's own, the same way the read query resolves it")
		}
	})

	t.Run("the payload itself is never mutated", func(t *testing.T) {
		payload := linkedWritePayload()
		TechCardSectionDigestsAsRead(payload, catalog)
		if payload.BomItems[0].Name != "" || payload.BomItems[0].Unit.Valid {
			t.Fatal("the enriched form is for hashing only; the stored columns must keep the client's values")
		}
	})
}

// TestBomIdentityResolutionMirrorsTheLeftJoin pins the fallbacks. The read query is a LEFT JOIN whose
// COALESCE(NULLIF(m.x, empty), bi.x) means a missing material, a missing catalog and an empty catalog
// value all leave the line exactly as it was sent — anything else would fingerprint content no read reports.
func TestBomIdentityResolutionMirrorsTheLeftJoin(t *testing.T) {
	asSent := TechCardSectionDigests(linkedWritePayload())[entity.SignoffMaterials]

	t.Run("no catalog at all", func(t *testing.T) {
		if got := TechCardSectionDigestsAsRead(linkedWritePayload(), nil)[entity.SignoffMaterials]; got != asSent {
			t.Fatal("a nil catalog must leave the payload's own fingerprint")
		}
	})

	t.Run("the linked material is not in the catalog", func(t *testing.T) {
		other := map[int64]BomMaterialIdentity{99: {Name: "someone else"}}
		if got := TechCardSectionDigestsAsRead(linkedWritePayload(), other)[entity.SignoffMaterials]; got != asSent {
			t.Fatal("a broken link must leave the line as sent, as the LEFT JOIN does")
		}
	})

	t.Run("an empty catalog value falls back to the line's own", func(t *testing.T) {
		payload := linkedWritePayload()
		payload.BomItems[0].Name = "last known name"
		payload.BomItems[0].Unit = sql.NullString{String: "m", Valid: true}
		blank := map[int64]BomMaterialIdentity{42: {Supplier: "Mill SpA"}}

		want := enrichedReadModel()
		want.BomItems[0].Name = "last known name" // NULLIF('','') -> bi.name
		want.BomItems[0].SupplierRef = sql.NullString{}
		want.BomItems[0].Composition = sql.NullString{}
		want.BomItems[0].Spec = sql.NullString{}

		got := TechCardSectionDigestsAsRead(payload, blank)[entity.SignoffMaterials]
		if got != TechCardSectionDigests(want)[entity.SignoffMaterials] {
			t.Fatal("an empty catalog field must not blank the line's own value")
		}
	})

	t.Run("a free-text line is untouched", func(t *testing.T) {
		payload := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{{
			LineKey: "k2", Section: entity.BomSectionFabric, Name: "hand-typed twill",
		}}}
		before := TechCardSectionDigests(payload)[entity.SignoffMaterials]
		if got := TechCardSectionDigestsAsRead(payload, materialCatalog())[entity.SignoffMaterials]; got != before {
			t.Fatal("a line with no material_id has nothing to resolve from")
		}
	})
}

// TestLinkResolutionTouchesOnlyMaterials keeps the correction where it belongs: the link feeds the
// MATERIALS projection and nothing else, so re-stamping any other section must be a no-op.
func TestLinkResolutionTouchesOnlyMaterials(t *testing.T) {
	payload := linkedWritePayload()
	payload.Concept = sql.NullString{String: "a coat", Valid: true}
	payload.Pieces = []entity.TechCardPiece{{LineKey: "p1", Name: "front", PiecesPerGarment: 2}}

	plain := TechCardSectionDigests(payload)
	resolved := TechCardSectionDigestsAsRead(payload, materialCatalog())
	for section, d := range plain {
		if section == entity.SignoffMaterials {
			if resolved[section] == d {
				t.Fatal("resolving the link must move the materials digest")
			}
			continue
		}
		if resolved[section] != d {
			t.Fatalf("resolving a BOM link moved the %q digest", section)
		}
	}
}
