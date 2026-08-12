package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func pbBool(v bool) *bool { return &v }

// ПРИСУТСТВИЕ, НЕ ЗНАЧЕНИЕ — тот же контракт, что у cut_symmetry, и та же цена ошибки. `optional`
// существует затем, чтобы вкладка, которая про градацию не спрашивает, не сняла пометку молча; весь
// механизм держится на том, что парсер отличает ОТСУТСТВИЕ от ЯВНОГО false. Слить их — каждое
// сохранение из старой вкладки стирает разметку; разделить неверно — пометку нельзя снять.
func TestParsePieceUngradedPresence(t *testing.T) {
	t.Run("absent means omitted, not cleared", func(t *testing.T) {
		got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "карман", PiecesPerGarment: 2},
		}))
		require.NoError(t, err)
		require.True(t, got.Pieces[0].UngradedOmitted, "a piece with no ungraded field must be marked omitted")
		require.False(t, got.Pieces[0].Ungraded, "omitted must not invent a value")
	})

	t.Run("explicit false clears", func(t *testing.T) {
		got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "карман", PiecesPerGarment: 2, Ungraded: pbBool(false)},
		}))
		require.NoError(t, err)
		require.False(t, got.Pieces[0].UngradedOmitted,
			"an explicitly sent false is a deliberate act and must NOT read as omitted, or the mark can never be removed")
		require.False(t, got.Pieces[0].Ungraded)
	})

	t.Run("explicit true is carried", func(t *testing.T) {
		got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "карман", PiecesPerGarment: 2, Ungraded: pbBool(true)},
		}))
		require.NoError(t, err)
		require.False(t, got.Pieces[0].UngradedOmitted)
		require.True(t, got.Pieces[0].Ungraded)
	})
}

// Обратный конец рейса. Поле обязано присутствовать на ЧТЕНИИ всегда — иначе у непомеченной детали
// (а таких сегодня почти все) оно пропадает из JSON, клиент честно возвращает прочитанное, то есть
// НИЧЕГО, и выглядит для стора ровно той старой вкладкой, от которой optional и защищает.
func TestEmitPieceUngradedIsAlwaysPresent(t *testing.T) {
	for _, want := range []bool{true, false} {
		out := techCardPiecesToPb([]entity.TechCardPiece{
			{Name: "карман", LineKey: "01ABCDEF0000000000000001", PiecesPerGarment: 2,
				Grainline: "lengthwise", Ungraded: want},
		})
		require.Len(t, out, 1)
		require.NotNil(t, out[0].Ungraded, "ungraded must be emitted even when false, or a round-tripping client goes silent")
		require.Equal(t, want, out[0].GetUngraded())
	}
}
