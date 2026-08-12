package entity

import (
	"errors"
	"fmt"
	"strings"
)

// ФИЗИЧЕСКОЕ УДАЛЕНИЕ КОЛОРВЕЯ — узкая дырка в правиле «архивируем, не удаляем».
//
// Правило R6/R9 («archive-not-delete», коммит b3bcf40) верно для всего, что когда-либо ЖИЛО: у
// проданного колорвея заморожен SKU, на него ссылается история заказов, и стереть его значит
// переписать прошлое. Но у правила есть цена, которую платит не история, а человек: список
// колорвеев карточки навсегда зарастает опечатками и брошенными цветами, потому что уйти оттуда
// можно только в архив, а архив — это тоже строка на экране.
//
// Владелец провёл границу: удаляем ровно то, чего НИКОГДА НЕ БЫЛО. Не продан, не стоит ни в одной
// партии (включая ЧЕРНОВУЮ), не стоит ни в одном настиле, нет остатка. Всё остальное — архив, и
// отказ обязан НАЗВАТЬ факт, который держит: «продан», «стоит в партии #12», а не «нельзя».
//
// Черновая партия ДЕРЖИТ УДАЛЕНИЕ намеренно. Черновик дёшево править — оператор убирает колорвей
// из состава сам; молча снести чужие плановые строки за него мы права не имеем.
//
// Классификация живёт здесь, а не в сторе, ровно потому, что она — единственная часть удаления,
// которую можно проверить без базы: стор в этом репозитории тестируется только интеграционно.

// ErrColorwayNotDeletable — вердикт «удалять нельзя». Несёт причины в ColorwayDeletionVerdict,
// который стор возвращает РЯДОМ с этой ошибкой, поэтому API-слой может выложить каждый блокер
// отдельным field violation, а не одной строкой. API-слой отображает её в FailedPrecondition.
var ErrColorwayNotDeletable = errors.New("colourway cannot be deleted")

// Коды причин. Строка, а не enum, — по тому же соглашению, что и refusal у оценки расхода
// (common/techcard.proto): код стабилен, живёт в одном месте и не заводит нулевого значения,
// которое во всяком замороженном слепке читалось бы как «причины нет».
const (
	// Блокеры — граница владельца.
	ColorwayBlockerSold          = "sold"           // есть строка заказа
	ColorwayBlockerProductionRun = "production_run" // стоит в составе партии (любой, включая draft)
	ColorwayBlockerLay           = "lay"            // стоит в настиле
	ColorwayBlockerStock         = "stock"          // ненулевой остаток на складе

	// Блокеры остаточных RESTRICT. ЭТО СУЖЕНИЕ ГРАНИЦЫ ВЛАДЕЛЬЦА, и назвать его надо честно: по
	// его четвёрке колорвей с планом запаса или примеркой удаляем, а здесь — нет. Причина не в
	// осторожности, а в схеме: у обоих FK стоит RESTRICT, и сама СУБД откажет — вопрос лишь в том,
	// увидит ли оператор названный факт или сырой MySQL 1451. Расширить границу в другую сторону —
	// перевести эти FK в CASCADE/SET NULL — значит начать сносить план запаса и калечить историю
	// примерок, а это уже расширение РАЗРУШЕНИЯ, чего владелец не разрешал.
	ColorwayBlockerInventoryTarget = "inventory_target" // inventory_target.product_id RESTRICT
	ColorwayBlockerFitting         = "fitting"          // fitting.product_id RESTRICT
	ColorwayBlockerReferenced      = "referenced"       // RESTRICT, которого нет в перечислении: схема ушла вперёд

	// Каскад — собственность колорвея, уходит вместе с ним (ON DELETE CASCADE).
	ColorwayCascadeVariant         = "variant"
	ColorwayCascadeVariantPrice    = "variant_price"
	ColorwayCascadePrice           = "price"
	ColorwayCascadeMedia           = "media"
	ColorwayCascadeTag             = "tag"
	ColorwayCascadeTranslation     = "translation"
	ColorwayCascadeRecipeUsage     = "recipe_usage"
	ColorwayCascadeSizeConsumption = "size_consumption" // каскад ВТОРОГО уровня: через строку рецепта
	ColorwayCascadePieceMaterial   = "piece_material"
	ColorwayCascadePackagingRecipe = "packaging_recipe"
	ColorwayCascadeLabDipRound     = "lab_dip_round"
	ColorwayCascadeCostEvent       = "cost_event"
	ColorwayCascadeWaitlist        = "waitlist"
	ColorwayCascadeStockHistory    = "stock_history"
	ColorwayCascadeStyleLink       = "style_link"

	// Сироты — ON DELETE SET NULL. Ни блокер, ни каскад: запись переживёт удаление и потеряет
	// колорвей. Третья категория существует потому, что первые две не описывают этот исход: строку
	// никто не удалял и не спасал, у неё пропала личность.
	ColorwayOrphanMarker           = "orphan_marker"
	ColorwayOrphanMaterialMovement = "orphan_material_movement"
	ColorwayOrphanSample           = "orphan_sample"
	ColorwayOrphanTask             = "orphan_task"
)

