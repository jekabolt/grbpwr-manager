package entity

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ЦЕПОЧКА ПРАВОК: UNDO / REDO (0387, T28 v2). Решения проверяются без базы; стор только читает звенья
// под замком и исполняет шаг.
//
// МУТАЦИИ (каждая краснит хотя бы один тест ниже):
//   - DesignEditChainCurrent не останавливается на undone_at (текущая = хвост) — Current/Undo/Controls;
//   - DesignSuccessorUndone читает hidden_at вместо undone_at — SpiedHiddenStillHolds;
//   - DesignUndoStep без ветки повтора — UndoReplay;
//   - DesignRedoStep не требует текущей версии (cur < 0 пропускается) — CutOffBranch.

func chainLink(id, next int, undone bool) DesignChainLink {
	l := DesignChainLink{Id: id, TechCardId: 7}
	if next > 0 {
		l.ReplacedBy = sql.NullInt32{Int32: int32(next), Valid: true}
	}
	if undone {
		l.UndoneAt = sql.NullTime{Time: time.Unix(1, 0), Valid: true}
	}
	return l
}

func chainIDs(chain []DesignChainLink) []int {
	out := make([]int, 0, len(chain))
	for _, l := range chain {
		out = append(out, l.Id)
	}
	return out
}

// 10 → 20 → 30 (30 отменён) и посторонний флэттен 40.
func chainProbe() []DesignChainLink {
	return []DesignChainLink{chainLink(30, 0, true), chainLink(10, 20, false), chainLink(40, 0, false), chainLink(20, 30, false)}
}

func TestDesignEditChainFromAnyLinkRootFirst(t *testing.T) {
	for _, id := range []int{10, 20, 30} {
		chain, err := DesignEditChain(chainProbe(), id)
		require.NoError(t, err)
		require.Equal(t, []int{10, 20, 30}, chainIDs(chain), "звено %d", id)
	}
	chain, err := DesignEditChain(chainProbe(), 40)
	require.NoError(t, err)
	require.Equal(t, []int{40}, chainIDs(chain))

	_, err = DesignEditChain(chainProbe(), 99)
	require.ErrorIs(t, err, ErrDesignNotFound)
	_, err = DesignEditChain([]DesignChainLink{chainLink(10, 20, false), chainLink(15, 20, false), chainLink(20, 0, false)}, 20)
	require.ErrorContains(t, err, "replaced by both")
	_, err = DesignEditChain([]DesignChainLink{chainLink(20, 10, false), chainLink(10, 0, false)}, 10)
	require.ErrorContains(t, err, "broken")
	_, err = DesignEditChain([]DesignChainLink{chainLink(10, 20, false)}, 10)
	require.ErrorContains(t, err, "broken", "обрыв: цель replaced_by не прочитана")
}

func TestDesignEditChainCurrentStopsBeforeTheFirstUndoneLink(t *testing.T) {
	chain, _ := DesignEditChain(chainProbe(), 10)
	require.Equal(t, 1, DesignEditChainCurrent(chain), "текущая — 20, не отменённый хвост 30")
	require.Equal(t, 2, DesignEditChainCurrent([]DesignChainLink{chainLink(1, 2, false), chainLink(2, 3, false), chainLink(3, 0, false)}))
	require.Equal(t, -1, DesignEditChainCurrent([]DesignChainLink{chainLink(5, 0, true)}), "отрезанная ветка")
}

func TestDesignUndoStep(t *testing.T) {
	live := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 30, false), chainLink(30, 0, false)}
	step, err := DesignUndoStep(live, 30, 0)
	require.NoError(t, err)
	require.Equal(t, DesignEditStep{Mark: 30, From: 30, To: 20}, step)

	_, err = DesignUndoStep(live, 20, 0)
	require.ErrorIs(t, err, ErrDesignStaleChain, "CAS: текущая 30, клиент видел 20")
	_, err = DesignUndoStep(live, 30, 2)
	require.ErrorIs(t, err, ErrDesignLiveCropParent)

	root := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 0, true)}
	_, err = DesignUndoStep(root, 10, 0)
	require.ErrorIs(t, err, ErrDesignNothingToUndo, "текущая — оригинал")
}

