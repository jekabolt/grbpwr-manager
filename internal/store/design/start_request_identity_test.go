package design

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ОДИН ЛИ ЭТО ЗАПРОС — ПРАВИЛО, А НЕ ДВА МНЕНИЯ (N11).
//
// У идемпотентности старта два пути: чтение до вставки и остаточный 1062 после неё. Пояс не
// сравнивал НИЧЕГО и отдавал найденную строку как есть, поэтому две одновременные заявки с одним
// ключом, но разными карточками или колорвеями могли получить чужой прогон с ответом OK. Теперь
// оба пути зовут designSameStartRequest — эта проба судит само правило.
//
// ⚠ ЧЕГО ОНА НЕ ДОКАЗЫВАЕТ, СКАЗАНО ВСЛУХ: что ПОЯС его зовёт. Путь 1062 достижим только гонкой,
// разрешившейся не дедлоком, — а чтение до вставки в SERIALIZABLE делает её недостижимой из
// одного соединения. Мутация «убрать вызов из пояса» эту пробу переживает, и это записанный
// пробел покрытия, а не зелень, которой можно верить.
func TestDesignSameStartRequestJudgesCardAndColorway(t *testing.T) {
	prior := func(card, liveCw int, params string) entity.DesignRun {
		r := entity.DesignRun{Id: 7, TechCardId: card}
		if liveCw > 0 {
			r.ColorwayId = sql.NullInt32{Int32: int32(liveCw), Valid: true}
		}
		if params != "" {
			r.Params = entity.RawJSON(params)
		}
		return r
	}
	req := func(card, cw int) entity.DesignRunStart {
		return entity.DesignRunStart{TechCardId: card, ColorwayId: cw, ClientRequestId: "k"}
	}

	// НАСТОЯЩИЙ ПОВТОР — молчание.
	require.NoError(t, designSameStartRequest(prior(1, 5, `{"colorway_id":5}`), req(1, 5)))

	// ЧУЖАЯ КАРТОЧКА — коллизия ключа, а не повтор.
	require.ErrorIs(t, designSameStartRequest(prior(2, 5, `{"colorway_id":5}`), req(1, 5)),
		entity.ErrDesignInvalidArgument)

	// ДРУГОЙ КОЛОРВЕЙ — другой запрос.
	require.ErrorIs(t, designSameStartRequest(prior(1, 5, `{"colorway_id":5}`), req(1, 6)),
		entity.ErrDesignColorwayMismatch)

	// КОЛОРВЕЙ УДАЛЁН: колонка погашена FK, а просьба в params цела — ретрай обязан пройти.
	require.NoError(t, designSameStartRequest(prior(1, 0, `{"colorway_id":5}`), req(1, 5)),
		"живое зеркало гаснет, запись просьбы — нет; различитель обязан смотреть на просьбу")

	// PARAMS МОЛЧАТ О КОЛОРВЕЕ — тогда колонка единственный свидетель, а не второе мнение.
	require.NoError(t, designSameStartRequest(prior(1, 5, ""), req(1, 5)))
	require.ErrorIs(t, designSameStartRequest(prior(1, 5, ""), req(1, 6)),
		entity.ErrDesignColorwayMismatch)
}
