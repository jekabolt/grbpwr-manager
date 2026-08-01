package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func TestConvertPbTechCardInsertMintsLineKeysBeforeSignoffDigests(t *testing.T) {
	zero := int32(0)
	in := &pb_common.TechCardInsert{
		StyleNumber: "KEYLESS-1",
		Name:        "Keyless card",
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "shell"},
			{LineKey: "client-bom", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "zip"},
		},
		Pieces: []*pb_common.TechCardPiece{
			{
				Name: "body",
				Materials: []*pb_common.TechCardPieceColorwayMaterial{
					{ColorwayId: 1, BomItemIndex: &zero},
					{ColorwayId: 2, BomLineKey: "client-bom"},
				},
			},
			{Name: "sleeve", LineKey: "client-piece"},
		},
		Signoffs: []*pb_common.TechCardSignoff{
			{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_MATERIALS, State: pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED},
			{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_CONSTRUCTION, State: pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED},
			{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COLOUR, State: pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED},
		},
	}

	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}
	if len(got.BomItems[0].LineKey) != 26 {
		t.Errorf("keyless BOM line received %q, want a 26-character key", got.BomItems[0].LineKey)
	}
	if got.BomItems[1].LineKey != "client-bom" {
		t.Errorf("client BOM key changed to %q", got.BomItems[1].LineKey)
	}
	if len(got.Pieces[0].LineKey) != 26 {
		t.Errorf("keyless piece received %q, want a 26-character key", got.Pieces[0].LineKey)
	}
	if got.Pieces[1].LineKey != "client-piece" {
		t.Errorf("client piece key changed to %q", got.Pieces[1].LineKey)
	}
	legacyRef := got.Pieces[0].Materials[0]
	if legacyRef.BomLineKey != "" || !legacyRef.BomItemIndex.Valid || legacyRef.BomItemIndex.Int32 != 0 {
		t.Errorf("minting a target key must leave its positional ref unchanged: %+v", legacyRef)
	}
	if got.Pieces[0].Materials[1].BomLineKey != "client-bom" {
		t.Errorf("existing keyed reference changed: %+v", got.Pieces[0].Materials[1])
	}

	digests := TechCardSectionDigests(got)
	for _, signoff := range got.Signoffs {
		if signoff.State != entity.SignoffStateApproved || !signoff.SignedDigest.Valid ||
			signoff.SignedDigest.String != digests[signoff.Section] {
			t.Errorf("%s signoff was not stamped after key minting: %+v", signoff.Section, signoff)
		}
	}
}
