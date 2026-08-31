package admin

import (
	"context"
	"database/sql"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ПРОБЫ НУМЕРАЦИИ ВЫНОСОК НА ОБЫЧНОМ СОХРАНЕНИИ ТЕХ-КАРТЫ.
//
// Номер выноски раздаёт ХЕНДЛЕР (dto.MintCalloutNumbers), а не стор, и раздаёт его ПЕРЕД штампом
// отпечатков разделов — см. UpdateTechCard. Здесь проверяются две половины ОДНОГО правила «какая
// выноска вообще претендует на номер»: раздатчик и граница, которая отказывает, пока номера не
// розданы. Разойдясь, они дают отказ, который человеку нечем закрыть.
//
// Стор замокан: проверяется решение хендлера, а не запись.

const calloutNumCardID = 7

func calloutNumStoredCard() *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{CalloutSeq: 0}}
}

func calloutNumDoc() *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber:     "TC-CALLOUT",
		Name:            "callout numbering subject",
		Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
		MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
	}
}

func calloutNumCtx() context.Context {
	return authsrv.PutAdminUsername(fullAccessCtx(), "designer")
}

// ДВЕ ВЫНОСКИ БЕЗ НОМЕРА: ОДНА НА ТЕХНИЧЕСКОМ ЭСКИЗЕ, ОДНА НА МУДБОРДНОЙ ПЛИТКЕ.
//
// Номер выноски — адрес НА ЭСКИЗЕ: на него ссылаются деталь кроя, операция, дефект и печатный
// тех-пак. Как только клиент начнёт слать client_ref и на мудбордных выносках (F-4 это требует,
// иначе они станут новыми легаси-нулями), заметка съела бы очередной номер — нумерация поехала бы
// дырами, а номер перестал бы означать «выноска N на эскизе».
func TestSaveNumbersTheSketchCalloutButNotTheMoodboardNote(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()
	cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, calloutNumCardID).Return(calloutNumStoredCard(), nil)
	var written *entity.TechCardInsert
	cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, calloutNumCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) { written = tc }).
		Return(nil, entity.ErrTechCardConflict)

	doc := calloutNumDoc()
	doc.TechnicalMedia = []*pb_common.TechCardMediaItem{
		{MediaId: 55, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT},
	}
	doc.MoodboardMedia = []*pb_common.TechCardMediaItem{
		{MediaId: 77, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_MOODBOARD},
	}
	doc.Callouts = []*pb_common.TechCardCallout{
		{Number: 0, ClientRef: "cr-sheet", MediaId: 55, Part: "collar"},
		{Number: 0, ClientRef: "cr-mood", MediaId: 77, Description: "this reference, but darker"},
	}

	_, err := (&Server{repo: repo}).UpdateTechCard(calloutNumCtx(), &pb_admin.UpdateTechCardRequest{
		Id: calloutNumCardID, ExpectedLockVersion: 3, TechCard: doc,
	})
	require.Equal(t, codes.Aborted, status.Code(err), "запрос обязан дойти до стора целиком")
	require.NotNil(t, written)
	require.Len(t, written.Callouts, 2)

	byRef := map[string]entity.TechCardCallout{}
	for _, c := range written.Callouts {
		byRef[c.ClientRef.String] = c
	}
	require.GreaterOrEqual(t, byRef["cr-sheet"].Number, 1,
		"выноска на техническом эскизе обязана получить номер")
	require.Equal(t, 0, byRef["cr-mood"].Number,
		"мудбордная заметка съела номер: нумерация поедет дырами, а номер перестанет "+
			"означать «выноска N на эскизе»")
	require.Equal(t, "cr-mood", byRef["cr-mood"].ClientRef.String,
		"её личность — ключ строки, и он обязан уцелеть: ноль без ключа это легаси-ноль, а это не он")
}

// ГРАНИЦА ПЕРЕД ОТПЕЧАТКОМ СОГЛАСОВАНА С ПРЕДИКАТОМ РАЗДАЧИ НОМЕРОВ.
//
// Если CalloutsAwaitingNumber считает мудбордную заметку «ждущей номера», а dto.MintCalloutNumbers
// её не трогает, то граница срабатывает ВЕЧНО: сохранение любой карточки с мудбордом отказывает
// словами про отпечаток секций. Проба ловит именно расхождение двух половин одного правила.
func TestAwaitingNumberAgreesWithTheNumberingPredicate(t *testing.T) {
	tc := &entity.TechCardInsert{
		Media: []entity.TechCardMediaItem{
			{MediaId: 55, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
			{MediaId: 77, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard},
		},
		Callouts: []entity.TechCardCallout{
			{Number: 0, MediaId: sql.NullInt32{Int32: 77, Valid: true},
				ClientRef: sql.NullString{String: "cr-mood", Valid: true}},
		},
	}
	require.False(t, dto.CalloutsAwaitingNumber(tc),
		"мудбордная заметка объявлена ждущей номера, а раздача её не трогает — граница будет отказывать вечно")

	tc.Callouts = append(tc.Callouts, entity.TechCardCallout{
		Number: 0, MediaId: sql.NullInt32{Int32: 55, Valid: true},
		ClientRef: sql.NullString{String: "cr-sheet", Valid: true},
	})
	require.True(t, dto.CalloutsAwaitingNumber(tc),
		"выноска на эскизе без номера обязана держать границу — иначе она сторожит мёртвый код")
}
