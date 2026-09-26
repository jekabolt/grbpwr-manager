package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ПРОБЫ ПРАВИЛА «ПЕРЕЗАПИСАТЬ» (0369, O-53) — решения без базы.
//
// Чтения (кадр, кадры его карточки) делает стор в транзакции флэттена, и они проверяются живыми
// пробами internal/store/design/replace_db_test.go (одноразовый контейнер, CI=1). Здесь — то, что от
// базы не зависит: какой отказ звучит при каком состоянии, В КАКОМ ПОРЯДКЕ, и стоит ли кусок листа.

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
// Сюда же — кропы, которые в счёт не идут: вызывающий считает только стоящие (DesignStandingPieces),
// и ноль — это «ни один кусок не стоит», а не «не спросили».
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

// ─── СТОИТ ЛИ КУСОК: ВСЯ ВЕТКА (O-53 review, раунд 3) ───

// standingSheet — лист, чьи куски судятся: 7, на виду, не заменён.
const standingSheet = 7

func shownNode(id int) DesignBranchNode {
	return DesignBranchNode{Id: id, TechCardId: replaceProbeCard}
}

func hiddenNode(id int) DesignBranchNode {
	return DesignBranchNode{Id: id, TechCardId: replaceProbeCard, HiddenAt: sql.NullTime{Valid: true}}
}

// onCard — n лежит на карточке card: порча, которую ни один писатель не делает (раунд 4).
func onCard(n DesignBranchNode, card int) DesignBranchNode {
	n.TechCardId = card
	return n
}

// cutFrom — n отрезан от parent: кроп.
func cutFrom(n DesignBranchNode, parent int) DesignBranchNode {
	n.DerivedFrom = sql.NullInt32{Int32: int32(parent), Valid: true}
	n.Derivation = DesignDerivationCrop
	return n
}

// editOf — n — правка parent: флэттен. «На месте» его делает replacedBy у parent, без него — «рядом».
func editOf(n DesignBranchNode, parent int) DesignBranchNode {
	n.DerivedFrom = sql.NullInt32{Int32: int32(parent), Valid: true}
	n.Derivation = DesignDerivationFlatten
	return n
}

// legacyOf — n — ребёнок parent с пустым глаголом: строка, которую бэкфилл 0359 не классифицировал.
func legacyOf(n DesignBranchNode, parent int) DesignBranchNode {
	n.DerivedFrom = sql.NullInt32{Int32: int32(parent), Valid: true}
	return n
}

// replacedBy — место n заняла правка next.
func replacedBy(n DesignBranchNode, next int) DesignBranchNode {
	n.ReplacedBy = sql.NullInt32{Int32: int32(next), Valid: true}
	return n
}

// standingCard — все кадры карточки: лист 7, ветка случая и ШУМ, который не держит лист ни в одном
// случае. В шуме на виду стоят правка листа «рядом» (8), легаси-ребёнок листа (9) и кусок ДРУГОГО
// листа (4 от 3), а спрятанный кадр 2 ни к чему не привязан. Поэтому каждый случай «отпускает»
// заодно проверяет, что обход не берёт куском правку, легаси или чужой кусок.
func standingCard(branch ...DesignBranchNode) []DesignBranchNode {
	nodes := []DesignBranchNode{
		hiddenNode(2),
		shownNode(3),
		cutFrom(shownNode(4), 3),
		shownNode(standingSheet),
		editOf(shownNode(8), standingSheet),
		legacyOf(shownNode(9), standingSheet),
	}
	return append(nodes, branch...)
}

// standingCase — один кусок 10 листа 7 и его ветка; holds — держит ли он лист.
type standingCase struct {
	name   string
	branch []DesignBranchNode
	holds  bool
}

