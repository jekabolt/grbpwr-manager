package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// ───────────────────────── «ПЕРЕЗАПИСАТЬ» ПРАВКОЙ (0369, O-53) ─────────────────────────
//
// Владелец: после правки в воркбенче FLAT спрашивать «overwrite или save as new». Перезапись — это
// ТОТ ЖЕ флэттен (FlattenEditLayer с ReplacePictureId): правка файлится сиблингом, ничего не
// перепикселивается и не прячется, а в той же транзакции слот верстака, где стоял оригинал,
// переезжает на правку и оригинал получает replaced_by.
//
// ОТКАЗЫ ЖИВУТ РЯДОМ С ПРАВИЛОМ, КОТОРОЕ ИХ ПОДНИМАЕТ, по тому же праву, что
// ErrDesignColourPlanRevMismatch: словарь на проводе один — таблица designRefusals в apisrv/admin, —
// и каждый из них стоит в ней ровно одной строкой.
var (
	// ErrDesignReplaceMismatch — названный кадр НЕ ТОТ, поверх которого нарисован слой: он на
	// другой карточке, либо его медиа не base_media_id слоя (слой, нарисованный с чистого листа, не
	// заменяет ничего). InvalidArgument: чинится правкой запроса, а не другим жестом.
	ErrDesignReplaceMismatch = errors.New("design: replace_mismatch")
	// ErrDesignAlreadyReplaced — у кадра уже есть замена. Сюда же приходит повтор перезаписи БЕЗ
	// ключа (client_request_id, 0370), и приходит ДО вставки: второй правки повтор не файлит. Повтор С
	// ключом сюда не доходит — ему отвечает кадр, поданный первой попыткой. Тот же отказ получает
	// разрез заменённого листа (O-53 review): резать пиксели, чьё место уже заняла правка, — значит
	// нарезать колоду из того, чего на экране больше нет.
	//
	// ОТКАЗ НЕСЁТ ГОЛОВУ ЦЕПОЧКИ (DesignReplacedError): «уже заменён» без ответа «чем» оставляло
	// клиенту гадать, чья правка стоит на месте и не его ли это собственная, потерявшая ответ.
	ErrDesignAlreadyReplaced = errors.New("design: already_replaced")
	// ErrDesignCutSheet — от листа отрезаны куски, которые ещё стоят на экране, и они остались бы
	// вырезанными из ОРИГИНАЛА: на месте листа встала бы правка, а в колоде лежали бы куски прежних
	// пикселей. Кусок судится ВСЕЙ СВОЕЙ ВЕТКОЙ (DesignStandingPieces, O-53 review, раунд 3): он
	// стоит, пока на экране хоть что-то, выросшее из него, — он сам, правка, занявшая его место, кусок,
	// отрезанный от любого из них, и так до конца. Чинится правкой куска либо прятаньем того, что от
	// куска стоит на экране, а не листа.
	ErrDesignCutSheet = errors.New("design: cut_sheet")
	// ErrDesignTechnicalSheet — медиа кадра стоит на ТЕХНИЧЕСКОМ ЛИСТЕ карточки (TechCard.
	// technical_media — строки tech_card_media с category = 'technical'), 27.09. Лист уходит в
	// тех-пакет своими плитами, и перезапись оставила бы на нём оригинал, а слот верстака отдала бы
	// правке: тех-пакет напечатал бы ДВЕ плиты одного вида — прежнюю с листа и правку из слота, — а
	// выноски листа остались бы приколоты к оригиналу. До этого отказа ловушку держал только снимок
	// формы в клиенте. FailedPrecondition: чинится снятием кадра с листа (сейвом карточки) либо
	// «save as new» — правка рядом ничьего места не занимает.
	ErrDesignTechnicalSheet = errors.New("design: technical_sheet")
	// ErrDesignHiddenPicture — жест, который повесил бы на ветку СПРЯТАННОГО кадра видимую строку:
	// кусок (разрез) или преемника (перезапись). Две двери, одно слово на проводе: hidden_picture,
	// FailedPrecondition — чинится другим жестом, а не правкой запроса.
	//
	// РАЗРЕЗ (O-53 review, раунд 3; DesignSplitHiddenRefusal). Кропы рождаются видимыми, и разрез
	// спрятанного кадра вешал бы живые куски под родителя, на которого никто не смотрит, — то самое
	// состояние, от которого hide стережёт live_crop_parent, только с другой стороны. А кусок,
	// отрезанный от спрятанной правки, держит её лист (cut_sheet) веткой, которой на экране не видно.
	// Чинится показом кадра (hide с hidden = false).
	//
	// ПЕРЕЗАПИСЬ (27.09; DesignReplaceRefusal). Правка встаёт на место оригинала ПРЕЕМНИКОМ: головой его
	// цепочки замен — и, если он кусок, частью ветки листа, от которого он отрезан. Спрятанный кадр человек
	// с экрана убрал, и преемник, рождённый видимым, вернул бы его место на экран в обход показа — сценарий
	// устаревшей вкладки (O-53 review, раунд 2: спрятанный кусок, перезаписанный из старой вкладки,
	// снова держал лист видимой правкой). Чинится «save as new» — правка рядом ничьим преемником не
	// становится — либо показом кадра. Клиент закрывает «overwrite» над спрятанным кадром сам; этот
	// отказ — задний пояс в транзакции флэттена.
	ErrDesignHiddenPicture = errors.New("design: hidden_picture")
)

// DesignRequestKeyMaxRunes — потолок ключа идемпотентности флэттена (design_picture.request_key,
// VARCHAR(64), 0370). UUID — 36 символов; потолок — ширина колонки, а не формат: формат ключа —
// дело клиента, и сервер его не угадывает.
const DesignRequestKeyMaxRunes = 64

// DesignReplacementChainMax — сколько звеньев цепочки замен читается, прежде чем чтение сдаётся.
// Это предохранитель ресурса, а не правило предметной области: цикла в цепочке быть не может
// (правка вставляется РАНЬШЕ штампа, её id всегда больше id оригинала, и DesignReplacementHead
// проверяет это на каждом звене), а сотни перезаписей одного кадра — мыслимая, пусть и долгая, работа
// над одним флэтом. Потолок отвечает на порчу данных, а не на усердие дизайнера.
const DesignReplacementChainMax = 1024