func TestDesignUndoStepReplay(t *testing.T) {
	// После undo(30): 30 отменён, текущая 20. Повтор того же undo — успех без записи.
	after := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 30, false), chainLink(30, 0, true)}
	step, err := DesignUndoStep(after, 30, 0)
	require.NoError(t, err)
	require.Equal(t, DesignEditStep{Mark: 30, From: 30, To: 20, Replay: true}, step)
	// Но undo(30), когда отменено уже и 20, — не повтор: текущая 10.
	further := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 30, true), chainLink(30, 0, true)}
	_, err = DesignUndoStep(further, 30, 0)
	require.ErrorIs(t, err, ErrDesignStaleChain)
}

func TestDesignRedoStep(t *testing.T) {
	undone := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 30, true), chainLink(30, 0, true)}
	step, err := DesignRedoStep(undone, 10, 0)
	require.NoError(t, err)
	require.Equal(t, DesignEditStep{Mark: 20, From: 10, To: 20}, step, "redo снимает отмену только со следующего звена")

	_, err = DesignRedoStep(undone, 20, 0)
	require.ErrorIs(t, err, ErrDesignStaleChain)

	after := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 30, false), chainLink(30, 0, true)}
	step, err = DesignRedoStep(after, 10, 0)
	require.NoError(t, err)
	require.Equal(t, DesignEditStep{Mark: 20, From: 10, To: 20, Replay: true}, step, "повтор redo(10)")

	tail := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 0, false)}
	_, err = DesignRedoStep(tail, 20, 0)
	require.ErrorIs(t, err, ErrDesignNothingToRedo)
}

// Ветка, отрезанная новой правкой поверх отменённого преемника, — корень отменён, текущей версии нет:
// redo не поднимает её на верстак, undo тоже ничего не делает.
func TestDesignEditChainCutOffBranch(t *testing.T) {
	cut := []DesignChainLink{chainLink(30, 0, true)}
	_, err := DesignRedoStep(cut, 30, 0)
	require.ErrorIs(t, err, ErrDesignStaleChain)
	_, err = DesignUndoStep(cut, 30, 0)
	require.ErrorIs(t, err, ErrDesignStaleChain)
	require.Empty(t, DesignEditChainControls(cut))
}

func TestDesignEditChainControls(t *testing.T) {
	links := []DesignChainLink{
		// 10 → 20 → 30, 30 отменён: текущая 20, и undo, и redo.
		chainLink(10, 20, false), chainLink(20, 30, false), chainLink(30, 0, true),
		// 50 → 60, всё живо: текущая 60, только undo.
		chainLink(50, 60, false), chainLink(60, 0, false),
		// 70 → 80, 80 отменён: текущая 70 (корень), только redo.
		chainLink(70, 80, false), chainLink(80, 0, true),
		// флэттен рядом, не в цепочке; отрезанная ветка.
		chainLink(90, 0, false), chainLink(95, 0, true),
	}
	require.Equal(t, map[int]DesignEditControls{
		20: {CanUndo: true, CanRedo: true},
		60: {CanUndo: true},
		70: {CanRedo: true},
	}, DesignEditChainControls(links))
}

