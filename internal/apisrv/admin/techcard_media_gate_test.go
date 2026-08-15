package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Таблица истинности щита операционных фотографий (0308).
//
// Девять клеток выписаны в шапке гейта, и покрыты они здесь по той же причине, что у щита узлов:
// четыре булевых входа дают шестнадцать сочетаний, спецификация описывает девять осмысленных, а
// остальные разошлись бы по мере правок молча.
//
// САМАЯ ВАЖНАЯ КЛЕТКА — «карточка со снимками, бандл осведомлён, снимков не несёт, снять не
// просил»: это и есть тихое стирание десятков выносок отставшей вкладкой, ради которого щит и
// написан.

func mediaPayload(aware, cleared bool, ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		MediaAware:   aware,
		MediaCleared: cleared,
		Operations:   ops,
	}
}

func opWithPhoto() *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{
		Media: []*pb_common.TechCardOperationMedia{{MediaId: 42}},
	}
}

// Шаг без снимков — то, что шлёт и новый клиент на неразмеченной карточке, и старый бандл:
// отличить их можно ТОЛЬКО по флагу, поле `media` у обоих пусто.
func opNoPhoto() *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{PieceLineKeys: []string{"FR"}}
}

func storedWithPhotos() *entity.TechCard {
	return storedCard(entity.TechCardOperation{
		Media: []entity.TechCardOperationMedia{{MediaId: 42}},
	})
}

func TestMediaGateTruthTable(t *testing.T) {
	cases := []struct {
		name       string
		pb         *pb_common.TechCardInsert
		stored     *entity.TechCard
		wantWire   codes.Code
		wantStored codes.Code
	}{
		{
			name:       "нет снимков нигде, старый бандл — сегодняшний путь",
			pb:         mediaPayload(false, false, opNoPhoto()),
			stored:     storedPlain(),
			wantWire:   codes.OK,
			wantStored: codes.OK,
		},
		{
			name:       "старый бандл эхоит поле, которого не знает",
			pb:         mediaPayload(false, false, opWithPhoto()),
			stored:     storedPlain(),
			wantWire:   codes.FailedPrecondition,
			wantStored: codes.OK,
		},
		{
			name:       "старый бандл выставил cleared — противоречие",
			pb:         mediaPayload(false, true, opNoPhoto()),
			stored:     storedPlain(),
			wantWire:   codes.InvalidArgument,
			wantStored: codes.OK,
		},
		{
			name:       "создание: осведомлён, снимки есть",
			pb:         mediaPayload(true, false, opWithPhoto()),
			stored:     nil,
			wantWire:   codes.OK,
			wantStored: codes.OK,
		},
		{
			name:       "создание: «снял» там, где снимать нечего",
			pb:         mediaPayload(true, true, opNoPhoto()),
			stored:     nil,
			wantWire:   codes.OK,
			wantStored: codes.InvalidArgument,
		},
		{
			name:       "карточка со снимками, бандл о них не знает — устаревшая вкладка",
			pb:         mediaPayload(false, false, opNoPhoto()),
			stored:     storedWithPhotos(),
			wantWire:   codes.OK,
			wantStored: codes.FailedPrecondition,
		},
		{
			name:       "карточка со снимками, обычное редактирование",
			pb:         mediaPayload(true, false, opWithPhoto()),
			stored:     storedWithPhotos(),
			wantWire:   codes.OK,
			wantStored: codes.OK,
		},
		{
			// РАДИ ЭТОЙ КЛЕТКИ ВСЁ И НАПИСАНО.
			name:       "карточка со снимками, осведомлённая пустота — бекстоп",
			pb:         mediaPayload(true, false, opNoPhoto()),
			stored:     storedWithPhotos(),
			wantWire:   codes.OK,
			wantStored: codes.FailedPrecondition,
		},
		{
			name:       "карточка со снимками, снятие объявлено — законный путь",
			pb:         mediaPayload(true, true, opNoPhoto()),
			stored:     storedWithPhotos(),
			wantWire:   codes.OK,
			wantStored: codes.OK,
		},
		{
			name:       "«снял» и одновременно прислал — противоречие",
			pb:         mediaPayload(true, true, opWithPhoto()),
			stored:     storedWithPhotos(),
			wantWire:   codes.InvalidArgument,
			wantStored: codes.OK,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := status.Code(mediaCapabilityWireGate(c.pb)); got != c.wantWire {
				t.Errorf("провод: %v, ожидалось %v", got, c.wantWire)
			}
			if got := status.Code(mediaCapabilityStoredGate(c.pb, c.stored)); got != c.wantStored {
				t.Errorf("сохранённая: %v, ожидалось %v", got, c.wantStored)
			}
		})
	}
}