// DesignReplacedError — already_replaced С ГОЛОВОЙ ЦЕПОЧКИ: кадр, названный запросом, и кадр,
// который стоит на его месте сейчас. errors.Is(err, ErrDesignAlreadyReplaced) остаётся правдой —
// таблица отказов хендлера и все пробы узнают отказ по-прежнему, а голову достаёт errors.As.
type DesignReplacedError struct {
	PictureId     int // названный кадр
	HeadPictureId int // голова его цепочки замен: текущая версия (до первого отменённого звена, 0387)
}

func (e *DesignReplacedError) Error() string {
	return fmt.Sprintf("%v: picture %d was already replaced; picture %d stands in its place now",
		ErrDesignAlreadyReplaced, e.PictureId, e.HeadPictureId)
}

func (e *DesignReplacedError) Unwrap() error { return ErrDesignAlreadyReplaced }

// DesignReplacementHead — ГОЛОВА ЦЕПОЧКИ ЗАМЕН p: идти по replaced_by, пока он не NULL и следующее звено
// не отменено (undone_at, 0387 — голова = текущая версия, DesignEditChainCurrent). Незаменённый
// кадр — сам себе голова. load читает кадр по id: стор — в своей транзакции, хендлер — своим
// чтением; обход один, чтобы два обхода не разошлись в том, что считать порчей.
//
// ПОРЧА НЕ ВЫДАЁТСЯ ЗА ОТКАЗ. Ссылка назад (id следующего не больше текущего), цепочка длиннее
// DesignReplacementChainMax, ссылка на несуществующий кадр и звено ЧУЖОЙ карточки — ошибка БЕЗ
// сентинела полосы: клиенту Internal, дежурному строка в логе. not_found здесь соврал бы о кадре,
// которого клиент не называл. Ошибка чтения прочего рода заворачивается через %w — дедлок 1213
// обязан остаться видимым для повтора транзакции.
//
// ЗВЕНО ЧУЖОЙ КАРТОЧКИ (D-57, раунд 3). Правка всегда файлится на карточке оригинала, но у
// replaced_by нет внешнего ключа, и испорченная ссылка на кадр другой карточки с бОльшим id прошла бы
// проверку «следующий новее» — и отказ назвал бы человеку чужую картинку как ту, что стоит на месте
// его чертежа. Каждое прочитанное звено сверяется с карточкой названного кадра.
func DesignReplacementHead(p DesignPicture, load func(id int) (DesignPicture, error)) (DesignPicture, error) {
	head := p
	for hops := 0; head.ReplacedBy.Valid; hops++ {
		next := int(head.ReplacedBy.Int32)
		if next <= head.Id || hops >= DesignReplacementChainMax {
			return head, fmt.Errorf("design picture %d: the replacement chain is broken at picture %d (replaced_by %d, link %d)",
				p.Id, head.Id, next, hops)
		}
		n, err := load(next)
		if err != nil {
			if errors.Is(err, ErrDesignNotFound) {
				return head, fmt.Errorf("design picture %d: the replacement chain points at picture %d, which does not exist",
					p.Id, next)
			}
			return head, fmt.Errorf("failed to follow the replacement chain of design picture %d: %w", p.Id, err)
		}
		if n.TechCardId != p.TechCardId {
			return head, fmt.Errorf("design picture %d: the replacement chain leaves tech card %d at picture %d, which belongs to tech card %d",
				p.Id, p.TechCardId, n.Id, n.TechCardId)
		}
		// ОТМЕНЁННОЕ ЗВЕНО (0387, T28 v2) — конец обхода: на месте стоит текущая версия, звено перед ним.
		if n.UndoneAt.Valid {
			break
		}
		head = n
	}
	return head, nil
}

// DesignSplitReplacedRefusal — РЕЖЕТСЯ ЛИ p ПО ЦЕПОЧКЕ ЗАМЕН (O-53 review; T28 v2): отказ already_replaced
// только когда на месте p стоит ДРУГОЙ кадр — голова цепочки (текущая версия) не p. Кадр, чей
// преемник отменён (undo вернул ему место), — текущий, и режется как незаменённый. nil — режется.
func DesignSplitReplacedRefusal(p DesignPicture, load func(id int) (DesignPicture, error)) error {
	if !p.ReplacedBy.Valid {
		return nil
	}
	head, err := DesignReplacementHead(p, load)
	if err != nil {
		return err
	}
	if head.Id == p.Id {
		return nil
	}
	return &DesignReplacedError{PictureId: p.Id, HeadPictureId: head.Id}
}

// DesignAlreadyReplaced — ОТКАЗ already_replaced ДЛЯ ЗАМЕНЁННОГО p, с его головой. Ошибка обхода
// возвращается вместо отказа: отказ без головы нарушил бы контракт (head_picture_id обещан всегда),
// а порча цепочки — не состояние, которое клиент чинит другим жестом.
func DesignAlreadyReplaced(p DesignPicture, load func(id int) (DesignPicture, error)) error {
	head, err := DesignReplacementHead(p, load)
	if err != nil {
		return err
	}
	return &DesignReplacedError{PictureId: p.Id, HeadPictureId: head.Id}
}

// DesignReplaceFacts — ТО, ЧТО СТОР ПРОЧИТАЛ О КАДРЕ В ТРАНЗАКЦИИ ФЛЭТТЕНА, кроме самого кадра.
// Нулевое значение — «ещё не прочитано и ничего не держит»: стор зовёт DesignReplaceRefusal сначала
// с ним (отказы, которым чтения не нужны, звучат до чтений), затем с тем, что прочитал, — и порядок
// отказов остаётся здесь, а не в стороже.
type DesignReplaceFacts struct {
	// OnTechnicalSheet — медиа кадра стоит на техническом листе карточки запроса: строка
	// tech_card_media этой карточки с category = 'technical' и media_id кадра.
	OnTechnicalSheet bool
	// StandingPieces — сколько кусков кадра ещё стоят (DesignStandingPieces).
	StandingPieces int
	// SuccessorUndone — у кадра есть преемник (replaced_by), и он ОТМЕНЁН (undone_at, 0387, T28 v2):
	// правка, которую человек отменил (UndoEdit). Отменённая ветка места не занимает, и новая правка
	// встаёт на место кадра поверх неё; сама ветка остаётся в истории отрезанной. Только undone_at:
	// просто спрятанный преемник (hidden_at) место держит, как и прежде. Стор читает преемника в
	// транзакции флэттена, ДО первого вызова решения.
	SuccessorUndone bool
}