// standingCases — таблица случаев «стоит ли кусок». Одна на две пробы: обход по всей карточке
// (TestDesignStandingPiecesJudgeTheWholeBranch) и обход по тому, что прочитало чтение ветки
// (TestDesignLoadBranchReadsWhatTheWalkNeeds), — ответы обязаны совпасть случай в случай.
func standingCases() []standingCase {
	return []standingCase{
		{"кусок на виду", []DesignBranchNode{
			cutFrom(shownNode(10), standingSheet),
		}, true},
		{"кусок спрятан, под ним ничего", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
		}, false},
		{"кусок спрятан, его правка на виду — устаревшая вкладка раунда 2", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(shownNode(11), 10),
		}, true},
		{"кусок на виду, его правка спрятана — сам кусок стоит", []DesignBranchNode{
			replacedBy(cutFrom(shownNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
		}, true},
		{"сценарий Codex: C на виду, его правка E спрятана, F отрезан от E и на виду", []DesignBranchNode{
			replacedBy(cutFrom(shownNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			cutFrom(shownNode(12), 11),
		}, true},
		{"спрятаны C и E, F отрезан от спрятанной головы и на виду", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			cutFrom(shownNode(12), 11),
		}, true},
		{"спрятано всё: C, E и F", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			cutFrom(hiddenNode(12), 11),
		}, false},
		{"кусок куска на виду", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			cutFrom(shownNode(11), 10),
		}, true},
		{"кусок куска спрятан", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			cutFrom(hiddenNode(11), 10),
		}, false},
		{"правка куска рядом не держит", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			editOf(shownNode(11), 10),
		}, false},
		{"легаси-ребёнок куска не держит", []DesignBranchNode{
			cutFrom(hiddenNode(10), standingSheet),
			legacyOf(shownNode(11), 10),
		}, false},
		{"две правки: голова спрятана, промежуточная на виду", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			replacedBy(editOf(shownNode(11), 10), 12),
			editOf(hiddenNode(12), 11),
		}, true},
		{"две правки: первая спрятана, голова на виду", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			replacedBy(editOf(hiddenNode(11), 10), 12),
			editOf(shownNode(12), 11),
		}, true},
		{"длинная ветка: замена, разрез, замена, разрез — на виду только последний", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			replacedBy(cutFrom(hiddenNode(12), 11), 13),
			editOf(hiddenNode(13), 12),
			cutFrom(shownNode(14), 13),
		}, true},
		{"длинная ветка спрятана до конца", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			editOf(hiddenNode(11), 10),
			replacedBy(cutFrom(hiddenNode(12), 11), 13),
			editOf(hiddenNode(13), 12),
			cutFrom(hiddenNode(14), 13),
		}, false},
	}
}

// ЛИСТ ДЕРЖИТ КУСОК, ПОКА НА ЭКРАНЕ ХОТЬ ЧТО-ТО ИЗ ЕГО ВЕТКИ.
//
// Каждый случай — ОДИН кусок 10 и его ветка, и ответ сверяется по куску, а не по счёту: общий счёт
// скрыл бы мутанта, у которого два неверных ответа складываются в верную сумму.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ:
//   - судить кусок по его строке — «спрятанный кусок, правка на виду», «спрятаны C и E, F на виду»,
//     «кусок куска», длинная ветка;
//   - судить по голове цепочки замен (раунд 2) — «кусок на виду, правка спрятана», сценарий Codex,
//     «спрятаны C и E», «кусок куска», «голова спрятана, промежуточная правка на виду»;
//   - выбросить ребро разреза (идти только по replaced_by) — «спрятаны C и E», «кусок куска», длинная
//     ветка;
//   - брать куском любого ребёнка, а не кроп, — «правка куска рядом», «легаси-ребёнок куска» и шум
//     standingCard в каждом отпускающем случае.
func TestDesignStandingPiecesJudgeTheWholeBranch(t *testing.T) {
	for _, tc := range standingCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DesignStandingPieces(standingSheet, standingCard(tc.branch...))
			require.NoError(t, err)
			if tc.holds {
				require.Equal(t, []int{10}, got, "кусок 10 стоит, и держит лист только он")
			} else {
				require.Empty(t, got, "от куска 10 на экране не осталось ничего")
			}
		})
	}
}

// НЕСКОЛЬКО КУСКОВ СУДЯТСЯ КАЖДЫЙ СВОЕЙ ВЕТКОЙ, И ПОРЯДОК ВХОДА НЕ ЗНАЧИТ НИЧЕГО.
//
// Ответ — ровно стоящие куски по возрастанию id, а не «первый стоящий» и не счёт.
func TestDesignStandingPiecesNameEveryStandingPiece(t *testing.T) {
	branch := []DesignBranchNode{
		cutFrom(shownNode(30), standingSheet),
		editOf(shownNode(21), 20),
		cutFrom(hiddenNode(10), standingSheet),
		replacedBy(cutFrom(hiddenNode(20), standingSheet), 21),
	}
	got, err := DesignStandingPieces(standingSheet, standingCard(branch...))
	require.NoError(t, err)
	require.Equal(t, []int{20, 30}, got)

	nodes := standingCard(branch...)
	for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}
	got, err = DesignStandingPieces(standingSheet, nodes)
	require.NoError(t, err)
	require.Equal(t, []int{20, 30}, got, "порядок строк SELECT ответа не меняет")
}

