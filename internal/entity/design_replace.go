package entity

import (
	"database/sql"
	"errors"
	"fmt"
)

// ───────────────────────── «ПЕРЕЗАПИСАТЬ» ПРАВКОЙ (0368, O-53) ─────────────────────────
//
// Владелец: после правки в воркбенче FLAT спрашивать «overwrite или save as new». Перезапись — это
// ТОТ ЖЕ флэттен (FlattenEditLayer с ReplacePictureId): правка файлится сиблингом, ничего не
// перепикселивается и не прячется, а в той же транзакции слот верстака, где стоял оригинал,
// переезжает на правку и оригинал получает replaced_by.
//
// ОТКАЗЫ ЖИВУТ РЯДОМ С ПРАВИЛОМ, КОТОРОЕ ИХ ПОДНИМАЕТ, по тому же праву, что
// ErrDesignColourPlanRevMismatch: словарь на проводе один — таблица designRefusals в apisrv/admin, —
// и каждый из трёх стоит в ней ровно одной строкой.
var (
	// ErrDesignReplaceMismatch — названный кадр НЕ ТОТ, поверх которого нарисован слой: он на
	// другой карточке, либо его медиа не base_media_id слоя (слой, нарисованный с чистого листа, не
	// заменяет ничего). InvalidArgument: чинится правкой запроса, а не другим жестом.
	ErrDesignReplaceMismatch = errors.New("design: replace_mismatch")
	// ErrDesignAlreadyReplaced — у кадра уже есть замена. Сюда же приходит СЛЕПОЙ ПОВТОР перезаписи,
	// и приходит ДО вставки: второй правки повтор не файлит.
	ErrDesignAlreadyReplaced = errors.New("design: already_replaced")
	// ErrDesignCutSheet — от листа отрезаны видимые куски, и они остались бы вырезанными из
	// ОРИГИНАЛА: на месте листа встала бы правка, а в колоде лежали бы куски прежних пикселей.
	// Чинится правкой куска, а не листа.
	ErrDesignCutSheet = errors.New("design: cut_sheet")
)

// DesignReplaceRefusal — МОЖЕТ ЛИ ПРАВКА СЛОЯ ЗАНЯТЬ МЕСТО КАДРА original. nil = может.
//
// Вход — то, что стор уже прочитал В ТРАНЗАКЦИИ ФЛЭТТЕНА: карточка запроса, base_media_id слоя, сам
// кадр и число его видимых незаменённых кропов. Чтение вне той транзакции было бы TOCTOU с именем
// поприличнее, поэтому здесь нет ни одного запроса — только решение, и оно чистое ровно затем,
// чтобы порядок отказов проверялся без базы.
//
// ⚠ ПОРЯДОК — ЧАСТЬ КОНТРАКТА, и он от «чинится запросом» к «чинится другим жестом»:
//
//  1. replace_mismatch — кадр на другой карточке, слой нарисован с чистого листа, либо медиа кадра
//     не подложка слоя. Медиа сверяется с base_media_id, а не с source_picture_id: слой держится
//     ключом подложки (один слой на файл), и равенство файлов — ровно то, что делает правку
//     картинкой ЭТОГО кадра. Две регистрации одного файла обе годятся: место занимается у НАЗВАННОЙ.
//  2. already_replaced — replaced_by уже стоит. Проверяется NULL-ность, а не знак, ровно как
//     `replaced_by IS NULL` в самом UPDATE: два сторожа одного факта не расходятся ни на одной строке.
//  3. cut_sheet — у кадра есть видимые (hidden_at IS NULL) незаменённые кропы. Кроп, уже
//     заменённый своей правкой, и спрятанный кроп в счёт не идут — считает вызывающий.
func DesignReplaceRefusal(cardID int, layerBaseMediaID sql.NullInt32, original DesignPicture, visibleCrops int) error {
	if original.TechCardId != cardID {
		return fmt.Errorf("%w: picture %d belongs to tech card %d, not to %d",
			ErrDesignReplaceMismatch, original.Id, original.TechCardId, cardID)
	}
	if !layerBaseMediaID.Valid || layerBaseMediaID.Int32 <= 0 {
		return fmt.Errorf("%w: the layer is drawn from nothing and takes the place of no picture (picture %d was named)",
			ErrDesignReplaceMismatch, original.Id)
	}
	if int(layerBaseMediaID.Int32) != original.MediaId {
		return fmt.Errorf("%w: the layer is drawn over media %d, and picture %d is media %d",
			ErrDesignReplaceMismatch, layerBaseMediaID.Int32, original.Id, original.MediaId)
	}
	if original.ReplacedBy.Valid {
		return fmt.Errorf("%w: picture %d was already replaced by picture %d",
			ErrDesignAlreadyReplaced, original.Id, original.ReplacedBy.Int32)
	}
	if visibleCrops > 0 {
		return fmt.Errorf("%w: picture %d is cut into %d visible piece(s), and they would stay cut from the original",
			ErrDesignCutSheet, original.Id, visibleCrops)
	}
	return nil
}
