package techcardanalysis

import (
	"strings"
	"unicode/utf8"
)

// aiBoundedText trims a short free-text answer to the column the save writes it into, in RUNES,
// marking the cut with an ellipsis.
//
// Cut rather than dropped: this qualifier carries the NUMBER the ordered scale cannot ("0.5 tighter
// than the top thread"), and dropping it would leave the technologist a step of the scale and
// nothing about how far. Marked rather than cut silently: a truncated Russian sentence read as if
// the model had ended it there is a different instruction from the one it wrote, and the ellipsis is
// what stops the draft from asserting it.
//
// КОПИЯ, А НЕ ПЕРЕЕЗД (design §6, «Заборы»): оригинал живёт в internal/apisrv/admin/techcard_ai.go и
// остаётся там — его правят параллельные фазы, и утащить его сюда значило бы поменять чужой файл
// ради своего импорта. Здесь у него ДВА потребителя: забор входа (имена деталей и узлов приезжают
// из DXF-блоков внешних файлов, нота ≤ 300 символов, имена ≤ 120) и забор выхода (тексты находок
// модели, §8 п.7). Копия крошечная и не имеет состояния; синхронизировать их нечем и незачем.
func aiBoundedText(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
