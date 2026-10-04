package entity

import (
	"database/sql"
	"errors"
	"fmt"
)

// ───────────────────────── ЦЕПОЧКА ПРАВОК: UNDO / REDO (0387, T28 v2) ─────────────────────────
//
// Владелец (пункт 28): правка в LATEST GENERATION или во FLAT SLOTS пропагейтится — на месте картинки
// стоит только новая, а на ховер есть undo и redo.
//
// ЦЕПОЧКА — replaced_by (0369): корень → правка → правка правки … Каждая перезапись (FlattenEditLayer с
// ReplacePictureId) дописывает звено. ОТМЕНА — design_picture.undone_at (0387), отдельная от hidden_at:
// спрятанность undo/redo не трогают, и прежние правила hidden_at живут как жили.
//
// ТЕКУЩАЯ ВЕРСИЯ цепочки — обход replaced_by от корня до ПЕРВОГО отменённого звена, не включая его
// (DesignEditChainCurrent). Undo ставит метку на текущую версию, redo снимает её со следующего звена;
// так отменённые звенья всегда образуют хвост цепочки. Цепочка, чей корень отменён, — ветка,
// отрезанная новой правкой поверх отменённого (FlattenEditLayer, replace над отменённым преемником):
// текущей версии у неё нет, и ни undo, ни redo над ней не делаются.
//
// Решения — здесь, чистыми функциями; стор читает звенья карточки под FOR UPDATE и исполняет шаг.

var (
	// ErrDesignStaleChain — CAS цепочки не сошёлся: текущая версия не та, что назвал запрос
	// (expected_current_id). Человек смотрел на старый экран — FailedPrecondition stale_chain, клиент
	// перечитывает полосу.
	ErrDesignStaleChain = errors.New("design: stale_chain")
	// ErrDesignNothingToUndo — текущая версия — корень цепочки (правок нет либо все отменены).
	ErrDesignNothingToUndo = errors.New("design: nothing_to_undo")
	// ErrDesignNothingToRedo — за текущей версией нет отменённого звена (либо у цепочки нет текущей
	// версии вовсе — отрезанная ветка).
	ErrDesignNothingToRedo = errors.New("design: nothing_to_redo")
	// ErrDesignUndonePicture — жест над ОТМЕНЁННЫМ кадром, который повесил бы на него видимую строку:
	// преемника (перезапись) или куски (разрез). Отменённое звено на верстаке не стоит; чинится redo
	// либо «save as new».
	ErrDesignUndonePicture = errors.New("design: undone_picture")
)

// DesignEditChainColumns — колонки звена, которые читает стор (DesignChainLink).
const DesignEditChainColumns = "id, tech_card_id, replaced_by, undone_at"

// DesignChainLink — кадр, каким его видит цепочка правок.
type DesignChainLink struct {
	Id         int           `db:"id"`
	TechCardId int           `db:"tech_card_id"`
	ReplacedBy sql.NullInt32 `db:"replaced_by"`
	UndoneAt   sql.NullTime  `db:"undone_at"`
}

