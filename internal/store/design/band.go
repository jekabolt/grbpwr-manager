package design

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// designFeedKinds — ЧТО ЛЕНТА ПОКАЗЫВАЕТ, И ЧЕГО ОНА НЕ ПОКАЗЫВАЕТ (B-21).
//
// Владелец дословно: «генерация DRAFT OF THE CONSTRUCTION не долна попадать в историю генераций в
// принципе». Текстовый черновик — строка РЕЕСТРА, а не ленты: он оплачен, у него есть попытка,
// оценка и снимок входов, он двигает дневной бюджет — но у него нет ни кадра, ни верстака, ни
// колорвея, а его ответ живёт на самом органе черновика, под кнопкой, а не в ленте.
//
// ⚠ ПРЕДИКАТ СТОИТ НА СЕРВЕРЕ, И ЭТО НЕ ВКУС. Заголовок ленты («N runs», «page 1 of M»), притязание
// firstRunId (runs.length >= totalRuns) и полка архива делят ЗАГРУЖЕННЫЕ строки на числа, которые
// приходят ОТСЮДА. Клиентский `.filter` заставил бы каждое из этих трёх чисел врать ровно на число
// черновиков — и врать правдоподобно, то есть незаметно.
//
// ⚠ ГДЕ ЕГО НЕТ И ПОЧЕМУ, ПОИМЕННО:
//   - designMaxRrev — черновик не несёт rrev вовсе (RequestedOutputs: 0), считать его там нечего;
//   - деньги (design_budget_day, design_run.price_*) — оплаченный вызов без строки в регистре это
//     дыра в бухгалтерии, и убрать его оттуда владелец не просил;
//   - GetRun(run_id) — прямое чтение по id не лента; строка остаётся живой, читаемой и ценной.
//
// ⚠ КУРСОР ОТ ЭТОГО НЕ ДВИГАЕТСЯ: предикат СУЖАЕТ набор, а `id < :cursor` под `ORDER BY id DESC`
// остаётся тем же ключом (тот же довод, что у `archived_at` ниже в listRunsTx).
//
// ⚠ РОД СОБИРАЕТСЯ ИЗ КОНСТАНТЫ, А НЕ ВПИСАН СТРОКОЙ: вторая, написанная от руки копия
// 'draft_idea' разошлась бы с entity.DesignRunKindDraftIdea в тот день, когда род переименуют, и
// разошлась бы МОЛЧА — предикат остался бы синтаксически верным и перестал бы что-либо прятать.
const designFeedKinds = `kind <> '` + entity.DesignRunKindDraftIdea + `'`