// Отмена — undone_at, а не hidden_at: спрятанный преемник место держит (already_replaced), отменённый
// освобождает. И наоборот: отменённый оригинал не перезаписывается (undone_picture) и не режется.
func TestDesignSpiedHiddenStillHoldsUndoneFrees(t *testing.T) {
	hidden := replaceProbeOriginal()
	hidden.HiddenAt = sql.NullTime{Time: time.Unix(1, 0), Valid: true}
	require.False(t, DesignSuccessorUndone(hidden), "спрятанный, но не отменённый преемник держит место")
	undone := replaceProbeOriginal()
	undone.UndoneAt = sql.NullTime{Time: time.Unix(1, 0), Valid: true}
	require.True(t, DesignSuccessorUndone(undone))

	replaced := replaceProbeOriginal()
	replaced.ReplacedBy = sql.NullInt32{Int32: 12, Valid: true}
	require.ErrorIs(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replaced,
		DesignReplaceFacts{SuccessorUndone: DesignSuccessorUndone(hidden)}), ErrDesignAlreadyReplaced)
	require.NoError(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), replaced,
		DesignReplaceFacts{SuccessorUndone: DesignSuccessorUndone(undone)}))

	require.ErrorIs(t, DesignReplaceRefusal(replaceProbeCard, replaceProbeBase(replaceProbeMedia), undone,
		DesignReplaceFacts{}), ErrDesignUndonePicture)
	require.ErrorIs(t, DesignSplitHiddenRefusal(undone), ErrDesignUndonePicture)
}

// Голова already_replaced — текущая версия: обход останавливается перед отменённым звеном.
func TestDesignReplacementHeadStopsBeforeAnUndoneLink(t *testing.T) {
	pics := map[int]DesignPicture{}
	for _, l := range []DesignChainLink{chainLink(10, 20, false), chainLink(20, 30, false), chainLink(30, 0, true)} {
		pics[l.Id] = DesignPicture{Id: l.Id, TechCardId: 7, ReplacedBy: l.ReplacedBy, UndoneAt: l.UndoneAt}
	}
	head, err := DesignReplacementHead(pics[10], func(id int) (DesignPicture, error) { return pics[id], nil })
	require.NoError(t, err)
	require.Equal(t, 20, head.Id)
}

// M1: ПОСЛЕ UNDO ВОССТАНОВЛЕННЫЙ ОРИГИНАЛ — ТЕКУЩАЯ ВЕРСИЯ (replaced_by стоит, преемник отменён): он
// режется и законно стоит на техническом листе. Живой преемник по-прежнему отказывает обоим, с головой.
// А redo над текущей версией с видимыми кусками — live_crop_parent.
//
// МУТАЦИИ: судить по ReplacedBy.Valid вместо головы (разрез и лист отказывают восстановленному);
// снять сторож кусков у redo.
func TestDesignRestoredOriginalIsCurrent(t *testing.T) {
	pics := map[int]DesignPicture{
		10: {Id: 10, TechCardId: replaceProbeCard, MediaId: 1010, ReplacedBy: sql.NullInt32{Int32: 20, Valid: true}},
		20: {Id: 20, TechCardId: replaceProbeCard, MediaId: 2020, UndoneAt: sql.NullTime{Time: time.Unix(1, 0), Valid: true}},
	}
	load := func(id int) (DesignPicture, error) { return pics[id], nil }

	require.NoError(t, DesignSplitReplacedRefusal(pics[10], load), "восстановленный оригинал режется")
	require.NoError(t, DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(1010)}, nil,
		[]DesignPicture{pics[10]}, load), "восстановленный оригинал законно стоит на листе")

	live := pics[20]
	live.UndoneAt = sql.NullTime{}
	pics[20] = live
	err := DesignSplitReplacedRefusal(pics[10], load)
	var replaced *DesignReplacedError
	require.ErrorAs(t, err, &replaced)
	require.Equal(t, 20, replaced.HeadPictureId)
	require.Error(t, DesignSheetReplacedRefusal(replaceProbeCard, []TechCardMediaItem{sheetItem(1010)}, nil,
		[]DesignPicture{pics[10]}, load))
	require.NoError(t, DesignSplitReplacedRefusal(DesignPicture{Id: 5, TechCardId: replaceProbeCard}, load))

	chain := []DesignChainLink{chainLink(10, 20, false), chainLink(20, 0, true)}
	_, err = DesignRedoStep(chain, 10, 1)
	require.ErrorIs(t, err, ErrDesignLiveCropParent)
}
