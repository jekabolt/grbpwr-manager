package design

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ─── ОДИН ПОРЯДОК ЗАМКОВ ДЛЯ ЗАПИСЕЙ ЦЕПОЧКИ ПРАВОК И ВЕРСТАКА (T28 v2 C2) ───
//
// Undo, redo, перезапись (FlattenEditLayer с ReplacePictureId) и постановка в слот (SetBenchSlot)
// трогают одни и те же строки — кадры цепочки и слоты верстака — и до этого брали их в разном
// порядке, а часть строк сначала читала (под SERIALIZABLE это S-замок) и потом писала (X): две такие
// транзакции ловили дедлок на взаимном S→X. Порядок теперь один, и каждая строка, которую транзакция
// напишет, берётся сразу X:
//
//  1. строка карточки — `tech_card` FOR UPDATE (lockDesignCard): все такие записи одной карточки идут
//     друг за другом. Сейв карточки читает ту же строку первой и design_picture после неё, то есть
//     порядок «карточка → кадры» у него тот же;
//  2. кадры, которые транзакция напишет, — по возрастанию id (lockDesignPictures, либо чтение звеньев
//     цепочки FOR UPDATE ORDER BY id);
//  3. слоты верстака карточки — все, по возрастанию id (lockDesignBench): их немного, и постановка,
//     переезд и CAS дальше читают уже свои X-строки.
//
// Поздние чтения тех же строк (pictureByID, slotByID внутри setBenchSlotTx) идут под уже взятым X и
// замок не поднимают.

const (
	designCardLock     = `SELECT id FROM tech_card WHERE id = :card FOR UPDATE`
	designPicturesLock = `SELECT id FROM design_picture WHERE id IN (:ids) AND tech_card_id = :card ORDER BY id FOR UPDATE`
	designBenchLock    = `SELECT id FROM design_bench_slot WHERE tech_card_id = :card ORDER BY id FOR UPDATE`
	designPictureCard  = `SELECT tech_card_id FROM design_picture WHERE id = :id`
)

// designLockRow — строка замка: только id (StructScan требует структуру).
type designLockRow struct {
	Id int `db:"id"`
}

// lockDesignCard — шаг 1. Нет карточки — not_found.
func lockDesignCard(ctx context.Context, db dependency.DB, card int) error {
	if _, err := storeutil.QueryNamedOne[designLockRow](ctx, db, designCardLock, map[string]any{"card": card}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: tech card %d", entity.ErrDesignNotFound, card)
		}
		return fmt.Errorf("failed to lock tech card %d: %w", card, err)
	}
	return nil
}

// lockDesignPictures — шаг 2: кадры этой карточки, по возрастанию id. Чужой кадр не запирается (его
// откажут сторожа ниже) — замок чужой карточки нарушил бы порядок шага 1.
func lockDesignPictures(ctx context.Context, db dependency.DB, card int, ids ...int) error {
	var want []int
	for _, id := range ids {
		if id > 0 {
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return nil
	}
	sort.Ints(want)
	if _, err := storeutil.QueryListNamed[designLockRow](ctx, db, designPicturesLock, map[string]any{"ids": want, "card": card}); err != nil {
		return fmt.Errorf("failed to lock design pictures %v: %w", want, err)
	}
	return nil
}

// lockDesignBench — шаг 3: все слоты верстака карточки.
func lockDesignBench(ctx context.Context, db dependency.DB, card int) error {
	if _, err := storeutil.QueryListNamed[designLockRow](ctx, db, designBenchLock, map[string]any{"card": card}); err != nil {
		return fmt.Errorf("failed to lock the design bench of tech card %d: %w", card, err)
	}
	return nil
}

// cardOfPicture — карточка кадра, прочитанная ВНЕ транзакции записи: tech_card_id кадра не меняется
// никогда, а чтение внутри (S на строке, которую шаг 2 потом запрёт X) и было бы тем подъёмом замка.
// Транзакция сверяет её с кадром, прочитанным уже под замком.
func cardOfPicture(ctx context.Context, db dependency.DB, id int) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		TechCardId int `db:"tech_card_id"`
	}](ctx, db, designPictureCard, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: design picture %d", entity.ErrDesignNotFound, id)
		}
		return 0, fmt.Errorf("failed to read the tech card of design picture %d: %w", id, err)
	}
	return row.TechCardId, nil
}