// THE FOUR HEADER AGGREGATES, AS NAMED CONSTANTS.
//
// They are constants rather than inline strings so that «counted over the whole card» is a
// property a test can CITE and a mutation can break. Every one of them is scoped by
// tech_card_id and by nothing else: no LIMIT, no cursor, no join to the page. Counting the loaded
// page instead would truncate the header by exactly the amount that is not on the screen — a card
// with forty runs would caption itself «12», and the number would look plausible.
//
// ⚠ ДВА ИЗ НИХ СЧИТАЮТ ЛЕНТУ, А НЕ РЕЕСТР (B-21): у обоих счётчиков прогонов стоит designFeedKinds,
// потому что число в заголовке ленты обязано совпадать с тем, что в ленте лежит. Третий (rrev) и
// счётчик пачек нетронуты — они отвечают на другие вопросы.
const (
	designCountRuns         = `SELECT COUNT(*) FROM design_run WHERE tech_card_id = :card AND ` + designFeedKinds
	designCountArchivedRuns = `SELECT COUNT(*) FROM design_run WHERE tech_card_id = :card AND archived_at IS NOT NULL AND ` + designFeedKinds
	designMaxRrev           = `SELECT COALESCE(MAX(rrev), 0) FROM design_run WHERE tech_card_id = :card`
	designCountBatches      = `SELECT COUNT(*) FROM design_batch WHERE tech_card_id = :card`
	// designCountFabricRenders — «ЕСТЬ ЛИ У КАРТОЧКИ ФАБРИК-РЕНДЕРЫ ВООБЩЕ» (W-13). Считается по
	// ВСЕЙ карточке и только по НЕСПРЯТАННЫМ кадрам: спрятанный рендер человек уже отверг.
	//
	// ⚠ ЭТО БОЛЬШЕ НЕ ТО, ЧТО ОТКРЫВАЕТ 3D, и прежняя формулировка («открывать им 3D значило бы
	// обещать дверь, за которую сервер откажет») эту волну не пережила: дверь открывает
	// designRenderBenchColorways — ЗАНЯТЫЕ РЕНДЕР-СЛОТЫ, потому что именно из них прогон собирает
	// входы. Два ответа законно расходятся: загруженный, но не поставленный рендер даёт здесь
	// единицу и оставляет то множество пустым. Оставлено как подсказка пустого состояния
	// («рендеров нет» против «рендеры есть, разложи их»), см. DesignBand.HasFabricRender.
	designCountFabricRenders = `SELECT COUNT(*) FROM design_picture
		WHERE tech_card_id = :card AND kind = 'render' AND hidden_at IS NULL`
	// designRenderBenchColorways — ВОРОТА 3D, И ОНИ СПРАШИВАЮТ ВЕРСТАК, А НЕ ПОЛОСУ (D5).
	//
	// Гейт обязан задавать РОВНО ТОТ ЖЕ ВОПРОС, что и отбор входов. 3D читает не картинки
	// карточки, а ЗАНЯТЫЕ РЕНДЕР-СЛОТЫ своего колорвея (designSelectBench: род слота = render,
	// колорвей слота = колорвей прогона, у слота есть плита с медиа). Прежний счёт по
	// design_picture отвечал на другой вопрос — «есть ли на карточке такой файл», — и расходился
	// с отбором ровно в главном случае: загруженный, но НЕ ПОСТАВЛЕННЫЙ рендер открывал дверь,
	// деньги дня резервировались, прогон уходил в работу с ПУСТЫМ набором плит. Оплаченный
	// прогон без входов — не редкость, а нормальный порядок работы: файл загружают раньше, чем
	// решают, на какую сторону его положить.
	//
	// JOIN на design_picture повторяет две последние проверки отбора (`slot.Picture != nil`,
	// `MediaId > 0`).
	//
	// ⚠ ТРЕТИЙ ПРЕДИКАТ, `hidden_at IS NULL`, У ОТБОРА ОТСУТСТВУЕТ, И ЭТО НАМЕРЕННАЯ
	// НЕСИММЕТРИЯ, а не «тот же вопрос» (F8). Отбор плит спрятанность не смотрит вовсе, а
	// attachSlotPictures прикрепляет спрятанную плиту наравне с прочими. То есть гейт СТРОЖЕ
	// отбора: множество занятых верстаков ⊆ множество верстаков, из которых отбор что-нибудь
	// возьмёт.
	//
	// НЕСИММЕТРИЯ ВЫБРАНА В СТОРОНУ ДЕНЕГ И ПРОВЕРЕНА ПО ОБОИМ ИСХОДАМ:
	//   * ложный ОТКАЗ (все плиты колорвея спрятаны, гейт закрыт) стоит одного клика «показать»;
	//   * ложное РАЗРЕШЕНИЕ (гейт открыт, отбор пуст) стоит оплаченного прогона без входов —
	//     ровно того, что D5 и закрывает.
	// Убрать `hidden_at` отсюда значило бы открывать дверь по спрятанной плите; добавить его в
	// отбор — вторая правка, меняющая ПОВЕДЕНИЕ уже уехавшего 3D (сегодня спрятанная плита в
	// слоте кормит прогон), и она этой волне не принадлежит.
	//
	// Остаточный перекос: у колорвея front спрятан, back виден — гейт открывает back, а прогон
	// возьмёт ОБА, включая спрятанный front. Это не потеря денег, но и не то, чего человек ждёт
	// от «спрятать». Долг записан здесь; чинится он в designSelectBench, вместе с решением, что
	// вообще значит спрятанная плита в занятом слоте (сегодня hidePictureGuards её не создаёт —
	// прятать стоящую в слоте отказано, — так что состояние достижимо только постановкой
	// спрятанной плиты либо прямой правкой базы).
	//
	// NULL схлопывается в 0: неатрибутированный легаси-верстак открывает дверь только
	// безколорвейному 3D. Скоуп — ВСЯ карточка, никакой страницы, ровно как у счётчика выше.
	designRenderBenchColorways = `SELECT DISTINCT COALESCE(s.colorway_id, 0) AS cw
		FROM design_bench_slot s
		JOIN design_picture p ON p.id = s.picture_id
		WHERE s.tech_card_id = :card AND s.kind = 'render'
		  AND p.media_id > 0 AND p.hidden_at IS NULL
		ORDER BY cw`
	// designListLayers — ПРОЕКЦИЯ СЛОЁВ ДЛЯ ПОЛОСЫ, И ОНА ИМЕНОВАННАЯ, ПОЭТОМУ КАЖДАЯ НОВАЯ
	// КОЛОНКА ПОПАДАЕТ СЮДА РУКАМИ. Пропуск не падает и ничего не логирует — полоса просто
	// сервирует поле нулём, и это ровно та форма, которую читают экраны: три колонки 0350 без
	// строки здесь сделали бы каждый слой «drawn» без файла за ним, а `raster_media_id` (0355) —
	// каждый закрашенный слой неотличимым от пустого до открытия редактора.
	//
	// `strokes` ЗДЕСЬ НЕТ НАМЕРЕННО, и это не противоречие: 512 KB на слой, слоёв на карточке
	// несколько, и список миниатюр не обязан возить их все. Растр — голый id, а не килобайты.
	designListLayers = `
		SELECT id, tech_card_id, base_media_id, rev, origin, source_media_id,
		       source_picture_id, raster_media_id, updated_by, updated_at
		FROM design_edit_layer WHERE tech_card_id = :card ORDER BY id`
	// designCardOutputsFrom / designCardOutputsWhere / designCardOutputsColorway — ГЕНЕРАТИВНЫЕ
	// ВЫХОДЫ КАРТОЧКИ, ОДИН ПРЕДИКАТ И ОДИН КЛЮЧ РАЗДЕЛА НА ДВА ЗАПРОСА.
	//
	// Список и счётчик разнесены по разным вызовам, но предикат у них ОБЯЗАН быть буквально один:
	// две копии разошлись бы при первой же правке словаря родов, и разошлись бы молча — сервер
	// сказал бы «выходов 14 из 20» там, где их ровно 14. Поэтому условие объявлено один раз и
	// подставляется в оба.
	//
	// ТО ЖЕ САМОЕ И СИЛЬНЕЕ — ПРО КЛЮЧ КОЛОРВЕЯ. Список режется окном PARTITION BY по этому
	// выражению, а счётчик группируется по нему же. Две копии здесь дали бы не «счёт разошёлся с
	// длиной», а «подпись раздела считана НЕ ПО ТЕМ строкам, которые в разделе лежат», — то есть
	// клиент нарисовал бы «60 из 143» над списком, где 143 относится к другому множеству.
	//
	// ЧТО СЮДА ВХОДИТ. Кадр из ПРОГОНА рода render|threed|pattern|recolor — род берётся у
	// ПРОГОНА, а не у кадра, и это не педантизм: перекрас рождает кадры рода `render`
	// (DesignPictureKindOfRun), и отбор по роду КАДРА подмешал бы результаты ON MODEL в рендеры,
	// а штамп ниже не смог бы их развести. Плюс кадр БЕЗ прогона рода render|threed|pattern —
	// загруженный вручную рендер это рендер карточки, а пометка «выбран» пишется по id любого
	// кадра: плита, которой нет в списке, не может быть выбрана.
	//
	// КРОПЫ ВХОДЯТ САМИ СОБОЙ и это половина всей задачи: разрез наследует run_id родителя
	// (pictures.go), значит кроп листа рендера — такой же кадр прогона рода render, как и лист.
	//
	// СПРЯТАННЫЕ ВХОДЯТ СО СВОИМ ФЛАГОМ — контракт полосы («hidden и archived едут С ФЛАГАМИ,
	// фильтрует клиент»). Добавить сюда hidden_at IS NULL значило бы завести второе, невидимое
	// место, где кадр исчезает.
	//
	// ⚠ ПЛЕЙГРАУНД (`freeform`, `cutout`) ВХОДИТ СЮДА ЖЕ, И ЭТО РЕШЕНИЕ, А НЕ ДОБАВКА В СПИСОК.
	// Выход плейграунда — выход КАРТОЧКИ: он родился на её бюджете, лежит в её ленте, и человек
	// ищет его там, где лежит всё остальное сгенерированное. Своего запроса ему заводить нельзя —
	// это был бы второй предикат «выходы карточки», а таких предикатов, как сказано выше, не
	// бывает двух согласных. Клиент сужает раздел по `run_kind`, ровно как уже сужает перекрас.
	//
	// СТРОКА КАДРА БЕЗ ПРОГОНА ЭТИХ РОДОВ НЕ ЗНАЕТ, И ЭТО ВЕРНО: кадром рода `freeform`/`cutout`
	// нельзя стать загрузкой руками — оба рода существуют только как ВЫХОД прогона.
	designCardOutputsFrom = `
		FROM design_picture p
		LEFT JOIN design_run r ON r.id = p.run_id`
	designCardOutputsWhere = `
		WHERE p.tech_card_id = :card
		  AND ((p.run_id IS NOT NULL AND r.kind IN ('render', 'threed', 'pattern', 'recolor', 'freeform', 'cutout', 'extend', 'inpaint'))
		    OR (p.run_id IS NULL AND p.kind IN ('render', 'threed', 'pattern')))`
	// designCardOutputsColorway — КЛЮЧ РАЗДЕЛА: колорвей САМОГО КАДРА, 0 = неатрибутированный.
	//
	// ПОЧЕМУ КОЛОРВЕЙ КАДРА, А НЕ ПРОГОНА. Это не «ещё одна такая же колонка», и выбор здесь
	// решает, где кадр окажется на экране:
	//
	//   - У КАЖДОГО кадра, рождённого прогоном, они РАВНЫ — не по совпадению, а по записи:
	//     queue.go кладёт в кадр `nullInt32(run.ColorwayId)`, кроп наследует колорвей родителя
	//     (pictures.go), флэттен тоже (layer.go). Так что для всего сгенерированного спор пустой.
	//   - РАСХОДЯТСЯ они ровно на кадре БЕЗ прогона: у загруженной вручную рендер-плиты
	//     run_colorway_id = 0 (прогона нет), а колорвей у неё назван и настоящий. Ключ по прогону
	//     свалил бы её в «неатрибутированный» раздел — то есть плита колорвея BLK лежала бы в
	//     чужом разделе и не выбиралась бы из своего.
	//   - И это ТОТ ЖЕ ключ, по которому карточка уже сужается везде: designBenchForColorway
	//     сравнивает `DesignColorwayOrNone(slot.ColorwayId)` — колорвей ПЛИТЫ, не прогона.
	//
	// Одно имя — один ключ. Разные ключи под одним словом «колорвей» и есть тот дефект, который
	// эта строка закрывает.
	designCardOutputsColorway = `COALESCE(p.colorway_id, 0)`
	// designCardOutputsSection — ВТОРАЯ ОСЬ ОКНА: 1 = выход плейграунда, 0 = всё остальное.
	//
	// ⚠ ЗАЧЕМ ВТОРАЯ ОСЬ, ЕСЛИ ПОТОЛОК УЖЕ ПОКОЛОРВЕЙНЫЙ. Плейграунд не читает карточку вовсе, и
	// колорвея у его прогона нет: КАЖДЫЙ его выход ложится в раздел 0 — тот же, где живут
	// неатрибутированные рендеры, загруженные плиты без колорвея и 3D. Потолок в 60 на раздел 0
	// поэтому тратится общим котлом: шестьдесят одна свободная игра в плейграунде вытесняет из
	// ответа СТАРЫЙ рендер того же раздела целиком — а клиент, сузивший раздел по `run_kind`,
	// получает ПУСТО и не может отличить это от «рендеров не делали». Это ровно дефект H-9,
	// отложенный на одну ось: голодание внутри раздела вместо голодания раздела.
	//
	// ⚠ И ЭТО НЕ «ЕЩЁ ОДИН ПОТОЛОК», А ТА ЖЕ ОСЬ, ПО КОТОРОЙ СУЖАЕТ ЭКРАН. Клиент делит ленту
	// выходов на разделы по `run_kind` (RENDERS / ON MODEL / 3D / PATTERNS против плейграунда), и
	// потолок обязан тратиться по той оси, по которой читатель сужает, — тот же довод, слово в
	// слово, по которому он стал поколорвейным. Двух буквальных родов здесь мало: делить окно на
	// девять родов значило бы возить до девяти потолков на колорвей, а плейграунд от прочих
	// отличается не родом, а происхождением — он единственный, кто НЕ ПРИВЯЗАН к колорвею.
	//
	// ⚠ ЧТО ЭТО СТОИТ, ЧЕСТНО. Худший ответ вырастает вдвое: (колорвеи + 1) × 2 × 60 вместо
	// (колорвеи + 1) × 60. Плата за то, что оплаченный рендер не исчезает с экрана из-за
	// бесплатных игр в соседней вкладке того же раздела.
	//
	// ОСТАТОЧНЫЙ ДОЛГ, НАЗВАННЫЙ ВСЛУХ: OutputsTotalByColorway на проводе остаётся ПОКОЛОРВЕЙНЫМ
	// (сумма обеих секций) — это поле контракта, и менять его форму эта правка не станет. Для
	// читателя, сузившего по плитке плейграунда, долг закрыт вторым полем провода —
	// OutputsTotalByWorkflow (band 31), которое считается тем же выражением, что режет окно
	// (designCardOutputsWorkflow).
	//
	// ⚠ PHASE 3: extend/inpaint JOINED this list (and nothing else changed) — section 1 is «the
	// playground's own pool, split per tile». recolor and threed stay in section 0 on purpose:
	// their outputs keep colourway semantics (one pool per colourway, 04-DECISIONS D4), so tiles
	// 4/5/12 can still be evicted by 60 renders of the same colourway — accepted, not missed.
	designCardOutputsSection = `CASE WHEN COALESCE(r.kind, '') IN ('freeform', 'cutout', 'extend', 'inpaint') THEN 1 ELSE 0 END`

	// designCardOutputsFabricPicture — «this recolor run sent at least one cloth WITH A PICTURE»:
	// some params.colour.fabrics[i].media_id > 0. The SQL twin of the door's
	// designAnyClothWithPicture (apisrv/admin/design_run.go), which is what entity.DesignWorkflowOf
	// receives as hasFabricPicture — the rule is «a cloth with a picture», NOT «any cloth row»: a
	// cloth stated in words alone (media_id 0) never reaches the model, so the run changed a colour.
	//
	// ⚠ WHY A REGEX AND NOT JSON_LENGTH OF THE PATH. `JSON_EXTRACT(params,
	// '$.colour.fabrics[*].media_id')` is the array of every media_id KEY PRESENT (NULL when none),
	// e.g. `[0, 12]`. Its LENGTH counts keys, not pictures: it says «swap» for an explicit
	// `media_id: 0`, a negative id (the door's media boundary skips ids <= 0, so one can be frozen)
	// and a JSON null — measured on MySQL 8.0.46 and 8.4.10, all three disagree with the Go rule. The regex asks
	// the array's text for a number token that starts with 1-9 and is not preceded by '-' or a digit,
	// i.e. «some element > 0» for integers (JSON forbids leading zeros, so `0` is the only zero);
	// a quoted "15" (protojson accepts it) matches too, as Go reads it as 15.
	// NULL (no params, no colour, no fabrics, no media_id keys) → REGEXP_LIKE(NULL) → NULL → the
	// CASE below takes its ELSE, i.e. change_color, exactly like Go's false.
	//
	// ⚠ NO COLON ANYWHERE IN THIS TEXT: the statements go through sqlx named binding, and `:x`
	// would become a bind parameter (so no `[[:<:]]` word boundaries).
	designCardOutputsFabricPicture = `REGEXP_LIKE(CAST(JSON_EXTRACT(r.params, '$.colour.fabrics[*].media_id') AS CHAR), '(^|[^-0-9])[1-9]')`

	// designCardOutputsWorkflow — THE run_workflow EXPRESSION: which PLAYGROUND tile an output
	// belongs to. The SQL twin of entity.DesignWorkflowOf(kind, preset, hasFabricPicture) and it
	// must read the same table (the twin is proved by
	// apisrv/admin/design_feed_workflow_twin_test.go — statically against the Go table, and live on
	// a throwaway MySQL when DESIGN_FEED_SQL_DSN is set):
	//
	//   - freeform: the frozen preset picks the tile; '' / NULL / missing freeform / NULL params /
	//     free / add_hardware / repaint_parts / any unknown word → create_edit (ELSE), so a run can
	//     never fall out of the grid. JSON_UNQUOTE returns utf8mb4_bin, so the match is
	//     case-sensitive like Go's `switch`; a JSON null preset unquotes to 'null' → ELSE, correct.
	//   - cutout → remove_background; threed → image_to_3d; extend → extend_image; inpaint →
	//     retouch_zone (phase 3);
	//   - recolor → swap_fabrics when a cloth carries a picture (designCardOutputsFabricPicture),
	//     else change_color;
	//   - every other kind, and a picture with no run (the LEFT JOIN gives NULL) → ''.
	//
	// ⚠ ONE EXPRESSION, THREE USES, NEVER A COPY AND NEVER AN ALIAS: it is the stamp
	// (`AS run_workflow`), it is inside the window key (designCardOutputsWindowKey, spelled out in
	// PARTITION BY — a same-SELECT alias is not what MySQL partitions by) and it is the count's
	// group key. A second spelling that drifted by one preset would caption a tile with a number
	// counted over other rows, or cut its window over other rows than the ones it stamps.
	designCardOutputsWorkflow = `CASE COALESCE(r.kind, '')
			WHEN 'freeform' THEN CASE COALESCE(JSON_UNQUOTE(JSON_EXTRACT(r.params, '$.freeform.preset')), '')
				WHEN 'tryon' THEN 'virtual_try_on'
				WHEN 'fabric_extract' THEN 'fabric_to_image'
				WHEN 'ghost_mannequin' THEN 'ghost_mannequin'
				WHEN 'add_logo' THEN 'add_logo'
				WHEN 'variations' THEN 'design_variations'
				WHEN 'retouch' THEN 'retouch_zone'
				ELSE 'create_edit' END
			WHEN 'cutout' THEN 'remove_background'
			WHEN 'extend' THEN 'extend_image'
			WHEN 'inpaint' THEN 'retouch_zone'
			WHEN 'recolor' THEN CASE WHEN ` + designCardOutputsFabricPicture + ` THEN 'swap_fabrics' ELSE 'change_color' END
			WHEN 'threed' THEN 'image_to_3d'
			ELSE '' END`
)

