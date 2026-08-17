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
// отказ обязан НАЗВАТЬ факт, который держит: “sold”, “is in run #12”, а не «нельзя».
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
const colorwayBlockerHowToFix = "archive the colorway instead of deleting it, or remove the named link"

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
		// “sold: 0 orders” было бы отказом, отрицающим сам себя.
		n := f.Orders
		if n == 0 {
			n = f.OrderLines
		}
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerSold,
			Count:  n,
			Text: fmt.Sprintf("sold: %s",
				pluralEN(n, "%d order", "%d orders")),
		})
	}
	if len(f.Runs) > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerProductionRun,
			Count:  len(f.Runs),
			Text:   fmt.Sprintf("is in %s", joinNames(runNames(f.Runs))),
		})
	}
	if len(f.Lays) > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerLay,
			Count:  len(f.Lays),
			Text:   fmt.Sprintf("is in %s", joinNames(layNames(f.Lays))),
		})
	}
	if f.StockUnits > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerStock,
			Count:  f.StockUnits,
			Text:   fmt.Sprintf("%d pcs in stock", f.StockUnits),
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
			Text: fmt.Sprintf("it has %s",
				pluralEN(f.InventoryTargets, "%d inventory target", "%d inventory targets")),
		})
	}
	if f.Fittings > 0 {
		v.Blockers = append(v.Blockers, ColorwayDeletionEntry{
			Reason: ColorwayBlockerFitting,
			Count:  f.Fittings,
			Text: fmt.Sprintf("%s booked against it",
				pluralEN(f.Fittings, "%d fitting", "%d fittings")),
		})
	}

	v.Deletable = len(v.Blockers) == 0

	// --- Каскад ------------------------------------------------------------------------------
	c := f.Cascade
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeVariant, c.Variants,
		"%d size variant", "%d size variants")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeVariantPrice, c.VariantPrices,
		"%d B-grade price", "%d B-grade prices")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadePrice, c.Prices,
		"%d price", "%d prices")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeMedia, c.Media,
		"%d media link", "%d media links")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeTag, c.Tags,
		"%d tag", "%d tags")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeTranslation, c.Translations,
		"%d translation", "%d translations")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeRecipeUsage, c.RecipeUsages,
		"%d recipe line", "%d recipe lines")
	// Каскад ВТОРОГО уровня: пер-размерные нормы висят на строке рецепта, а не на колорвее, и
	// уходят вместе с ней. В диалоге они обязаны быть названы отдельно — «3 строки рецепта» ничего
	// не говорит о том, что вместе с ними исчезает ряд норм по размерам, который снимали руками.
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeSizeConsumption, c.SizeConsumptions,
		"%d per-size consumption norm", "%d per-size consumption norms")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadePieceMaterial, c.PieceMaterials,
		"%d fabric link to a cut piece", "%d fabric links to cut pieces")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadePackagingRecipe, c.PackagingRecipes,
		"%d packaging line", "%d packaging lines")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeLabDipRound, c.LabDipRounds,
		"%d lab dip round", "%d lab dip rounds")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeCostEvent, c.CostEvents,
		"%d cost event", "%d cost events")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeWaitlist, c.Waitlist,
		"%d waitlist entry", "%d waitlist entries")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeStockHistory, c.StockHistory,
		"%d stock history entry", "%d stock history entries")
	v.Cascade = appendEntry(v.Cascade, ColorwayCascadeStyleLink, c.StyleLinks,
		"%d style link", "%d style links")

	// --- Сироты ------------------------------------------------------------------------------
	// Раскладка, снятая ПОД этот колорвей, переживёт удаление и станет длиной, померенной ни на
	// чём: артикул, на котором её мерили, из неё исчезнет. Это не повод отказать (замер остаётся
	// верным для ткани), но оператор обязан узнать об этом ДО подтверждения, а не после.
	o := f.Orphans
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanMarker, o.Markers,
		"%d marker will lose the colorway (the measurement stays, the article disappears from it)",
		"%d markers will lose the colorway (the measurement stays, the article disappears from it)")
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanMaterialMovement, o.MaterialMovements,
		"%d material movement will lose the colorway", "%d material movements will lose the colorway")
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanSample, o.Samples,
		"%d sample will lose the colorway", "%d samples will lose the colorway")
	v.Orphans = appendEntry(v.Orphans, ColorwayOrphanTask, o.Tasks,
		"%d task will lose the colorway", "%d tasks will lose the colorway")

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

func appendEntry(dst []ColorwayDeletionEntry, reason string, n int, one, many string) []ColorwayDeletionEntry {
	if n <= 0 {
		return dst
	}
	return append(dst, ColorwayDeletionEntry{Reason: reason, Count: n, Text: pluralEN(n, one, many)})
}

// maxNamedObjects — сколько партий/настилов называем поимённо, прежде чем свернуть хвост в
// “and N more”. Список из тридцати номеров — это уже не сообщение, а дамп.
const maxNamedObjects = 5

func runNames(runs []ColorwayRunRef) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, fmt.Sprintf("run #%d (%s)", r.ID, runStatusLabel(r.Status)))
	}
	return out
}

func layNames(lays []ColorwayLayRef) []string {
	out := make([]string, 0, len(lays))
	for _, l := range lays {
		if l.Name == "" {
			out = append(out, fmt.Sprintf("an unnamed lay of run #%d", l.RunID))
			continue
		}
		out = append(out, fmt.Sprintf("lay “%s” of run #%d", l.Name, l.RunID))
	}
	return out
}

func joinNames(names []string) string {
	if len(names) <= maxNamedObjects {
		return strings.Join(names, ", ")
	}
	rest := len(names) - maxNamedObjects
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxNamedObjects], ", "), rest)
}

// runStatusLabel — статус партии словом. Словарь закрыт (CHECK chk_production_run_status, 0298);
// незнакомое значение отдаём как есть, а не подменяем «неизвестно»: соврать про статус партии,
// которая держит удаление, хуже, чем показать сырой код.
func runStatusLabel(status string) string {
	switch status {
	case "draft":
		return "draft"
	case "planned":
		return "planned"
	case "in_progress":
		return "in progress"
	case "partially_received":
		return "partially received"
	case "received":
		return "received"
	case "closed":
		return "closed"
	case "cancelled":
		return "cancelled"
	default:
		return status
	}
}

// pluralEN substitutes n into whichever of the two forms English counting requires: 1 order,
// 2 orders. Each form is a format with a single %d, so the number and the word can never be
// pulled apart. Only n == 1 takes the singular — 0 and negatives read as plural ("0 orders"),
// which is what English says.
func pluralEN(n int, one, many string) string {
	form := many
	if n == 1 {
		form = one
	}
	return fmt.Sprintf(form, n)
}