// colorwayBlockerHowToFix — общий выход из любого блокера. Один на все: архив — это и есть
// «удалить» для всего, что уже прожило хоть что-то.
const colorwayBlockerHowToFix = "заархивируйте колорвей вместо удаления, либо снимите названную связь"

// ColorwayRunRef — партия, в составе которой стоит колорвей. Статус едет вместе с номером,
// потому что «черновик» и «в производстве» — разные разговоры с оператором, хотя блокируют оба.
type ColorwayRunRef struct {
	ID     int    `db:"id"`
	Status string `db:"status"`
}

// ColorwayLayRef — настил, ссылающийся на колорвей. Имя настила пустое у безымянного.
type ColorwayLayRef struct {
	ID    int    `db:"id"`
	RunID int    `db:"run_id"`
	Name  string `db:"name"`
}

// ColorwayCascadeCounts — сколько собственных строк колорвея уйдёт вместе с ним.
type ColorwayCascadeCounts struct {
	Variants         int // product_size
	VariantPrices    int // product_size_price (B-grade)
	Prices           int // product_price
	Media            int // product_media
	Tags             int // product_tag
	Translations     int // product_translation
	RecipeUsages     int // tech_card_colorway_usage
	SizeConsumptions int // tech_card_colorway_usage_consumption — ЧЕРЕЗ строку рецепта (каскад второго уровня)
	PieceMaterials   int // tech_card_piece_material
	PackagingRecipes int // packaging_recipe
	LabDipRounds     int // product_lab_dip_round
	CostEvents       int // product_cost_event
	Waitlist         int // product_waitlist
	StockHistory     int // product_stock_change_history
	StyleLinks       int // tech_card_product
}

// ColorwayOrphanCounts — сколько ЧУЖИХ записей переживут удаление и потеряют колорвей.
type ColorwayOrphanCounts struct {
	Markers           int // tech_card_marker.colorway_id
	MaterialMovements int // material_stock_movement.product_id
	Samples           int // sample.colorway_id
	Tasks             int // task.product_id
}

// ColorwayDeletionFacts — сырые факты о колорвее одним снимком. Стор читает их одним запросом и
// ПОВТОРНО внутри транзакции удаления: предикат, доказанный вне транзакции, — это гонка, а эта
// гонка удаляет.
type ColorwayDeletionFacts struct {
	ColorwayID int
	// Label — как называть колорвей в предложении: SKU, если он уже намят, иначе «цвет (код)».
	Label string

	Orders           int // РАЗНЫХ заказов, держащих колорвей
	OrderLines       int // строк заказа
	StockUnits       int // сумма остатков по вариантам
	Runs             []ColorwayRunRef
	Lays             []ColorwayLayRef
	InventoryTargets int
	Fittings         int

	Cascade ColorwayCascadeCounts
	Orphans ColorwayOrphanCounts
}

// ColorwayDeletionEntry — одна запись вердикта: стабильный код, готовая фраза, количество.
// Фраза собирается на сервере, а не на клиенте, по тому же доводу, что и refusal_text у оценки
// расхода: формулировка одна на систему, второй перевод на клиенте с ней разойдётся.
type ColorwayDeletionEntry struct {
	Reason string
	Text   string
	Count  int
}

// ColorwayDeletionVerdict — ответ на вопрос «можно ли удалить», одинаковый для сухого прогона и
// для настоящего удаления. Три списка, а не два: каскад — что умрёт, сироты — что осиротеет.
type ColorwayDeletionVerdict struct {
	ColorwayID int
	Label      string
	Deletable  bool
	Blockers   []ColorwayDeletionEntry
	Cascade    []ColorwayDeletionEntry
	Orphans    []ColorwayDeletionEntry
}

