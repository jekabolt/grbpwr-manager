package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ТРАНСПОРТНЫЙ ФЛАГ ВОЛНЫ ВИДОВ ОПЕРАЦИЙ — О ОТПРАВИТЕЛЕ, А НЕ О КАРТОЧКЕ.
//
// operation_kinds_aware говорит, знает ли бандл про 32 колонки волны; содержание карточки от него
// не зависит. Захешируй его — и подпись CONSTRUCTION протухала бы у КАЖДОЙ карточки в день выкатки
// клиента, причём с формулировкой «секция отредактирована после подписания», которую никто не смог
// бы связать с причиной. Тот же довод, по которому в проекции не появились machine_fields_aware,
// assembly_aware и media_aware.
//
// Проверяется РАЗБОРОМ, а не подстановкой в сущность: у флага нет поля в entity.TechCardInsert
// вовсе, и тест ловит ровно тот день, когда кто-нибудь его туда заведёт «для симметрии» с
// MachineFieldsAware и протянет в дайджест.
func TestOperationKindsAwareIsNotHashed(t *testing.T) {
	build := func(aware bool) *entity.TechCardInsert {
		pb := &pb_common.TechCardInsert{
			StyleNumber:         "OK-TRANSPORT-1",
			Name:                "Jacket",
			OperationKindsAware: aware,
			Operations: []*pb_common.TechCardOperation{{
				OperationNumber: 10,
				OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
				Zone:            pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_FRONT,
				MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTONHOLE,
				Fastening: &pb_common.TechCardOperationFastening{
					ButtonholeStyle:       pb_common.TechCardButtonholeStyle_TECH_CARD_BUTTONHOLE_STYLE_EYELET,
					CutLengthMm:           dec("18"),
					ButtonholeOrientation: pb_common.TechCardButtonholeOrientation_TECH_CARD_BUTTONHOLE_ORIENTATION_VERTICAL,
					BartackLengthMm:       dec("6"),
				},
			}},
		}
		tc, err := ConvertPbTechCardInsertToEntity(pb)
		if err != nil {
			t.Fatalf("конверсия payload'а (aware=%v) не должна падать: %v", aware, err)
		}
		return tc
	}

	awareDigest := constructionDigest(build(true))
	unawareDigest := constructionDigest(build(false))
	if awareDigest != unawareDigest {
		t.Fatalf("транспортный флаг попал в отпечаток CONSTRUCTION: aware=%s, unaware=%s",
			awareDigest, unawareDigest)
	}
	if awareDigest == "" {
		t.Fatal("отпечаток CONSTRUCTION пуст — тест не проверил бы ничего")
	}
}
