package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ОДНО СОДЕРЖАНИЕ — ОДИН ОТПЕЧАТОК: пустая обёртка и её отсутствие.
//
// Дефект, который здесь закрыт: блок карточки (упаковка, конструкция, костинг) мог быть nil или
// «присутствует, но ни одного заполненного поля», и эти два состояния давали РАЗНЫЕ отпечатки —
// массив пустых позиций против null. Следствие: карточка, у которой кто-то однажды открыл и закрыл
// вкладку, ничего не заполнив, объявляла свою секцию изменённой без единого изменения; а там, где
// пустую обёртку рождает СТОР (Construction у карточки с профилями оборудования, но без строки
// конструкции), отпечаток записи и отпечаток чтения расходились навсегда — подпись рождалась
// протухшей и не лечилась переутверждением.
//
// Каноничным написанием выбрано ОТСУТСТВИЕ — то же решение, что уже принято у парка оборудования
// (equipmentProfilesTail). Доводы — у blankAsAbsent.
//
// ЧЕГО ЭТИ ТЕСТЫ НЕ УТВЕРЖДАЮТ: что «пусто» и «ноль» различимы. Проекция и до сведения хешировала
// .String/.Int32, то есть NULL и "" (NULL и 0) давали один байт — это её сознательная нормализация,
// описанная в заголовке файла, и сведение пустой обёртки ничего к ней не добавляет.

func blankBlockDigest(t *testing.T, tc *entity.TechCardInsert, sec entity.TechCardSignoffSection) string {
	t.Helper()
	d := TechCardSectionDigests(tc)
	if d == nil {
		t.Fatalf("секционные отпечатки не посчитались вовсе")
	}
	return d[sec]
}

// TestBlankPackagingHashesAsAbsentPackaging — ГЛАВНОЕ УТВЕРЖДЕНИЕ ПРАВКИ, прямым сравнением hex.
func TestBlankPackagingHashesAsAbsentPackaging(t *testing.T) {
	absent := blankBlockDigest(t, &entity.TechCardInsert{}, entity.SignoffPackaging)

	empty := blankBlockDigest(t, &entity.TechCardInsert{
		Packaging: &entity.TechCardPackaging{},
	}, entity.SignoffPackaging)
	if empty != absent {
		t.Errorf("пустой упаковочный лист и отсутствующий дают РАЗНЫЕ отпечатки — карточка, у "+
			"которой вкладку упаковки открыли и закрыли, читается как изменённая.\n"+
			"нет листа:   %s\nпустой лист: %s", absent, empty)
	}

	// Та же пустота другим написанием: не NULL, а Valid-но-пустые значения. Клиент шлёт именно так.
	blank := blankBlockDigest(t, &entity.TechCardInsert{
		Packaging: &entity.TechCardPackaging{
			FoldingMethod: ns(""), Polybag: ns(""), BagSticker: ns(""), Inserts: ns(""),
			BoxMarking: ns(""), BoxDimensions: ns(""), Notes: ns(""),
		},
	}, entity.SignoffPackaging)
	if blank != absent {
		t.Errorf("лист из пустых строк не сошёлся с отсутствующим: %s против %s", blank, absent)
	}

	// Обратная половина: сведение не должно СЪЕДАТЬ содержание.
	if got := packagingDigest(packagingGoldCard()); got == absent {
		t.Errorf("заполненный упаковочный лист хешируется как отсутствующий — сведение съело "+
			"содержание, и подпись под упаковкой читалась бы как действительная под любой другой: %s", got)
	}
	if got := blankBlockDigest(t, &entity.TechCardInsert{
		Packaging: &entity.TechCardPackaging{UnitsPerBox: ni32(12)},
	}, entity.SignoffPackaging); got == absent {
		t.Errorf("лист с единственным заполненным полем хешируется как отсутствующий: %s", got)
	}
}

// TestBlankConstructionHashesAsAbsentConstruction — тот же дефект у CONSTRUCTION, и он там не
// гипотетический: пустую обёртку заводит САМ СТОР (0306, production.go) карточке, у которой есть
// профили оборудования, но нет строки конструкции.
func TestBlankConstructionHashesAsAbsentConstruction(t *testing.T) {
	absent := blankBlockDigest(t, &entity.TechCardInsert{}, entity.SignoffConstruction)

	empty := blankBlockDigest(t, &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{},
	}, entity.SignoffConstruction)
	if empty != absent {
		t.Errorf("пустая конструкция и отсутствующая дают РАЗНЫЕ отпечатки — карточка, которой стор "+
			"завёл пустую обёртку ради профилей, читалась бы как изменённая.\n"+
			"нет блока:   %s\nпустой блок: %s", absent, empty)
	}

	// Пустая обёртка С ПРОФИЛЯМИ: голова схлопывается, а парк обязан остаться в отпечатке — он
	// висит на КАРТОЧКЕ и уезжает отдельным хвостом внешнего кортежа.
	withPark := blankBlockDigest(t, &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{
			EquipmentDefaults: &entity.TechCardEquipmentDefaults{
				Machines: []entity.TechCardMachineProfile{{ProfileKey: "M1", MachineType: "lockstitch"}},
			},
		},
	}, entity.SignoffConstruction)
	if withPark == absent {
		t.Errorf("парк оборудования пропал из отпечатка вместе с пустой головой — сменить профилю "+
			"температуру стало бы невидимым для подписи: %s", withPark)
	}

	if filled := blankBlockDigest(t, &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{HemFinish: ns("подгибка 2 см")},
	}, entity.SignoffConstruction); filled == absent {
		t.Errorf("заполненная конструкция хешируется как отсутствующая: %s", filled)
	}
}