// DesignSuccessorUndone — ОСВОБОЖДАЕТ ЛИ ПРЕЕМНИК next МЕСТО КАДРА (DesignReplaceFacts.SuccessorUndone):
// только отменённый (undone_at, 0387). Спрятанный, но не отменённый преемник место держит — hidden_at
// и undone_at два разных факта (T28 v2).
func DesignSuccessorUndone(next DesignPicture) bool {
	return next.UndoneAt.Valid
}

// DesignReplaceRefusal — МОЖЕТ ЛИ ПРАВКА СЛОЯ ЗАНЯТЬ МЕСТО КАДРА original. nil = может.
//
// Вход — то, что стор уже прочитал В ТРАНЗАКЦИИ ФЛЭТТЕНА: карточка запроса, base_media_id слоя, сам
// кадр и факты о нём (DesignReplaceFacts: лист и стоящие куски). Чтение вне той транзакции было бы
// TOCTOU с именем поприличнее, поэтому здесь нет ни одного запроса — только решение, и оно чистое
// ровно затем, чтобы порядок отказов проверялся без базы.
//
// ⚠ ПОРЯДОК — ЧАСТЬ КОНТРАКТА, и он от «чинится запросом» к «чинится другим жестом»:
//
//  1. replace_mismatch — кадр на другой карточке, слой нарисован с чистого листа, либо медиа кадра
//     не подложка слоя. Медиа сверяется с base_media_id, а не с source_picture_id: слой держится
//     ключом подложки (один слой на файл), и равенство файлов — ровно то, что делает правку
//     картинкой ЭТОГО кадра. Две регистрации одного файла обе годятся: место занимается у НАЗВАННОЙ.
//  2. already_replaced — replaced_by уже стоит, и преемник НЕ отменён (T28 v2: преемник с undone_at —
//     отменённая правка, facts.SuccessorUndone, и место кадра снова свободно; спрятанный, но не
//     отменённый преемник место держит). Штамп стора пишет поверх ровно прочитанного
//     (`replaced_by <=> :was`): два сторожа одного факта не расходятся ни на одной строке.
//     undone_picture — сам оригинал ОТМЕНЁН: его преемник встал бы за отменённым звеном, то есть
//     нигде (ErrDesignUndonePicture). Сразу после already_replaced.
//     Голову цепочки здесь не узнать — это чтение, — и стор дописывает её (DesignAlreadyReplaced).
//  3. hidden_picture — оригинал СПРЯТАН (hidden_at, 27.09): правка встала бы преемником кадра, которого
//     на экране нет (ErrDesignHiddenPicture). Чтения не нужно — hidden_at лежит на уже прочитанной
//     строке, — и отказ звучит в первом проходе, с нулевыми фактами, вместе с двумя первыми. ПОСЛЕ
//     already_replaced: заменённый и спрятанный кадр получает already_replaced с головой — слепой
//     повтор перезаписи узнаёт себя по этому слову, даже если кадр успели спрятать после первой подачи.
//     ДО technical_sheet: отказ, которому чтение не нужно, за чтение листа не платит.
//  4. technical_sheet — медиа кадра стоит на техническом листе карточки (27.09): лист печатается в
//     тех-пакет, и после перезаписи в нём стояли бы две плиты одного вида. ПОСЛЕ already_replaced:
//     повтор перезаписи без ключа обязан узнать себя по already_replaced с головой, даже если кадр
//     успели поставить на лист после первой подачи. ДО cut_sheet: оба чинятся другим жестом, но лист
//     отвечает одним чтением по индексу, а куски — чтением ветки уровнями, и запрос, который лист и
//     так закрывает, за ветку не платит.
//  5. cut_sheet — у кадра есть кропы, которые ещё стоят: на экране хоть что-то из ветки куска (O-53
//     review, раунд 3). Правка куска и кусок, отрезанный от неё, нарезаны из прежних пикселей листа, и
//     перезапись листа оставила бы две живые ветки одного листа. Считает вызывающий —
//     DesignStandingPieces.
//
// «SAVE AS NEW» СЮДА НЕ ПРИХОДИТ ВОВСЕ: правка рядом ничьего места не занимает, и ни один из пяти
// отказов её не касается — в том числе лист (кадр на листе остаётся на нём, а правка ложится рядом) и
// спрятанность (правка спрятанного кадра ложится рядом с ним и ничьим преемником не становится).
func DesignReplaceRefusal(cardID int, layerBaseMediaID sql.NullInt32, original DesignPicture, facts DesignReplaceFacts) error {
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
	if original.ReplacedBy.Valid && !facts.SuccessorUndone {
		return fmt.Errorf("%w: picture %d was already replaced by picture %d",
			ErrDesignAlreadyReplaced, original.Id, original.ReplacedBy.Int32)
	}
	if original.UndoneAt.Valid {
		return fmt.Errorf("%w: the original, picture %d, is an undone edit — redo it first, or save the edit as a new picture",
			ErrDesignUndonePicture, original.Id)
	}
	if original.HiddenAt.Valid {
		return fmt.Errorf("%w: the original, picture %d, is hidden — an edit cannot take a hidden picture's place; "+
			"save the edit as a new picture", ErrDesignHiddenPicture, original.Id)
	}
	if facts.OnTechnicalSheet {
		return fmt.Errorf("%w: the original, picture %d, is on the card's technical sheet and would stay there beside the edit — "+
			"take it off the sheet first, or save the edit as a new picture", ErrDesignTechnicalSheet, original.Id)
	}
	if facts.StandingPieces > 0 {
		return fmt.Errorf("%w: picture %d is cut into %d piece(s) that still stand on screen, and they would stay cut from the original",
			ErrDesignCutSheet, original.Id, facts.StandingPieces)
	}
	return nil
}