// designCardOutputsWindowKey — THE THIRD AXIS OF THE WINDOW: the workflow, but ONLY inside
// section 1 (the playground's pool); the empty string everywhere else.
//
// WHY. Every playground run carries colourway 0, so all its tiles shared ONE window of 60 on
// (0, section 1): a busy tile (sixty Create/Edit plays) evicted a quiet tile's results entirely and
// that tile's panel came back empty — the same H-9 starvation the section axis was added to stop,
// moved one axis down (04-DECISIONS D4). Splitting by workflow inside section 1 gives every tile its
// own «newest 60». Section 0 is NOT split: recolor and threed keep the per-colourway pool they
// share with renders (see designCardOutputsSection), which is why the key is empty there rather than
// the workflow.
//
// ⚠ BUILT FROM ITS TWO PIECES, NEVER WRITTEN OUT: the builder derives it from the SAME section and
// workflow text it stamps and groups by, so a window key that disagrees with the stamp is not
// representable (the probes check the composition: design_shape_test.go, compile-only, and the
// runnable apisrv/admin/design_feed_workflow_twin_test.go).
//
// THE COUNT REFINES THIS KEY, IT DOES NOT COPY IT. The count groups by the full workflow (so
// OutputsTotalByWorkflow names recolor and threed tiles too — the contract of band 31 counts every
// non-empty run_workflow), and the key is a function of (section, workflow): every window partition
// is exactly a union of count groups, so per-colourway and per-workflow totals are both sums over
// the same rows the window cuts.
func designCardOutputsWindowKey(section, workflow string) string {
	return `CASE WHEN ` + section + ` = 1 THEN ` + workflow + ` ELSE '' END`
}