// ClassifyColorwayDeletion превращает факты в вердикт. Единственное место, где живёт предикат
// удаляемости: и сухой прогон, и удаление применяют ОДНО И ТО ЖЕ правило. Совпадение ОТВЕТОВ это
// не гарантирует и гарантировать не может — если между прогоном и подтверждением колорвей продали
// или поставили в партию, транзакция обязана решить иначе, и именно ради этого случая она читает
// факты заново. Одинаково здесь правило, а не результат.
//
// Пустые категории в списки НЕ попадают: «0 раскладок осиротеет» — это шум, который оператор
// научится пролистывать вместе с настоящими строками.
func ClassifyColorwayDeletion(f ColorwayDeletionFacts) ColorwayDeletionVerdict {
	v := ColorwayDeletionVerdict{ColorwayID: f.ColorwayID, Label: f.Label}

	// --- Блокеры: четвёрка владельца ---------------------------------------------------------
	if f.OrderLines > 0 {
		// Считаем ЗАКАЗЫ, а не строки: оператор мыслит заказами, а один заказ легко держит
		// несколько строк одного цвета в разных размерах. Падение на строки — защита от
		// невозможного (order_item.order_id NOT NULL + FK), а не альтернативная формулировка:
		// «продан: 0 заказов» было бы отказом, отрицающим сам себя.
		n := f.Orders
		if n == 0 {
			n = f.OrderLines
		}
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerSold,
			Count:  n,
			Text: fmt.Sprintf("продан: %s",
				pluralRU(n, "%d заказ", "%d заказа", "%d заказов")),
		})
	}
	if len(f.Runs) > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerProductionRun,
			Count:  len(f.Runs),
			Text:   fmt.Sprintf("стоит в %s", joinNames(runNames(f.Runs))),
		})
	}
	if len(f.Lays) > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerLay,
			Count:  len(f.Lays),
			Text:   fmt.Sprintf("стоит в %s", joinNames(layNames(f.Lays))),
		})
	}
	if f.StockUnits > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerStock,
			Count:  f.StockUnits,
			Text:   fmt.Sprintf("на складе %d шт", f.StockUnits),
		})
	}

	// --- Блокеры остаточных RESTRICT ---------------------------------------------------------
	// ОНИ СУЖАЮТ ГРАНИЦУ ВЛАДЕЛЬЦА: по его четвёрке такой колорвей удаляется, здесь — нет.
	// Выбора между «блокировать» и «не блокировать» тут нет — FK всё равно RESTRICT, и СУБД
	// откажет в любом случае. Выбор только между названным фактом и сырым MySQL 1451 в лице
	// оператора, ради устранения которого фича и написана. См. комментарий у констант выше.
	if f.InventoryTargets > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerInventoryTarget,
			Count:  f.InventoryTargets,
			Text: fmt.Sprintf("на него заведён %s",
				pluralRU(f.InventoryTargets, "%d план запаса", "%d плана запаса", "%d планов запаса")),
		})
	}
	if f.Fittings > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerFitting,
			Count:  f.Fittings,
			Text: fmt.Sprintf("на него записано %s",
				pluralRU(f.Fittings, "%d примерка", "%d примерки", "%d примерок")),
		})
	}

	v.Deletable = len(v.Blockers) == 0

	// --- Каскад ------------------------------------------------------------------------------
	c := f.Cascade
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeVariant, c.Variants,
		"%d размерная позиция (вариант)", "%d размерные позиции (варианта)", "%d размерных позиций (вариантов)")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeVariantPrice, c.VariantPrices,
		"%d цена второго сорта", "%d цены второго сорта", "%d цен второго сорта")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadePrice, c.Prices,
		"%d цена", "%d цены", "%d цен")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeMedia, c.Media,
		"%d привязка медиа", "%d привязки медиа", "%d привязок медиа")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeTag, c.Tags,
		"%d тег", "%d тега", "%d тегов")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeTranslation, c.Translations,
		"%d перевод", "%d перевода", "%d переводов")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeRecipeUsage, c.RecipeUsages,
		"%d строка рецепта", "%d строки рецепта", "%d строк рецепта")
	// Каскад ВТОРОГО уровня: пер-размерные нормы висят на строке рецепта, а не на колорвее, и
	// уходят вместе с ней. В диалоге они обязаны быть названы отдельно — «3 строки рецепта» ничего
	// не говорит о том, что вместе с ними исчезает ряд норм по размерам, который снимали руками.
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeSizeConsumption, c.SizeConsumptions,
		"%d пер-размерная норма расхода", "%d пер-размерные нормы расхода", "%d пер-размерных норм расхода")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadePieceMaterial, c.PieceMaterials,
		"%d привязка ткани к детали кроя", "%d привязки ткани к деталям кроя", "%d привязок ткани к деталям кроя")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadePackagingRecipe, c.PackagingRecipes,
		"%d строка упаковки", "%d строки упаковки", "%d строк упаковки")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeLabDipRound, c.LabDipRounds,
		"%d раунд лабдипа", "%d раунда лабдипа", "%d раундов лабдипа")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeCostEvent, c.CostEvents,
		"%d событие себестоимости", "%d события себестоимости", "%d событий себестоимости")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeWaitlist, c.Waitlist,
		"%d запись в листе ожидания", "%d записи в листе ожидания", "%d записей в листе ожидания")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeStockHistory, c.StockHistory,
		"%d запись истории остатка", "%d записи истории остатка", "%d записей истории остатка")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeStyleLink, c.StyleLinks,
		"%d связь со стилем", "%d связи со стилем", "%d связей со стилем")

	// --- Сироты ------------------------------------------------------------------------------
	// Раскладка, снятая ПОД этот колорвей, переживёт удаление и станет длиной, померенной ни на
	// чём: артикул, на котором её мерили, из неё исчезнет. Это не повод отказать (замер остаётся
	// верным для ткани), но оператор обязан узнать об этом ДО подтверждения, а не после.
	o := f.Orphans
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanMarker, o.Markers,
		"%d раскладка потеряет колорвей (замер останется, артикул из него исчезнет)",
		"%d раскладки потеряют колорвей (замер останется, артикул из него исчезнет)",
		"%d раскладок потеряют колорвей (замер останется, артикул из него исчезнет)")
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanMaterialMovement, o.MaterialMovements,
		"%d движение материала потеряет колорвей", "%d движения материала потеряют колорвей", "%d движений материала потеряют колорвей")
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanSample, o.Samples,
		"%d образец потеряет колорвей", "%d образца потеряют колорвей", "%d образцов потеряют колорвей")
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanTask, o.Tasks,
		"%d задача потеряет колорвей", "%d задачи потеряют колорвей", "%d задач потеряют колорвей")

	return v
}