// ─── ЗЕРКАЛО technical_sheet: СЕЙВ КАРТОЧКИ НЕ СТАВИТ НА ЛИСТ ЗАМЕНЁННЫЙ КАДР (27.09, D-57) ───
//
// technical_sheet сторожит инвариант с одной стороны: перезапись не штампует кадр, чьё медиа стоит на
// листе. Другая сторона — сейв карточки, и до этого сторожа она была открыта: tech_card_media
// переписывается сейвом целиком, флэттен lock_version карточки не трогает, поэтому форма, открытая
// до перезаписи, клала файл заменённого кадра на лист как ни в чём не бывало — а жертва дедлока,
// повторённая транзакцией, могла закоммитить то же самое уже ПОСЛЕ флэттена. Инвариант один на обе
// двери, и он про ФАЙЛЫ, потому что лист держит медиа, а не кадры:
//
//	ни одна дверь не ДОБАВЛЯЕТ на технический лист карточки файл кадра этой карточки, у которого
//	стоит replaced_by: перезапись не штампует кадр, чей файл на листе; сейв не кладёт на лист
//	вхождение файла заменённого кадра сверх того, что там уже стоит.
//
// Для всего, что записано после сторожа, это и есть «на листе нет файла заменённого кадра». Строки,
// оставшиеся от листа, собранного ДО сторожа, живут, пока их не снимут, — см. ниже, почему не иначе.
//
// Обе проверки читают в своей SERIALIZABLE-транзакции, и порядок закрывается любой: сейв первым —
// флэттен видит файл на листе (technical_sheet); флэттен первым — сейв видит замену (этот отказ).
//
// СЕЙВ СУДИТ ПЕРЕХОД, А НЕ СОСТОЯНИЕ (D-57, раунд 3). Лист, собранный ДО сторожа, законно держит
// такой файл (на бете перезапись уехала раньше technical_sheet), и сторож «всего входящего листа»
// делал бы такую карточку несохраняемой целиком: каждый автосейв и правка любого другого поля
// отказывали бы, а инвариант от отказа не чинился. Поэтому отказ получает только вхождение файла
// заменённого кадра СВЕРХ того числа, сколько раз он уже стоит на листе.
// Счёт, а не множество: одна унаследованная строка не разрешает второй копии. Снятый с листа файл
// обратно не встаёт: после снятия его число ноль. Голову цепочки сервер сам на лист НЕ ставит —
// выноски листа приколоты в координатах оригинала, и заменить файл значит решить за человека.
//
// ⚠ ДВЕ РЕГИСТРАЦИИ ОДНОГО ФАЙЛА. Если тот же файл держит ещё и незаменённый кадр карточки, отказ
// всё равно звучит — ровно как technical_sheet отказывает перезаписи, не спрашивая, чей ещё это
// файл: строка листа называет файл, а не кадр, и «чья это плита» на ней не записано.

// DesignSheetReplacedReason — код причины поимённого отказа сейва карточки (ValidationError →
// google.rpc.BadRequest FieldViolation; клиент читает его префиксом описания — violationReason).
// Один на весь код: стор отказывает им, пробы узнают по нему отказ.
const DesignSheetReplacedReason = "replaced_picture"

// DesignSheetMediaIds — файлы технического листа из входящего сейва, без повторов, в порядке листа.
// Мудборд не спрашивается: он плит не печатает, и перезапись он не держит (technical_sheet тоже).
func DesignSheetMediaIds(media []TechCardMediaItem) []int {
	seen := make(map[int]bool, len(media))
	var ids []int
	for _, m := range media {
		if m.Category != TechCardMediaCategoryTechnical || seen[m.MediaId] {
			continue
		}
		seen[m.MediaId] = true
		ids = append(ids, m.MediaId)
	}
	return ids
}

// DesignSheetReplacedRefusal — МОЖЕТ ЛИ СЕЙВ КАРТОЧКИ cardID ПОСТАВИТЬ НА ТЕХНИЧЕСКИЙ ЛИСТ ЭТИ
// ФАЙЛЫ. nil = может.
//
// media — входящий список сейва (мудборд и лист вместе, как их собирает dto); stored — сколько раз
// каждый файл стоит на листе карточки СЕЙЧАС (строки tech_card_media с category = 'technical',
// прочитанные стором в транзакции сейва ДО их переписи; отсутствующий ключ — ноль); replaced —
// кадры, прочитанные там же по файлам листа (заменённые кадры этой карточки); load читает звено
// цепочки там же. Решение здесь, чтения — у стора: правило проверяется без базы.
//
// ОТКАЗ — ТОТ ЖЕ КАНАЛ, ЧТО У ОСТАЛЬНЫХ ПОИМЁННЫХ ОТКАЗОВ СЕЙВА (NewFieldViolation, как
// kind_not_available_yet в dto на той же строке листа): поле technical_media[i].media_id, где i —
// место в списке листа с нуля, как его пишет dto; человеку — номер с единицы и голова цепочки замен,
// то есть кадр, который стоит на месте этого сейчас. Называется ПЕРВОЕ ВХОЖДЕНИЕ СВЕРХ СОХРАНЁННОГО
// ЧИСЛА В ПОРЯДКЕ ТЕХНИЧЕСКОГО СПИСКА — и только оно: при числе ноль это первое вхождение файла, при
// одной сохранённой строке — второе по порядку. Это место в списке, а не личность копии: у строк
// листа нет ключа, и какую из двух копий положил человек, сервер не знает — при сохранённом {M: 1} и
// входящем [M новая, M прежняя] названа вторая, index 1. Отказ при этом верен; неточна только
// подсказка, на какой плите чинить. Канал несёт одно нарушение.
//
// Кадр другой карточки не держит (лист печатает свою карточку), незаменённый — тоже; порча цепочки —
// не отказ, а ошибка без сентинела (DesignReplacementHead): клиенту Internal, дежурному строка в логе.
func DesignSheetReplacedRefusal(cardID int, media []TechCardMediaItem, stored map[int]int, replaced []DesignPicture, load func(id int) (DesignPicture, error)) error {
	byMedia := make(map[int]DesignPicture, len(replaced))
	heads := make(map[int]int, len(replaced))
	for _, p := range replaced {
		if p.TechCardId != cardID || !p.ReplacedBy.Valid {
			continue
		}
		// ТЕКУЩАЯ ВЕРСИЯ НЕ ЗАМЕНЕНА (T28 v2): у кадра, чей преемник отменён, голова — он сам, и его
		// файл на листе законен. Голова считается ДО решения, а не только для текста отказа.
		head, err := DesignReplacementHead(p, load)
		if err != nil {
			return err
		}
		if head.Id == p.Id {
			continue
		}
		heads[p.Id] = head.Id
		// Тот же файл у двух заменённых кадров: называется старший — ответ не зависит от порядка
		// строк, в котором их прочитали.
		if held, ok := byMedia[p.MediaId]; !ok || p.Id < held.Id {
			byMedia[p.MediaId] = p
		}
	}
	if len(byMedia) == 0 {
		return nil
	}
	seen := make(map[int]int, len(byMedia))
	item := 0
	for _, m := range media {
		if m.Category != TechCardMediaCategoryTechnical {
			continue
		}
		if p, ok := byMedia[m.MediaId]; ok {
			seen[m.MediaId]++
			// Вхождения в пределах сохранённого числа — лист, каким он уже стоит: их сейв не судит.
			if seen[m.MediaId] > stored[m.MediaId] {
				// item — место первого вхождения сверх сохранённого числа в техническом списке.
				return NewFieldViolation(fmt.Sprintf("technical_media[%d].media_id", item), DesignSheetReplacedReason, "",
					fmt.Sprintf("technical sheet item %d: this drawing was replaced by picture #%d — "+
						"put the replacement on the sheet, or take this one off", item+1, heads[p.Id]))
			}
		}
		item++
	}
	return nil
}