// designListCardOutputs / designCountCardOutputsByColorway — список выходов и поколорвейный счёт,
// СОБРАННЫЕ ОДНОЙ ФУНКЦИЕЙ ИЗ ОДНИХ КУСКОВ.
//
// ⚠ ПОЧЕМУ ФУНКЦИЯ, А НЕ ДВЕ КОНСТАНТЫ СО СКЛЕЙКОЙ. Прежняя редакция склеивала те же куски прямо
// в двух объявлениях, и проверить это можно было только `strings.Contains(запрос, кусок)` — а
// такую проверку проходит и побайтно вписанная копия. То есть сторож ловил «кусок пропал» и НЕ
// ловил «кусок продублирован и потом разошёлся», ради чего он и заведён. Через builder это
// проверяется по существу: проба зовёт его с ЧАСОВЫМИ вместо предиката и ключа и убеждается, что
// в готовых запросах не осталось ни одного слова настоящего предиката (design_shape_test.go).
var designListCardOutputs, designCountCardOutputsByColorway = designCardOutputsStatements(
	designCardOutputsFrom+designCardOutputsWhere, designCardOutputsColorway, designCardOutputsSection,
	designCardOutputsWorkflow)

// designCardOutputsStatements строит оба запроса выходов из ОБЛАСТИ (FROM+WHERE) и ДВУХ КЛЮЧЕЙ
// РАЗДЕЛА — колорвея и секции.
//
// Список — сами строки, СВЕЖИЕ ПЕРВЫМИ, со штампом прогона рядом, по MaxCardOutputsPerColorway на
// (колорвей, секцию). COALESCE стоит на всех трёх колонках прогона потому, что JOIN у кадра из пачки НЕ
// СОВПАДАЕТ: без него сканер получил бы NULL в string/int и упал бы на первой же загруженной
// плите. Ноль и пустая строка здесь читаются как «прогона нет», и штамп это говорит явно.
//
// ⚠ ПОТОЛОК ТРАТИТСЯ ОКНОМ, А НЕ ОДНИМ LIMIT НА ВСЮ КАРТОЧКУ. `ORDER BY p.id DESC LIMIT 200`
// выбрасывал самые старые строки карточки ЦЕЛИКОМ — а читатель сужает по колорвею, и раздел
// колорвея, все кадры которого старше двухсотого id, выходил ПУСТЫМ. Это тот самый дефект,
// который волна закрывает, просто отодвинутый на 200 строк (довод целиком — у
// MaxCardOutputsPerColorway). ROW_NUMBER PARTITION BY даёт каждому разделу собственное окно,
// поэтому объём одного колорвея физически не может съесть другой.
//
// ⚠ И ТОТ ЖЕ ДОВОД ВТОРЫМ КЛЮЧОМ — СЕКЦИЕЙ. Выходы плейграунда колорвея не имеют вовсе и ложатся
// в раздел 0 рядом с неатрибутированными рендерами и 3D; одноключевое окно позволяло шестидесяти
// бесплатным играм вытеснить оттуда оплаченный рендер целиком, и клиент, сузивший по `run_kind`,
// видел пусто. Довод и цена — у designCardOutputsSection.
//
// Порядок внутри окна и на выходе — по id кадра убыванием: id монотонен, значит это «свежие
// первыми» без обращения к created_at, и он же ключ индекса idx_design_picture_card
// (tech_card_id, id). Внутри окна он решает, ЧТО остаётся при усечении (свежие), снаружи — в
// каком порядке это приходит клиенту.
//
// ЧТО ЭТО СТОИТ, ЧЕСТНО. До окна план был обратным сканом индекса без сортировки вовсе. Теперь
// EXPLAIN показывает `Using temporary; Using filesort` — но ДОСТУП К ТАБЛИЦЕ НЕ ИЗМЕНИЛСЯ: тот же
// `ref` по idx_design_picture_card, то есть трогаются кадры ОДНОЙ карточки, а прогон приходит
// `eq_ref` по первичному ключу. Сортируется, стало быть, ровно то, что полоса и так читает целиком
// в соседних запросах (loadHiddenCounts, designCountFabricRenders). Внешняя сортировка идёт уже по
// (колорвеи × 2 × 60) строкам. Это цена за то, чтобы усечение не выкашивало раздел, и она
// заплачена сознательно.
//
// Счёт — сколько их ВСЕГО, тем же предикатом, ТЕМИ ЖЕ ДВУМЯ ключами раздела и без потолка. Существует
// затем, чтобы усечение было измеримо ИМЕННО ТЕМ читателем, который сузил: «больше двухсот где-то
// на карточке» не подписывает раздел одного колорвея. Карточный итог складывается из этих же
// чисел — второй COUNT(*) был бы вторым источником правды о том же множестве.
// ⚠ ОКНО РЕЖЕТСЯ ДВУМЯ КЛЮЧАМИ — колорвеем И секцией (designCardOutputsSection), — И СЧЁТ
// ГРУППИРУЕТСЯ ТЕМИ ЖЕ ДВУМЯ. Ключ, разошедшийся между списком и счётом, подписал бы раздел
// числом, посчитанным не по тем строкам; поэтому оба выражения приходят сюда параметрами и
// подставляются в оба запроса, а поколорвейный итог складывается уже в Go (loadCardOutputs).
//
// ⚠ PHASE 2 (PLAYGROUND): A THIRD WINDOW AXIS AND A STAMP, FROM ONE MORE PARAMETER. `workflow` is
// designCardOutputsWorkflow; the builder spells it out THREE times and never lets anyone else:
//
//   - list: `<workflow> AS run_workflow` — the stamp each output carries;
//   - list: PARTITION BY colorway, section, designCardOutputsWindowKey(section, workflow) — the
//     FULL expression, never the `run_workflow` alias of the same SELECT;
//   - count: `<workflow> AS workflow` and GROUP BY the same full expression.
//
// The count groups by the workflow itself, not by the window key: that is a REFINEMENT of the
// window partition (the key is a function of section and workflow), so each window partition is
// a union of count groups and both Go sums (per colourway, per workflow) stay exact.
//
// THE ACTIVE RUN IS NOT PINNED HERE, AND THAT IS STATED, NOT FORGOTTEN (05-CODEX-S02 §9). There is
// no server-side union with «the run in progress»: a run still working has no outputs to list, and
// its live placeholder is the client's, drawn from `runs` (the first history page of this same
// read). A run that just finished owns the highest picture ids of its partition, and the window is
// newest-first per (colourway, section, workflow-in-section-1) — so another tile's traffic can no
// longer push it out (that is what the third axis buys); only newer outputs OF THE SAME TILE can.
func designCardOutputsStatements(scope, colorway, section, workflow string) (list, count string) {
	key := designCardOutputsWindowKey(section, workflow)
	list = `
		SELECT o.* FROM (
			SELECT p.*,
			       COALESCE(r.kind, '') AS run_kind,
			       COALESCE(r.rrev, 0) AS run_rrev,
			       COALESCE(r.colorway_id, 0) AS run_cw,
			       ` + workflow + ` AS run_workflow,
			       ROW_NUMBER() OVER (
			           PARTITION BY ` + colorway + `, ` + section + `, ` + key + `
			           ORDER BY p.id DESC
			       ) AS rn` + scope + `
		) o
		WHERE o.rn <= :per_colorway
		ORDER BY o.id DESC`
	count = `
		SELECT ` + colorway + ` AS colorway_id, ` + section + ` AS section, ` + workflow + ` AS workflow, COUNT(*) AS n` + scope + `
		GROUP BY ` + colorway + `, ` + section + `, ` + workflow
	return list, count
}

