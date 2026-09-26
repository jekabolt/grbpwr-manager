package entity

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ПРОБЫ ПРАВИЛА «ПЕРЕЗАПИСАТЬ» (0368, O-53) — решения без базы.
//
// Чтения (кадр, число его видимых кропов) делает стор в транзакции флэттена, и они проверяются
// живыми пробами internal/store/design/replace_db_test.go (одноразовый контейнер, CI=1). Здесь —
// то, что от базы не зависит: какой отказ звучит при каком состоянии и В КАКОМ ПОРЯДКЕ.

const (
	replaceProbeCard  = 41
	replaceProbeMedia = 900
)

// replaceProbeOriginal — кадр, который ГОДИТСЯ: на этой карточке, медиа = подложка слоя, не заменён.
func replaceProbeOriginal() DesignPicture {
	return DesignPicture{Id: 7, TechCardId: replaceProbeCard, MediaId: replaceProbeMedia, Kind: DesignPictureKindFlat}
}

func replaceProbeBase(media int32) sql.NullInt32 { return sql.NullInt32{Int32: media, Valid: true} }

// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: без него каждая проба ниже зеленела бы и на правиле, отказывающем ВСЕГДА.
//
// Сюда же — кропы, которые в счёт не идут: вызывающий считает только видимые незаменённые, и ноль —
// это «резать нечего», а не «не спросили».
func TestDesignReplaceRefusalLetsTheNamedOriginalThrough(t *testing.T) {
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replaceProbeOriginal(), 0))

	// Кроп и правка кропа — законные оригиналы: «edit a piece instead» ведёт именно сюда.
	piece := replaceProbeOriginal()
	piece.DerivedFrom = sql.NullInt32{Int32: 3, Valid: true}
	piece.Derivation = DesignDerivationCrop
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), piece, 0))

	// Спрятанный оригинал не отказывается: прятанье — другой ярус, и правило о нём не судит.
	hidden := replaceProbeOriginal()
	hidden.HiddenAt = sql.NullTime{Valid: true}
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), hidden, 0))
}

// КАЖДЫЙ ОТКАЗ НАЗЫВАЕТ СВОЁ, И ТОЛЬКО СВОЁ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять любую из пяти проверок (подслучай становится nil); перепутать
// сентинел (errors.Is подслучая краснеет); сравнивать медиа с чем-то кроме подложки слоя (случай
// «другой файл» проходит); читать заменённость по знаку вместо NULL-ности (случай replaced_by = 0,
// Valid — невозможный для писателя, но возможный для руки в базе — проходит мимо сторожа, которого
// UPDATE … WHERE replaced_by IS NULL всё равно не пустил бы: два сторожа одного факта разошлись бы).
func TestDesignReplaceRefusalNamesEachRefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		card  int
		base  sql.NullInt32
		pic   func(DesignPicture) DesignPicture
		crops int
		want  error
	}{
		{"кадр чужой карточки", replaceProbeCard + 1, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"слой нарисован с чистого листа", replaceProbeCard, sql.NullInt32{},
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"подложка слоя — ноль", replaceProbeCard, sql.NullInt32{Valid: true},
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"слой нарисован поверх другого файла", replaceProbeCard, replaceProbeBase(replaceProbeMedia + 1),
			func(p DesignPicture) DesignPicture { return p }, 0, ErrDesignReplaceMismatch},
		{"кадр уже заменён", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture {
				p.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
				return p
			}, 0, ErrDesignAlreadyReplaced},
		{"заменённость читается по NULL, а не по знаку", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture {
				p.ReplacedBy = sql.NullInt32{Valid: true}
				return p
			}, 0, ErrDesignAlreadyReplaced},
		{"лист разрезан", replaceProbeCard, replaceProbeBase(replaceProbeMedia),
			func(p DesignPicture) DesignPicture { return p }, 2, ErrDesignCutSheet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := DesignReplaceRefusal(tc.card, tc.base, tc.pic(replaceProbeOriginal()), tc.crops)
			require.Error(t, err)
			require.ErrorIs(t, err, tc.want)
			for _, other := range []error{ErrDesignReplaceMismatch, ErrDesignAlreadyReplaced, ErrDesignCutSheet} {
				if !errors.Is(tc.want, other) {
					require.NotErrorIs(t, err, other, "один отказ — одна причина на проводе")
				}
			}
		})
	}
}

// ПОРЯДОК ОТКАЗОВ: от «чинится запросом» к «чинится другим жестом».
//
// Кадр, у которого неверно ВСЁ сразу, обязан получить replace_mismatch: запрос, назвавший не тот
// кадр, не должен выглядеть как «уже заменён» — клиент перечитал бы полосу и повторил ту же ошибку.
// Заменённый И разрезанный — already_replaced: слепой повтор перезаписи узнаёт себя по этому слову,
// даже если лист успели разрезать после первой подачи.
//
// МУТАЦИЯ: переставить проверки — одна из двух половин краснеет.
func TestDesignReplaceRefusalOrder(t *testing.T) {
	everythingWrong := replaceProbeOriginal()
	everythingWrong.TechCardId = replaceProbeCard + 1
	everythingWrong.MediaId = replaceProbeMedia + 1
	everythingWrong.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
	require.ErrorIs(t,
		DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), everythingWrong, 3),
		ErrDesignReplaceMismatch)

	replacedAndCut := replaceProbeOriginal()
	replacedAndCut.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
	require.ErrorIs(t,
		DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replacedAndCut, 3),
		ErrDesignAlreadyReplaced)
}
