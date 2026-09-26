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
	// ErrDesignAlreadyReplaced — у кадра уже есть замена. Сюда же приходит повтор перезаписи БЕЗ
	// ключа (client_request_id, 0369), и приходит ДО вставки: второй правки повтор не файлит. Повтор С
	// ключом сюда не доходит — ему отвечает кадр, поданный первой попыткой. Тот же отказ получает
	// разрез заменённого листа (O-53 review): резать пиксели, чьё место уже заняла правка, — значит
	// нарезать колоду из того, чего на экране больше нет.
	//
	// ОТКАЗ НЕСЁТ ГОЛОВУ ЦЕПОЧКИ (DesignReplacedError): «уже заменён» без ответа «чем» оставляло
	// клиенту гадать, чья правка стоит на месте и не его ли это собственная, потерявшая ответ.
	ErrDesignAlreadyReplaced = errors.New("design: already_replaced")
	// ErrDesignCutSheet — от листа отрезаны куски, которые ещё стоят на экране, и они остались бы
	// вырезанными из ОРИГИНАЛА: на месте листа встала бы правка, а в колоде лежали бы куски прежних
	// пикселей. Кусок судится по ГОЛОВЕ своей цепочки замен (DesignVisibleCropBranches): кусок,
	// перезаписанный правкой, лист держит, пока правка на виду, — даже если сам кусок спрятан.
	// Чинится правкой куска либо прятаньем того, что от куска стоит на экране, а не листа.
	ErrDesignCutSheet = errors.New("design: cut_sheet")
)

// DesignRequestKeyMaxRunes — потолок ключа идемпотентности флэттена (design_picture.request_key,
// VARCHAR(64), 0369). UUID — 36 символов; потолок — ширина колонки, а не формат: формат ключа —
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
	HeadPictureId int // голова его цепочки замен: единственное звено с replaced_by = NULL
}

func (e *DesignReplacedError) Error() string {
	return fmt.Sprintf("%v: picture %d was already replaced; picture %d stands in its place now",
		ErrDesignAlreadyReplaced, e.PictureId, e.HeadPictureId)
}

func (e *DesignReplacedError) Unwrap() error { return ErrDesignAlreadyReplaced }

// DesignReplacementHead — ГОЛОВА ЦЕПОЧКИ ЗАМЕН p: идти по replaced_by, пока он не NULL. Незаменённый
// кадр — сам себе голова. load читает кадр по id: стор — в своей транзакции, хендлер — своим
// чтением; обход один, чтобы два обхода не разошлись в том, что считать порчей.
//
// ПОРЧА НЕ ВЫДАЁТСЯ ЗА ОТКАЗ. Ссылка назад (id следующего не больше текущего), цепочка длиннее
// DesignReplacementChainMax и ссылка на несуществующий кадр — ошибка БЕЗ сентинела полосы: клиенту
// Internal, дежурному строка в логе. not_found здесь соврал бы о кадре, которого клиент не называл.
// Ошибка чтения прочего рода заворачивается через %w — дедлок 1213 обязан остаться видимым для
// повтора транзакции.
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
		head = n
	}
	return head, nil
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