// GetBand reads the whole band in ONE read transaction.
//
// THE AGGREGATES ARE COUNTED IN THAT SAME TRANSACTION, over the WHOLE card, never over the page
// that happens to have been loaded. total_runs, archived_runs, MAX(rrev), the colour-history
// chips and hidden_by_run are all "how much of this exists", and computing them from the loaded
// slice would silently truncate the header and the chips to whatever fitted in the page. That is
// not an optimisation question; a header that says «12 runs» when there are forty is wrong.
//
// ARCHIVED ROWS ARE IN THE PAGE, carrying their flag. The contract is explicit that hidden and
// archived travel WITH their flags and the client filters (Д1) — the server never lies about
// what exists.
func (s *Store) GetBand(ctx context.Context, cardID, runLimit int) (*entity.DesignBand, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	band := &entity.DesignBand{HiddenByRun: map[int]int{}, HiddenByBatch: map[int]int{}}
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		var err error

		if band.Bench, err = listBenchSlots(ctx, db, cardID); err != nil {
			return err
		}
		benchPtrs := make([]*entity.DesignBenchSlot, 0, len(band.Bench))
		for i := range band.Bench {
			benchPtrs = append(benchPtrs, &band.Bench[i])
		}
		if err = attachSlotPictures(ctx, rep, benchPtrs); err != nil {
			return err
		}
		if band.Budget, err = loadBudget(ctx, db, s.Now()); err != nil {
			return err
		}
		if band.References, err = storeutil.QueryListNamed[entity.DesignReference](ctx, db, `
			SELECT * FROM design_reference WHERE tech_card_id = :card ORDER BY ordinal, id`,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to list design references: %w", err)
		}
		// THE SHELF WALL AND ITS MARKS (0354), IN THIS SAME SNAPSHOT. The studio draws bench,
		// references and shelves in one frame; read separately they could disagree about which
		// instant of the card is on screen. Neither list is paged and neither needs to be — the
		// shelves are capped on the WRITE side (entity.MaxDesignAssetsPerCard), so «all of them»
		// is a bounded answer rather than an unbounded one, and the count on the wall is the whole
		// truth instead of «as much as fitted».
		if band.Assets, err = listAssets(ctx, db, cardID); err != nil {
			return err
		}
		// The file of each asset, in ONE batch. Without it a shelf tile has an id and no swatch,
		// which is the same defect a bench slot without its plate had.
		if err = attachAssetMedia(ctx, rep, band.Assets); err != nil {
			return err
		}
		if band.AssetPlacements, err = listAssetPlacements(ctx, db, cardID); err != nil {
			return err
		}
		// THE FABRIC OF EVERY (COLOURWAY, SLOT), 0368, IN THE SAME SNAPSHOT AS THE SHELF IT POINTS
		// INTO — read apart, a binding could name a tile the wall no longer holds. Whole card: the
		// pattern step draws every colourway's slots on one screen.
		if band.AssetBindings, err = listAssetBindings(ctx, db, cardID); err != nil {
			return err
		}
		// Layers WITHOUT their strokes: 512 KB is the cap per LAYER and a card may hold several,
		// so shipping them all would make every open of the tab cost megabytes to draw a list.
		//
		// ⚠ THE COLUMN LIST IS NAMED, SO EVERY NEW COLUMN MUST BE ADDED TO IT BY HAND, and the
		// three of 0350 are here for that reason. A projection that omits `origin` and its two
		// source ids does not fail — the band simply serves every layer as `drawn` with no file
		// behind it, which is precisely the shape the mixed-provenance warning reads.
		//
		// `raster_media_id` (0355) IS HERE AND `strokes` IS STILL NOT, and the two are not an
		// inconsistency: strokes are up to 512 KB per layer, the raster is a bare id. Omitting it
		// would not fail either — the band would simply serve every painted layer as unpainted, so
		// the tab could not tell a canvas with brushwork on it from an empty one until the editor
		// was opened.
		if band.Layers, err = storeutil.QueryListNamed[entity.DesignEditLayer](ctx, db,
			designListLayers, map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to list design edit layers: %w", err)
		}

		if band.TotalRuns, err = storeutil.QueryCountNamed(ctx, db, designCountRuns,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to count design runs: %w", err)
		}
		if band.ArchivedRuns, err = storeutil.QueryCountNamed(ctx, db, designCountArchivedRuns,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to count archived design runs: %w", err)
		}
		if band.MaxRrev, err = storeutil.QueryCountNamed(ctx, db, designMaxRrev,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to read design max rrev: %w", err)
		}
		if band.ColourRecipes, err = loadColourRecipes(ctx, db, cardID); err != nil {
			return err
		}
		// ЦВЕТОВОЙ ПЛАН (0364) — В ЭТОМ ЖЕ СНИМКЕ, ПО ТОМУ ЖЕ ДОВОДУ, ЧТО ПОЛКИ И ВЕРСТАК: студия
		// рисует верстак, полки и строку деталей одним кадром, а второе чтение позволило бы им
		// разойтись в том, какой момент карточки на экране.
		//
		// ⚠ nil ЗДЕСЬ — ЭТО ОТВЕТ, А НЕ ПРОБЕЛ: «на этой карточке плана нет». Пустой план — это
		// «покрасили и стёрли», состояние, сделанное руками; подменив одно другим, полоса сообщила
		// бы клиенту rev 0 у несуществующей строки, и первое же сохранение прошло бы мимо CAS.
		if band.ColourPlan, err = colourPlanByCard(ctx, db, cardID); err != nil {
			return err
		}
		if band.TotalBatches, err = storeutil.QueryCountNamed(ctx, db, designCountBatches,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to count design batches: %w", err)
		}
		renders, err := storeutil.QueryCountNamed(ctx, db, designCountFabricRenders,
			map[string]any{"card": cardID})
		if err != nil {
			return fmt.Errorf("failed to count design fabric renders: %w", err)
		}
		band.HasFabricRender = renders > 0
		cwRows, err := storeutil.QueryListNamed[struct {
			Cw int `db:"cw"`
		}](ctx, db, designRenderBenchColorways, map[string]any{"card": cardID})
		if err != nil {
			return fmt.Errorf("failed to list design render bench colourways: %w", err)
		}
		band.RenderBenchColorways = make([]int, 0, len(cwRows))
		for _, r := range cwRows {
			band.RenderBenchColorways = append(band.RenderBenchColorways, r.Cw)
		}

		if band.HiddenByRun, err = loadHiddenCounts(ctx, db, cardID, "run_id"); err != nil {
			return err
		}
		if band.HiddenByBatch, err = loadHiddenCounts(ctx, db, cardID, "batch_id"); err != nil {
			return err
		}

		// ВЫХОДЫ КАРТОЧКИ ЦЕЛИКОМ, В ЭТОМ ЖЕ СНИМКЕ. Раздел «рендеры этой карточки» и лента
		// обязаны показывать ОДИН момент карточки; вторым чтением они разошлись бы ровно на те
		// кадры, что родились между двумя запросами.
		band.Outputs, band.OutputsTotal, band.OutputsTotalByColorway, band.OutputsTotalByWorkflow, err =
			loadCardOutputs(ctx, rep, cardID)
		if err != nil {
			return err
		}

		// THE BAND'S OWN PAGE IS TAKEN WITH IncludeArchived TRUE, and the token it mints carries
		// that flag. A cursor born over the unfiltered list and then continued with
		// include_archived=false would change the row set MID-PAGINATION and skip rows in
		// silence; making the flag part of the token turns that from a hope about the client into
		// a property of the server.
		page, err := listRunsTx(ctx, rep, entity.DesignRunPage{
			TechCardId: cardID, Limit: runLimit, IncludeArchived: true,
		})
		if err != nil {
			return err
		}
		band.Runs, band.NextCursor = page.Runs, page.NextCursor
		band.Batches, band.NextBatchCursor = page.Batches, page.NextBatchCursor
		return nil
	})
	if err != nil {
		return nil, err
	}
	return band, nil
}