// ─── СТОИТ ЛИ КУСОК: ВСЯ ВЕТКА, А НЕ ГОЛОВА И НЕ СТРОКА (O-53 review, раунд 3) ───

// DesignStandingNodesMax — СКОЛЬКО КАДРОВ ОДИН ЗАПРОС ЧИТАЕТ И ОБХОДИТ, СУДЯ КУСКИ ЛИСТА. Потолок
// ОБЩИЙ на запрос, а не на ветку и не на уровень: N кусков по L звеньев — это N×L кадров, и потолок на
// цепочку не ограничивал бы ничего (O-53 review, раунд 3). Лист — первый из них. Держат его обе
// половины: чтение ветки (DesignLoadBranch) отказывает, не дочитав лишнего (раунд 4), а обход — на
// любом наборе, откуда бы тот ни пришёл. Это предохранитель ресурса, а не правило предметной
// области: у честной ветки кадров горстка, и потолок отвечает на порчу, а не на усердие.
const DesignStandingNodesMax = 8192

// DesignBranchColumns — КОЛОНКИ, КОТОРЫЕ ЧИТАЕТ ОБХОД, и только они. Стор выбирает ровно их обоими
// чтениями ветки (DesignBranchReads), а TestDesignBranchColumnsAreTheNodeFields держит этот список и
// поля DesignBranchNode в одном порядке.
//
// ⚠ ЗАБЫТАЯ КОЛОНКА НЕ ПАДАЕТ, А ОТКРЫВАЕТ ЛИСТ. sqlx (Unsafe) оставил бы поле нулём, и три нуля из
// шести пускают перезапись: без derivation или derived_from чтение и обход не видят ни одного куска,
// без replaced_by — ни одной замены. Без tech_card_id каждый кадр ветки выглядел бы чужим, и сторож
// закрылся бы Internal на любом разрезанном листе. run_id здесь нет, и это решение: ни одно ребро
// обхода не идёт по строке прогона, а колонку, которую правило не читает, не заметила бы ни одна
// проба.
const DesignBranchColumns = "id, tech_card_id, hidden_at, replaced_by, derived_from, derivation"

// DesignBranchNode — кадр, каким его видит обход: карточка, видимость и два ребра — replaced_by
// (правка, занявшая место кадра) и derived_from с глаголом (кусок, отрезанный от кадра).
type DesignBranchNode struct {
	Id          int           `db:"id"`
	TechCardId  int           `db:"tech_card_id"`
	HiddenAt    sql.NullTime  `db:"hidden_at"`
	ReplacedBy  sql.NullInt32 `db:"replaced_by"`
	DerivedFrom sql.NullInt32 `db:"derived_from"`
	Derivation  string        `db:"derivation"`
}

// DesignBranchNodeOf — кадр, уже прочитанный целиком (лист), в виде узла обхода.
func DesignBranchNodeOf(p DesignPicture) DesignBranchNode {
	return DesignBranchNode{
		Id:          p.Id,
		TechCardId:  p.TechCardId,
		HiddenAt:    p.HiddenAt,
		ReplacedBy:  p.ReplacedBy,
		DerivedFrom: p.DerivedFrom,
		Derivation:  p.Derivation,
	}
}

// DesignStandingPieces — КУСКИ ЛИСТА sheetID, КОТОРЫЕ ЕЩЁ СТОЯТ (сторож cut_sheet), по возрастанию id.
// nodes — лист и то, что чтение ветки (DesignLoadBranch) собрало от его кусков в транзакции
// флэттена (колонки — DesignBranchColumns); обход базы не трогает.
//
//	standing(X) = X на виду
//	            ∨ standing(replaced_by(X))
//	            ∨ standing(c) для любого c, отрезанного от X (derived_from = X, derivation = crop)
//
// Лист держится, если стоит хоть один его кусок. Иначе говоря, кусок стоит, пока на экране хоть один
// кадр, достижимый от него по заменам и разрезам, — сам кусок тоже.
//
// ПОЧЕМУ ВСЯ ВЕТКА (O-53 review, раунд 3). Раунд 1 судил кусок по его строке, и его обходила
// устаревшая вкладка: кусок спрятан, спрятанный кусок перезаписан, правка на виду. Раунд 2 судил по
// голове цепочки замен — и пропускал кусок, отрезанный от спрятанной головы: лист S разрезан на C, C
// перезаписан правкой E, E спрятана, устаревшая вкладка режет спрятанную E на F, F рождается видимой,
// и S уходил под правку, пока F, нарезанная из прежних пикселей S, стоит на экране. Ребро разреза и
// ребро замены чередуются как угодно, поэтому судит только вся ветка.
//
// ПРАВКА «РЯДОМ» НЕ ДЕРЖИТ (c13a040). Сиблинг-флэттен кадра — не разрез и не замена: он ничего не
// занял и ничего не отрезал, и лист, у которого есть только правки, режется и перезаписывается. То
// же о легаси-ребёнке с пустым глаголом: бэкфилл 0359 не смог доказать, что это кроп, и назвать его
// куском здесь значило бы вписать догадку.
//
// ПОРЧА — ОШИБКА БЕЗ СЕНТИНЕЛА (клиенту Internal, дежурному строка в логе), и лист по ней не уходит
// под правку: ребро на кадр не новее того, из которого оно идёт (правка и кусок вставляются ПОСЛЕ
// своего кадра, и их id всегда больше — без такой ссылки назад не бывает и цикла); кадр, достигнутый
// дважды; кадр, которого нет среди прочитанных; кадр ДРУГОЙ КАРТОЧКИ (раунд 4: оба писателя кладут
// кроп и правку на карточку родителя, derived_from и replaced_by без FK, и чужой кадр в ветке —
// только порча данных; чтение ветки карточку не спрашивает намеренно, чтобы такой кадр не пропал
// молча); больше DesignStandingNodesMax кадров на запрос.
//
// ⚠ ОТВЕТ «ДЕРЖИТ» ОСТАНАВЛИВАЕТ ОБХОД КУСКА, ОТВЕТ «ОТПУСКАЕТ» — НЕТ. Первый же видимый кадр ветки
// решает за весь кусок, и порча за ним уже ничего не меняет: отказ закрывает дверь и без неё. А
// «кусок не стоит» звучит только после того, как пройдена вся его ветка, — значит, лист уходит под
// правку лишь тогда, когда каждый достижимый кадр прочитан, спрятан и цел.
func DesignStandingPieces(sheetID int, nodes []DesignBranchNode) ([]int, error) {
	w := designStandingWalk{
		sheet:   sheetID,
		byID:    make(map[int]DesignBranchNode, len(nodes)),
		crops:   map[int][]int{},
		visited: map[int]struct{}{},
	}
	for _, n := range nodes {
		w.byID[n.Id] = n
		if n.DerivedFrom.Valid && n.Derivation == DesignDerivationCrop {
			parent := int(n.DerivedFrom.Int32)
			w.crops[parent] = append(w.crops[parent], n.Id)
		}
	}
	for _, ids := range w.crops {
		sort.Ints(ids)
	}
	sheet, ok := w.byID[sheetID]
	if !ok {
		return nil, fmt.Errorf("design picture %d: the sheet is not among the pictures read for it", sheetID)
	}
	w.card = sheet.TechCardId
	w.visited[sheetID] = struct{}{}
	var standing []int
	for _, piece := range w.crops[sheetID] {
		ok, err := w.stands(piece)
		if err != nil {
			return nil, err
		}
		if ok {
			standing = append(standing, piece)
		}
	}
	return standing, nil
}