// DesignReplaceRefusal — МОЖЕТ ЛИ ПРАВКА СЛОЯ ЗАНЯТЬ МЕСТО КАДРА original. nil = может.
//
// Вход — то, что стор уже прочитал В ТРАНЗАКЦИИ ФЛЭТТЕНА: карточка запроса, base_media_id слоя, сам
// кадр и число его кропов, чья ветка стоит на экране (DesignVisibleCropBranches). Чтение вне той
// транзакции было бы TOCTOU с именем поприличнее, поэтому здесь нет ни одного запроса — только
// решение, и оно чистое ровно затем, чтобы порядок отказов проверялся без базы.
//
// ⚠ ПОРЯДОК — ЧАСТЬ КОНТРАКТА, и он от «чинится запросом» к «чинится другим жестом»:
//
//  1. replace_mismatch — кадр на другой карточке, слой нарисован с чистого листа, либо медиа кадра
//     не подложка слоя. Медиа сверяется с base_media_id, а не с source_picture_id: слой держится
//     ключом подложки (один слой на файл), и равенство файлов — ровно то, что делает правку
//     картинкой ЭТОГО кадра. Две регистрации одного файла обе годятся: место занимается у НАЗВАННОЙ.
//  2. already_replaced — replaced_by уже стоит. Проверяется NULL-ность, а не знак, ровно как
//     `replaced_by IS NULL` в самом UPDATE: два сторожа одного факта не расходятся ни на одной строке.
//     Голову цепочки здесь не узнать — это чтение, — и стор дописывает её (DesignAlreadyReplaced).
//  3. cut_sheet — у кадра есть кропы, чья ветка стоит на экране: видима ГОЛОВА цепочки замен куска
//     (O-53 review, раунд 2). Правка куска нарезана из прежних пикселей листа, и перезапись листа
//     оставила бы две живые ветки одного листа; спрятанная голова ветку снимает. Считает вызывающий —
//     DesignVisibleCropBranches.
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

// DesignVisibleCropBranches — СКОЛЬКО КУСКОВ ЛИСТА ЕЩЁ СТОИТ НА ЭКРАНЕ (сторож cut_sheet, O-53 review,
// раунд 2). crops — ВСЕ кропы листа, спрятанные тоже; load читает кадр по id, как у
// DesignReplacementHead.
//
// КУСОК СУДИТСЯ ПО ГОЛОВЕ СВОЕЙ ЦЕПОЧКИ ЗАМЕН, А НЕ ПО СОБСТВЕННОЙ СТРОКЕ. Сторож смотрел на строку
// куска, и устаревшая вкладка обходила его так: лист S разрезан на кусок C, C открыт в редакторе, C
// спрятали, вкладка перезаписывает спрятанный C (спрятанный оригинал перезаписывать можно, и правка E
// рождается видимой), затем перезаписывают S — строка C спрятана, сторож молчит, и S уходит под
// правку, пока E, нарезанная из прежних пикселей S, стоит на экране. На экране от ветки куска стоит
// её ГОЛОВА: видимая голова держит лист, спрятанная — нет, чем бы ни были звенья до неё.
//
// Ошибка обхода — ошибка, а не ноль: «не смогли прочесть цепочку» не значит «куска нет», и лист не
// уходит под правку по порче данных.
func DesignVisibleCropBranches(crops []DesignPicture, load func(id int) (DesignPicture, error)) (int, error) {
	n := 0
	for _, c := range crops {
		head, err := DesignReplacementHead(c, load)
		if err != nil {
			return 0, err
		}
		if !head.HiddenAt.Valid {
			n++
		}
	}
	return n, nil
}

// DesignFlattenReplayRefusal — ОТВЕЧАЕТ ЛИ prior (кадр, уже поданный этой карточкой под ключом key) НА
// ЗАПРОС req. nil — да, это повтор, и ответ ему — prior. tookThePlaceOf — чьё место prior занял: id
// родителя, когда родитель подписан prior-ом (replaced_by = prior), иначе 0; это чтение, и его делает
// стор, а решение — здесь, чтобы проверяться без базы.
//
// Сверяется то, что делает флэттен ТЕМ ЖЕ жестом, в порядке от «чужой глагол» к «не то место»:
//   - глагол: prior — флэттен, а не кусок разреза (ключ однажды будет и у разреза);
//   - СЛОЙ (0370, O-53 review, раунд 2): prior расплющен из того слоя, который назван. Без этой
//     сверки два слоя одной карточки на одной ревизии делили ключ: «save as new» слоя L2 под ключом,
//     уже потраченным на L1, получал картинку L1 как свой успех. Кадр без записанного слоя (флэттен до
//     0370) под ключом появиться не может — ключ и слой пишутся одной вставкой, — и отказ здесь честнее,
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