// cardOutputRow — строка design_picture ПЛЮС три колонки её прогона. Колонки прогона отдельными
// полями, а не вторым чтением design_run: прогон такого кадра почти всегда вне страницы истории,
// и второй запрос по id пришлось бы делать на каждый кадр.
//
// Rn — номер строки ВНУТРИ ОКНА своего колорвея. Он не едет наружу и существует здесь только
// потому, что StructScan требует поле на каждую выданную колонку, а внешний `SELECT o.*` тащит и
// её. Резать окно в Go вместо SQL значило бы прочитать всю карточку, чтобы выбросить хвост.
type cardOutputRow struct {
	entity.DesignPicture
	RunKind string `db:"run_kind"`
	RunRrev int    `db:"run_rrev"`
	RunCw   int    `db:"run_cw"`
	// RunWorkflow — designCardOutputsWorkflow, the PLAYGROUND tile of this output ('' = none).
	RunWorkflow string `db:"run_workflow"`
	Rn          int    `db:"rn"`
}

// cardOutputCountRow — одна строка счёта: колорвей, СЕКЦИЯ и сколько их там.
//
// Секция читается не ради провода, а ради того, чтобы счёт группировался ТЕМ ЖЕ ключом, каким
// режется окно списка (см. designCardOutputsSection). Наружу она складывается: поле контракта
// OutputsTotalByColorway поколорвейное, и эта правка его формы не меняет.
//
// Workflow — designCardOutputsWorkflow of the group (empty = no tile). The count groups by it so the
// per-workflow total (OutputsTotalByWorkflow, band 31) is summed from the SAME rows the window
// cuts; the colourway total still adds every workflow of the colourway together.
type cardOutputCountRow struct {
	ColorwayId int    `db:"colorway_id"`
	Section    int    `db:"section"`
	Workflow   string `db:"workflow"`
	N          int    `db:"n"`
}

// loadCardOutputs читает ГЕНЕРАТИВНЫЕ ВЫХОДЫ ВСЕЙ КАРТОЧКИ вместе со штампом прогона у каждого,
// по MaxCardOutputsPerColorway самых свежих НА КАЖДУЮ ПАРУ (колорвей, секция), и поколорвейный
// счёт рядом — сложенный из тех же групп.
//
// ПОЧЕМУ ЭТО НЕ СТРАНИЦА ЛЕНТЫ. Раздел «рендеры этой карточки» читал `Runs` — первую страницу
// ленты, — и потому терял рендеры по одному, стоило человеку сделать десяток прогонов другого
// рода. Здесь область видимости совпадает с обещанием заголовка: карточка.
//
// ⚠ И ПОЧЕМУ ПОТОЛОК ТРАТИТСЯ ПОКОЛОРВЕЙНО. Прежняя редакция этого абзаца говорила, что «отвечать
// так позволяют ДЕНЬГИ: эти роды платные». Это неправда — кропы и флэттены наследуют run_id и kind
// и не стоят ничего (довод и замер — у MaxCardOutputsPerColorway). Потолок достижим, значит его
// нельзя тратить общим `LIMIT ... ORDER BY id DESC`: он выбрасывал бы самые старые строки
// КАРТОЧКИ, а читатель сужает по КОЛОРВЕЮ — и раздел, целиком лежащий за горизонтом, приходил бы
// пустым, то есть с ровно тем дефектом, который волна закрывает.
//
// СЧЁТ ИДЁТ ТЕМ ЖЕ ПРЕДИКАТОМ, ТЕМ ЖЕ КЛЮЧОМ РАЗДЕЛА И В ТОЙ ЖЕ ЧИТАЮЩЕЙ ТРАНЗАКЦИИ: посчитанный
// другим условием либо вне снимка, он подписывал бы список, которого не видел. Карточный итог —
// сумма тех же чисел, а не отдельный COUNT(*): второй запрос был бы вторым источником правды.
func loadCardOutputs(ctx context.Context, rep dependency.Repository, cardID int) (
	[]entity.DesignCardOutput, int, map[int]int, map[string]int, error,
) {
	db := rep.DB()
	counts, err := storeutil.QueryListNamed[cardOutputCountRow](ctx, db,
		designCountCardOutputsByColorway, map[string]any{"card": cardID})
	if err != nil {
		return nil, 0, nil, nil, fmt.Errorf("failed to count design card outputs: %w", err)
	}
	// Секции СКЛАДЫВАЮТСЯ в поколорвейное число: на проводе OutputsTotalByColorway обещает «сколько
	// выходов у этого колорвея ВСЕГО», и присваивание вместо сложения молча отдало бы число одной
	// секции — последней, какую вернул MySQL.
	byColorway := make(map[int]int, len(counts))
	// PER WORKFLOW, THE SAME GROUPS SUMMED ACROSS COLOURWAYS AND SECTIONS. `+=`, not `=`, for the
	// same reason as above: a workflow can own rows on several colourways (a crop keeps its
	// parent's colourway), and assignment would report one of them. '' is «no tile» and is not a
	// key of the wire map (band 31: outputs of no workflow are not counted there).
	byWorkflow := make(map[string]int)
	total := 0
	for _, c := range counts {
		byColorway[c.ColorwayId] += c.N
		if c.Workflow != "" {
			byWorkflow[c.Workflow] += c.N
		}
		total += c.N
	}
	rows, err := storeutil.QueryListNamed[cardOutputRow](ctx, db, designListCardOutputs,
		map[string]any{"card": cardID, "per_colorway": MaxCardOutputsPerColorway})
	if err != nil {
		return nil, 0, nil, nil, fmt.Errorf("failed to list design card outputs: %w", err)
	}
	out := make([]entity.DesignCardOutput, 0, len(rows))
	for _, r := range rows {
		out = append(out, entity.DesignCardOutput{
			Picture: r.DesignPicture,
			// RunId берётся у САМОЙ КАРТИНКИ, а не у джойна: NULL здесь означает «кадр из пачки»,
			// и ноль это ровно то, что обязан увидеть клиент.
			RunId:         int(r.RunId.Int32),
			RunKind:       r.RunKind,
			RunRrev:       r.RunRrev,
			RunColorwayId: r.RunCw,
			RunWorkflow:   r.RunWorkflow,
		})
	}
	// Файлы — ОДНИМ пакетом на весь список. Без этого у выхода есть id и нет миниатюры, то есть
	// раздел нечем нарисовать: тот же дефект, что у слота верстака без плиты.
	flat := make([]*entity.DesignPicture, 0, len(out))
	for i := range out {
		flat = append(flat, &out[i].Picture)
	}
	if err := resolveMedia(ctx, rep, flat); err != nil {
		return nil, 0, nil, nil, err
	}
	return out, total, byColorway, byWorkflow, nil
}