// ПОРЧА — ОШИБКА, А НЕ ОТВЕТ «ОТПУСКАЕТ».
//
// Ни одна ошибка обхода не несёт сентинела полосы: клиенту Internal, а не cut_sheet и не not_found про
// кадр, которого он не называл.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ:
//   - проглотить потерянный кадр (считать его спрятанным и пустым) — «замена ведёт мимо карточки»
//     отпускает лист;
//   - снять сверку «ребро ведёт к новому кадру» — «ссылка на старый спрятанный кадр» уходит в чужой
//     спрятанный кадр и отпускает лист;
//   - снять visited — «кадр достигнут дважды» проходится дважды и отпускает лист (все рёбра там идут
//     вперёд, и сверка порядка его не видит);
//   - снять сверку карточки (раунд 4) — «видимый кусок другой карточки» держит лист молча, а
//     «замена на кадр другой карточки» отпускает его.
func TestDesignStandingPiecesRefuseABrokenBranch(t *testing.T) {
	for _, tc := range brokenBranchCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DesignStandingPieces(standingSheet, standingCard(tc.branch...))
			require.Error(t, err, "порча не выдаётся за ответ (%v)", got)
			require.Contains(t, err.Error(), tc.names)
			for _, sentinel := range []error{ErrDesignCutSheet, ErrDesignNotFound, ErrDesignInvalidArgument, ErrDesignAlreadyReplaced} {
				require.NotErrorIs(t, err, sentinel, "порча — Internal, а не отказ полосы")
			}
		})
	}

	t.Run("лист не прочитан", func(t *testing.T) {
		_, err := DesignStandingPieces(standingSheet, []DesignBranchNode{cutFrom(shownNode(10), standingSheet)})
		require.Error(t, err)
	})

	// Обратная сторона: ВИДИМЫЙ кусок держит лист, и порча за ним ответа не меняет — отказ закрывает
	// дверь и без неё. Иначе потерянная ссылка превращала бы честный cut_sheet в Internal.
	t.Run("порча за видимым кадром", func(t *testing.T) {
		got, err := DesignStandingPieces(standingSheet, standingCard(
			replacedBy(cutFrom(shownNode(10), standingSheet), 99),
		))
		require.NoError(t, err)
		require.Equal(t, []int{10}, got)
	})
}

// brokenBranchCase — ветка куска 10 с порчей; names — что обязана назвать ошибка.
type brokenBranchCase struct {
	name   string
	branch []DesignBranchNode
	names  string
}

// brokenBranchCases — порча ветки. Одна таблица на обход по всей карточке и на обход по чтению ветки.
func brokenBranchCases() []brokenBranchCase {
	return []brokenBranchCase{
		{"замена ведёт мимо карточки", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 99),
		}, "picture 99"},
		{"замена ведёт назад, на сам лист", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), standingSheet),
		}, "picture 7"},
		{"ссылка на старый спрятанный кадр", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 2),
		}, "picture 2"},
		{"цикл замен", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			replacedBy(editOf(hiddenNode(11), 10), 10),
		}, "picture 11"},
		{"кадр достигнут дважды", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 12),
			cutFrom(hiddenNode(11), 10),
			cutFrom(hiddenNode(12), 11),
		}, "picture 12 is reached twice"},
		// Раунд 4: кроп с derived_from = лист на ЧУЖОЙ карточке. Скан по карточке его не видел, и
		// видимый кусок молча не держал лист; теперь он прочитан и назван.
		{"видимый кусок другой карточки", []DesignBranchNode{
			onCard(cutFrom(shownNode(10), standingSheet), replaceProbeCard+1),
		}, "belongs to tech card 42"},
		{"замена на кадр другой карточки", []DesignBranchNode{
			replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
			onCard(editOf(hiddenNode(11), 10), replaceProbeCard+1),
		}, "picture 11, reached from picture 10, belongs to tech card 42"},
	}
}