// FieldViolations раскладывает блокеры в field-tagged отказы для apierr.FailedPreconditionMany —
// по одному на КАТЕГОРИЮ, а не один на весь отказ. Оператор, у которого колорвей и продан, и
// стоит в партии, должен увидеть обе причины за один заход: узнавать вторую после того, как
// снял первую, — это два круга там, где сервер знал ответ целиком с первого раза.
func (v ColorwayDeletionVerdict) FieldViolations() []*ValidationError {
	out := make([]*ValidationError, 0, len(v.Blockers))
	for _, b := range v.Blockers {
		out = append(out, NewFieldViolation("colorway_id", b.Reason, b.Text, colorwayBlockerHowToFix))
	}
	return out
}

// BlockerSummary — одна строка для сообщения статуса (клиент, читающий только message).
func (v ColorwayDeletionVerdict) BlockerSummary() string {
	if len(v.Blockers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(v.Blockers))
	for _, b := range v.Blockers {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "; ")
}

func appendEntry(dst []ColorwayDeletionEntry, reason string, n int, one, few, many string) []ColorwayDeletionEntry {
	if n <= 0 {
		return dst
	}
	return append(dst, ColorwayDeletionEntry{Reason: reason, Count: n, Text: pluralRU(n, one, few, many)})
}

// maxNamedObjects — сколько партий/настилов называем поимённо, прежде чем свернуть хвост в
// «и ещё N». Список из тридцати номеров — это уже не сообщение, а дамп.
const maxNamedObjects = 5

func runNames(runs []ColorwayRunRef) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, fmt.Sprintf("партии #%d (%s)", r.ID, runStatusRU(r.Status)))
	}
	return out
}

func layNames(lays []ColorwayLayRef) []string {
	out := make([]string, 0, len(lays))
	for _, l := range lays {
		if l.Name == "" {
			out = append(out, fmt.Sprintf("безымянном настиле партии #%d", l.RunID))
			continue
		}
		out = append(out, fmt.Sprintf("настиле «%s» партии #%d", l.Name, l.RunID))
	}
	return out
}

func joinNames(names []string) string {
	if len(names) <= maxNamedObjects {
		return strings.Join(names, ", ")
	}
	rest := len(names) - maxNamedObjects
	return fmt.Sprintf("%s и ещё %d", strings.Join(names[:maxNamedObjects], ", "), rest)
}

// runStatusRU — статус партии словом. Словарь закрыт (CHECK chk_production_run_status, 0298);
// незнакомое значение отдаём как есть, а не подменяем «неизвестно»: соврать про статус партии,
// которая держит удаление, хуже, чем показать сырой код.
func runStatusRU(status string) string {
	switch status {
	case "draft":
		return "черновик"
	case "planned":
		return "запланирована"
	case "in_progress":
		return "в производстве"
	case "partially_received":
		return "частично принята"
	case "received":
		return "принята"
	case "closed":
		return "закрыта"
	case "cancelled":
		return "отменена"
	default:
		return status
	}
}

// pluralRU подставляет n в ту из трёх форм, которой требует русский счёт: 1 заказ, 2 заказа,
// 5 заказов. Каждая форма — формат с одним %d, чтобы число и слово нельзя было развести местами.
func pluralRU(n int, one, few, many string) string {
	form := many
	mod100 := n % 100
	if mod100 < 11 || mod100 > 14 {
		switch n % 10 {
		case 1:
			form = one
		case 2, 3, 4:
			form = few
		}
	}
	return fmt.Sprintf(form, n)
}