// ListRuns returns one page of the history WITH the pictures of that page. A flat picture list
// beside the rows would ship 120 MediaFull for a card with 40 runs of 3 outputs.
func (s *Store) ListRuns(ctx context.Context, p entity.DesignRunPage) (*entity.DesignRunPageResult, error) {
	if err := requireCard(p.TechCardId); err != nil {
		return nil, err
	}
	var out entity.DesignRunPageResult
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := listRunsTx(ctx, rep, p)
		if err != nil {
			return err
		}
		out = *res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// listRunsTx is the keyset page. A CURSOR, NOT AN OFFSET: rows are born at the HEAD of this list,
// and an offset page would duplicate and skip rows exactly while somebody is generating. The
// cursor is the id of the last row returned, and `id < cursor` under `ORDER BY id DESC` is a
// stable keyset because design_run.id is a monotone AUTO_INCREMENT.
//
// A cursor minted by GetBand (which includes archived rows) stays valid for a ListRuns call that
// excludes them: the predicate narrows, the keyset does not move.
func listRunsTx(ctx context.Context, rep dependency.Repository, p entity.DesignRunPage) (*entity.DesignRunPageResult, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultRunPageLimit
	}
	if limit > MaxRunPageLimit {
		limit = MaxRunPageLimit
	}
	db := rep.DB()
	// ⚠ ТРЕТИЙ ПРЕДИКАТ СТРАНИЦЫ — РОД (B-21), И ОН ЖЕ СТОИТ У ОБОИХ СЧЁТЧИКОВ ЗАГОЛОВКА. Страница
	// без него, а счётчики с ним (или наоборот) — это лента, чей заголовок обещает строки, которых
	// на ней нет: пейджер считает «страницу N из M» по числу, которое пришло из другого запроса.
	where := "tech_card_id = :card AND " + designFeedKinds
	params := map[string]any{"card": p.TechCardId, "limit": limit + 1}
	if !p.IncludeArchived {
		where += " AND archived_at IS NULL"
	}
	if p.Cursor > 0 {
		where += " AND id < :cursor"
		params["cursor"] = p.Cursor
	}
	runs, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db,
		`SELECT * FROM design_run WHERE `+where+` ORDER BY id DESC LIMIT :limit`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list design runs: %w", err)
	}
	res := &entity.DesignRunPageResult{}
	if len(runs) > limit {
		runs = runs[:limit]
		res.NextCursor = runs[len(runs)-1].Id
	}
	// The upload shelves ride in the same page and carry their OWN keyset. With the generative
	// machine cut from this wave they are not a secondary branch — they are the only source of
	// pictures, and a batch picture hangs under no history row by construction.
	if res.Batches, res.NextBatchCursor, err = loadBatches(ctx, rep, p.TechCardId, limit, p.BatchCursor); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return res, nil
	}

	ids := make([]int, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.Id)
	}
	pics, err := loadPicturesByRuns(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	attempts, err := storeutil.QueryListNamed[entity.DesignRunAttempt](ctx, db,
		`SELECT * FROM design_run_attempt WHERE run_id IN (:ids) ORDER BY run_id, attempt_no`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to load design run attempts: %w", err)
	}
	byRun := map[int][]entity.DesignRunAttempt{}
	for _, a := range attempts {
		byRun[a.RunId] = append(byRun[a.RunId], a)
	}

	var flat []*entity.DesignPicture
	for i := range runs {
		runs[i].Attempts = byRun[runs[i].Id]
		runs[i].Pictures = pics[runs[i].Id]
		for j := range runs[i].Pictures {
			flat = append(flat, &runs[i].Pictures[j])
		}
	}
	if err := resolveMedia(ctx, rep, flat); err != nil {
		return nil, err
	}
	res.Runs = runs
	return res, nil
}

// loadColourRecipes builds the chips of the colour history.
//
// FORMAT, since the plan does not fix one: the RAW JSON of design_run.params->'$.colour' of the
// card's render runs, newest first, de-duplicated by the encoded recipe, capped at
// MaxColourRecipes. The chip restores a RECIPE and never a picture — the recipe migrates, the
// pixels do not — so the store hands the recipe object through untouched rather than inventing a
// flattened shape the contract would then have to mirror.
//
// The JSON PATH IS SNAKE_CASE because the writer stores protojson with UseProtoNames: true. If
// wave 2's StartRun ever writes default protojson (lowerCamelCase), this query returns nothing
// and the chips vanish with no error anywhere. See entity.DesignRunJSONFieldColour.
func loadColourRecipes(ctx context.Context, db dependency.DB, cardID int) ([]json.RawMessage, error) {
	raw, err := storeutil.QueryScalarListNamed[[]byte](ctx, db, `
		SELECT JSON_EXTRACT(params, '$.colour')
		FROM design_run
		WHERE tech_card_id = :card AND kind = 'render'
			AND params IS NOT NULL AND JSON_EXTRACT(params, '$.colour') IS NOT NULL
		ORDER BY id DESC
		LIMIT :scan`,
		map[string]any{"card": cardID, "scan": colourRecipeScanRuns})
	if err != nil {
		return nil, fmt.Errorf("failed to read design colour recipes: %w", err)
	}
	out := make([]json.RawMessage, 0, len(raw))
	seen := map[string]struct{}{}
	for _, r := range raw {
		if len(r) == 0 || string(r) == "null" {
			continue
		}
		if _, ok := seen[string(r)]; ok {
			continue
		}
		seen[string(r)] = struct{}{}
		out = append(out, json.RawMessage(append([]byte(nil), r...)))
		if len(out) >= MaxColourRecipes {
			break
		}
	}
	return out, nil
}