// standingLine — кусок first листа 7 и n-1 спрятанных звеньев под ним, замена и разрез по очереди;
// всё спрятано, так что обход обязан пройти ветку до конца.
func standingLine(first, n int) []DesignBranchNode {
	line := []DesignBranchNode{cutFrom(hiddenNode(first), standingSheet)}
	for i := 1; i < n; i++ {
		id, prev := first+i, first+i-1
		if i%2 == 1 {
			line[i-1] = replacedBy(line[i-1], id)
			line = append(line, editOf(hiddenNode(id), prev))
		} else {
			line = append(line, cutFrom(hiddenNode(id), prev))
		}
	}
	return line
}

// ПОТОЛОК — ОБЩИЙ НА ЗАПРОС, И ЛИСТ — ПЕРВЫЙ ИЗ НЕГО.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: потолок на ветку вместо запроса (два куска по половине потолка проходят);
// сдвиг границы на единицу (ровно потолок отказывается либо потолок плюс один проходит).
func TestDesignStandingPiecesStopAtTheTotalCeiling(t *testing.T) {
	sheet := shownNode(standingSheet)
	t.Run("ровно потолок", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax-1)...)
		got, err := DesignStandingPieces(standingSheet, nodes)
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("на один кадр больше", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax)...)
		_, err := DesignStandingPieces(standingSheet, nodes)
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("more than %d pictures", DesignStandingNodesMax))
		require.NotErrorIs(t, err, ErrDesignCutSheet)
	})
	t.Run("два куска, каждый под потолком, вместе над ним", func(t *testing.T) {
		half := DesignStandingNodesMax / 2
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, half)...)
		nodes = append(nodes, standingLine(10+half, half)...)
		_, err := DesignStandingPieces(standingSheet, nodes)
		require.Error(t, err, "лист и две ветки по %d — это %d кадров", half, 1+2*half)
		require.Contains(t, err.Error(), fmt.Sprintf("more than %d pictures", DesignStandingNodesMax))
	})
}

// КОЛОНКИ ЗАПРОСА — РОВНО ПОЛЯ УЗЛА, В ТОМ ЖЕ ПОРЯДКЕ.
//
// МУТАЦИИ: выбросить колонку из DesignBranchColumns (sqlx молча оставил бы поле нулём — без
// derivation, derived_from или replaced_by обход не видит рёбер и отпускает лист); завести поле, не
// выбранное запросом.
func TestDesignBranchColumnsAreTheNodeFields(t *testing.T) {
	typ := reflect.TypeOf(DesignBranchNode{})
	tags := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("db")
		require.NotEmpty(t, tag, "поле %s без колонки", typ.Field(i).Name)
		tags = append(tags, tag)
	}
	require.Equal(t, strings.Join(tags, ", "), DesignBranchColumns)
}

// ЛИСТ СТАНОВИТСЯ УЗЛОМ ОБХОДА ЦЕЛИКОМ: каждое поле узла взято из одноимённого поля кадра.
//
// МУТАЦИЯ: забыть поле в DesignBranchNodeOf — прежде всего tech_card_id (лист с карточкой 0 сделал бы
// чужим каждый кадр своей ветки) или replaced_by.
func TestDesignBranchNodeOfCopiesEveryField(t *testing.T) {
	p := DesignPicture{
		Id: 7, TechCardId: replaceProbeCard, Derivation: DesignDerivationCrop,
		HiddenAt:    sql.NullTime{Time: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Valid: true},
		ReplacedBy:  sql.NullInt32{Int32: 12, Valid: true},
		DerivedFrom: sql.NullInt32{Int32: 3, Valid: true},
	}
	n := DesignBranchNodeOf(p)
	nv, pv := reflect.ValueOf(n), reflect.ValueOf(p)
	for i := 0; i < nv.NumField(); i++ {
		name := nv.Type().Field(i).Name
		src := pv.FieldByName(name)
		require.True(t, src.IsValid(), "у кадра нет поля %s", name)
		require.False(t, src.IsZero(), "проба обязана заполнить %s кадра", name)
		require.Equal(t, src.Interface(), nv.Field(i).Interface(), "поле %s", name)
	}
}

