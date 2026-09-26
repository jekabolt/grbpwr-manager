package entity

import (
	"database/sql"
	"errors"
	"fmt"
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
// Сюда же — кропы, которые в счёт не идут: вызывающий считает только видимые (заменённые своей
// правкой — тоже, O-53 review), и ноль — это «резать нечего», а не «не спросили».
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

// ─── ГОЛОВА ЦЕПОЧКИ ЗАМЕН (O-53 review) ───

// replaceChain — кадры цепочки 7 → 12 → 19 (19 — голова) и загрузчик по ним, считающий обращения.
func replaceChain() (map[int]DesignPicture, func(int) (DesignPicture, error), *int) {
	link := func(id, next int) DesignPicture {
		p := DesignPicture{Id: id, TechCardId: replaceProbeCard, MediaId: replaceProbeMedia + id}
		if next > 0 {
			p.ReplacedBy = sql.NullInt32{Int32: int32(next), Valid: true}
		}
		return p
	}
	chain := map[int]DesignPicture{7: link(7, 12), 12: link(12, 19), 19: link(19, 0)}
	calls := 0
	load := func(id int) (DesignPicture, error) {
		calls++
		p, ok := chain[id]
		if !ok {
			return DesignPicture{}, fmt.Errorf("%w: design picture %d", ErrDesignNotFound, id)
		}
		return p, nil
	}
	return chain, load, &calls
}

// ГОЛОВА — ПОСЛЕДНЕЕ ЗВЕНО, А НЕ ПЕРВАЯ ЗАМЕНА.
//
// МУТАЦИИ: вернуть replaced_by названного кадра вместо обхода (голова 12 вместо 19 — клиент открыл
// бы промежуточную правку, которую уже перезаписали); не читать незаменённый кадр как голову самого
// себя (лишнее чтение или ошибка на пустом месте).
func TestDesignReplacementHeadFollowsTheChainToItsEnd(t *testing.T) {
	chain, load, calls := replaceChain()

	head, err := DesignReplacementHead(chain[7], load)
	require.NoError(t, err)
	require.Equal(t, 19, head.Id, "голова — звено с replaced_by = NULL, а не первая замена")
	require.Equal(t, 2, *calls, "по одному чтению на звено")

	*calls = 0
	head, err = DesignReplacementHead(chain[19], load)
	require.NoError(t, err)
	require.Equal(t, 19, head.Id, "незаменённый кадр — сам себе голова")
	require.Zero(t, *calls, "и читать для этого нечего")
}

// ПОРЧА ЦЕПОЧКИ — НЕ ОТКАЗ И НЕ not_found.
//
// МУТАЦИИ: убрать проверку «следующий новее» (кольцо 7 → 12 → 7 крутится до потолка вместо того,
// чтобы быть названным на первом же шаге назад); завернуть ненайденное звено через %w (клиенту ушло
// бы not_found про кадр, которого он не называл); потерять %w у ошибки чтения прочего рода (дедлок
// 1213 перестал бы повторяться транзакцией).
func TestDesignReplacementHeadNamesACorruptChain(t *testing.T) {
	t.Run("ссылка назад", func(t *testing.T) {
		chain, load, calls := replaceChain()
		loop := chain[19]
		loop.ReplacedBy = sql.NullInt32{Int32: 7, Valid: true}
		chain[19] = loop
		_, err := DesignReplacementHead(chain[7], load)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrDesignAlreadyReplaced)
		require.NotErrorIs(t, err, ErrDesignNotFound)
		require.Equal(t, 2, *calls, "шаг назад называется сразу, а не на потолке")
	})
	t.Run("звено не существует", func(t *testing.T) {
		chain, load, _ := replaceChain()
		delete(chain, 19)
		_, err := DesignReplacementHead(chain[7], load)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrDesignNotFound, "not_found соврал бы о кадре, которого клиент не называл")
		require.Contains(t, err.Error(), "19")
	})
	t.Run("чтение упало", func(t *testing.T) {
		chain, _, _ := replaceChain()
		transient := errors.New("Error 1213: Deadlock found when trying to get lock")
		_, err := DesignReplacementHead(chain[7], func(int) (DesignPicture, error) { return DesignPicture{}, transient })
		require.ErrorIs(t, err, transient, "ошибка чтения обязана остаться видимой для повтора транзакции")
	})
	t.Run("потолок", func(t *testing.T) {
		// Бесконечная, но честно растущая цепочка: каждый кадр заменён следующим по id.
		endless := func(id int) (DesignPicture, error) {
			return DesignPicture{Id: id, ReplacedBy: sql.NullInt32{Int32: int32(id + 1), Valid: true}}, nil
		}
		start, _ := endless(1)
		_, err := DesignReplacementHead(start, endless)
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("link %d", DesignReplacementChainMax))
	})
}

// ОТКАЗ already_replaced НЕСЁТ ГОЛОВУ — и остаётся already_replaced для всех, кто узнаёт его по
// сентинелу (таблица отказов хендлера, пробы стора).
//
// МУТАЦИИ: убрать Unwrap (errors.Is перестаёт узнавать отказ — хендлер отдал бы Internal); класть в
// HeadPictureId названный кадр или первую замену; вернуть отказ без головы, когда обход упал.
func TestDesignAlreadyReplacedCarriesTheHead(t *testing.T) {
	chain, load, _ := replaceChain()
	err := DesignAlreadyReplaced(chain[7], load)
	require.ErrorIs(t, err, ErrDesignAlreadyReplaced)
	var replaced *DesignReplacedError
	require.ErrorAs(t, fmt.Errorf("store: %w", err), &replaced, "голова переживает заворачивание")
	require.Equal(t, 7, replaced.PictureId)
	require.Equal(t, 19, replaced.HeadPictureId)
	require.Contains(t, err.Error(), "already_replaced")
	require.Contains(t, err.Error(), "picture 7")
	require.Contains(t, err.Error(), "picture 19")

	delete(chain, 19)
	err = DesignAlreadyReplaced(chain[7], load)
	require.Error(t, err)
	require.False(t, errors.As(err, &replaced), "отказ без головы нарушил бы обещание head_picture_id")
}