// loadHiddenCounts counts, over the WHOLE card, how many pictures of each run (or of each batch)
// are hidden.
//
// FORMAT, since the plan does not fix one: owner id → count, and an owner with nothing hidden is
// ABSENT rather than present with a zero — «· 2 hidden» is a badge, and a zero badge is noise.
// Rows whose owner column is NULL are excluded: a key of 0 would read as «no owner», and the
// badge belongs to the collapsed row, not to the orphan.
//
// THE AGGREGATE EXISTS BECAUSE BOTH LISTS ARE PAGED. A run or a shelf that is off the page has
// no pictures in the response, so the client has nothing to count — the header would silently
// lose exactly the part that is not on screen.
//
// The column name is interpolated, NOT bound: it is one of two literals chosen right here, never
// caller input. sqlx would bind it as a string value and the query would compare a column to the
// text "run_id".
func loadHiddenCounts(ctx context.Context, db dependency.DB, cardID int, ownerCol string) (map[int]int, error) {
	if ownerCol != "run_id" && ownerCol != "batch_id" {
		return nil, fmt.Errorf("unsupported design hidden-count column %q", ownerCol)
	}
	type row struct {
		Owner int `db:"owner"`
		N     int `db:"n"`
	}
	rows, err := storeutil.QueryListNamed[row](ctx, db, `
		SELECT `+ownerCol+` AS owner, COUNT(*) AS n
		FROM design_picture
		WHERE tech_card_id = :card AND hidden_at IS NOT NULL AND `+ownerCol+` IS NOT NULL
		GROUP BY `+ownerCol,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to count hidden design pictures: %w", err)
	}
	out := make(map[int]int, len(rows))
	for _, r := range rows {
		out[r.Owner] = r.N
	}
	return out, nil
}

// loadBatches reads the card's upload shelves WITH their pictures, newest first.
//
// THIS IS THE MAIN READ OF THE WAVE, not a secondary branch. The generative machine is cut from
// it entirely, so on beta there will be no runs at all and every picture will arrive through a
// batch. And a batch picture hangs under NO history row by construction — design_picture.run_id
// is NULL for a manual upload and design_run has no row to express the gesture — so without this
// the upload shelf is empty forever after the first tab reload.
//
// THE CEILING IS MaxBandBatches, declared in the same shape as the contract's other limits
// (10 §6). What happens after it: those batches are NOT shipped and get no badge either, which
// is honest — they are not on the screen. TotalBatches makes the overflow measurable, and a card
// that exceeds the ceiling needs a paged batch read that does not exist yet.
func loadBatches(ctx context.Context, rep dependency.Repository, cardID, limit, cursor int) ([]entity.DesignBatch, int, error) {
	db := rep.DB()
	if limit <= 0 || limit > MaxBandBatches {
		limit = MaxBandBatches
	}
	where := "tech_card_id = :card"
	params := map[string]any{"card": cardID, "limit": limit + 1}
	if cursor > 0 {
		where += " AND id < :cursor"
		params["cursor"] = cursor
	}
	batches, err := storeutil.QueryListNamed[entity.DesignBatch](ctx, db,
		`SELECT * FROM design_batch WHERE `+where+` ORDER BY id DESC LIMIT :limit`, params)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list design batches: %w", err)
	}
	next := 0
	if len(batches) > limit {
		batches = batches[:limit]
		next = batches[len(batches)-1].Id
	}
	if len(batches) == 0 {
		return batches, next, nil
	}
	ids := make([]int, 0, len(batches))
	for _, b := range batches {
		ids = append(ids, b.Id)
	}
	pics, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db, `
		SELECT * FROM design_picture WHERE batch_id IN (:ids) ORDER BY batch_id, ordinal, id`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to load design batch pictures: %w", err)
	}
	byBatch := map[int][]entity.DesignPicture{}
	for _, p := range pics {
		if !p.BatchId.Valid {
			continue
		}
		byBatch[int(p.BatchId.Int32)] = append(byBatch[int(p.BatchId.Int32)], p)
	}
	var flat []*entity.DesignPicture
	for i := range batches {
		batches[i].Pictures = byBatch[batches[i].Id]
		for j := range batches[i].Pictures {
			flat = append(flat, &batches[i].Pictures[j])
		}
	}
	if err := resolveMedia(ctx, rep, flat); err != nil {
		return nil, 0, err
	}
	return batches, next, nil
}

// GetBudget reports today's money bar. The DAY KEY IS COMPUTED IN GO, in the organisation's
// timezone — the MySQL session's day is a property of whichever server answered, not an answer
// of the organisation.
func (s *Store) GetBudget(ctx context.Context) (entity.DesignBudget, error) {
	var b entity.DesignBudget
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		b, err = loadBudget(ctx, rep.DB(), s.Now())
		return err
	})
	return b, err
}

// GetSettings reads the singleton row that IS the band's whole configuration.
func (s *Store) GetSettings(ctx context.Context) (entity.DesignSettings, error) {
	var out entity.DesignSettings
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		out, err = loadSettings(ctx, rep.DB())
		return err
	})
	return out, err
}

func loadSettings(ctx context.Context, db dependency.DB) (entity.DesignSettings, error) {
	s, err := storeutil.QueryNamedOne[entity.DesignSettings](ctx, db, `
		SELECT currency, budget_timezone, updated_by, updated_at
		FROM design_settings WHERE id = 1`, map[string]any{})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 0344 seeds the row with INSERT IGNORE, so this is only reachable if somebody
			// deleted it. Falling back to the schema defaults keeps the band readable instead of
			// making a missing configuration row look like a broken card.
			// ⚠ ЗДЕСЬ ЖИЛА ЛОВУШКА, И ОНА УМЕРЛА ВМЕСТЕ С КОЛОНКОЙ (0358). Фолбэк отдавал
			// DailyBudget = 0, а ноль значил «сегодня не запускаем», — то есть инсталляция, у
			// которой строку синглтона кто-то удалил, была ЗАКРЫТА НАВСЕГДА, и сказано это было
			// бы теми же словами «потолок исчерпан», что и обычное исчерпание. Теперь отсутствие
			// строки не может закрыть полосу: закрывать нечем.
			return entity.DesignSettings{
				Currency:       "USD",
				BudgetTimezone: "Europe/Warsaw",
			}, nil
		}
		return s, fmt.Errorf("failed to read design settings: %w", err)
	}
	return s, nil
}

// DesignBudgetDayKey is the day key of an instant in the organisation's timezone. Exported
// because wave 2's StartRun reserves against exactly this key and the two must not compute it
// differently.
func DesignBudgetDayKey(now time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || loc == nil {
		// An unloadable zone name must not silently become the server's own local day, which
		// would move the reset by hours without telling anyone. UTC is the neutral fallback and
		// it is the one the column's own default day would agree with.
		loc = time.UTC
	}
	return now.In(loc).Format("2006-01-02")
}

func loadBudget(ctx context.Context, db dependency.DB, now time.Time) (entity.DesignBudget, error) {
	set, err := loadSettings(ctx, db)
	if err != nil {
		return entity.DesignBudget{}, err
	}
	day := DesignBudgetDayKey(now, set.BudgetTimezone)
	b := entity.DesignBudget{
		Day:      day,
		Spent:    decimal.Zero,
		Reserved: decimal.Zero,
		Currency: set.Currency,
		Timezone: set.BudgetTimezone,
	}
	type row struct {
		Reserved decimal.Decimal `db:"reserved"`
		Spent    decimal.Decimal `db:"spent"`
		Currency string          `db:"currency"`
	}
	r, err := storeutil.QueryNamedOne[row](ctx, db,
		`SELECT reserved, spent, currency FROM design_budget_day WHERE day = :day`,
		map[string]any{"day": day})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No row for today is not an error — it is a day on which nothing has been spent.
			return b, nil
		}
		return b, fmt.Errorf("failed to read design budget day: %w", err)
	}
	b.Reserved, b.Spent = r.Reserved, r.Spent
	if r.Currency != "" {
		b.Currency = r.Currency
	}
	return b, nil
}