// DesignEditChain — ЦЕПОЧКА, В КОТОРОЙ СТОИТ id, от корня. links — звенья одной карточки (минимум:
// все кадры с replaced_by и все их цели). Порча (ссылка назад, два предшественника, обрыв, цикл,
// звено чужой карточки, длина сверх DesignReplacementChainMax) — ошибка без сентинела: Internal.
func DesignEditChain(links []DesignChainLink, id int) ([]DesignChainLink, error) {
	byID := make(map[int]DesignChainLink, len(links))
	pred := make(map[int]int, len(links))
	for _, l := range links {
		byID[l.Id] = l
	}
	for _, l := range links {
		if !l.ReplacedBy.Valid {
			continue
		}
		next := int(l.ReplacedBy.Int32)
		if prev, dup := pred[next]; dup {
			return nil, fmt.Errorf("design picture %d: replaced by both picture %d and picture %d", next, prev, l.Id)
		}
		pred[next] = l.Id
	}
	start, ok := byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: design picture %d", ErrDesignNotFound, id)
	}
	root := start
	for hops := 0; ; hops++ {
		p, has := pred[root.Id]
		if !has {
			break
		}
		if p >= root.Id || hops >= DesignReplacementChainMax {
			return nil, fmt.Errorf("design picture %d: the replacement chain is broken at picture %d", id, root.Id)
		}
		root = byID[p]
	}
	chain := []DesignChainLink{root}
	for cur := root; cur.ReplacedBy.Valid; {
		next, has := byID[int(cur.ReplacedBy.Int32)]
		if !has || next.Id <= cur.Id || len(chain) > DesignReplacementChainMax {
			return nil, fmt.Errorf("design picture %d: the replacement chain is broken at picture %d (replaced_by %d)",
				id, cur.Id, cur.ReplacedBy.Int32)
		}
		if next.TechCardId != root.TechCardId {
			return nil, fmt.Errorf("design picture %d: the replacement chain leaves tech card %d at picture %d",
				id, root.TechCardId, next.Id)
		}
		chain = append(chain, next)
		cur = next
	}
	return chain, nil
}

// DesignEditChainCurrent — ИНДЕКС ТЕКУЩЕЙ ВЕРСИИ: последнее звено перед первым отменённым. -1 — корень
// отменён (отрезанная ветка), текущей версии нет.
func DesignEditChainCurrent(chain []DesignChainLink) int {
	cur := -1
	for i, l := range chain {
		if l.UndoneAt.Valid {
			break
		}
		cur = i
	}
	return cur
}

// DesignEditStep — ЧТО ДЕЛАЕТ ШАГ. Mark — кадр, чья метка undone_at меняется (undo ставит, redo снимает);
// From → To — текущая версия до и после: слоты, держащие From, переезжают на To. Replay — переход уже
// стоит (повтор того же жеста): ничего не пишется, ответ — нынешнее состояние.
type DesignEditStep struct {
	Mark   int
	From   int
	To     int
	Replay bool
}

// DesignUndoStep — ШАГ UNDO над цепочкой, прочитанной под замком. expected — текущая версия, которую
// видел клиент. standingCrops — сколько видимых кусков отрезано от текущей версии (они остались бы
// висеть под отменённой правкой: live_crop_parent).
//
// ПОРЯДОК: CAS (с повтором) → nothing_to_undo → live_crop_parent.
//
// ПОВТОР ПО ИСХОДУ, А НЕ ПО КЛЮЧУ: expected уже отменён и стоит сразу за текущей версией — это ровно
// состояние после этого undo, и ответ ему успех без записи (прецедент — SplitPicture, «idempotent by
// derivation»). Иначе — stale_chain.
func DesignUndoStep(chain []DesignChainLink, expected int, standingCrops int) (DesignEditStep, error) {
	cur := DesignEditChainCurrent(chain)
	if cur < 0 || chain[cur].Id != expected {
		if cur >= 0 && cur+1 < len(chain) && chain[cur+1].Id == expected && chain[cur+1].UndoneAt.Valid {
			return DesignEditStep{Mark: expected, From: expected, To: chain[cur].Id, Replay: true}, nil
		}
		return DesignEditStep{}, designStaleChain(chain, cur, expected)
	}
	if cur == 0 {
		return DesignEditStep{}, fmt.Errorf("%w: picture %d is the original of its chain", ErrDesignNothingToUndo, expected)
	}
	if standingCrops > 0 {
		return DesignEditStep{}, fmt.Errorf("%w: picture %d is cut into %d visible piece(s); undoing it would leave them under an undone edit",
			ErrDesignLiveCropParent, expected, standingCrops)
	}
	return DesignEditStep{Mark: chain[cur].Id, From: chain[cur].Id, To: chain[cur-1].Id}, nil
}

