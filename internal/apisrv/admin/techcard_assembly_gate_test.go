package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func asmPayload(aware, cleared bool, ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		AssemblyAware:   aware,
		AssemblyCleared: cleared,
		Operations:      ops,
	}
}

func opWithUnit(key string) *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{OutputUnitKey: key, InputKeys: []string{"FR", "BK"}}
}

func opPlain() *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{PieceLineKeys: []string{"FR"}}
}

func storedCard(ops ...entity.TechCardOperation) *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{Operations: ops}}
}

func storedWithUnits() *entity.TechCard {
	return storedCard(entity.TechCardOperation{OutputUnitKey: sql.NullString{String: "SHELL", Valid: true}})
}

func storedPlain() *entity.TechCard {
	return storedCard(entity.TechCardOperation{PieceLineKeys: []string{"FR", "BK"}})
}

// TestAssemblyGateTruthTable проходит таблицу истинности пары флагов целиком. Она выписана в
// шапке гейта именно потому, что четыре булевых входа дают шестнадцать клеток, а спецификация
// первой редакции описывала пять — остальные разошлись бы по мере правок молча.
func TestAssemblyGateTruthTable(t *testing.T) {
	cases := []struct {
		name       string
		stored     *entity.TechCard
		pb         *pb_common.TechCardInsert
		wantWire   codes.Code // OK = пропустить
		wantStored codes.Code
	}{
		{
			name:   "неразмеченная карточка, старый бандл — сегодняшний путь",
			stored: storedPlain(), pb: asmPayload(false, false, opPlain()),
			wantWire: codes.OK, wantStored: codes.OK,
		},
		{
			name:   "старый бандл эхоит узлы — отказ на проводе",
			stored: storedPlain(), pb: asmPayload(false, false, opWithUnit("SHELL")),
			wantWire: codes.FailedPrecondition, wantStored: codes.OK,
		},
		{
			name:   "cleared без aware — бандл просит снять то, о чём не знает",
			stored: storedPlain(), pb: asmPayload(false, true, opPlain()),
			wantWire: codes.InvalidArgument, wantStored: codes.OK,
		},
		{
			name:   "cleared на неразмеченной карточке — теневое намерение",
			stored: storedPlain(), pb: asmPayload(true, true, opPlain()),
			wantWire: codes.OK, wantStored: codes.OK,
		},
		{
			name:   "размеченная карточка, старый бандл — устаревшая вкладка",
			stored: storedWithUnits(), pb: asmPayload(false, false, opPlain()),
			wantWire: codes.OK, wantStored: codes.FailedPrecondition,
		},
		{
			name:   "размеченная карточка, обычное редактирование",
			stored: storedWithUnits(), pb: asmPayload(true, false, opWithUnit("SHELL")),
			wantWire: codes.OK, wantStored: codes.OK,
		},
		{
			name:   "размеченная карточка, осведомлённая ПУСТАЯ запись — бекстоп",
			stored: storedWithUnits(), pb: asmPayload(true, false, opPlain()),
			wantWire: codes.OK, wantStored: codes.FailedPrecondition,
		},
		{
			name:   "размеченная карточка, снятие разметки с объявленным намерением",
			stored: storedWithUnits(), pb: asmPayload(true, true, opPlain()),
			wantWire: codes.OK, wantStored: codes.OK,
		},
		{
			name:   "cleared вместе с присланными узлами — противоречие",
			stored: storedWithUnits(), pb: asmPayload(true, true, opWithUnit("SHELL")),
			wantWire: codes.InvalidArgument, wantStored: codes.OK,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := status.Code(assemblyCapabilityWireGate(c.pb)); got != c.wantWire {
				t.Errorf("щит провода: %v, ожидался %v", got, c.wantWire)
			}
			if got := status.Code(assemblyCapabilityStoredGate(c.pb, c.stored)); got != c.wantStored {
				t.Errorf("щит хранилища: %v, ожидался %v", got, c.wantStored)
			}
		})
	}
}

// TestStoredHasAssemblyFactsIgnoresPieceLinks — связи шага с ДЕТАЛЯМИ разметкой не являются.
// Считать их разметкой значило бы объявить устаревшими вкладки, редактирующие каждую сегодняшнюю
// карточку: связи есть у карточек, где узлов никто не размечал.
func TestStoredHasAssemblyFactsIgnoresPieceLinks(t *testing.T) {
	if storedHasAssemblyFacts(storedPlain()) {
		t.Error("карточка со связями шаг↔деталь ошибочно считана размеченной")
	}
	if !storedHasAssemblyFacts(storedWithUnits()) {
		t.Error("карточка с выходным узлом не считана размеченной")
	}
	unitInput := storedCard(entity.TechCardOperation{
		AssemblyInputs: []entity.OperationInput{{Kind: entity.AssemblyInputUnit, Key: "SHELL"}},
	})
	if !storedHasAssemblyFacts(unitInput) {
		t.Error("вход-узел не считан фактом сборки")
	}
	if storedHasAssemblyFacts(nil) {
		t.Error("nil-карточка не может нести фактов")
	}
}