// СПРЯТАННЫЙ КАДР НЕ РЕЖЕТСЯ — СВОИМ СЛОВОМ, А НЕ hidden_plate.
//
// МУТАЦИИ: судить не по hidden_at (спрятанный проходит); отдать сентинел постановки в слот (клиент
// показал бы «плиту нельзя поставить» на жест разреза).
func TestDesignSplitHiddenRefusal(t *testing.T) {
	shown := replaceProbeOriginal()
	require.NoError(t, DesignSplitHiddenRefusal(shown))

	hidden := replaceProbeOriginal()
	hidden.HiddenAt = sql.NullTime{Valid: true}
	err := DesignSplitHiddenRefusal(hidden)
	require.ErrorIs(t, err, ErrDesignHiddenPicture)
	require.NotErrorIs(t, err, ErrDesignHiddenPlate)
	require.Contains(t, err.Error(), "hidden_picture")
	require.Contains(t, err.Error(), "picture 7")
}

// ─── ЧТЕНИЕ ВЕТКИ УРОВНЯМИ (O-53 review, раунд 4) ───

// branchCall — один вызов чтения: какое чтение, какие id, какой limit.
type branchCall struct {
	read  string // "id" | "crops"
	ids   []int
	limit int
}

// branchTable — design_picture в памяти, отвечающая на DesignBranchReads так же, как SQL стора: БЕЗ
// предиката карточки, по возрастанию id, не больше limit строк. calls — каждый вызов по порядку.
type branchTable struct {
	rows  map[int]DesignBranchNode
	crops map[int][]DesignBranchNode // derived_from → кропы
	calls []branchCall
	fail  error
}

func newBranchTable(nodes ...DesignBranchNode) *branchTable {
	b := &branchTable{rows: map[int]DesignBranchNode{}, crops: map[int][]DesignBranchNode{}}
	for _, n := range nodes {
		b.rows[n.Id] = n
		if n.DerivedFrom.Valid && n.Derivation == DesignDerivationCrop {
			parent := int(n.DerivedFrom.Int32)
			b.crops[parent] = append(b.crops[parent], n)
		}
	}
	return b
}

func (b *branchTable) reads() DesignBranchReads {
	answer := func(read string, ids []int, limit int, rows []DesignBranchNode) ([]DesignBranchNode, error) {
		b.calls = append(b.calls, branchCall{read: read, ids: append([]int(nil), ids...), limit: limit})
		if b.fail != nil {
			return nil, b.fail
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Id < rows[j].Id })
		if len(rows) > limit {
			rows = rows[:limit]
		}
		return rows, nil
	}
	return DesignBranchReads{
		ByID: func(ids []int, limit int) ([]DesignBranchNode, error) {
			var rows []DesignBranchNode
			for _, id := range ids {
				if n, ok := b.rows[id]; ok {
					rows = append(rows, n)
				}
			}
			return answer("id", ids, limit, rows)
		},
		CropsOf: func(parents []int, limit int) ([]DesignBranchNode, error) {
			var rows []DesignBranchNode
			for _, p := range parents {
				rows = append(rows, b.crops[p]...)
			}
			return answer("crops", parents, limit, rows)
		},
	}
}

// loadedIDs — id прочитанного набора, по возрастанию.
func loadedIDs(nodes []DesignBranchNode) []int {
	ids := make([]int, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.Id)
	}
	sort.Ints(ids)
	return ids
}

