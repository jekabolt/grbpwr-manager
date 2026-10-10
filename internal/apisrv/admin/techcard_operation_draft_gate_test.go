package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ТАБЛИЦА ИСТИННОСТИ ЩИТА ЧЕРНОВИКА КАРКАСА (0410).
//
// Главная клетка — «у карточки есть draft-шаги, бандл НЕ осведомлён»: открытая старая вкладка,
// сохраняя любую правку, полной заменой стёрла бы каждую метку «не проверено». ОТКАЗ словами
// «reload the page». Осведомлённая запись проходит всегда, и её draft доезжает до entity.
//
// ⚠️ МУТАЦИЯ (прогнана и откачена): из operationDraftStoredGate убран ранний выход по флагу —
// TestOperationDraftAwareUpdateWritesDraft покраснел (осведомлённая запись стала отказом). Сами
// вызовы гейтов держит TestOperationDraftGateIsWiredIntoUpdate по тексту исходника.

func draftPayload(aware bool, drafts ...bool) *pb_common.TechCardInsert {
	ops := make([]*pb_common.TechCardOperation, 0, len(drafts))
	for i, d := range drafts {
		ops = append(ops, &pb_common.TechCardOperation{
			OperationNumber: int32((i + 1) * 10),
			OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:            gateZone(),
			MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Draft:           d,
		})
	}
	return &pb_common.TechCardInsert{
		StyleNumber:         "DRAFT-GATE",
		Name:                "gate",
		OperationDraftAware: aware,
		Operations:          ops,
	}
}

func storedDraftCard(draft bool) *entity.TechCard {
	return storedCard(entity.TechCardOperation{OperationType: entity.OpTypeMachine, Draft: draft})
}

func TestOperationDraftUnawareUpdateRefusedOverDraftSteps(t *testing.T) {
	err := operationDraftStoredGate(draftPayload(false, false), storedDraftCard(true))
	wantFailedPrecondition(t, err, "старый бандл поверх карточки с draft-шагами")
	if !strings.Contains(err.Error(), "reload the page") {
		t.Fatalf("отказ обязан сказать, что делать: %v", err)
	}
}

func TestOperationDraftUnawareUpdateKeepsWorkingWithoutDrafts(t *testing.T) {
	if err := operationDraftStoredGate(draftPayload(false, false), storedDraftCard(false)); err != nil {
		t.Fatalf("карточка без черновиков обязана сохраняться старым бандлом как раньше: %v", err)
	}
	if err := operationDraftWireGate(draftPayload(false, false)); err != nil {
		t.Fatalf("payload без draft и без флага — сегодняшний путь: %v", err)
	}
}

func TestOperationDraftUnawarePayloadCarryingDraftRefused(t *testing.T) {
	wantFailedPrecondition(t, operationDraftWireGate(draftPayload(false, true)),
		"draft = true без флага — эхо, старый бандл такого поля не знает")
}

func TestOperationDraftAwareUpdateWritesDraft(t *testing.T) {
	in := draftPayload(true, true, false)
	if err := operationDraftWireGate(in); err != nil {
		t.Fatalf("осведомлённая запись с draft: %v", err)
	}
	if err := operationDraftStoredGate(in, storedDraftCard(true)); err != nil {
		t.Fatalf("осведомлённая запись поверх draft-карточки: %v", err)
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("конверсия: %v", err)
	}
	if len(tc.Operations) != 2 || !tc.Operations[0].Draft || tc.Operations[1].Draft {
		t.Fatalf("draft обязан доехать до entity шаг в шаг: %+v", []bool{tc.Operations[0].Draft, tc.Operations[1].Draft})
	}
}

// Гейт без вызова — пустое обещание: держим сам вызов в UpdateTechCard и в двух проводных путях.
func TestOperationDraftGateIsWiredIntoUpdate(t *testing.T) {
	src := readDraftGateSource(t, "techcard.go")
	if strings.Count(src, "operationDraftWireGate(") < 2 || !strings.Contains(src, "operationDraftStoredGate(in, stored)") {
		t.Fatal("щит черновика не вызывается на create/update — устаревшая вкладка стирала бы метки")
	}
	if !strings.Contains(readDraftGateSource(t, "techcard_archive.go"), "operationDraftWireGate,") {
		t.Fatal("импорт архива не прогоняет щит черновика")
	}
}

func readDraftGateSource(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("не читается %s: %v", name, err)
	}
	return string(body)
}
