package techcard

import (
	"os"
	"strings"
	"testing"
)

// ГОНКА «ПОМЕТКА ↔ ЗАМЕР» ЗАКРЫВАЕТСЯ ПОРЯДКОМ ОПЕРАТОРОВ, А ПОРЯДОК НЕЛЬЗЯ ПРОВЕРИТЬ ЗНАЧЕНИЕМ.
//
// Два пути правят разные таблицы и проверяют друг друга: сохранение карточки ставит UNI, глядя на
// площади, а запись площадей смотрит на UNI. Обычные snapshot-чтения такую пару не сериализуют —
// незакоммиченный замер невидим для карточки, и обе транзакции коммитятся, оставив в базе
// противоречие, которого потом не поймает уже ничто («уже true» — не переход).
//
// Сериализует их ОДНА строка tech_card, и держится это на двух фактах порядка: путь замера берёт
// строку FOR UPDATE первым же действием (иначе снимок откроется раньше замка и прочитает состояние
// «до»), а путь карточки бампает lock_version ДО записи детей (иначе гард отработает без замка).
// Проверить это без базы можно только по исходнику — зато это ровно то, что ломает будущий рефактор,
// переставляющий строки местами. Тест держит инвариант, который комментарий рядом ОБЕЩАЕТ.
func TestUngradedRaceIsClosedByCardRowLock(t *testing.T) {
	t.Run("the area write locks the card row before it reads anything", func(t *testing.T) {
		body := funcBody(t, "piece_area.go", "func (s *Store) SaveTechCardPieceAreas")
		lock := strings.Index(body, "lockTechCardRow(")
		if lock < 0 {
			t.Fatal("SaveTechCardPieceAreas no longer locks the card row: the UNI guard on the card-save path can now be raced by an in-flight measurement")
		}
		for _, after := range []string{"RequireMutableTechCard(", "loadRollGoodsLines(", "storeutil.Query"} {
			at := strings.Index(body, after)
			if at >= 0 && at < lock {
				t.Fatalf("%q runs before lockTechCardRow: in REPEATABLE READ the first plain read opens the snapshot, so everything this transaction sees would be the state BEFORE the lock", after)
			}
		}
	})

	t.Run("the lock is a locking read, not a plain one", func(t *testing.T) {
		body := funcBody(t, "piece_area.go", "func lockTechCardRow")
		if !strings.Contains(body, "FOR UPDATE") {
			t.Fatal("lockTechCardRow stopped taking the row lock; a plain SELECT serialises nothing")
		}
	})

	// Вторая половина пары. Замок карточки — побочный эффект её собственного оптимистичного апдейта,
	// поэтому он ничего не стоит, но и держится ровно на том, что апдейт стоит РАНЬШЕ детей.
	t.Run("the card save bumps lock_version before it writes children", func(t *testing.T) {
		body := funcBody(t, "techcard.go", "func (s *Store) updateTechCardAndListOrphanedPatternURLs")
		bump := strings.Index(body, "lock_version = lock_version + 1")
		children := strings.Index(body, "insertTechCardChildren(")
		if bump < 0 || children < 0 {
			t.Fatalf("update path changed shape: bump=%d children=%d", bump, children)
		}
		if bump > children {
			t.Fatal("the card row is now locked AFTER the pieces are written: the UNI guard would read areas without holding the row, and a concurrent measurement could commit between the two")
		}
	})
}

// funcBody returns the source of one top-level function, from its signature to the next one.
func funcBody(t *testing.T, file, signature string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	body := string(src)
	start := strings.Index(body, signature)
	if start < 0 {
		t.Fatalf("%s: %q not found — the invariant this test guards may have moved, not disappeared", file, signature)
	}
	body = body[start+len(signature):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	return body
}