// ЧТЕНИЕ ВЕТКИ ДАЁТ ОБХОДУ РОВНО ТО, ЧТО ЕМУ НУЖНО — И НИЧЕГО ИЗ ШУМА КАРТОЧКИ.
//
// Каждый случай обеих таблиц проходит дважды: обход по всей карточке и обход по набору, который
// собрало чтение из той же карточки. Ответы обязаны совпасть — вердикт в случаях «стоит ли кусок» и
// названная порча в случаях порчи, — а шум карточки (чужой лист 3 и его кусок 4, ничей спрятанный 2,
// правка листа «рядом» 8, легаси-ребёнок листа 9) не читается ни разу, кроме случая, где на кадр 2
// ведёт порченая замена.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: остановиться после первого уровня или не читать кропы глубже листа
// («кусок куска на виду» отпускает лист — ребро разреза пропадает МОЛЧА, это единственное ребро,
// потерю которого обход не видит); не читать замены (случаи со стоящей правкой отпускают лист или
// падают потерянным кадром); читать детей без глагола (в набор попадают 8 и 9).
func TestDesignLoadBranchReadsWhatTheWalkNeeds(t *testing.T) {
	sheet := shownNode(standingSheet)
	noise := []int{3, 4, 8, 9}
	for _, tc := range standingCases() {
		t.Run(tc.name, func(t *testing.T) {
			card := standingCard(tc.branch...)
			loaded, err := DesignLoadBranch(sheet, newBranchTable(card...).reads())
			require.NoError(t, err)
			require.Equal(t, standingSheet, loaded[0].Id, "лист — первый в наборе")
			ids := loadedIDs(loaded)
			for _, id := range append(noise, 2) {
				require.NotContains(t, ids, id, "шум карточки не читается")
			}
			whole, err := DesignStandingPieces(standingSheet, card)
			require.NoError(t, err)
			got, err := DesignStandingPieces(standingSheet, loaded)
			require.NoError(t, err)
			require.Equal(t, whole, got, "обход по прочитанному отвечает так же, как по всей карточке")
		})
	}
	for _, tc := range brokenBranchCases() {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := DesignLoadBranch(sheet, newBranchTable(standingCard(tc.branch...)...).reads())
			require.NoError(t, err, "чтение порчу не судит — оно её дочитывает и останавливается")
			for _, id := range noise {
				require.NotContains(t, loadedIDs(loaded), id)
			}
			_, err = DesignStandingPieces(standingSheet, loaded)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.names)
		})
	}

	// Сценарий Codex, прочитанный уровнями: C (10) и его замена E (11) спрятаны, F (12) отрезан от E.
	codex := standingCard(
		replacedBy(cutFrom(hiddenNode(10), standingSheet), 11),
		editOf(hiddenNode(11), 10),
		cutFrom(shownNode(12), 11),
	)
	table := newBranchTable(codex...)
	loaded, err := DesignLoadBranch(sheet, table.reads())
	require.NoError(t, err)
	require.Equal(t, []int{7, 10, 11, 12}, loadedIDs(loaded))
	require.Equal(t, []branchCall{
		{read: "crops", ids: []int{7}, limit: DesignStandingNodesMax},
		{read: "id", ids: []int{11}, limit: DesignStandingNodesMax - 1},
		{read: "crops", ids: []int{10}, limit: DesignStandingNodesMax - 2},
		{read: "crops", ids: []int{11}, limit: DesignStandingNodesMax - 2},
		{read: "crops", ids: []int{12}, limit: DesignStandingNodesMax - 3},
	}, table.calls, "уровень за уровнем: кропы листа, затем замены и кропы каждого нового уровня")
}

// ШИРОКИЙ УРОВЕНЬ ЧИТАЕТСЯ КУСКАМИ ПО DesignBranchChunk id.
//
// Лист с 2·DesignBranchChunk+1 спрятанными кусками, у каждого — спрятанная замена: на первом уровне
// столько же целей замены и родителей кропов, на втором — родителей кропов. Каждый вызов называет не
// больше DesignBranchChunk id, id идут по возрастанию, и вместе вызовы одного чтения покрывают
// уровень целиком и без повторов. Сам кусок не шире eq_range_index_dive_limit MySQL по умолчанию —
// см. DesignBranchChunk.
//
// МУТАЦИИ: снять разбиение (один вызов на весь уровень); поднять кусок выше 200.
func TestDesignLoadBranchReadsInBoundedChunks(t *testing.T) {
	require.LessOrEqual(t, DesignBranchChunk, 200, "сверх eq_range_index_dive_limit оценка IN идёт по статистике")
	pieces := 2*DesignBranchChunk + 1
	nodes := []DesignBranchNode{shownNode(standingSheet)}
	var editIDs []int
	for i := 0; i < pieces; i++ {
		piece, edit := 10+2*i, 11+2*i
		nodes = append(nodes,
			replacedBy(cutFrom(hiddenNode(piece), standingSheet), edit),
			editOf(hiddenNode(edit), piece))
		editIDs = append(editIDs, edit)
	}
	table := newBranchTable(nodes...)
	loaded, err := DesignLoadBranch(nodes[0], table.reads())
	require.NoError(t, err)
	require.Len(t, loaded, 1+2*pieces)

	var sizes []string
	var replaced []int
	for _, c := range table.calls {
		require.LessOrEqual(t, len(c.ids), DesignBranchChunk, "%s: %d id в одном запросе", c.read, len(c.ids))
		require.True(t, sort.IntsAreSorted(c.ids))
		sizes = append(sizes, fmt.Sprintf("%s:%d", c.read, len(c.ids)))
		if c.read == "id" {
			replaced = append(replaced, c.ids...)
		}
	}
	full := func(read string) []string {
		return []string{
			fmt.Sprintf("%s:%d", read, DesignBranchChunk), fmt.Sprintf("%s:%d", read, DesignBranchChunk), read + ":1",
		}
	}
	want := []string{"crops:1"}
	want = append(want, full("id")...)
	want = append(want, full("crops")...)
	want = append(want, full("crops")...)
	require.Equal(t, want, sizes)
	require.Equal(t, editIDs, replaced, "цели замен прочитаны все, по разу")

	got, err := DesignStandingPieces(standingSheet, loaded)
	require.NoError(t, err)
	require.Empty(t, got)
}

