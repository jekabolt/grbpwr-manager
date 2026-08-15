package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Отпечаток CONSTRUCTION и фотографии шага с выносками (0308).
//
// Утверждений три, и первое из них — про то, чего фича НЕ должна сделать. Хвост в позиционном
// кортеже операции опасен ровно одним: безусловный элемент сдвигает отпечаток КАЖДОЙ карточки и
// объявляет все подписанные CONSTRUCTION устаревшими в момент выката — до того, как кто-либо
// приложил хоть одну фотографию. Тест ловит именно это, а не «работает ли проекция».

func mediaDigestCard(ops ...entity.TechCardOperation) *entity.TechCardInsert {
	return &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{HemFinish: ns("подгибка 2 см")},
		Operations:   ops,
		Pieces: []entity.TechCardPiece{
			{LineKey: "FR", Name: "полочка", PiecesPerGarment: 1},
			{LineKey: "BK", Name: "спинка", PiecesPerGarment: 1},
		},
	}
}

func mediaPlainOp() entity.TechCardOperation {
	return entity.TechCardOperation{
		OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
		PieceLineKeys: []string{"FR", "BK"},
	}
}

// Своё имя, а не `dec`: в пакете уже есть тестовый хелпер с этим именем и другой сигнатурой.
func unit(v string) decimal.Decimal {
	d, err := decimal.NewFromString(v)
	if err != nil {
		panic(err)
	}
	return d
}

func oneAnnotation(text string) entity.TechCardOperationMedia {
	return entity.TechCardOperationMedia{
		MediaId: 42,
		Caption: ns("втачивание рукава"),
		Annotations: []entity.TechCardAnnotation{{
			Kind: entity.AnnotationKindDim,
			Points: []entity.TechCardAnnotationPoint{
				{X: unit("0.2"), Y: unit("0.3")},
				{X: unit("0.6"), Y: unit("0.3")},
			},
			Text:   text,
			LabelX: unit("0.4"),
			LabelY: unit("0.1"),
		}},
	}
}

// САМОЕ ВАЖНОЕ УТВЕРЖДЕНИЕ ФИЧИ ДЛЯ БАЗЫ: карточка без операционных фотографий обязана хешировать
// байт-в-байт так же, как до правки. Пустой список — не «пустое значение», а отсутствие хвоста.
func TestOperationMediaDigestUnchangedWithoutMedia(t *testing.T) {
	noField := mediaPlainOp() // как читалась до 0308: поля нет вовсе
	empty := mediaPlainOp()
	empty.Media = []entity.TechCardOperationMedia{} // как может прийти с провода: пустой список

	before := TechCardSectionDigests(mediaDigestCard(noField))
	after := TechCardSectionDigests(mediaDigestCard(empty))

	require.Equal(t, before[entity.SignoffConstruction], after[entity.SignoffConstruction],
		"пустой список фотографий не факт цеха: хвост не должен появляться, иначе выкатка объявит устаревшими все подписанные карточки")
}

// Хвост появляется ровно тогда, когда появляется фотография, — и подпись протухает по делу:
// указание «здесь припосадить 6 мм», нарисованное на снимке узла, это инструкция цеху того же
// рода, что припуск или класс шва.
func TestOperationMediaDigestMovesWhenPhotoAdded(t *testing.T) {
	bare := mediaPlainOp()
	withPhoto := mediaPlainOp()
	withPhoto.Media = []entity.TechCardOperationMedia{oneAnnotation("6 мм")}

	before := TechCardSectionDigests(mediaDigestCard(bare))
	after := TechCardSectionDigests(mediaDigestCard(withPhoto))

	require.NotEqual(t, before[entity.SignoffConstruction], after[entity.SignoffConstruction],
		"добавленная фотография с выноской — новое указание цеху, подпись обязана протухнуть")
}

// Правка САМОЙ выноски тоже двигает отпечаток: сдвинули точку мерки — сменилось указание.
func TestOperationMediaDigestMovesWhenAnnotationEdited(t *testing.T) {
	six := mediaPlainOp()
	six.Media = []entity.TechCardOperationMedia{oneAnnotation("6 мм")}
	eight := mediaPlainOp()
	eight.Media = []entity.TechCardOperationMedia{oneAnnotation("8 мм")}

	require.NotEqual(t,
		TechCardSectionDigests(mediaDigestCard(six))[entity.SignoffConstruction],
		TechCardSectionDigests(mediaDigestCard(eight))[entity.SignoffConstruction],
		"текст выноски — само указание, его правка обязана двигать отпечаток")
}

// А ЦВЕТ — НЕ ДВИГАЕТ. Он различает пересекающиеся выноски и смысла не несёт; протухать подпись от
// перекраски значило бы наказывать за наведение порядка на снимке.
func TestOperationMediaDigestIgnoresColor(t *testing.T) {
	plain := mediaPlainOp()
	plain.Media = []entity.TechCardOperationMedia{oneAnnotation("6 мм")}

	painted := mediaPlainOp()
	m := oneAnnotation("6 мм")
	m.Annotations[0].Color = entity.AnnotationColorRed
	painted.Media = []entity.TechCardOperationMedia{m}

	require.Equal(t,
		TechCardSectionDigests(mediaDigestCard(plain))[entity.SignoffConstruction],
		TechCardSectionDigests(mediaDigestCard(painted))[entity.SignoffConstruction],
		"цвет выноски не факт цеха: перекраска не должна протухать подпись")
}