// TestBlankCostingHashesAsAbsentCosting — тот же дефект у COSTING. Пустую обёртку здесь рождает не
// только клиент, но и серверный путь релиза (production_run_release_cost).
func TestBlankCostingHashesAsAbsentCosting(t *testing.T) {
	absent := blankBlockDigest(t, &entity.TechCardInsert{}, entity.SignoffCosting)

	empty := blankBlockDigest(t, &entity.TechCardInsert{
		Costing: &entity.TechCardCosting{},
	}, entity.SignoffCosting)
	if empty != absent {
		t.Errorf("пустой блок себестоимости и отсутствующий дают РАЗНЫЕ отпечатки.\n"+
			"нет блока:   %s\nпустой блок: %s", absent, empty)
	}

	if filled := blankBlockDigest(t, &entity.TechCardInsert{
		Costing: &entity.TechCardCosting{CmtCost: nd("12.50")},
	}, entity.SignoffCosting); filled == absent {
		t.Errorf("заполненный блок себестоимости хешируется как отсутствующий: %s", filled)
	}
}

// TestEmptyDetailMediaListHashesAsAbsentList — ТОТ ЖЕ КЛАСС У СПИСКА, и здесь он ловится не
// рассуждением, а настоящим разбором с провода.
//
// parseTechCardDetails собирает media_ids через make(..., 0, n) и отдаёт аспект без картинок как
// `[]`, а чтение из стора оставляет срез nil и отдаёт `null`. До сведения отпечаток DESIGN такой
// карточки не совпадал сам с собой: подпись рождалась протухшей.
func TestEmptyDetailMediaListHashesAsAbsentList(t *testing.T) {
	parsed, err := parseTechCardDetails([]*pb_common.TechCardDetail{{Key: "collar", Text: "стойка 3 см"}})
	if err != nil {
		t.Fatalf("разбор аспекта отказал: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("ожидался один аспект, получено %d", len(parsed))
	}
	if parsed[0].MediaIds == nil {
		t.Skip("разбор перестал отдавать пустой срез — расхождение написаний закрыто и на входе")
	}

	asWritten := blankBlockDigest(t, &entity.TechCardInsert{Details: parsed}, entity.SignoffDesign)
	asRead := blankBlockDigest(t, &entity.TechCardInsert{Details: []entity.TechCardDetail{{
		Key: ns("collar"), Text: ns("стойка 3 см"), MediaIds: nil,
	}}}, entity.SignoffDesign)
	if asWritten != asRead {
		t.Errorf("аспект без картинок хешируется по-разному на ЗАПИСИ и на ЧТЕНИИ — подпись DESIGN "+
			"рождается протухшей и не лечится переутверждением.\nзапись: %s\nчтение: %s", asWritten, asRead)
	}

	withMedia := blankBlockDigest(t, &entity.TechCardInsert{Details: []entity.TechCardDetail{{
		Key: ns("collar"), Text: ns("стойка 3 см"), MediaIds: []int{7},
	}}}, entity.SignoffDesign)
	if withMedia == asRead {
		t.Errorf("приложенная к аспекту картинка не сдвинула отпечаток: %s", withMedia)
	}
}

// TestEmptyOperationKeyListsHashAsAbsentLists — позиции 4 и 5 кортежа шага. Оба сегодняшних
// производителя отдают nil; правило стоит в проекции, чтобы третий не завёл второе написание молча.
func TestEmptyOperationKeyListsHashAsAbsentLists(t *testing.T) {
	op := func(pieces, boms []string) *entity.TechCardInsert {
		return &entity.TechCardInsert{Operations: []entity.TechCardOperation{{
			OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
			PieceLineKeys: pieces, BomLineKeys: boms,
		}}}
	}
	absent := blankBlockDigest(t, op(nil, nil), entity.SignoffConstruction)
	if empty := blankBlockDigest(t, op([]string{}, []string{}), entity.SignoffConstruction); empty != absent {
		t.Errorf("пустые списки ключей шага дают отпечаток, отличный от отсутствующих.\n"+
			"nil: %s\n[]:  %s", absent, empty)
	}
	if filled := blankBlockDigest(t, op([]string{"FR"}, nil), entity.SignoffConstruction); filled == absent {
		t.Errorf("деталь, назначенная шагу, не сдвинула отпечаток: %s", filled)
	}
}

// TestEmptyAnnotationPieceListHashesAsAbsentList — тот же список внутри выноски на снимке шага.
// Хвост стиля эмитится из-за пунктира, а список деталей в нём при этом пуст — и оба его написания
// обязаны сойтись.
func TestEmptyAnnotationPieceListHashesAsAbsentList(t *testing.T) {
	card := func(keys []string) *entity.TechCardInsert {
		return &entity.TechCardInsert{Operations: []entity.TechCardOperation{{
			OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
			Media: []entity.TechCardOperationMedia{{
				MediaId: 42,
				Annotations: []entity.TechCardAnnotation{{
					Kind:   entity.AnnotationKindDim,
					Points: []entity.TechCardAnnotationPoint{{X: unit("0.2"), Y: unit("0.3")}},
					Text:   "припосадить 6 мм", LabelX: unit("0.4"), LabelY: unit("0.1"),
					Dashed:        true,
					PieceLineKeys: keys,
				}},
			}},
		}}}
	}
	absent := blankBlockDigest(t, card(nil), entity.SignoffConstruction)
	if empty := blankBlockDigest(t, card([]string{}), entity.SignoffConstruction); empty != absent {
		t.Errorf("пустой список деталей выноски дал отпечаток, отличный от отсутствующего.\n"+
			"nil: %s\n[]:  %s", absent, empty)
	}
	if filled := blankBlockDigest(t, card([]string{"FR"}), entity.SignoffConstruction); filled == absent {
		t.Errorf("названная в выноске деталь не сдвинула отпечаток: %s", filled)
	}
}
