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
// и каждый из трёх стоит в ней ровно одной строкой.
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
	// ErrDesignHiddenPicture — разрез СПРЯТАННОГО кадра (O-53 review, раунд 3). Кропы рождаются
	// видимыми, и разрез спрятанного кадра вешал бы живые куски под родителя, на которого никто не
	// смотрит, — то самое состояние, от которого hide стережёт live_crop_parent, только с другой
	// стороны. А кусок, отрезанный от спрятанной правки, держит её лист (cut_sheet) веткой, которой на
	// экране не видно. FailedPrecondition: чинится показом кадра (hide с hidden = false), а не правкой
	// запроса.
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
// кадр и число его стоящих кусков (DesignStandingPieces). Чтение вне той транзакции было бы TOCTOU с
// именем поприличнее, поэтому здесь нет ни одного запроса — только решение, и оно чистое ровно затем,
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
//     Голову цепочки здесь не узнать — это чтение, — и стор дописывает её (DesignAlreadyReplaced).
//  3. cut_sheet — у кадра есть кропы, которые ещё стоят: на экране хоть что-то из ветки куска (O-53
//     review, раунд 3). Правка куска и кусок, отрезанный от неё, нарезаны из прежних пикселей листа, и
//     перезапись листа оставила бы две живые ветки одного листа. Считает вызывающий —
//     DesignStandingPieces.
func DesignReplaceRefusal(cardID int, layerBaseMediaID sql.NullInt32, original DesignPicture, standingPieces int) error {
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
	if standingPieces > 0 {
		return fmt.Errorf("%w: picture %d is cut into %d piece(s) that still stand on screen, and they would stay cut from the original",
			ErrDesignCutSheet, original.Id, standingPieces)
	}
	return nil
}

// ─── СТОИТ ЛИ КУСОК: ВСЯ ВЕТКА, А НЕ ГОЛОВА И НЕ СТРОКА (O-53 review, раунд 3) ───

// DesignStandingNodesMax — СКОЛЬКО КАДРОВ ОДИН ЗАПРОС ОБХОДИТ, СУДЯ КУСКИ ЛИСТА. Потолок ОБЩИЙ на
// запрос, а не на ветку: N кусков по L звеньев — это N×L кадров, и потолок на цепочку не ограничивал
// бы ничего (O-53 review, раунд 3). Лист — первый из них. Это предохранитель ресурса, а не правило
// предметной области: у честной карточки кадров сотни, и потолок отвечает на порчу, а не на усердие.
const DesignStandingNodesMax = 8192

// DesignBranchColumns — КОЛОНКИ, КОТОРЫЕ ЧИТАЕТ ОБХОД, и только они. Стор выбирает ровно их одним
// SELECT по карточке листа, а TestDesignBranchColumnsAreTheNodeFields держит этот список и поля
// DesignBranchNode в одном порядке.
//
// ⚠ ЗАБЫТАЯ КОЛОНКА НЕ ПАДАЕТ, А ОТКРЫВАЕТ ЛИСТ. sqlx (Unsafe) оставил бы поле нулём, и три нуля из
// пяти пускают перезапись: без derivation или derived_from обход не видит ни одного куска, без
// replaced_by — ни одной замены. run_id здесь нет, и это решение: ни одно ребро обхода не идёт по
// строке прогона, а колонку, которую правило не читает, не заметила бы ни одна проба.
const DesignBranchColumns = "id, hidden_at, replaced_by, derived_from, derivation"

// DesignBranchNode — кадр карточки, каким его видит обход: видимость и два ребра — replaced_by
// (правка, занявшая место кадра) и derived_from с глаголом (кусок, отрезанный от кадра).
type DesignBranchNode struct {
	Id          int           `db:"id"`
	HiddenAt    sql.NullTime  `db:"hidden_at"`
	ReplacedBy  sql.NullInt32 `db:"replaced_by"`
	DerivedFrom sql.NullInt32 `db:"derived_from"`
	Derivation  string        `db:"derivation"`
}

// DesignStandingPieces — КУСКИ ЛИСТА sheetID, КОТОРЫЕ ЕЩЁ СТОЯТ (сторож cut_sheet), по возрастанию id.
// nodes — ВСЕ кадры карточки листа, прочитанные одним SELECT в транзакции флэттена (колонки —
// DesignBranchColumns); обход базы не трогает.
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
// дважды; кадр, которого нет среди кадров карточки; больше DesignStandingNodesMax кадров на запрос.
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
	if _, ok := w.byID[sheetID]; !ok {
		return nil, fmt.Errorf("design picture %d: the sheet is not among the pictures read for its tech card", sheetID)
	}
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
	broken := func(format string, args ...any) error {
		return fmt.Errorf("design picture %d: the branch of its pieces is broken: %s", w.sheet, fmt.Sprintf(format, args...))
	}
	if l.to <= l.from {
		return DesignBranchNode{}, broken("picture %d links to picture %d, which is not newer", l.from, l.to)
	}
	if _, seen := w.visited[l.to]; seen {
		return DesignBranchNode{}, broken("picture %d is reached twice, the second time from picture %d", l.to, l.from)
	}
	n, ok := w.byID[l.to]
	if !ok {
		return DesignBranchNode{}, broken("picture %d links to picture %d, which is not a picture of this tech card", l.from, l.to)
	}
	if len(w.visited) >= DesignStandingNodesMax {
		return DesignBranchNode{}, broken("more than %d pictures would be read", DesignStandingNodesMax)
	}
	w.visited[l.to] = struct{}{}
	return n, nil
}

// DesignSplitHiddenRefusal — РАЗРЕЗ СПРЯТАННОГО КАДРА (ErrDesignHiddenPicture). nil — кадр на виду.
// Одно правило на две двери: транзакция SplitPicture, где оно авторитетно, и предпроверка хендлера,
// которая лишь сберегает байтовую работу.
func DesignSplitHiddenRefusal(p DesignPicture) error {
	if p.HiddenAt.Valid {
		return fmt.Errorf("%w: picture %d is hidden; show it before cutting it", ErrDesignHiddenPicture, p.Id)
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
