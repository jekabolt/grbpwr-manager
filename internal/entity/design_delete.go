package entity

import (
	"database/sql"
	"fmt"
)

// ─── УДАЛЕНИЕ КАРТИНКИ НАСОВСЕМ (O-68, D-74) ───
//
// Удаляются ТОЛЬКО производные картинки — кроп (derived_from → плита или композит) и правка кропа
// (флэттен, derived_from → кроп); механически правило одно: derived_from NULL — корень, двери нет.
// Вместе с картинкой уходит всё, что от неё произведено: обход по derived_from (FK у колонки нет —
// 0340, обход по колонке), уровнями, пока уровень не добавит ничего.

// DesignSubtreeNode — кадр, каким его видит обход удаления: id, медиа, которое он держит, и
// родитель. Колонки ровно эти три (DesignSubtreeColumns): обходу больше ничего не нужно.
type DesignSubtreeNode struct {
	Id          int           `db:"id"`
	MediaId     int           `db:"media_id"`
	DerivedFrom sql.NullInt32 `db:"derived_from"`
}

// DesignSubtreeColumns — колонки чтения обхода, в порядке полей DesignSubtreeNode.
const DesignSubtreeColumns = "id, media_id, derived_from"

// DesignSubtreeNodeOf — кадр, прочитанный целиком, в виде узла обхода.
func DesignSubtreeNodeOf(p DesignPicture) DesignSubtreeNode {
	return DesignSubtreeNode{Id: p.Id, MediaId: p.MediaId, DerivedFrom: p.DerivedFrom}
}

// DesignPictureDeletable — правило двери: удаляется только производная картинка. Корень (derived_from
// NULL — плита прогона, загрузка руками) отказан picture_is_root; его прячут, не стирают.
func DesignPictureDeletable(p DesignPicture) error {
	if !p.DerivedFrom.Valid {
		return fmt.Errorf("%w: design picture %d is not derived from anything — a root plate is hidden, never deleted",
			ErrDesignPictureIsRoot, p.Id)
	}
	return nil
}

// DesignCollectSubtree — корень и всё, что от него произведено, УРОВНЯМИ: [корень], его дети, их
// дети… пока уровень не добавит ничего. childrenOf отвечает на один уровень — строки, чей
// derived_from среди parents; ids ему приходят кусками не больше DesignBranchChunk.
//
// Кадр, уже стоящий в наборе, второй раз не добавляется и его дети не спрашиваются — порченый цикл
// derived_from останавливает обход, а не раскручивает его. Потолок — DesignStandingNodesMax на весь
// набор, как у чтения ветки сторожа перезаписи: набор больше него — отказ без сентинела (Internal),
// потому что такой ветки у живой карточки не бывает, а DELETE по тысячам строк под SERIALIZABLE
// запер бы полосу.
//
// Порядок уровней — порядок удаления НАОБОРОТ: стор идёт с последнего уровня к первому, и ребёнок
// всегда уходит раньше родителя.
func DesignCollectSubtree(root DesignSubtreeNode, childrenOf func(parents []int) ([]DesignSubtreeNode, error)) ([][]DesignSubtreeNode, error) {
	seen := map[int]struct{}{root.Id: {}}
	levels := [][]DesignSubtreeNode{{root}}
	total := 1
	frontier := []int{root.Id}
	for len(frontier) > 0 {
		var next []DesignSubtreeNode
		for start := 0; start < len(frontier); start += DesignBranchChunk {
			end := min(start+DesignBranchChunk, len(frontier))
			rows, err := childrenOf(frontier[start:end])
			if err != nil {
				return nil, fmt.Errorf("failed to read the children of design pictures %v: %w", frontier[start:end], err)
			}
			for _, r := range rows {
				if _, dup := seen[r.Id]; dup {
					continue
				}
				seen[r.Id] = struct{}{}
				next = append(next, r)
			}
		}
		if len(next) == 0 {
			break
		}
		total += len(next)
		if total > DesignStandingNodesMax {
			return nil, fmt.Errorf("design picture %d has more than %d descendants — refusing to delete the branch",
				root.Id, DesignStandingNodesMax)
		}
		levels = append(levels, next)
		frontier = frontier[:0]
		for _, n := range next {
			frontier = append(frontier, n.Id)
		}
	}
	return levels, nil
}

// DesignPictureDeletion — ЧТО УШЛО по DeletePicture: строки картинок в порядке удаления (дети
// раньше родителей, названная картинка последней) и их медиа — ЧИТАННЫЕ В ТОЙ ЖЕ ТРАНЗАКЦИИ, до
// того как строки ушли: хендлеру нужны адреса объектов, а строка media к его вопросу ещё цела,
// но строки картинки уже нет.
type DesignPictureDeletion struct {
	// PictureIds — в порядке удаления: потомки раньше, названная картинка последней.
	PictureIds []int
	// MediaIds — медиа удалённых картинок, без повторов, в порядке первого появления в PictureIds;
	// за ними — медиа удалённых слоёв правки (растр и векторный исходник), если такие были.
	MediaIds []int
	// Media — строки media по MediaIds, как их застала транзакция; исчезнувшая строка отсутствует.
	Media map[int]MediaFull
}