// DesignRedoStep — ШАГ REDO: снять отмену со звена за текущей версией.
//
// ПОВТОР ПО ИСХОДУ: expected стоит сразу перед текущей версией — состояние после этого redo. (Его же
// даёт и новая правка над expected, сделанная другой вкладкой между попытками; ответ несёт настоящую
// цепочку, и экран рисует правду.)
func DesignRedoStep(chain []DesignChainLink, expected int) (DesignEditStep, error) {
	cur := DesignEditChainCurrent(chain)
	if cur < 0 || chain[cur].Id != expected {
		if cur >= 1 && chain[cur-1].Id == expected {
			return DesignEditStep{Mark: chain[cur].Id, From: expected, To: chain[cur].Id, Replay: true}, nil
		}
		return DesignEditStep{}, designStaleChain(chain, cur, expected)
	}
	if cur+1 >= len(chain) {
		return DesignEditStep{}, fmt.Errorf("%w: picture %d has no undone edit after it", ErrDesignNothingToRedo, expected)
	}
	next := chain[cur+1]
	return DesignEditStep{Mark: next.Id, From: expected, To: next.Id}, nil
}

func designStaleChain(chain []DesignChainLink, cur, expected int) error {
	if cur < 0 {
		return fmt.Errorf("%w: the chain of picture %d has no current version (it was cut off by a newer edit)",
			ErrDesignStaleChain, chain[0].Id)
	}
	return fmt.Errorf("%w: picture %d stands as the current version, not %d", ErrDesignStaleChain, chain[cur].Id, expected)
}

// DesignEditControls — углы undo/redo кадра.
type DesignEditControls struct {
	CanUndo bool
	CanRedo bool
}

// DesignEditChainControls — УГЛЫ ДЛЯ КАЖДОЙ ТЕКУЩЕЙ ВЕРСИИ среди links (звенья карточки или карточек).
// Ключ — id кадра; кадра без углов в ответе нет. Порчу не судит (это чтение для показа): обход
// цепочки, наткнувшийся на обрыв или ссылку назад, останавливается.
//
//	CanUndo — кадр текущий и у него есть предшественник;
//	CanRedo — кадр текущий и за ним стоит отменённое звено.
func DesignEditChainControls(links []DesignChainLink) map[int]DesignEditControls {
	byID := make(map[int]DesignChainLink, len(links))
	targeted := make(map[int]bool, len(links))
	for _, l := range links {
		byID[l.Id] = l
		if l.ReplacedBy.Valid {
			targeted[int(l.ReplacedBy.Int32)] = true
		}
	}
	out := map[int]DesignEditControls{}
	for _, root := range links {
		if targeted[root.Id] || !root.ReplacedBy.Valid {
			continue
		}
		chain := []DesignChainLink{root}
		for cur := root; cur.ReplacedBy.Valid && len(chain) <= DesignReplacementChainMax; {
			next, has := byID[int(cur.ReplacedBy.Int32)]
			if !has || next.Id <= cur.Id {
				break
			}
			chain = append(chain, next)
			cur = next
		}
		cur := DesignEditChainCurrent(chain)
		if cur < 0 {
			continue
		}
		c := DesignEditControls{CanUndo: cur > 0, CanRedo: cur+1 < len(chain)}
		if c.CanUndo || c.CanRedo {
			out[chain[cur].Id] = c
		}
	}
	return out
}

// DesignEditChainStepRequest — UndoEdit / RedoEdit. PictureId — любое звено цепочки (её адрес);
// ExpectedCurrentId — текущая версия, которую видел клиент (CAS). IdempotencyKey обязателен и
// пишется в лог; повтор узнаётся по исходу (DesignUndoStep, DesignRedoStep).
type DesignEditChainStepRequest struct {
	PictureId         int
	ExpectedCurrentId int
	IdempotencyKey    string
	Actor             string
}

// DesignEditChainResult — цепочка после шага: текущая версия, все звенья (от корня, с файлами и
// углами) и слоты верстака, которые держат текущую версию (переехавшие этим шагом).
type DesignEditChainResult struct {
	CurrentPictureId int
	Pictures         []DesignPicture
	Slots            []DesignBenchSlot
}