// ОДИН ПОТОЛОК НА ВСЁ ЧТЕНИЕ, ЛИСТ ВКЛЮЧИТЕЛЬНО, И ОТКАЗ — НА ТОМ ЖЕ ШАГЕ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: считать потолок на уровень (линия из одного кадра на уровень не упирается
// ни в один уровень и дочитывается до конца); не сверять ответ с оставшимся местом (лишнее
// принимается); сдвиг границы на единицу («ровно потолок» отказывает либо «на один больше» проходит);
// читать дальше отказа (широкий уровень делает больше одного вызова).
func TestDesignLoadBranchSharesOneCap(t *testing.T) {
	sheet := shownNode(standingSheet)
	t.Run("ровно потолок", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax-1)...)
		loaded, err := DesignLoadBranch(sheet, newBranchTable(nodes...).reads())
		require.NoError(t, err)
		require.Len(t, loaded, DesignStandingNodesMax)
	})
	t.Run("на один кадр больше — по кадру на уровень", func(t *testing.T) {
		nodes := append([]DesignBranchNode{sheet}, standingLine(10, DesignStandingNodesMax)...)
		table := newBranchTable(nodes...)
		_, err := DesignLoadBranch(sheet, table.reads())
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("more than %d pictures", DesignStandingNodesMax))
		require.NotErrorIs(t, err, ErrDesignCutSheet)
		last := table.calls[len(table.calls)-1]
		require.Equal(t, 1, last.limit, "последнее чтение просило одну строку сверх потолка — и получило её")
	})
	t.Run("широкий уровень над потолком — один вызов и отказ", func(t *testing.T) {
		nodes := []DesignBranchNode{sheet}
		for i := 0; i < DesignStandingNodesMax; i++ {
			nodes = append(nodes, cutFrom(hiddenNode(10+i), standingSheet))
		}
		table := newBranchTable(nodes...)
		_, err := DesignLoadBranch(sheet, table.reads())
		require.Error(t, err)
		require.Len(t, table.calls, 1, "дальше отказа не читается")
		require.Equal(t, DesignStandingNodesMax, table.calls[0].limit, "место под потолком плюс одна строка")
	})
}

// ЗАМЕНА САМОГО ЛИСТА НЕ ЧИТАЕТСЯ: в правило она не входит, а заменённый лист отказан раньше.
//
// МУТАЦИЯ: начинать уровни с листа как с обычного кадра — лишнее чтение 50 и её ветки под замком.
func TestDesignLoadBranchSkipsTheSheetsOwnReplacement(t *testing.T) {
	sheet := replacedBy(shownNode(standingSheet), 50)
	table := newBranchTable(sheet, editOf(shownNode(50), standingSheet), cutFrom(hiddenNode(10), standingSheet))
	loaded, err := DesignLoadBranch(sheet, table.reads())
	require.NoError(t, err)
	require.Equal(t, []int{7, 10}, loadedIDs(loaded))
	for _, c := range table.calls {
		require.NotContains(t, c.ids, 50)
	}
}

// ОШИБКА ЧТЕНИЯ ВИДНА ПОВТОРУ ТРАНЗАКЦИИ.
//
// МУТАЦИЯ: завернуть ошибку чтения без %w — дедлок 1213 перестал бы повторяться.
func TestDesignLoadBranchKeepsTheReadError(t *testing.T) {
	transient := errors.New("Error 1213: Deadlock found when trying to get lock")
	table := newBranchTable(shownNode(standingSheet))
	table.fail = transient
	_, err := DesignLoadBranch(shownNode(standingSheet), table.reads())
	require.ErrorIs(t, err, transient)
	require.Contains(t, err.Error(), "design picture 7")
}

// ─── ПОВТОР ПО КЛЮЧУ ОТВЕЧАЕТ ТОЛЬКО ТОМУ ЖЕ ЖЕСТУ (0370/0371, O-53 review) ───