// designStandingWalk — один обход на запрос: visited и потолок ОБЩИЕ для всех кусков листа.
type designStandingWalk struct {
	sheet   int
	card    int // карточка листа: кадр другой карточки в ветке — порча
	byID    map[int]DesignBranchNode
	crops   map[int][]int // родитель → его кропы по возрастанию id
	visited map[int]struct{}
}

// designStandingLink — ребро обхода: кадр to, достигнутый из кадра from.
type designStandingLink struct{ from, to int }

// stands — standing(piece): есть ли на экране хоть один кадр, достижимый от куска. Обход в глубину
// своим стеком, чтобы глубина ветки не становилась глубиной стека Go. Со стека первой снимается
// замена, затем кропы по возрастанию id: порядок не меняет ответа, он лишь делает детерминированным,
// какая порча назовётся первой.
func (w *designStandingWalk) stands(piece int) (bool, error) {
	stack := []designStandingLink{{from: w.sheet, to: piece}}
	for len(stack) > 0 {
		l := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n, err := w.visit(l)
		if err != nil {
			return false, err
		}
		if !n.HiddenAt.Valid {
			return true, nil
		}
		kids := w.crops[n.Id]
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, designStandingLink{from: n.Id, to: kids[i]})
		}
		if n.ReplacedBy.Valid {
			stack = append(stack, designStandingLink{from: n.Id, to: int(n.ReplacedBy.Int32)})
		}
	}
	return false, nil
}

// visit — кадр, в который ведёт ребро, либо порча, которая не выдаётся за ответ.
func (w *designStandingWalk) visit(l designStandingLink) (DesignBranchNode, error) {
	broken := func(format string, args ...any) error { return designBranchBroken(w.sheet, format, args...) }
	if l.to <= l.from {
		return DesignBranchNode{}, broken("picture %d links to picture %d, which is not newer", l.from, l.to)
	}
	if _, seen := w.visited[l.to]; seen {
		return DesignBranchNode{}, broken("picture %d is reached twice, the second time from picture %d", l.to, l.from)
	}
	n, ok := w.byID[l.to]
	if !ok {
		return DesignBranchNode{}, broken("picture %d links to picture %d, which was not read", l.from, l.to)
	}
	if n.TechCardId != w.card {
		return DesignBranchNode{}, designBranchForeign(w.sheet, w.card, l.from, n)
	}
	if len(w.visited) >= DesignStandingNodesMax {
		return DesignBranchNode{}, designBranchOverCeiling(w.sheet)
	}
	w.visited[l.to] = struct{}{}
	return n, nil
}

// designBranchBroken — ПОРЧА ВЕТКИ кусков листа sheet: ошибка без сентинела (клиенту Internal). Одна
// на обход и на чтение ветки: порча называется одними словами, кто бы её ни нашёл.
func designBranchBroken(sheet int, format string, args ...any) error {
	return fmt.Errorf("design picture %d: the branch of its pieces is broken: %s", sheet, fmt.Sprintf(format, args...))
}

// designBranchForeign — кадр n ДРУГОЙ карточки (не card), достигнутый из кадра from.
func designBranchForeign(sheet, card, from int, n DesignBranchNode) error {
	return designBranchBroken(sheet, "picture %d, reached from picture %d, belongs to tech card %d, not to tech card %d",
		n.Id, from, n.TechCardId, card)
}

// designBranchOverCeiling — кадров на запрос больше DesignStandingNodesMax.
func designBranchOverCeiling(sheet int) error {
	return designBranchBroken(sheet, "more than %d pictures would be read", DesignStandingNodesMax)
}

// ─── ЧТЕНИЕ ВЕТКИ: ОТ КУСКОВ ЛИСТА ВНИЗ, УРОВНЯМИ (O-53 review, раунд 4) ───

// DesignBranchChunk — СКОЛЬКО id НАЗЫВАЕТ ОДНО ЧТЕНИЕ ВЕТКИ. Список IN ограничен, чтобы ни один
// запрос не рос вместе с веткой: широкий уровень читается несколькими запросами, а не одним.
//
// 199, А НЕ 200 (O-53 review, раунд 5): при eq_range_index_dive_limit = 200 (умолчание MySQL)
// оптимизатор меряет значения IN погружениями в индекс, только пока равенств МЕНЬШЕ предела, — то
// есть до 199 включительно; с 200-го он берёт среднее из статистики, а у derived_from она кривая —
// корни ветвей лежат одной NULL-группой, и среднее на значение раздуто. Раздутая оценка толкала бы
// его к полному проходу таблицы, а полный проход под SERIALIZABLE — замок на всю design_picture.
// Индекс кропов теперь назван и в самом запросе (FORCE INDEX, стор — designBranchCropsOf); кусок —
// второй пояс к нему: оценка остаётся замером, а не догадкой, и ширина IN не растёт с веткой.
const DesignBranchChunk = 199

// DesignBranchReads — ДВА ЧТЕНИЯ, из которых DesignLoadBranch собирает ветку. Каждый вызов — ОДИН
// запрос: не больше DesignBranchChunk id и не больше limit строк, и limit стоит В САМОМ SQL (LIMIT,
// O-53 review, раунд 5). Одной остановки курсора мало: драйвер, закрывая курсор, дочитывает ответ до
// конца, и сервер сканировал бы, слал и под SERIALIZABLE запирал всё, что подошло, — потолок держал
// бы только память Go. Курсор дальше limit-й строки по-прежнему не читается — второй пояс. Порядок
// строк чтению не важен: набор от него не зависит.
//
// ⚠ БЕЗ ПРЕДИКАТА КАРТОЧКИ. id и derived_from называют строки однозначно на всю таблицу, и кадр
// другой карточки, повисший на кадре ветки, обязан быть прочитан — иначе его не увидел бы никто и
// порча прошла бы молча; прочитанный, он отказывается тут же (DesignLoadBranch, раунд 5) теми
// словами, что сказал бы обход.
type DesignBranchReads struct {
	// ByID — кадры с этими id: цели replaced_by.
	ByID func(ids []int, limit int) ([]DesignBranchNode, error)
	// CropsOf — кропы этих кадров: derived_from среди parents И derivation = crop. Правка «рядом» и
	// легаси-ребёнок с пустым глаголом не читаются — обход по ним не ходит.
	CropsOf func(parents []int, limit int) ([]DesignBranchNode, error)
}

// DesignLoadBranch — ЛИСТ И ВСЁ, ЧТО ОБХОД ПРОЙДЁТ ОТ ЕГО КУСКОВ по кропам и заменам: ровно тот
// набор, который читает DesignStandingPieces. Лист — первый в ответе.
//
// ПОЧЕМУ УРОВНЯМИ, А НЕ ВСЯ КАРТОЧКА (O-53 review, раунд 4). Раунд 3 читал все кадры карточки одним
// SELECT, и под SERIALIZABLE это разделяемый замок на каждый кадр карточки: прятанье, выбор, разрез
// и загрузка той же карточки ждали перезаписи или ловили дедлок, а память росла с карточкой, пока
// потолок проверялся уже после чтения. Теперь читается только ветка: уровень 0 — кропы листа, затем
// для каждого нового уровня — цели replaced_by его СПРЯТАННЫХ кадров (по id) и кропы его СПРЯТАННЫХ
// кадров (по derived_from), пока уровень не добавит ничего. Замены самого листа в правиле нет
// (заменённый лист отказан already_replaced раньше), и она не читается.
//
// КАДР НА ВИДУ — КОНЕЦ ЧТЕНИЯ ЗА НИМ (раунд 5). Обход решает кусок на первом видимом кадре и дальше
// не идёт, поэтому ни замена видимого кадра, ни его кропы не читаются. Иначе чтение упиралось бы в
// потолок там, куда обход не смотрит, — видимый кусок с 8191 законной перезаписью за ним (перезапись
// ничего не прячет) отказывал бы Internal вместо честного cut_sheet, — и запирало бы кадры, которые
// ничего не решают. Кропы листа читаются всегда: судятся именно они.
//
// КАДР ДРУГОЙ КАРТОЧКИ — ОТКАЗ НА ЧТЕНИИ (раунд 5), теми же словами, что сказал бы обход, и прежде
// чем прочитано хоть что-то за ним: его замена и его кропы — это уже чужая карточка.
//
// РЕШЕНИЕ ТО ЖЕ, ЧТО У ОБХОДА ПО ВСЕЙ ТАБЛИЦЕ, ВСЕГДА: лист отпущен ровно тогда, когда каждый
// достижимый кадр спрятан, свой и прочитан, а тогда чтение и обход проходят один и тот же набор.
// Разниться может только РОД отказа, и только там, где обход нашёл бы видимый кадр раньше, чем порчу
// или потолок: чужой кадр за стоящим куском, либо спрятанный кусок, у которого видимая замена и
// тысячи спрятанных кропов, — обход сказал бы cut_sheet, чтение говорит Internal. Дверь закрыта в
// обоих случаях.
//
// ОДИН ПОТОЛОК НА ЗАПРОС: DesignStandingNodesMax, лист включительно, на все уровни и все чтения
// вместе. Чтение, вернувшее больше, чем осталось места, — отказ на этом же шаге (ошибка без
// сентинела, Internal), а не после того, как прочитано всё.
//
// ПРОЧИТАННОЕ НЕ ЧИТАЕТСЯ ВТОРОЙ РАЗ: цель, которая уже в наборе, не запрашивается, и повторная
// строка не добавляется. Поэтому порченый цикл замен останавливает чтение, а не раскручивает его, —
// и называет его уже обход (ссылка назад, кадр, достигнутый дважды). Цель, которой нет в таблице,
// просто не приходит; обход назовёт её потерянной.
//
// Ошибка чтения заворачивается через %w: дедлок 1213 обязан остаться видимым для повтора транзакции.
func DesignLoadBranch(sheet DesignBranchNode, reads DesignBranchReads) ([]DesignBranchNode, error) {
	l := designBranchLoad{
		sheet: sheet.Id,
		card:  sheet.TechCardId,
		seen:  map[int]struct{}{sheet.Id: {}},
		nodes: []DesignBranchNode{sheet},
	}
	frontier, err := l.read(reads.CropsOf, "crops", []int{sheet.Id}, designCropParent)
	if err != nil {
		return nil, err
	}
	for len(frontier) > 0 {
		var targets, parents []int
		replacing := map[int]int{} // цель замены → кадр уровня, чья это замена
		for _, n := range frontier {
			if !n.HiddenAt.Valid {
				continue // на виду: обход решает кусок здесь, и за этим кадром не читается ничего
			}
			parents = append(parents, n.Id)
			if !n.ReplacedBy.Valid {
				continue
			}
			t := int(n.ReplacedBy.Int32)
			if _, done := l.seen[t]; done {
				continue
			}
			if _, dup := replacing[t]; dup {
				continue
			}
			replacing[t] = n.Id
			targets = append(targets, t)
		}
		replacements, err := l.read(reads.ByID, "replacements", targets,
			func(r DesignBranchNode) int { return replacing[r.Id] })
		if err != nil {
			return nil, err
		}
		crops, err := l.read(reads.CropsOf, "crops", parents, designCropParent)
		if err != nil {
			return nil, err
		}
		frontier = append(replacements, crops...)
	}
	return l.nodes, nil
}