// replayPrior — кадр, поданный первой попыткой: флэттен слоя 5 на ревизии 4.
func replayPrior() DesignPicture {
	return DesignPicture{
		Id: 12, TechCardId: replaceProbeCard, Derivation: DesignDerivationFlatten,
		DerivedFrom:   sql.NullInt32{Int32: 7, Valid: true},
		SourceLayerId: sql.NullInt32{Int32: 5, Valid: true}, LayerRev: 4,
	}
}

func replayReq(layer, rev, replace int) DesignEditLayerFlatten {
	return DesignEditLayerFlatten{
		TechCardId: replaceProbeCard, LayerId: layer, ExpectedRev: rev, MediaId: 901,
		ReplacePictureId: replace, ClientRequestId: "k-1",
	}
}

// ПОВТОР ТОГО ЖЕ ЖЕСТА ПРОХОДИТ — В ОБОИХ РЕЖИМАХ И У СЛОЯ С ЧИСТОГО ЛИСТА.
//
// Положительный контроль: без него пробы отказов ниже зеленели бы и на правиле, отказывающем всегда.
// media_id запроса намеренно не совпадает ни с чем — ключ это жест, а не байты.
func TestDesignFlattenReplayOfTheSameGestureIsAReplay(t *testing.T) {
	require.NoError(t, DesignFlattenReplayRefusal(replayReq(5, 4, 7), "k-1", replayPrior(), 7), "перезапись")
	require.NoError(t, DesignFlattenReplayRefusal(replayReq(5, 4, 0), "k-1", replayPrior(), 0), "рядом")

	root := replayPrior()
	root.DerivedFrom, root.Derivation = sql.NullInt32{}, DesignDerivationNone
	require.NoError(t, DesignFlattenReplayRefusal(replayReq(5, 4, 0), "k-1", root, 0),
		"слой с чистого листа: у кадра нет родителя, есть слой")
}

// КЛЮЧ, ПОТРАЧЕННЫЙ НА ДРУГОЙ ФЛЭТТЕН, — invalid_argument, И КАЖДОЕ РАСХОЖДЕНИЕ НАЗЫВАЕТ СЕБЯ.
//
// Главный случай раунда 2 — ДРУГОЙ СЛОЙ НА ТОЙ ЖЕ РЕВИЗИИ, в обоих режимах: раньше повтор «save as
// new» слоя L2 под ключом слоя L1 получал картинку L1 как свой успех.
//
// МУТАЦИИ: снять сверку слоя (оба подслучая «другой слой» проходят); читать отсутствие слоя как
// совпадение (кадр до 0371 отвечает любому слою); снять сверку ревизии, глагола или места.
func TestDesignFlattenReplayRefusesAKeySpentElsewhere(t *testing.T) {
	noLayer := replayPrior()
	noLayer.SourceLayerId = sql.NullInt32{}
	crop := replayPrior()
	crop.Derivation = DesignDerivationCrop

	for _, tc := range []struct {
		name  string
		req   DesignEditLayerFlatten
		prior DesignPicture
		took  int
		says  string
	}{
		{"другой слой, та же ревизия — рядом", replayReq(6, 4, 0), replayPrior(), 0, "from layer 5, not layer 6"},
		{"другой слой, та же ревизия — перезапись", replayReq(6, 4, 7), replayPrior(), 7, "from layer 5, not layer 6"},
		{"кадр без записанного слоя", replayReq(5, 4, 0), noLayer, 0, "records no layer"},
		{"кусок разреза", replayReq(5, 4, 0), crop, 0, "is a crop"},
		{"другая ревизия", replayReq(5, 5, 0), replayPrior(), 0, "layer rev 4, not 5"},
		{"подан рядом, просят на место", replayReq(5, 4, 7), replayPrior(), 0, "filed beside"},
		{"подан на место, просят рядом", replayReq(5, 4, 0), replayPrior(), 7, "this request files beside"},
		{"подан на место другого кадра", replayReq(5, 4, 8), replayPrior(), 7, "not of picture 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := DesignFlattenReplayRefusal(tc.req, "k-1", tc.prior, tc.took)
			require.ErrorIs(t, err, ErrDesignInvalidArgument)
			require.Contains(t, err.Error(), tc.says)
			require.Contains(t, err.Error(), `client_request_id "k-1" already filed picture 12`)
		})
	}
}