// designCropParent — кадр, из которого чтение пришло к кропу r: его родитель.
func designCropParent(r DesignBranchNode) int { return int(r.DerivedFrom.Int32) }

// designBranchLoad — одно чтение ветки на запрос: набор и потолок общие для всех уровней.
type designBranchLoad struct {
	sheet int
	card  int // карточка листа: кадр другой карточки — порча, и чтение отказывает на нём
	seen  map[int]struct{}
	nodes []DesignBranchNode
}

// read — один шаг уровня: ids кусками по DesignBranchChunk, по возрастанию; новые кадры — в набор и
// в ответ. limit каждого вызова — место, оставшееся под потолком, плюс одна строка: пришла лишняя —
// потолок пройден, и чтение отказывает, не читая дальше. Новый кадр другой карточки — отказ тут же
// (designBranchForeign); from называет кадр, из которого чтение к нему пришло.
func (l *designBranchLoad) read(read func(ids []int, limit int) ([]DesignBranchNode, error), what string, ids []int,
	from func(DesignBranchNode) int) ([]DesignBranchNode, error) {
	ids = append([]int(nil), ids...)
	sort.Ints(ids)
	var fresh []DesignBranchNode
	for start := 0; start < len(ids); start += DesignBranchChunk {
		chunk := ids[start:min(start+DesignBranchChunk, len(ids))]
		room := DesignStandingNodesMax - len(l.nodes)
		rows, err := read(chunk, room+1)
		if err != nil {
			return nil, fmt.Errorf("failed to read the %s in the branch of design picture %d: %w", what, l.sheet, err)
		}
		if len(rows) > room {
			return nil, designBranchOverCeiling(l.sheet)
		}
		for _, r := range rows {
			if _, done := l.seen[r.Id]; done {
				continue
			}
			if r.TechCardId != l.card {
				return nil, designBranchForeign(l.sheet, l.card, from(r), r)
			}
			l.seen[r.Id] = struct{}{}
			l.nodes = append(l.nodes, r)
			fresh = append(fresh, r)
		}
	}
	return fresh, nil
}

// DesignSplitHiddenRefusal — РАЗРЕЗ СПРЯТАННОГО КАДРА (ErrDesignHiddenPicture). nil — кадр на виду.
// Одно правило на две двери: транзакция SplitPicture, где оно авторитетно, и предпроверка хендлера,
// которая лишь сберегает байтовую работу.
func DesignSplitHiddenRefusal(p DesignPicture) error {
	if p.HiddenAt.Valid {
		return fmt.Errorf("%w: picture %d is hidden; show it before cutting it", ErrDesignHiddenPicture, p.Id)
	}
	// ОТМЕНЁННАЯ ПРАВКА (0387, T28 v2) на верстаке не стоит, и её куски родились бы видимыми под
	// звеном, которого нет на экране — тот же довод, что у спрятанного.
	if p.UndoneAt.Valid {
		return fmt.Errorf("%w: picture %d is an undone edit; redo it before cutting it", ErrDesignUndonePicture, p.Id)
	}
	return nil
}

// DesignFlattenReplayRefusal — ОТВЕЧАЕТ ЛИ prior (кадр, уже поданный этой карточкой под ключом key) НА
// ЗАПРОС req. nil — да, это повтор, и ответ ему — prior. tookThePlaceOf — чьё место prior занял: id
// родителя, когда родитель подписан prior-ом (replaced_by = prior), иначе 0; это чтение, и его делает
// стор, а решение — здесь, чтобы проверяться без базы.
//
// Сверяется то, что делает флэттен ТЕМ ЖЕ жестом, в порядке от «чужой глагол» к «не то место»:
//   - глагол: prior — флэттен, а не кусок разреза (ключ однажды будет и у разреза);
//   - СЛОЙ (0371, O-53 review, раунд 2): prior расплющен из того слоя, который назван. Без этой
//     сверки два слоя одной карточки на одной ревизии делили ключ: «save as new» слоя L2 под ключом,
//     уже потраченным на L1, получал картинку L1 как свой успех. Кадр без записанного слоя (флэттен до
//     0371) под ключом появиться не может — ключ и слой пишутся одной вставкой, — и отказ здесь честнее,
//     чем догадка;
//   - ревизия: prior растеризован из той ревизии слоя, которую эхом назвал запрос;
//   - место: prior занял место ровно названного кадра либо не занял ничьего, если запрос — «рядом».
//
// media_id НЕ сверяется, и это решение: ключ — это жест, а не байты. Каждое расхождение —
// invalid_argument: клиент потратил ключ на другой запрос, и вернуть ему чужой ответ значило бы
// соврать, что исполнен его.
func DesignFlattenReplayRefusal(req DesignEditLayerFlatten, key string, prior DesignPicture, tookThePlaceOf int) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: client_request_id %q already filed picture %d, which %s",
			ErrDesignInvalidArgument, key, prior.Id, fmt.Sprintf(format, args...))
	}
	switch {
	case prior.Derivation == DesignDerivationCrop:
		return refuse("is a crop, not a flatten")
	case !prior.SourceLayerId.Valid:
		return refuse("records no layer it was flattened from, and this request names layer %d", req.LayerId)
	case int(prior.SourceLayerId.Int32) != req.LayerId:
		return refuse("was flattened from layer %d, not layer %d", prior.SourceLayerId.Int32, req.LayerId)
	case prior.LayerRev != req.ExpectedRev:
		return refuse("was flattened from layer rev %d, not %d", prior.LayerRev, req.ExpectedRev)
	case tookThePlaceOf == req.ReplacePictureId:
		return nil
	case tookThePlaceOf == 0:
		return refuse("was filed beside its base, not in the place of picture %d", req.ReplacePictureId)
	case req.ReplacePictureId == 0:
		return refuse("took the place of picture %d, and this request files beside", tookThePlaceOf)
	default:
		return refuse("took the place of picture %d, not of picture %d", tookThePlaceOf, req.ReplacePictureId)
	}
}
