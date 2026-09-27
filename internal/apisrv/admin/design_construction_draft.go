package admin

// СТРУКТУРНЫЙ ЧЕРНОВИК КОНСТРУКЦИИ — ВТОРАЯ ФОРМА ОТВЕТА ТОГО ЖЕ ПЛАТНОГО ПРОГОНА.
//
// ЧТО ЭТО. Владелец (круг 19, пункт 9, дословно): «Всё, чем наполнен мудборд — картинки + указания
// + CONCEPT & CONSTRUCTION DESCRIPTION — должно попадать в промпт. Внизу вместо кнопки
// `DRAFT THE IDEA ▸` мы генерируем ВЕСЬ construction info на основании того, что знаем». Кнопка
// уже была мультимодальным, платным, идемпотентным прогоном, читающим ровно эти три входа; ей
// не хватало (i) структурного ответа и (ii) места, куда он ложится, кроме `concept`. Этот файл —
// первая половина: ФОРМА ОТВЕТА и его ПРОВЕРКА. Вторая половина (приём по строкам) живёт на
// клиенте и ничего сюда не пишет.
//
// ⚠ ЧЕТЫРЕ РЕШЕНИЯ, БЕЗ КОТОРЫХ ЭТОТ ФАЙЛ ЧИТАЕТСЯ НЕВЕРНО:
//
//  1. ЭТО ФЛАГ НА СТАРОМ ГЛАГОЛЕ, А НЕ НОВЫЙ ГЛАГОЛ И НЕ НОВЫЙ РОД ПРОГОНА. Отсутствующий флаг
//     обязан давать ПРЕЖНИЕ БАЙТЫ запроса и прежнюю прозу в `output_text` — клиент, который о
//     флаге не знает, продолжает резать ответ по трём заголовкам. Новый род (`kind`) прошёл бы
//     рябью по каждой клиентской таблице родов и не сказал бы там ничего нового: это по-прежнему
//     один текстовый прогон по доске, в том же денежном регистре и с той же идемпотентностью.
//
//  2. ХРАНИТСЯ ПРОВЕРЕННЫЙ КАНОНИЧЕСКИЙ JSON, А НЕ ОТВЕТ МОДЕЛИ. Идемпотентный повтор отдаёт
//     СОХРАНЁННУЮ строку и модель не зовёт; всё, чего нельзя восстановить из `output_text`,
//     исчезло бы при втором нажатии той же кнопки. Поэтому в `output_text` уезжает protojson
//     ЭТОГО ЖЕ сообщения — то, что получил клиент, а не то, что напечатала модель.
//
//  3. КОЭРЦИЯ ПРОТИВ ОТКАЗА — ГРАНИЦА ПРОХОДИТ ПО ФОРМЕ, А НЕ ПО СОДЕРЖАНИЮ (заимствовано у
//     разбора тех-карты, techcardanalysis.VerifyModelRun). Узнаваемый дрейф написания
//     («Collar», «sleeve cuff», «FABRIC») приводится к нашему словарю молча; неузнаваемое —
//     выбрасывается по одной строке, а не роняет весь оплаченный прогон. Ронять целиком имеет
//     право ровно одно: ответ НЕ ТОЙ ФОРМЫ (нет JSON, нет ни одного ключа) и ответ, ОБРЕЗАННЫЙ
//     потолком токенов (`finish_reason=length`) — половина черновика неотличима от полного и
//     выглядела бы как «модель этого не увидела».
//
//  4. МОДЕЛЬ НЕ НАЗЫВАЕТ НАШИХ ИДЕНТИФИКАТОРОВ. Выноска рождается БЕЗ номера и без пина (номер
//     минтит сервер на сейве, пин ставит человек на картинке), а `material_id` на этой фазе
//     ПРИНУДИТЕЛЬНО ноль: каталог в промпт не уезжает (это фаза 4), значит подтвердить артикул
//     нечем, а строка, выглядящая связанной и оценённой, но указывающая на чужой артикул, — это
//     ошибка себестоимости с ценником.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ─────────────────────────── потолки и словари ───────────────────────────

const (
	// designConstructionMaxTokens — потолок ответа. Без потолка одна доска однажды выкупает ответ
	// на тысячи строк, а с потолком обрезанный ответ ЛОВИТСЯ (finish_reason=length) вместо того,
	// чтобы приехать половиной черновика.
	//
	// ⚠ 3000 → 8000, И ЭТО ЗАМЕР, А НЕ ЗАПАС. Число 3000 было взято у разбора тех-карты, когда
	// контракт ответа состоял из четырёх текстов, аспектов и спеки. С тех пор в тот же ответ
	// вошли ТРИ новых списка: колорвеи (B-25) — до 4 × 15 цветов слотов, две колонки оценки на
	// КАЖДОЙ из 15 строк спеки (B-16), и до 15 выносок (B-13) освободились, но не исчезли из
	// формы. Потолок при этом не двигали ни разу.
	//
	// ЧЕМ ЭТО КОНЧАЕТСЯ: `finish_reason=length` — это ОТКАЗ ВСЕГО ПРОГОНА (designFailDraftAs), и
	// отказ ОПЛАЧЕННЫЙ — токены поставщик уже напечатал. То есть тесный потолок не экономит, а
	// покупает ноль за полную цену.
	//
	// ЗАМЕР. Ответ, заполненный ПО ВСЕМ потолкам этого файла (10 аспектов по 60 слов, 15 строк
	// спеки со всеми девятью ключами, 4 колорвея, 8 записок «что приколоть»), в JSON той формы,
	// какой его просит системный промпт, при средней длине слова 4.3 знака:
	//
	//	5 цветов слотов на колорвей (реалистичное «сколько тканей в спеке»):
	//	    10 189 байт плотного JSON ≈ 3 400 токенов;  с отступами — 13 312 ≈ 4 440
	//	15 цветов слотов на колорвей (назван КАЖДЫЙ слот, потолок правила 9):
	//	    14 249 байт плотного JSON ≈ 4 750 токенов;  с отступами — 20 012 ≈ 6 670
	//
	// Токены считаны консервативно — 3 знака на токен; у JSON с короткими ключами и пунктуацией
	// это нижняя граница отношения, английская проза даёт ~4. Отступы считаются, потому что
	// json-режим их не запрещает и модели их ставят.
	//
	// 8000 покрывает ХУДШИЙ из четырёх замеров (6 670) с запасом ~20%. Даже нижняя строка таблицы
	// — та, что раньше называлась «обычным ответом», — уже не помещалась в 3000, то есть потолок
	// отказывал не в аварии, а в штатном полном ответе.
	//
	// ⚠ ПЕРЕЗАМЕР 26.09 (ревью T32/T33, MAJOR 4) — И ПОТОЛОК 8000 → 10 000. Замер выше был «по всем
	// потолкам» лишь на словах: силуэт и ткань несли ~212 байт, замысел — ноль, при принимаемых
	// 2000 рунах на каждое; с ними ответ давал ≈9 100 токенов, то есть finish_reason=length на
	// ПОЛНОМ ответе. Теперь у каждой строки есть предел, названный модели (правило 7) и держимый
	// разбором (таблица designConstructionMax*Runes), цветов слота — 8, `flat_details` — 6 × (40 +
	// 200), и замер заполняет КАЖДЫЙ предел ровно: плотный 20 839 Б ≈ 6 950 токенов, с отступами
	// 24 917 Б ≈ 8 305. Под 8000 это −3.8 %; под 10 000 — запас 17 %.
	//
	// T45 (27.09): палитра предложения — 4 цвета × (подпись 40 + пантон 24 + hex) на каждое из 4
	// предложений, верхние pantone / hex из схемы ушли (они — зеркало colours[0]). Замер: плотный
	// 22 371 Б ≈ 7 457 токенов, с отступами 27 309 Б ≈ 9 103. Под 10 000 — запас 9 %, поэтому
	// потолок поднят до 11 000 (запас 17 %); ужать палитру до того же запаса можно было лишь до
	// одного цвета, а это отменяет саму многоцветность.
	//
	// ПОЧЕМУ ПОТОЛОК, А НЕ ЕЩЁ ОДНО УЖАТИЕ. Удержать 8000 с запасом ≥ 15 % можно лишь так: 5 цветов
	// слота, аспект 300 рун (≈45 слов вместо обещанных 60), записка 120, замысел 600, состав 40, цвет
	// 30 — и запас 15.2 % на границе ошибки оценки. Это режет то, что человек читает, ради $0.03 на
	// нажатие (2000 токенов × $15/M); цена структурной базы и бюджет вызова (+67 s) выводятся из
	// entity.DesignConstructionMaxTokens тем же коммитом. Замер живёт в
	// TestConstructionAnswerCeilingHoldsTheWorstRealisticAnswer и требует запас ≥ 15 %.
	//
	// ⚠ ПОТОЛОК ВЕСЬ УХОДИТ В ОТВЕТ, А НЕ В РАЗМЫШЛЕНИЕ: CompleteWithImages выключает `reasoning`
	// ровно тогда, когда потолок задан (см. multimodal.go) — иначе думающая модель тратила бы этот
	// же бюджет до ответа, и замер выше не значил бы ничего.
	// ⚠ ЧИСЛО ПЕРЕЕХАЛО В entity, А ЗДЕСЬ ОСТАЛСЯ ЗАМЕР. Из него выводятся ЦЕНА (design_run.go),
	// СРОК ВЫЗОВА (openrouter.CompletionBudget) и ЛИЗА ХЕНДЛЕРА (store/design.HandlerLeaseFor) — три
	// величины в трёх пакетах, две из которых деньги и время. Пока копия числа стояла в каждом,
	// они расходились молча: см. довод у entity.DesignConstructionMaxTokens.
	designConstructionMaxTokens = entity.DesignConstructionMaxTokens

	// ─── ПОТОЛКИ ДЛИНЫ, КОТОРЫЕ ПРОМПТ НАЗЫВАЕТ ВСЛУХ (правило 7), А РАЗБОР ДЕРЖИТ (ревью 26.09) ───
	//
	// ⚠ ОДНА ТАБЛИЦА НА ТРЁХ ЧИТАТЕЛЕЙ: правило 7 промпта, разбор и замер потолка ответа
	// (TestConstructionAnswerCeilingHoldsTheWorstRealisticAnswer). Пока «длинные» поля принимали
	// 2000 рун каждое, а промпт о пределах молчал, замер «по всем потолкам» был неправдой: ответ,
	// честно заполненный ДО НАШИХ ЖЕ пределов, давал ≈9 100 токенов при потолке 8 000 — то есть
	// finish_reason=length и потерю всего оплаченного прогона. Предел, которого модель не знает, —
	// не предел: он режет ответ молча ПОСЛЕ того, как за него заплачено. Поэтому каждое число ниже
	// (i) названо модели в правиле 7, (ii) обрезается разбором с маркером и счётчиком Truncated,
	// (iii) входит в замер, и TestConstructionPromptNamesTheSameLimitsTheParserHolds не даёт (i)
	// и (ii) разойтись.
	//
	// Числа — не столбцы, а ЗАМЫСЕЛ ПОЛЯ: замысел — абзац (700), силуэт и ткань — по короткому
	// абзацу (300), аспект — «одна мысль», ≈60 слов (400), записка «что приколоть» — одна строка
	// (160). Строки спеки и цвета слотов — подписи, не описания. Все — в РУНАХ, потому что предел
	// смысловой; байтовые потолки колонок (VARCHAR) стоят ниже по маршруту и остаются последним
	// словом, хотя после рунных их уже не достичь (60 рун кириллицы — 121 байт при 255).
	designConstructionMaxConceptRunes     = 700
	designConstructionMaxSilhouetteRunes  = 300
	designConstructionMaxFabricRunes      = 300
	designConstructionMaxAspectRunes      = 400
	designConstructionMaxMissingRunes     = 160
	designConstructionMaxNameRunes        = 60 // bom.name и slot — имя строки спеки
	designConstructionMaxCompositionRunes = 60 // «NN% fibre, NN% fibre»
	designConstructionMaxColourRunes      = 40 // bom.colour, slot.colour — слова цвета
	designConstructionMaxPantoneRunes     = 24 // «19-4005 TCX», с запасом на «PANTONE » впереди
	designConstructionMaxColourCodeRunes  = 24 // код словаря либо имя цвета, которое сложит проверка; 24 руны кириллицы — 49 байт при varchar(64)
	// designConstructionMaxTraceKeyRunes — САМОДЕЛЬНЫЙ ключ выброшенного отсутствия в строке лога.
	// Лог получает ключ и только ключ; словарный — каноническим, самодельный — не длиннее подписи.
	designConstructionMaxTraceKeyRunes = 40
	// designConstructionMaxTextRunes — потолок ВЫНОСОК (поле 6): промпт их не просит с B-13, разбор
	// жив ради повтора старых прогонов. В замер потолка они не входят — их нет в форме ответа.
	designConstructionMaxTextRunes = 500
	// ─── ПОТОЛКИ КОЛОНОК. СЧИТАЮТСЯ БАЙТЫ, А НЕ РУНЫ, И ЭТО НЕ ПЕДАНТИЗМ ───
	//
	// ⚠ РЕВЬЮ КРУГА 19: ШЕСТЬ ПОЛЕЙ ЕХАЛИ С ПОТОЛКОМ 500 РУН В КОЛОНКИ VARCHAR(255), А PANTONE — БЕЗ
	// ПОТОЛКА ВОВСЕ В VARCHAR(64) (0363). Дальше по маршруту стоят сторожа DTO, и они меряют `len()`,
	// то есть БАЙТЫ: ~85 рун кириллицы уже не влезают в 255. Промах любого из них — не «поле
	// обрезалось», а ОТКАЗ В СОХРАНЕНИИ ВСЕЙ КАРТОЧКИ (UpsertTechCard — всё-или-ничего), либо, у
	// pantone, сырой MySQL 1406, не называющий ни строки, ни поля. Поэтому предложение режется ПО
	// НАЗНАЧЕНИЮ и В ТЕХ ЖЕ ЕДИНИЦАХ, в которых считает сторож.
	//
	// ⚠ ПОТОЛОК РУН ОСТАЁТСЯ ТАМ, ГДЕ КОЛОНКА TEXT (описание выноски, тексты аспектов, «что стоит
	// приколоть»): там ограничение смысловое — «одна мысль на строку», — а не про размер колонки.
	designConstructionMaxVarchar255 = 255 // bom.name/colour/composition, callout.part, callout.dimensions
	designConstructionMaxVarchar64  = 64  // bom.pantone (0363) и detail_key самодельного аспекта
	// Потолки ОЦЕНКИ расхода (0365). Три знака после точки — сколько хранит DECIMAL(12,3);
	// миллион — потолок здравого смысла на ОДНО изделие (см. designBoundedDecimal), заведомо ниже
	// сторожа DTO (bomQtyLimit), чтобы предложение не могло стать отказом в сохранении карточки.
	designEstUsageMaxFrac = 3
	designEstUsageLimit   = 1_000_000

	// Потолки списков. Не вкус: предложение, которое человек обязан просмотреть по строкам, за
	// этими числами перестаёт быть предложением и становится работой.
	// ─── ПОТОЛКИ СЕКЦИИ «УЖЕ НА КАРТОЧКЕ» (входные токены КАЖДОГО нажатия) ───
	//
	// Числа взяты у соседа по смыслу, а не по величине: строка карточки читается моделью ради
	// одного — «этого не предлагай», — и для этого хватает начала строки и первых двух десятков
	// строк каждого списка. Байтовый потолок — последнее слово: он держит СУММУ трёх списков.
	designConstructionMaxAlreadyRows      = 20
	designConstructionMaxAlreadyLineRunes = 200
	designConstructionMaxAlreadyBytes     = 8 << 10 // 8 KiB на всю секцию
	// designConstructionMaxSlotsToColour — СКОЛЬКО ИМЁН СЛОТОВ ЕДЕТ СЕКЦИЕЙ «Slots to colour»
	// (O-44 п.2). Те же входные токены каждого нажатия, поэтому потолок есть; двадцать имён — с
	// запасом над восемью цветами колорвея, и список режется В ПОРЯДКЕ ПРИОРИТЕТА (главные ткани,
	// нитка, остальное — см. designSlotsToColour), так что слот, который правило 9 успевает окрасить,
	// за потолок не выпадает, а об остальных говорит честный хвост «(+N more …)». Имя идёт в мере
	// разбора — designSlotName (60 рун).
	designConstructionMaxSlotsToColour = 20

	designConstructionMaxAspects  = 10
	designConstructionMaxCallouts = 15
	designConstructionMaxBom      = 15
	designConstructionMaxMissing  = 8

	// ─── ПОТОЛКИ ДЕТАЛЕЙ ДЛЯ ОТДЕЛЬНОГО РИСУНКА (O-33, D-32) ───
	//
	// ШЕСТЬ, ПОТОМУ ЧТО КАЖДАЯ — ОТДЕЛЬНЫЙ ПЛАТНЫЙ ПРОГОН ФЛЭТА, который человек запускает по
	// одному; список длиннее шести — это уже не «что стоит нарисовать отдельно», а «нарисуй всё».
	// Имя — подпись слота на плитке, одна строка (40 рун); записка — «что должен показать рисунок»,
	// одна мысль, не абзац (200 рун).
	designConstructionMaxFlatDetails         = 6
	designConstructionMaxFlatDetailNameRunes = 40
	designConstructionMaxFlatDetailNoteRunes = 200

	// ─── ПОТОЛКИ ПРЕДЛОЖЕННЫХ КОЛОРВЕЕВ (B-25) ───
	//
	// ЧЕТЫРЕ, ПОТОМУ ЧТО ВЛАДЕЛЕЦ ПРОСИЛ «НЕСКОЛЬКО», А НЕ «СПИСОК»: подтверждение колорвея
	// СОЗДАЁТ ПРОДУКТ, и предложение, которое человек обязан просмотреть по одному, за четырьмя
	// строками перестаёт быть предложением. Промпт просит 2–4; потолок — последнее слово.
	designConstructionMaxColourways = 4
	// ВОСЕМЬ ЦВЕТОВ НА КОЛОРВЕЙ, А НЕ ПОТОЛОК СПЕКИ (ревью 26.09). Слот берётся из строк спеки, и
	// больше пятнадцати их быть не может — но ткани среди пятнадцати строк три–шесть (остальное
	// нитка, фурнитура, ярлыки), а 4 × 15 подписанных слотов — это половина потолка ответа, купленная
	// ради случая, которого не бывает. Восемь — с запасом над самой пёстрой спекой; девятый цвет
	// считается (OverLimit), и правило 9 просит главные ткани первыми.
	designConstructionMaxColourwaySlots = 8
	// ЧЕТЫРЕ ЦВЕТА ПАЛИТРЫ НА ПРЕДЛОЖЕНИЕ (T45, 27.09). Палитра колорвея — 1…8 цветов (решение
	// владельца 5), но предложение модели — это «чем колорвей отличается от соседнего»: главная
	// ткань и два-три акцента. Четвёрка держит потолок ответа (4 × 4 цвета по подписи и пантону —
	// ≈900 токенов сверх прежнего худшего ответа, см. TestConstructionAnswerCeilingHoldsTheWorst
	// RealisticAnswer); пятый цвет считается (OverLimit), а человек допишет его на вкладке.
	designConstructionMaxColourwayColours = 4
	// ИМЯ КОЛОРВЕЯ — 64 РУНЫ (сверх этого — потолок колонки tech_card_colorway.dev_name,
	// varchar(255), в байтах). Это ПОДПИСЬ («Black / Bone»), а не описание: пикер колорвеев рисует
	// её в одну строку, и длинное имя не читается ни там, ни в списке продукта.
	designConstructionMaxColourwayNameRunes = 64

	// designConstructionMaxColourRows — СКОЛЬКО СТРОК СЛОВАРЯ ЦВЕТА УЕЗЖАЕТ В ПРОМПТ.
	//
	// Тот же довод и то же место, что у потолка секции «уже на карточке»: список едет во ВХОДНЫХ
	// токенах КАЖДОГО нажатия. Двести строк — ≈4 KiB, ≈1k токенов, ≈$0.003; тысяча строк словаря
	// стоила бы впятеро дороже и не помогла бы модели выбрать. За потолком список не режется, а НЕ
	// ДАЁТСЯ ВОВСЕ, и промпт говорит об этом вслух: половина словаря заставила бы модель выбирать
	// «ближайший код» из набора, который мы ей молча урезали.
	designConstructionMaxColourRows = 200
)

// designConstructionReasonInvalidOutput — машинная причина «ответ не той формы», она же
// `error_code` проваленного прогона. Одно слово на два места: клиент отличает её от провала
// поставщика, не разбирая английскую прозу, а история прогонов — по колонке.
const designConstructionReasonInvalidOutput = "invalid_output"

// designReasonBudgetExhausted — «модель истратила весь бюджет ответа и не ответила».
//
// ⚠ ТРЕТИЙ КОД РЯДОМ С ДВУМЯ, А НЕ СИНОНИМ ОДНОГО ИЗ НИХ. `provider_error` значит «ответа не было»
// (транспорт, 404, неверная настройка), `invalid_output` — «ответ был, и он не той формы». Здесь
// ответ БЫЛ, он пуст, детерминирован и ОПЛАЧЕН токенами завершения; чинит его не дежурный и не
// промпт, а потолок вместе с выключенным мышлением. Слив его в «поставщик падает», мы получили бы
// график аварии там, где мал наш собственный потолок.
const designReasonBudgetExhausted = "budget_exhausted"

// designReasonShapeMismatch — тот же client_request_id пришёл с ПРОТИВОПОЛОЖНЫМ флагом формы.
// Флаг в ключ идемпотентности не входит (он не свойство прогона, а свойство нажатия), поэтому
// расхождение ловится сверкой с уже сохранённой строкой — ровно как расхождение по колорвею.
const designReasonShapeMismatch = "shape_mismatch"

const (
	// Две прозы на два РАЗНЫХ исхода, и различие несущее: первый чинится повтором, второй —
	// уменьшением доски или описания. Одна фраза на оба отправила бы человека жать ту же кнопку
	// до тех пор, пока он не бросит.
	designConstructionShapeRefusalMsg = "the model did not answer in the shape asked for — draft again"
	designConstructionCutRefusalMsg   = "the answer was cut off — fewer pictures or a shorter description, then draft again"
	// ТРЕТЬЯ ПРОЗА НА ТРЕТИЙ ИСХОД: бюджет ответа истрачен целиком, а ответа нет. Чинится тем же
	// жестом, что и обрезанный ответ, — доска поменьше, — но исход другой и путать их нельзя.
	designConstructionBudgetRefusalMsg = "the model used up the whole answer budget without answering — fewer pictures or a shorter description, then draft again"

	// ─── ПРОЗА ПОВТОРА: ПРОГОН УЖЕ ОТВЕЧЕН, И ОТВЕЧЕН ОН ПРОВАЛОМ ───
	//
	// Все три говорят одно и то же действие — НОВОЕ нажатие, — потому что повторить прогон под тем
	// же ключом идемпотентности нельзя: вторая платная попытка под одним ключом сломала бы
	// единственное, ради чего ключ существует.
	designConstructionReplayShapeMsg  = "this draft already failed: the model did not answer in the shape asked for — press draft again to start a new one"
	designConstructionReplayBudgetMsg = "this draft already failed: the model used up the whole answer budget without answering — press draft again to start a new one"
	designDraftReplayFailedMsg        = "this draft already failed — press draft again to start a new one"
	// ЧЕТВЁРТАЯ ПРОЗА НА ЧЕТВЁРТЫЙ ИСХОД (designReasonProviderCut): предыдущее нажатие доехало до
	// модели и было оплачено, а ответа не привезло. Жест починки тот же — новое нажатие, — но
	// новость другая, и умолчать про деньги значило бы вернуть ровно тот дефект.
	designDraftReplayCutMsg = "this draft reached the model and lost the answer on the way back; it was charged — press draft again to start a new one"

	// ФОРМА ОТВЕТА ПРИБИТА К ПРОГОНУ, А НЕ К НАЖАТИЮ: прогон отвечен один раз и навсегда в той
	// форме, в какой был отвечен.
	designConstructionShapeMismatchMsg = "this request has already been answered in the other form — press draft again to start a new one"
)

// designConstructionAspectKeys — СТАНДАРТНЫЕ КЛЮЧИ АСПЕКТОВ, В ПОРЯДКЕ РЕДАКТОРА.
//
// ⚠ ПИШУТСЯ ТАК, КАК ИХ ХРАНИТ КАРТОЧКА, а не так, как их читает человек: `sleeveCuff`, а не
// `sleeve / cuff` и не `sleeve_cuff`. Ключ — это то, по чему клиент делает upsert строки
// `details[]`; ключ «почти тот» родил бы ВТОРУЮ строку рядом с существующей, и на экране один и
// тот же аспект оказался бы дважды.
//
// ⚠ СПИСОК — КОПИЯ КЛИЕНТСКОГО СЛОВАРЯ (components/tech-card-options.ts, detailAspects), И ЭТО
// НАЗВАНО ВСЛУХ, ПОТОМУ ЧТО КОПИЯ — ЭТО ДОЛГ. Серверного источника у него нет: колонка detail_key
// объявлена freeform, и никакой таблицы «законные аспекты» не существует. Пока это так, ключ,
// добавленный на клиенте и не добавленный здесь, не сломается — он просто приедет как
// САМОДЕЛЬНЫЙ, а редактор аспектов их принимает. Обратное («здесь есть, там нет») даёт строку без
// подписи, и это тоже видно глазом, а не молча.
var designConstructionAspectKeys = []string{
	"silhouette",
	"fabric",
	"collar",
	"fastening",
	"pockets",
	"sleeveCuff",
	"topstitching",
	"extraDetails",
	"auxMaterials",
}

// designConstructionAspectByFold — тот же словарь, сложенный для узнавания: «Sleeve / Cuff»,
// «sleeve_cuff» и «sleeveCuff» это один ключ, а не три.
var designConstructionAspectByFold = func() map[string]string {
	m := make(map[string]string, len(designConstructionAspectKeys))
	for _, k := range designConstructionAspectKeys {
		m[designFoldToken(k)] = k
	}
	return m
}()

// designConstructionFits — СЛОВАРЬ ПОСАДКИ.
//
// ⚠ ТОЖЕ КОПИЯ КЛИЕНТСКОГО СПИСКА (components/style-facts-field.tsx, FIT_OPTIONS), и тоже потому,
// что серверного словаря посадки НЕ СУЩЕСТВУЕТ: `tech_card.fit` — свободная строка, факт стиля.
// Он нужен здесь по одной причине: посадка — это ПИКЕР, и значение вне его списка человек не
// сможет принять одним кликом. Слово, которого в списке нет, поэтому не выдумывается и не
// подставляется — оно просто не предлагается вовсе (пустая строка), и карточка остаётся при своём.
var designConstructionFits = []string{
	"regular", "slim", "loose", "relaxed", "skinny", "cropped", "tailored",
}

var designConstructionFitByFold = func() map[string]string {
	m := make(map[string]string, len(designConstructionFits))
	for _, f := range designConstructionFits {
		m[designFoldToken(f)] = f
	}
	return m
}()

// designEnumVocabulary — токены одного enum'а спецификации: карта узнавания и порядок для промпта.
//
// СТРОИТСЯ ИЗ САМОГО ENUM'А, А НЕ ВЫПИСЫВАЕТСЯ РУКАМИ. Переписанный от руки список — это ровно то
// место, где словарь молча теряет значение, добавленное в другом файле, и правильный ответ модели
// становится UNKNOWN.
//
// ⚠ В КАРТУ КЛАДЁТСЯ И КОРОТКОЕ ИМЯ, И ПОЛНОЕ ИМЯ ЧЛЕНА, и это не щедрость. Коротким («fabric»)
// отвечает модель — так её просит промпт; полным («TECH_CARD_BOM_SECTION_FABRIC») отвечает НАШ
// СОБСТВЕННЫЙ канонический JSON, который тот же разбор читает на идемпотентном повторе. Приняв
// только короткое, повтор терял бы секцию у каждой строки спеки.
//
// НУЛЕВОЙ ЧЛЕН НЕ ПОПАДАЕТ НИ В КАРТУ, НИ В СПИСОК: UNKNOWN/UNSET — это «не задано», ответ, а не
// значение, и предлагать его модели значило бы просить её отвечать «не знаю» словом из словаря.
func designEnumVocabulary[E ~int32](prefix string, values map[string]int32) (map[string]E, []string) {
	byFold := make(map[string]E, len(values))
	type member struct {
		token string
		num   int32
	}
	members := make([]member, 0, len(values))
	for full, num := range values {
		if num == 0 {
			continue
		}
		token := strings.ToLower(strings.TrimPrefix(full, prefix))
		byFold[designFoldToken(token)] = E(num)
		byFold[designFoldToken(full)] = E(num)
		members = append(members, member{token: token, num: num})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].num < members[j].num })
	tokens := make([]string, 0, len(members))
	for _, m := range members {
		tokens = append(tokens, m.token)
	}
	return byFold, tokens
}

var designBomSectionByFold, designBomSectionTokens = designEnumVocabulary[pb_common.TechCardBomSection](
	"TECH_CARD_BOM_SECTION_", pb_common.TechCardBomSection_value)

var designBomPurposeByFold, designBomPurposeTokens = designEnumVocabulary[pb_common.TechCardBomPurpose](
	"TECH_CARD_BOM_PURPOSE_", pb_common.TechCardBomPurpose_value)

var designBomKindByFold, designBomKindTokens = designEnumVocabulary[pb_common.TechCardBomKind](
	"TECH_CARD_BOM_KIND_", pb_common.TechCardBomKind_value)

// ЕДИНИЦА ОЦЕНКИ (B-16) — СПИСОК ДЛЯ ПРОМПТА, И ТОЛЬКО ОН. Это КОРОТКИЕ имена членов MaterialUnit
// («m», «pcs», «kg») в порядке энума: ровно те написания, которыми предложение обязано отвечать,
// потому что в них же оно и ляжет в `tech_card_bom_item.unit`.
//
// ⚠ КАРТА УЗНАВАНИЯ ЗДЕСЬ БОЛЬШЕ НЕ СТРОИТСЯ, И ЭТО ПОЧИНКА, А НЕ УБОРКА. Она строилась на
// designFoldToken, а тот оставляет только IsLetter||IsDigit — «²» это категория No, не Nd, поэтому
// fold("m²") == "m" И ЕДИНИЦА ПЛОЩАДИ МОЛЧА СТАНОВИЛАСЬ ПОГОННЫМ МЕТРОМ, внутри ПОДПИСАННОГО
// дайджеста MATERIALS и с UnitsUnset == 0, то есть без единого следа в логе. Узнаванием ведает
// entity.NormalizeMaterialUnit — ТА ЖЕ функция, через которую единицу читает КАЖДЫЙ её потребитель
// (pbMaterialUnit, SameMaterialUnit, план материалов), и она знает «m²», «sqm», «м», «шт», «pc».
// Второй словарь узнавания рядом с ней — это ровно то место, где написание, добавленное в один
// файл, теряется в другом; см. designUnitToken.
var _, designUnitTokens = designEnumVocabulary[pb_common.MaterialUnit](
	"MATERIAL_UNIT_", pb_common.MaterialUnit_value)

// designFoldToken складывает написание до узнаваемого ядра: регистр и ВСЯ пунктуация исчезают.
//
// ⚠ ИМЕННО ВСЯ, А НЕ ТОЛЬКО ПРОБЕЛЫ И ДЕФИСЫ. Наши собственные ключи живут в camelCase
// (`sleeveCuff`), модель отвечает snake_case или словами («sleeve / cuff»), а канонический JSON —
// ПРОПИСНЫМИ С ПОДЧЁРКИВАНИЯМИ. Складка, оставляющая подчёркивание, развела бы эти три написания на
// три разных ключа, то есть завела бы у одного аспекта три строки.
func designFoldToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ─────────────────────────── отсутствие — не аспект ───────────────────────────
//
// ДВА ПРЕДИКАТА, И ГРАНИЦА МЕЖДУ НИМИ — ВЕСЬ СМЫСЛ (ревью 26.09 к O-32, MAJOR 2). Первая версия
// сторожа выбрасывала любой текст, НАЧИНАЮЩИЙСЯ с «no / none / without…», и уносила настоящую
// конструкцию: «Nothing but a raw-edge finish at the hem», «Without side seams — tubular-knit body»,
// «Without lining; single layer throughout» (терялся факт «в один слой»). Отсутствие — это либо
// ГОЛАЯ ЗАГЛУШКА («none», «n/a», «Fastening: N/A», «—»), либо КОРОТКОЕ ЧИСТОЕ ОТРИЦАНИЕ («no
// closures», «there aren't any fastenings»); а всё, что длиннее четырёх слов после отрицания или
// несёт ПОЛОЖИТЕЛЬНУЮ СВЯЗКУ (but, except, with, single, only, instead, just, then, запятая, точка с
// запятой, тире, двоеточие), — это ОПИСАНИЕ, и оно остаётся, даже когда начинается со слова «нет».
//
// ⚠ ТРЕТИЙ ПРЕДИКАТ — (в), КЛЮЧ ОТРИЦАЕТ СВОЙ ПРЕДМЕТ (правка координатора к ревью). Без него первый
// пример владельца («No visible closures; pull-on construction, relying on jersey stretch for fit»
// под FASTENING) оставался бы на совести правила 11: после точки с запятой стоит описание, и правило
// (б) его бережёт — тем же жестом, которым бережёт «Without lining; single layer throughout». Но под
// ключом fastening открывающее «no visible closures» — это ответ на вопрос ключа словом «нет», а
// описание после связки — про то, ПОЧЕМУ застёжек нет, а не про застёжку. Поэтому (в) — с ключом:
// первая клауза (до «; , : — –» или дефиса с пробелами) — короткое отрицание по (б), в ней — предмет
// ключа (designAspectAbsenceNouns), а после связки о предмете больше ни слова → отсутствие. Тот же
// текст под ключом без словаря (extraDetails, silhouette, самодельный «vent») остаётся описанием.
// Ярлык из второго примера — по-прежнему содержание, и его держит правило 11. Сторож — сетка на
// заглушки и «нет», а не цензор смысла: цена ложного срабатывания — потерянная деталь конструкции,
// цена пропуска — одна лишняя строка, которую человек отвергнет щелчком.
//
// ⚠ ПРИМЕНЯЕТСЯ ТОЛЬКО К ЖИВОМУ ОТВЕТУ МОДЕЛИ (designParseLive; MAJOR 1). Повтор читает НАШ
// канонический JSON — то, что человек уже видел и за что заплачено, — и не имеет права прочитать
// его иначе, чем в первый раз: прогон, сохранённый до этой волны с «No closures» в аспектах, обязан
// вернуть его и сегодня, иначе один client_request_id отдаёт разное число аспектов до и после
// выката, а `run.output_text` и `construction.aspects` в одном ответе противоречат друг другу.

// designAbsenceSentinels — (а) ГОЛЫЕ ОТВЕТЫ-ЗАГЛУШКИ: весь текст — одно из этих слов, после снятия
// метки «ключ:» впереди, пунктуации и кавычек по краям, в нижнем регистре. Пустой остаток («—»,
// «-», «…») — тоже заглушка.
var designAbsenceSentinels = map[string]struct{}{
	"no": {}, "none": {}, "n/a": {}, "na": {}, "not applicable": {}, "does not apply": {},
	"not present": {}, "absent": {}, "omitted": {}, "nothing": {}, "nil": {}, "null": {},
	"zero": {}, "0": {},
}

// designFlatDetailSentinels — заглушки, которыми модель отвечает на «какие детали рисовать отдельно»
// вместо пустого списка. Имя детали проверяется ТОЛЬКО правилом (а) и этим списком: имя — подпись,
// не фраза, и «Without side seams — tubular-knit body» — законное имя детали.
var designFlatDetailSentinels = map[string]struct{}{
	"no separate drawing needed": {}, "no separate drawings needed": {},
	"no separate drawing": {}, "none needed": {},
}

// designAspectAbsenceNouns — (в) СЛОВАРЬ ПРЕДМЕТА КЛЮЧА: чем ключ «владеет». Аспект, который под этим
// ключом ОТКРЫВАЕТСЯ отрицанием своего предмета («No visible closures; …» под fastening, «Without
// lining; …» под lining), — отсутствие, сколько бы описания ни шло после связки. Тот же текст под
// ключом без словаря (extraDetails, silhouette, fabric, самодельный) — описание, и (в) его не трогает.
//
// Ключи — складкой (designFoldToken): модель пишет «sleeve_cuff», «Cuffs», «cuff». Существительные —
// в единственном числе, множественное узнаётся по «-s»/«-es». Таблица стоит рядом со словарём ключей
// (designConstructionAspectKeys) намеренно: новый стандартный ключ без строки здесь — это ключ, чьё
// «нет» разбор не узнаёт, и это видно глазом.
//
// ⚠ ОСТАТОК ПОСЛЕ СВЯЗКИ, НАЗЫВАЮЩИЙ ДРУГОЙ ПРЕДМЕТ ТОГО ЖЕ КЛЮЧА, СПАСАЕТ СТРОКУ: «No zipper; three
// buttons at the placket» — описание застёжки, а не её отсутствие. (в) стреляет только когда после
// отрицания предмета о предмете больше не сказано ничего.
var designAspectAbsenceNouns = func() map[string][]string {
	fastening := []string{"closure", "fastening", "fastener", "zip", "zipper", "button", "snap", "hook",
		"velcro", "drawstring", "drawcord", "tie", "toggle", "clasp", "popper", "stud", "lace"}
	lining := []string{"lining", "liner"}
	pockets := []string{"pocket"}
	collar := []string{"collar", "neckband", "neckline"}
	cuffs := []string{"cuff"}
	hem := []string{"hem", "hemline"}
	topstitching := []string{"topstitch", "topstitching", "stitching"}
	hardware := []string{"hardware", "eyelet", "grommet", "rivet", "buckle"}
	aux := []string{"interfacing", "fusing", "tape", "elastic"}
	src := map[string][]string{
		"fastening": fastening, "fastenings": fastening,
		"lining": lining, "linings": lining,
		"pockets": pockets, "pocket": pockets,
		"collar":     collar,
		"sleeveCuff": cuffs, "cuffs": cuffs, "cuff": cuffs,
		"hem": hem, "hems": hem,
		"topstitching": topstitching,
		"hardware":     hardware,
		"auxMaterials": aux,
	}
	out := make(map[string][]string, len(src))
	for key, nouns := range src {
		out[designFoldToken(key)] = nouns
	}
	return out
}()

// designNegationOpeners — (б) открывающие слова КОРОТКОГО ЧИСТОГО ОТРИЦАНИЯ. Длинные раньше
// коротких: «there is no» обязан узнаться прежде «no». «0» проверяется отдельно — см.
// designIsShortNegation: «0.5 cm hem allowance» — не отрицание.
var designNegationOpeners = []string{
	"there isn't any", "there aren't any", "there is no", "there are no",
	"without", "nothing", "none", "not", "no", "zero", "0",
}

// designNegationMaxWords — сколько слов после открывающего ещё «чистое отрицание»: «no visible
// closures at all» — четыре; пятое слово — уже описание.
const designNegationMaxWords = 4

// designPositiveConnectorWords / designPositiveConnectorRunes — ПОЛОЖИТЕЛЬНЫЕ СВЯЗКИ: после них
// идёт описание, и текст остаётся целиком («Nothing but a raw-edge finish», «Without lining; single
// layer throughout»). Тире — em (U+2014), en (U+2013) и дефис-минус с пробелами по бокам; дефис
// внутри слова («raw-edge») связкой не считается.
var designPositiveConnectorWords = map[string]struct{}{
	"but": {}, "except": {}, "with": {}, "single": {}, "only": {}, "instead": {}, "just": {}, "then": {},
}

const designPositiveConnectorRunes = ",;:—–"

// designWordJoiners — знаки, СЦЕПЛЯЮЩИЕ слово: сразу после открывающего слова они означают
// продолжение, а не границу («no-sew», «no‑sew» с U+2011, «n/a», «no_sew»). U+2010…U+2013 — дефисы
// и тире Юникода, которыми модели пишут «no‑sew» так же охотно, как ASCII-дефисом.
const designWordJoiners = "-/_‐‑‒–"

func designIsWordJoiner(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(designWordJoiners, r)
}

// designWordCore — слово без пунктуации по краям («closures;» → «closures», «(none)» → «none»).
func designWordCore(w string) string {
	return strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// designAbsenceNormalize — ОДНА НОРМАЛИЗАЦИЯ НА ОБА ПРЕДИКАТА: нижний регистр, прямые апострофы
// вместо типографских («aren’t» → «aren't»), обычный пробел вместо неразрывного, снятая метка
// «<ключ>:» впереди («Fastening: N/A»), снятые пунктуация, кавычки и пробелы по краям.
func designAbsenceNormalize(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.NewReplacer("’", "'", "‘", "'", " ", " ").Replace(t)
	t = designStripLeadingLabel(t)
	return strings.TrimFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// designStripLeadingLabel снимает метку «<ключ>:» в начале текста — «Fastening: N/A», «sleeve /
// cuff: none». Меткой считается ТОЛЬКО короткое имя ключа: до трёх слов из букв, пробелов и
// сцепляющих знаков перед первым двоеточием. «There aren't any fastenings: pull-on» меткой не
// является (четыре слова с апострофом), и его двоеточие остаётся связкой правила (б).
func designStripLeadingLabel(t string) string {
	i := strings.IndexByte(t, ':')
	if i <= 0 {
		return t
	}
	label := strings.TrimSpace(t[:i])
	if label == "" || len(strings.Fields(label)) > 3 {
		return t
	}
	// «No collar: bound neckline» — не метка ключа, а отрицание с двоеточием; метка — ИМЯ ключа.
	if designIsBareSentinel(label, nil) || designIsShortNegation(label) {
		return t
	}
	for _, r := range label {
		if !unicode.IsLetter(r) && !unicode.IsSpace(r) && !strings.ContainsRune(designWordJoiners, r) {
			return t
		}
	}
	return strings.TrimSpace(t[i+1:])
}

// designIsAbsentAspect — текст аспекта ПОД ЭТИМ КЛЮЧОМ говорит «этого нет» вместо того, чтобы
// описывать деталь: (а) голая заглушка, (б) короткое чистое отрицание, (в) отрицание предмета ключа
// в открывающей клаузе. Ключ — сырой, как написала модель; складывается здесь.
func designIsAbsentAspect(key, text string) bool {
	t := designAbsenceNormalize(text)
	return designIsBareSentinel(t, nil) || designIsShortNegation(t) ||
		designIsKeyDenial(designFoldToken(key), t)
}

// designIsAbsentAspectText — то же БЕЗ ключа: только (а) и (б). Для табличных проб самих предикатов
// и для текста, у которого ключа нет.
func designIsAbsentAspectText(text string) bool { return designIsAbsentAspect("", text) }

// designIsKeyDenial — правило (в) над нормализованным текстом: первая клауза — короткое отрицание по
// (б), среди её слов — предмет ключа, а остаток о предмете молчит.
func designIsKeyDenial(keyFold, t string) bool {
	nouns, ok := designAspectAbsenceNouns[keyFold]
	if !ok {
		return false
	}
	first, rest := designFirstClause(t)
	if !designIsShortNegation(first) {
		return false
	}
	mentions := func(clause string) bool {
		for _, w := range strings.Fields(clause) {
			if designNounMatches(designWordCore(w), nouns) {
				return true
			}
		}
		return false
	}
	return mentions(first) && !mentions(rest)
}

// designFirstClause — то, чем текст ОТКРЫВАЕТСЯ: до первой связки («; , : — –» или дефис с
// пробелами), и остаток после неё (со связкой впереди — designWordCore её снимет).
func designFirstClause(t string) (first, rest string) {
	cut := len(t)
	if i := strings.IndexAny(t, designPositiveConnectorRunes); i >= 0 {
		cut = i
	}
	if i := strings.Index(t, " - "); i >= 0 && i < cut {
		cut = i
	}
	return strings.TrimSpace(t[:cut]), strings.TrimSpace(t[cut:])
}

// designNounMatches — слово целиком, в единственном или множественном числе.
func designNounMatches(word string, nouns []string) bool {
	for _, n := range nouns {
		if word == n || word == n+"s" || word == n+"es" {
			return true
		}
	}
	return false
}

// designIsAbsentFlatDetailName — имя детали для отдельного рисунка — заглушка. ТОЛЬКО правило (а)
// плюс заглушки флэта: имя — подпись, и любая фраза длиннее заглушки — это имя детали, а не отказ
// отвечать («No closures» именем детали не бывает, но и выбрасывать его правилом (б) здесь незачем).
func designIsAbsentFlatDetailName(name string) bool {
	return designIsBareSentinel(designAbsenceNormalize(name), designFlatDetailSentinels)
}

// designIsBareSentinel — правило (а) над УЖЕ нормализованным текстом.
func designIsBareSentinel(t string, extra map[string]struct{}) bool {
	if t == "" {
		return true // одна пунктуация: «—», «-», «…»
	}
	if _, ok := designAbsenceSentinels[t]; ok {
		return true
	}
	_, ok := extra[t]
	return ok
}

// designIsShortNegation — правило (б) над УЖЕ нормализованным текстом: открывающее слово отрицания,
// за ним граница слова, не больше designNegationMaxWords слов и НИ ОДНОЙ положительной связки.
func designIsShortNegation(t string) bool {
	for _, opener := range designNegationOpeners {
		if !strings.HasPrefix(t, opener) {
			continue
		}
		rest := t[len(opener):]
		if rest != "" {
			r, _ := utf8.DecodeRuneInString(rest)
			if opener == "0" && !unicode.IsSpace(r) {
				continue // «0.5 cm», «0-ply» — число, а не «ноль штук»
			}
			if designIsWordJoiner(r) {
				continue // «notched», «nonwoven», «no-sew», «no‑sew», «zero-waste» — слово продолжается
			}
		}
		if strings.ContainsAny(rest, designPositiveConnectorRunes) || strings.Contains(rest, " - ") {
			return false
		}
		words := strings.Fields(rest)
		if len(words) > designNegationMaxWords {
			return false
		}
		for _, w := range words {
			if _, connector := designPositiveConnectorWords[designWordCore(w)]; connector {
				return false
			}
		}
		return true
	}
	return false
}

// designAbsenceTrace — НАБЛЮДАТЕЛЬ ЗА ВЫБРОШЕННЫМИ ОТСУТСТВИЯМИ, а не второй канал ответа.
//
// Разбор чист и контекста не знает, а хендлер обязан НАЗВАТЬ в логе, что именно выброшено (на
// Debug: это факт про промпт — «модель всё ещё пишет отсутствия»). Наблюдатель получает КЛЮЧ, И
// ТОЛЬКО КЛЮЧ — словарный каноническим, самодельный обрезанным до designConstructionMaxTraceKeyRunes:
// текст аспекта выведен из слов человека на доске, и в лог он не едет (ревью 26.09, MINOR).
// Счётчик в статистике говорит СКОЛЬКО, наблюдатель — КАКОЙ КЛЮЧ; список внутри статистики сломал
// бы правило «каждое поле — int, и каждое печатается» (TestConstructionDraftLogPrintsEveryCounter).
// nil — законное значение: повтор и пробы ничего не наблюдают.
type designAbsenceTrace func(key string)

// designParseMode — ЧЕЙ ТЕКСТ ЧИТАЕТ РАЗБОР: живой ответ модели или наш канонический JSON.
//
// ⚠ ОДИН РАЗБОР, ДВА РЕЖИМА, И РАЗНИЦА — РОВНО СМЫСЛОВЫЕ СТОРОЖА. Форму (потолки, дедуп, обрезка,
// словари токенов) проверяют оба: канон обязан читаться той же формой, которой был написан. А
// сторожа СМЫСЛА — «этот текст описывает отсутствие», «это имя — заглушка» — только живой: канон
// уже прошёл их перед записью, и прочитать сохранённое строже, чем в первый раз, значило бы отдать
// на повторе не то, что человек видел (ревью 26.09, MAJOR 1).
type designParseMode uint8

const (
	designParseCanonical designParseMode = iota // повтор: форма, и только форма
	designParseLive                             // ответ модели: форма + смысловые сторожа
)

// ─────────────────────────── системный промпт ───────────────────────────

// designConstructionSystemPrompt — РОЛЬ И ФОРМА ОТВЕТА.
//
// ⚠ ЭТО ВТОРАЯ РОЛЬ, А НЕ ПРАВКА ПЕРВОЙ. draftIdeaSystemPrompt рядом остаётся ДОСЛОВНО тем же:
// его три заголовка — контракт со старым клиентом (V-19), и клиент, который их разбирает,
// продолжает работать ровно до тех пор, пока эти байты не тронуты.
//
// РОЛЬ ГОВОРИТ ПРО КАРТИНКИ, ПОТОМУ ЧТО КАРТИНКИ ПРИЕЗЖАЮТ, и про то, что каждая записка называет
// свою картинку и место на ней: за привязку уже заплачено сборкой промпта, и роль, умалчивающая о
// ней, велела бы модели не пользоваться тем, что ей дали.
//
// ⚠ КРУГ 20, ДВЕ ПРАВКИ, И ОБЕ — ПРО ТО, ЗА ЧТО МЫ ПЛАТИМ ВЫХОДНЫМИ ТОКЕНАМИ.
//
//  1. ВЫНОСОК БОЛЬШЕ НЕ ПРОСЯТ (B-13). Владелец дословно: «DRAFT OF THE CONSTRUCTION не должен
//     добавлять коллауты все это можно добавить в CONSTRUCTION аспектами». Клиент строки выносок
//     всё равно перестал рисовать — но ВЫБРОСИТЬ ИХ ТОЛЬКО НА КЛИЕНТЕ БЫЛО БЫ НЕЧЕСТНО ДВАЖДЫ: мы
//     продолжали бы платить до пятнадцати выходных строк (~$0.01 за нажатие) за то, чего никто не
//     читает, И ТЕРЯЛИ БЫ САМИ ФАКТЫ — швы, застёжки, края, карманы, — вместо того чтобы положить
//     их туда, куда владелец их и адресовал. Поэтому правило 3 переписано на «аспекты», а не
//     удалено, и ключ ушёл из формы ответа. Поле 6 провода и его разбор ОСТАЛИСЬ: сохранённый до
//     этой волны прогон обязан читаться обратно на идемпотентном повторе.
//
//  3. ОЦЕНКА РАСХОДА И ЕДИНИЦА СПРАШИВАЮТСЯ ЗДЕСЬ ЖЕ (B-16), правило 4, и НИТКА С ФУРНИТУРОЙ
//     ПРОСЯТСЯ ВСЛУХ (B-19), правило 10. Отдельный прогон «оцени расход» перечитывал бы те же
//     ≤12 картинок ради вопроса, входы которого этот ответ уже держит, и стоил бы второй кнопки,
//     второй истории и второго счёта. Цена здесь — две строки в контракте и одно правило; за них
//     таблица слотов получает колонку EST USAGE, у которой на стадии замысла нет другого адреса
//     (норма живёт в рецепте колорвея, которого ещё нет, а qty_per_garment — подписанная норма
//     закупки, и приближение модели в ней двигало бы деньги).
//
//     ⚠ ПРАВИЛО 10, А НЕ 9: девятое уже занято колорвеями. Правила нумеруются один раз и не
//     перенумеровываются — сохранённый прогон читается тем же разбором, а промпт, у которого
//     смысл номера уехал, читается человеком неверно.
//
//  2. КОЛОРВЕИ СПРАШИВАЮТСЯ ТЕМ ЖЕ ПЛАТНЫМ ПРОГОНОМ (B-25), правило 9. Второй прогон перечитывал бы
//     те же ≤12 картинок (≈$0.06 входных за нажатие) ради вопроса, входы которого — слоты ткани и
//     цвета доски — этот ответ уже держит.
//
//     ⚠ ЦЕНА ЭТОГО СПИСКА БОЛЬШЕ НЕ ПРИПИСЫВАЕТСЯ К БАЗЕ ОТДЕЛЬНОЙ ВЕЛИЧИНОЙ. Так было («+$0.005,
//     0.03 → 0.035»), и ровно так цена и отстала от потолка: следующая правка подняла
//     designConstructionMaxTokens 3000 → 8000, а приписка осталась прежней. Теперь структурная
//     база СЧИТАЕТСЯ ИЗ ЭТОГО ПОТОЛКА целиком (design_run.go: designDraftIdeaConstructionBaseUSD),
//     то есть колорвеи оплачены тем же числом, что и всё прочее в ответе, — и приписывать к базе
//     больше нечего.
//
// ⚠ ВОЛНА 26.09 (O-32, D-33) — АСПЕКТ ТОЛЬКО КОГДА ОН ЕСТЬ, правило 11. Владелец дословно: после
// генерации в CONSTRUCTION появился FASTENING с текстом «No visible closures; pull-on construction,
// relying on jersey stretch for fit» — у изделия, в котором застёжек нет, — и AUX MATERIALS =
// «Small woven brand/size label sewn at inner side seam», то есть ярлык: строка СПЕКИ, а не деталь
// конструкции. Модель заполняла КАЖДЫЙ ключ из списка, потому что список ей дали, а разрешения
// пропустить ключ — нет. Правило 11 даёт его вслух: аспект — это деталь конструкции, которая НА
// ЭТОМ изделии ЕСТЬ; неприменимый ключ ПРОПУСКАЕТСЯ, отсутствие НЕ ОПИСЫВАЕТСЯ, а ярлыки/бирки
// названы спецификацией по имени. Вторая половина починки — разбор (designIsAbsentAspectText):
// просьба к модели без сторожа остаётся просьбой.
//
// ⚠ ТА ЖЕ ВОЛНА (O-33, D-32) — ДЕТАЛИ ДЛЯ ОТДЕЛЬНОГО РИСУНКА, правило 12 и ключ `flat_details`.
// Клиент делал DETAIL-слот из КАЖДОГО аспекта — и у пуловера появлялся «DETAIL · FASTENING». Но
// «какие детали заслуживают собственного рисунка» — ДРУГОЙ вопрос, чем «какая тут конструкция»,
// и обычный ответ на него — «никакие». Поэтому он задаётся ОТДЕЛЬНОЙ инструкцией ТОГО ЖЕ платного
// вызова (второй прогон перечитывал бы те же картинки ради вопроса, входы которого этот ответ уже
// держит) и отвечается отдельным ключом; пустой список — законный и ожидаемый ответ, и промпт
// говорит это вслух. Стандартные элементы (подгибка, шов, отстрочка, ярлык) не перечисляются.
//
// ⚠ РЕВЬЮ 26.09 (Codex, T32/T33) — ТРИ ПРАВКИ РОЛИ. (1) Правило 7 называет ДЛИНУ каждого поля в
// знаках: предел, которого модель не знает, режет ответ молча ПОСЛЕ оплаты, а полный ответ по
// прежним пределам (2000 рун на замысел, силуэт и ткань) не влезал в потолок токенов вовсе (≈9 100
// при 8 000). Числа — те же константы, что держит разбор и меряет TestConstructionAnswerCeiling
// HoldsTheWorstRealisticAnswer; TestConstructionPromptNamesTheSameLimitsTheParserHolds не даёт им
// разойтись. (2) Правило 12 больше не говорит «не повторяй аспекты»: необычный карман по правилу 3
// — аспект, а по правилу 12 — деталь для рисунка, и буквальная модель, выполняя запрет, опускала бы
// рисунок — ровно то, ради чего ключ заведён. Теперь: не превращать КАЖДЫЙ аспект в деталь
// механически, но одна черта может быть в обоих. (3) Слотов цвета на колорвей — не больше восьми
// (см. designConstructionMaxColourwaySlots).
//
// ⚠ O-44 п.2 (26.09) — КОЛОРВЕЙ КРАСИТ КАЖДЫЙ ЦВЕТНОЙ СЛОТ, НИТКУ ТОЖЕ. Владелец: «в MATERIAL SLOTS
// есть слот THREAD но в COLOURWAYS этого слота нету». Правило 9 просило красить «cloth slot from
// bom», и модель честно красила одни ткани. Теперь оно просит каждый цветной слот карточки и ответа
// — ткань, подклад, нитку, фурнитуру, отделку — с Pantone (TCX для ткани, TCX или C для остального),
// а пользовательский промпт называет слоты карточки поимённо, секцией «Slots to colour»
// (designSlotsToColour).
//
// ⚠ РЕВЬЮ O-44 (26.09, Codex) — ОДИН ПОРЯДОК НА СПИСОК И НА ПРАВИЛО. «Каждый цветной слот» при
// потолке в восемь цветов — обещание, которого потолок не держит, а порядок «сначала все ткани»
// выталкивал нитку за восьмёрку уже на девяти рулонных строках, а на двадцати — за сам список.
// Теперь правило 9 и список говорят одно: не больше восьми, в порядке «главные ткани, нитка,
// остальное», и список режется в этом же порядке (designSlotsToColour держит место нитке внутри
// восьми). Имена списка — в мере разбора (designSlotName), чтобы эхо длинного имени привязалось.
const designConstructionSystemPrompt = "You are a garment technologist's assistant. " +
	"You are shown the moodboard pictures, the designer's concept & construction description, and " +
	"the notes pinned on the pictures — every note names its picture by number and the spot it " +
	"marks, so you know exactly which part of which image it refers to.\n" +
	"Answer with ONE JSON object and nothing else — no prose before or after it, no code fence. " +
	"English. The object has exactly these keys:\n" +
	"{\"silhouette\": string, \"fabric\": string, \"fit\": string, \"concept\": string, " +
	"\"aspects\": [{\"key\": string, \"text\": string}], " +
	"\"bom\": [{\"section\": string, \"purpose\": string, \"kind\": string, \"name\": string, " +
	"\"composition\": string, \"colour\": string, \"pantone\": string, " +
	"\"est_usage\": number, \"unit\": string}], " +
	"\"colourways\": [{\"name\": string, \"color_code\": string, " +
	"\"colours\": [{\"label\": string, \"pantone\": string, \"hex\": string}], " +
	"\"slots\": [{\"slot\": string, \"pantone\": string, \"hex\": string, " +
	"\"colour\": string}]}], " +
	"\"flat_details\": [{\"name\": string, \"note\": string}], " +
	"\"missing\": [string]}\n" +
	"Rules:\n" +
	"1. Never invent a fabric, a colour, a measurement or a piece of hardware that the pictures do " +
	"not show and the notes do not state. Leave the field empty and name what is missing under " +
	"\"missing\" instead.\n" +
	"2. Prefer the designer's own words where they say the same thing.\n" +
	"3. Construction features visible on the pictures — seams, closures, edges, pockets, bindings — " +
	"go into \"aspects\" under the fitting key (fastening, pockets, topstitching, extraDetails, or " +
	"a short custom key); do not list them separately.\n" +
	"4. \"bom\" names components BY THEIR ROLE («main fabric», «neck binding», «care label»), one " +
	"line per component. Use the section / purpose / kind tokens given in the prompt; leave a token " +
	"empty when it does not apply. \"composition\" is written as \"NN% fibre, NN% fibre\". " +
	"\"est_usage\" is an approximate per-garment consumption for the base size, in \"unit\": " +
	"metres of cloth or thread, pieces of hardware; leave both empty when the pictures give no " +
	"basis.\n" +
	"5. \"aspects\" use the keys given in the prompt, or a short custom key when none of them fits; " +
	"at most 60 words each.\n" +
	"6. Do not repeat what the card already says — refine it or leave the field empty.\n" +
	"7. Limits: at most 10 aspects, 15 bom lines, 8 missing notes, 6 flat details, 4 colourways " +
	"of at most 4 colours and 8 slot colours each. Lengths, in characters — longer text is cut: \"concept\" 700; " +
	"\"silhouette\" and \"fabric\" 300 each; an aspect 400 (about 60 words); a missing note 160; " +
	"a flat detail \"name\" 40 and \"note\" 200; a bom \"name\" or \"composition\" 60, a " +
	"\"colour\" 40, a Pantone code 24; a colourway \"name\" 64, its \"color_code\" 24; a colour " +
	"\"label\" 40; a slot \"colour\" 40.\n" +
	"8. \"concept\" is answered ONLY when the prompt says the card has none; otherwise leave it " +
	"empty.\n" +
	"9. \"colourways\": 2 to 4 colour combinations the pictures and the description support — one " +
	"entry per combination, naming the colour-bearing slots of the card and of \"bom\" — cloth, " +
	"lining, thread, hardware, trims — each by its exact name, at most 8, in this order: the main " +
	"cloths, the thread, then the rest; each with a Pantone code (TCX for cloth, TCX or C " +
	"otherwise) and a hex. The card's own slots, when it has any, are listed in the prompt under " +
	"\"Slots to colour\" in that order and spelled as the answer must spell them. \"colours\" is " +
	"the combination's palette: 1 to 4 distinct colours, the main cloth's colour first, each a " +
	"Pantone code and a hex, or a short \"label\" when no Pantone code fits. \"color_code\" is " +
	"the code from the colour list in the prompt closest to the MAIN colour (empty when none is " +
	"close); several colourways may share a code. Never invent a colour the board does not show.\n" +
	"10. \"bom\" always includes one \"thread\" line (sewing thread) unless the card already has " +
	"one. Include hardware and trim lines ONLY when the pictures or the notes show them — a zipper, " +
	"buttons, a drawcord, an eyelet; never add hardware the pictures do not show.\n" +
	"11. An aspect is a construction or making detail that is actually on this garment. Include " +
	"an aspect ONLY when the garment has it: OMIT any key that does not apply, and never write an " +
	"entry that describes an absence (\"no closures\", \"none\", \"not applicable\"). Labels, hang " +
	"tags, care labels, size labels and brand labels are BOM / specification items, never aspects: " +
	"list them under \"bom\" when the pictures show them. \"auxMaterials\" means making aids that " +
	"shape the garment — interfacing, fusing, tape, elastic — not labels.\n" +
	"12. \"flat_details\": the details that need a drawing of their OWN because they cannot be " +
	"understood from the front and back flats — an unusual pocket construction, a special collar, " +
	"cuff, placket or vent, a hidden fastening detail, a hardware detail. Each entry: \"name\" " +
	"(what it is, a few words) and \"note\" (what the drawing must show). If nothing needs a " +
	"separate drawing, return an empty list. Do not list standard elements — plain hems, plain " +
	"seams, topstitching, labels. Do not mechanically turn every aspect into a flat detail; a " +
	"feature may appear in both when its construction fact belongs in \"aspects\" and it also " +
	"needs its own drawing."

// ─────────────────────────── пользовательский промпт ───────────────────────────

// designConstructionUserPrompt — СЛОВЕСНАЯ ЧАСТЬ ЗАПРОСА структурного черновика.
//
// ⚠ СЕКЦИИ 2 И 3 (замысел и записки, приколотые на картинки) СОБИРАЕТ designBoardPromptBody —
// ТА ЖЕ ФУНКЦИЯ, ЧТО СОБИРАЕТ ИХ ДЛЯ ПРОЗАИЧЕСКОГО ЧЕРНОВИКА. Там живёт привязка «picture N +
// место в долях кадра», ради которой был отдельный круг работы и на которую стоят пробы; вторая
// сборка тех же строк разошлась бы с первой в первый же раз, когда правят одну.
//
// ЧТО ДОБАВЛЕНО СВЕРХУ И ЗАЧЕМ КАЖДОЕ:
//   - ШАПКА ИЗДЕЛИЯ — категория, пол, размерный ряд с отмеченным базовым: без неё модель отвечает
//     про «одежду вообще», и ответ приходится править руками там, где карточка уже знает ответ.
//   - «УЖЕ НА КАРТОЧКЕ» — правило 6 системного промпта («не повторяй») невыполнимо, пока модель не
//     видит, что там написано. Без этой секции половина предложений приезжает дубликатами того,
//     что человек уже набрал, и он платит вниманием за каждую строку.
//   - ТОКЕНЫ — закрытые словари спеки. Модель, которой не показали список, отвечает синонимом, и
//     синоним превращается в UNSET у строки, которая на самом деле была верной.
//   - СЛОВАРЬ ЦВЕТА (B-25) — ровно тот же довод, но с ценником: `color_code` предложенного
//     колорвея обязан быть КОДОМ ИЗ СЛОВАРЯ, потому что CreateColorway без него отказывает.
//     Модель, которой словарь не показали, называет цвет словом, разбор обнуляет код, и человек
//     получает предложение, которое нельзя подтвердить — то есть оплаченный список, ни одна
//     строка которого не доводит до продукта.
//   - СЛОТЫ ПОД ЦВЕТ (O-44 п.2) — слоты карточки поимённо, чтобы колорвей красил и нитку с
//     фурнитурой, а не одни ткани, и называл их теми именами, по складке которых цвет
//     привязывается (designSlotsToColour).
//
// ⚠ ЦВЕТА ПРИХОДЯТ ПАРАМЕТРОМ, А НЕ ЧИТАЮТСЯ ЗДЕСЬ. Функция остаётся ЧИСТОЙ: она не ходит ни в
// стор, ни в кэш, и накрывается табличными пробами без обвязки. Тот же список хендлер отдаёт
// проверке ответа (designVerifyColourways) — один список на вопрос и на проверку ответа, потому
// что второе чтение словаря между запросом и разбором дало бы код, которого модели не показывали.
func designConstructionUserPrompt(
	card *entity.TechCard, mood *pb_common.DesignMoodSnapshot, attachedIDs []int,
	colours []entity.Color,
) string {
	var b strings.Builder

	// ─── 1. ШАПКА ИЗДЕЛИЯ ───
	if card != nil {
		if v := strings.TrimSpace(card.Name); v != "" {
			b.WriteString("Garment: " + v + "\n")
		}
		if v := strings.TrimSpace(card.Fit.String); v != "" {
			b.WriteString("Fit: " + v + "\n")
		}
		if v := designCategoryName(card); v != "" {
			b.WriteString("Category: " + v + "\n")
		}
		if v := strings.TrimSpace(card.TargetGender.String); v != "" {
			b.WriteString("Gender: " + v + "\n")
		}
		if v := designSizeRunLine(card); v != "" {
			b.WriteString("Size run: " + v + "\n")
		}
	}

	// ─── 2–3. ЗАМЫСЕЛ И ЗАПИСКИ НА КАРТИНКАХ — ОДНОЙ СБОРКОЙ НА ДВА ПРОМПТА ───
	//
	// ⚠ ВЫЗОВ, А НЕ КОПИЯ И НЕ ВЫРЕЗКА ИЗ ГОТОВОЙ СТРОКИ. Там живёт привязка «picture N + место в
	// долях кадра», ради которой был отдельный круг работы и на которую стоят пробы. Вырезка по
	// заголовку (первый вариант этой функции) держалась бы на том, что заголовок не поправят, —
	// а поправив его, мы вынули бы замысел и записки из платного запроса МОЛЧА.
	b.WriteString(designBoardPromptBody(mood, attachedIDs))

	// ─── 4. УЖЕ НА КАРТОЧКЕ ───
	if already := designCardAlreadySays(card); already != "" {
		b.WriteString("\nAlready on the card — refine, do not repeat:\n" + already)
	}

	// ⚠ ЗАМЫСЕЛ ПРЕДЛАГАЕТСЯ ТОЛЬКО ПУСТОЙ КАРТОЧКЕ, И СКАЗАНО ЭТО ЗДЕСЬ, А НЕ В РОЛИ: роль одна
	// на все карточки, а условие — про ЭТУ. Слова дизайнера старше слов модели, и предложение,
	// соперничающее с ними, попросило бы человека защищать то, что он уже написал.
	if card != nil && strings.TrimSpace(card.Concept.String) == "" {
		b.WriteString("\nThe card has no concept & construction description yet: propose one in \"concept\".\n")
	} else {
		b.WriteString("\nThe card already has a concept & construction description: leave \"concept\" empty.\n")
	}

	// ─── 5. ТОКЕНЫ ───
	b.WriteString("\nTokens — use these spellings exactly:\n")
	b.WriteString("aspect keys: " + strings.Join(designConstructionAspectKeys, ", ") +
		" (or a short custom key when none fits; omit a key this garment does not have)\n")
	b.WriteString("bom sections: " + strings.Join(designBomSectionTokens, ", ") + "\n")
	b.WriteString("bom purposes (roll goods only): " + strings.Join(designBomPurposeTokens, ", ") + "\n")
	b.WriteString("bom kinds (hardware / trims / decoration only): " +
		strings.Join(designBomKindTokens, ", ") + "\n")
	b.WriteString("bom units (for \"unit\"): " + strings.Join(designUnitTokens, ", ") + "\n")
	b.WriteString("fit: " + strings.Join(designConstructionFits, ", ") + "\n")
	b.WriteString(designColourTokenLine(colours))

	// ─── 6. СЛОТЫ ПОД ЦВЕТ (O-44 п.2) ───
	//
	// Рядом со словарём цвета, потому что отвечают они на один вопрос — «чем и что красить в
	// колорвее». Пустая карточка секции не получает: правило 9 и так велит красить слоты ответа.
	if slots := designSlotsToColour(card); slots != "" {
		b.WriteString("\nSlots to colour — the card's material slots, in the order to colour them " +
			"(the main cloths, the thread, then the rest); in every colourway name each " +
			"colour-bearing one exactly as written here:\n" + slots)
	}

	// ⚠ КАТАЛОГА НЕТ, И ЭТО ГОВОРИТСЯ ВСЛУХ. Модель, которой не сказали, что артикулов ей не дали,
	// охотно придумает `material_id`; разбор его всё равно обнулит, но потраченные на выдумку
	// выходные токены обнулить нельзя.
	b.WriteString("\nNo materials catalogue is given: never invent an article id.\n")

	return strings.TrimSpace(b.String())
}

// designColourTokenLine — СТРОКА СЛОВАРЯ ЦВЕТА ДЛЯ ПРОМПТА, ИЛИ ЧЕСТНОЕ «СПИСКА НЕТ» (B-25).
//
// ⚠ ТРИ ИСХОДА, И ТОЛЬКО ОДИН ИЗ НИХ — СПИСОК. Пустой словарь и словарь длиннее потолка дают ОДНУ
// И ТУ ЖЕ строку «списка нет — оставь color_code пустым», потому что вопрос, который она закрывает,
// один: «есть ли у модели чем выбрать код». Урезанный список был бы третьим, ХУДШИМ ответом —
// модель выбирала бы «ближайший код» из набора, который мы молча обрезали, и её выбор выглядел бы
// таким же уверенным, как настоящий.
//
// ⚠ АРХИВНЫЕ ЦВЕТА НЕ ЕДУТ ВОВСЕ (ListColors(ctx,false) у вызывающего): архивный код нельзя дать
// новому продукту, и предложение с ним нельзя подтвердить.
func designColourTokenLine(colours []entity.Color) string {
	if len(colours) == 0 || len(colours) > designConstructionMaxColourRows {
		return "colours: no colour list is given; leave \"color_code\" empty\n"
	}
	rows := make([]string, 0, len(colours))
	for _, c := range colours {
		code := strings.TrimSpace(c.Code)
		if code == "" {
			continue
		}
		row := code
		if name := strings.TrimSpace(c.Name); name != "" {
			row += " · " + name
		}
		if hex := strings.TrimSpace(c.Hex.String); hex != "" {
			row += " · " + hex
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return "colours: no colour list is given; leave \"color_code\" empty\n"
	}
	return "colours (code · name · hex): " + strings.Join(rows, ", ") + "\n"
}

// designColourDictionary — СЛОВАРЬ ЦВЕТА, СЛОЖЕННЫЙ ДЛЯ УЗНАВАНИЯ: складка → канонический код.
//
// ⚠ В КАРТУ КЛАДЁТСЯ И КОД, И ИМЯ, И ЭТО НЕ ЩЕДРОСТЬ, А ЗАМЕР ТОГО, КАК ОТВЕЧАЮТ МОДЕЛИ. Промпт
// показывает «BLK · black · #000000» и просит код; модель регулярно отвечает тем, что человекообразнее
// — именем. Приняв только код, мы обнуляли бы верный ответ и заставляли человека доискивать в пикере
// то, что модель уже выбрала.
//
// ⚠ КОД СТАРШЕ ИМЕНИ, И СТОЛКНОВЕНИЕ ИМЁН УБИВАЕТ ОБЕ СТОРОНЫ. Складка, занятая КОДОМ, именем не
// переписывается: код — это идентификатор, имя — подпись. Складка, на которую претендуют ДВА разных
// имени, выбрасывается из карты вовсе: «bone» двух разных цветов — это не узнавание, а жребий, и
// жребий здесь стоит чужого продукта.
type designColourDictionary map[string]string

func designBuildColourDictionary(colours []entity.Color) designColourDictionary {
	dict := make(designColourDictionary, len(colours)*2)
	for _, c := range colours {
		code := strings.TrimSpace(c.Code)
		if code == "" {
			continue
		}
		dict[designFoldToken(code)] = code
	}
	byName := make(map[string]string, len(colours))
	ambiguous := make(map[string]struct{}, 4)
	for _, c := range colours {
		code, name := strings.TrimSpace(c.Code), designFoldToken(c.Name)
		if code == "" || name == "" {
			continue
		}
		if prev, seen := byName[name]; seen && prev != code {
			ambiguous[name] = struct{}{}
			continue
		}
		byName[name] = code
	}
	for name, code := range byName {
		if _, clash := ambiguous[name]; clash {
			continue
		}
		if _, taken := dict[name]; taken {
			continue
		}
		dict[name] = code
	}
	return dict
}

// designCardSlotFolds — СТРОКИ СПЕКИ, УЖЕ СТОЯЩИЕ НА КАРТОЧКЕ, ПО КЛЮЧУ ПРИВЯЗКИ (designSlotKey).
//
// Это половина ответа на вопрос «есть ли такой слот»; вторая половина — строки спеки САМОГО ОТВЕТА
// (см. designVerifyColourways). Обе нужны: колорвей, предложенный в одном ответе со своими слотами,
// обязан к ним привязаться ДО того, как человек их принял, а колорвей на карточку, где слоты уже
// набраны руками, — к набранным.
//
// ЗНАЧЕНИЕ — ПОЛНОЕ ИМЯ СТРОКИ, КОГДА МЕРА ЕГО ОБРЕЗАЕТ, ИНАЧЕ ПУСТО (ревью O-44, MINOR). Длинное имя
// модель видит обрезанным и обрезанным же повторяет; клиент привязывает цвет к строке по складке
// ПОЛНОГО имени, и обрезанное эхо у него не сложилось бы ни с чем. Поэтому проверка возвращает такому
// слоту имя строки целиком. Имя в пределах меры не трогается: складка и так совпадает, а написание
// ответа — его собственное. Имя длиннее колонки (255 байт) не возвращается: канон повтора режет по
// колонке, и повтор разошёлся бы с первым ответом. Из дублей по ключу остаётся первая строка
// карточки — та, что стоит в списке «Slots to colour».
func designCardSlotFolds(card *entity.TechCard) map[string]string {
	out := make(map[string]string)
	if card == nil {
		return out
	}
	var sink designConstructionStats
	for _, item := range card.BomItems {
		full := designOneLine(item.Name)
		bounded := designSlotName(full, &sink)
		key := designFoldToken(bounded)
		if key == "" {
			continue
		}
		if _, taken := out[key]; taken {
			continue
		}
		restore := ""
		if bounded != full && len(full) <= designConstructionMaxVarchar255 {
			restore = full
		}
		out[key] = restore
	}
	return out
}

// designCategoryName — имя категории карточки, если словарь его знает.
//
// ⚠ ПУСТАЯ СТРОКА — ЗАКОННЫЙ ОТВЕТ, И СТРОКА ПРОМПТА ТОГДА НЕ ПИШЕТСЯ ВОВСЕ. Кэш словарей
// наполняется стартом приложения; в пробе он пуст, и промпт, печатающий «Category: » без значения,
// сообщал бы модели пустое поле как факт.
func designCategoryName(card *entity.TechCard) string {
	if card == nil || !card.CategoryId.Valid || card.CategoryId.Int32 <= 0 {
		return ""
	}
	c, ok := cache.GetCategoryById(int(card.CategoryId.Int32))
	if !ok {
		return ""
	}
	return strings.TrimSpace(c.Name)
}

// designSizeRunLine — размерный ряд ИМЕНАМИ, с отмеченным базовым размером.
//
// Базовый отмечается словом, а не порядком: «по нему считается норма» — это факт о карточке, и
// модель, которой он нужен для ответа про пропорции, не обязана угадывать его из позиции в списке.
func designSizeRunLine(card *entity.TechCard) string {
	if card == nil || len(card.SizeIds) == 0 {
		return ""
	}
	baseID := 0
	if card.BaseSampleSizeId.Valid {
		baseID = int(card.BaseSampleSizeId.Int32)
	}
	names := make([]string, 0, len(card.SizeIds))
	for _, id := range card.SizeIds {
		s, ok := cache.GetSizeById(id)
		if !ok || strings.TrimSpace(s.Name) == "" {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if id == baseID {
			name += " (base)"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// designCardAlreadySays — то, что карточка УЖЕ говорит про конструкцию, тремя списками.
//
// Пустая строка, когда карточка не говорит ничего: секция «не повторяй то, чего нет» — это
// инструкция ни о чём, и она стоила бы входных токенов на каждом нажатии.
//
// ⚠ У СЕКЦИИ ЕСТЬ ПОТОЛОК, И ОН НЕ ВКУС, А ДЕНЬГИ. Ревью круга 19: цикл обходил ВСЕ `card.Details`
// (колонка TEXT), ВСЕ неприколотые выноски и ВСЕ строки спецификации без единого предела. Карточка
// со ста выносками и шестьюдесятью строками спеки добавляет десятки килобайт (~15k входных токенов,
// ≈$0.045) к КАЖДОМУ нажатию — и невидимо для оценки, которая считает одни картинки. Соседний
// платный путь (techcardanalysis/context.go, promptField) держит «единственные ворота, через
// которые проходит каждая строка карточки»; здесь ворот не было ни одних.
//
// ТРИ ПРЕДЕЛА, И КАЖДЫЙ ОТВЕЧАЕТ НА СВОЙ ВОПРОС:
//   - СТРОКА — потолок рун на строку: одна строка не имеет права съесть секцию целиком;
//   - СПИСОК — потолок строк на список: сто выносок читаются не лучше двадцати;
//   - СЕКЦИЯ — потолок БАЙТОВ на всё: три списка вместе не имеют права вырасти без предела, даже
//     когда каждый по отдельности в своём пределе.
//
// ⚠ ОБРЕЗКА НАЗЫВАЕТ СЕБЯ ВСЛУХ. Секция велит модели «не повторяй то, что уже написано»; молча
// показав её половину, мы велели бы молчать о том, чего не показали, — и получили бы дубликаты
// именно там, где обещали их не получать. Строка «(+N more … not listed)» стоит десяток байт и
// делает список ЧЕСТНЫМ вместо ПОЛНОГО.
func designCardAlreadySays(card *entity.TechCard) string {
	if card == nil {
		return ""
	}
	var b strings.Builder
	// budget — общий потолок секции в БАЙТАХ; строки берут из него, пока он не кончится.
	budget := designConstructionMaxAlreadyBytes
	write := func(line string) bool {
		if len(line) > budget {
			return false
		}
		budget -= len(line)
		b.WriteString(line)
		return true
	}
	// tail печатает пропущенное число, если оно есть. Сам он в бюджет не входит: это НАША строка,
	// а не строка карточки, и урезать честность ради данных было бы обменом не в ту сторону.
	tail := func(kind string, skipped int) {
		if skipped > 0 {
			b.WriteString("- (+" + strconv.Itoa(skipped) + " more " + kind + " on the card, not listed)\n")
		}
	}

	shown, skipped := 0, 0
	for _, d := range card.Details {
		key := aiBoundedText(strings.TrimSpace(d.Key.String), designConstructionMaxVarchar64)
		text := aiBoundedText(designOneLine(d.Text.String), designConstructionMaxAlreadyLineRunes)
		if key == "" || text == "" {
			continue
		}
		if shown >= designConstructionMaxAlreadyRows || !write("- "+key+": "+text+"\n") {
			skipped++
			continue
		}
		shown++
	}
	tail("aspects", skipped)

	shown, skipped = 0, 0
	for _, c := range card.Callouts {
		// ВЫНОСКИ ТАБЛИЦЫ, А НЕ ДОСКИ: приколотые на картинки уже уехали секцией 3, и второй раз
		// они приехали бы как «уже сказано», то есть велели бы модели молчать о том, что она
		// как раз и должна прочитать.
		if c.MediaId.Valid && c.MediaId.Int32 > 0 {
			continue
		}
		line := aiBoundedText(designOneLine(entity.TechCardCalloutPrintedLine(c)),
			designConstructionMaxAlreadyLineRunes)
		if line == "" {
			continue
		}
		if c.Number > 0 {
			line = "#" + strconv.Itoa(c.Number) + " " + line
		}
		if shown >= designConstructionMaxAlreadyRows || !write("- callout: "+line+"\n") {
			skipped++
			continue
		}
		shown++
	}
	tail("callouts", skipped)

	shown, skipped = 0, 0
	for _, item := range card.BomItems {
		name := aiBoundedText(designOneLine(item.Name), designConstructionMaxAlreadyLineRunes)
		if name == "" {
			continue
		}
		line := "- bom: " + string(item.Section) + " · " + name
		if comp := aiBoundedText(designOneLine(item.Composition.String),
			designConstructionMaxAlreadyLineRunes); comp != "" {
			line += " · " + comp
		}
		if shown >= designConstructionMaxAlreadyRows || !write(line+"\n") {
			skipped++
			continue
		}
		shown++
	}
	tail("bom lines", skipped)

	return b.String()
}

// designSlotsToColour — СЛОТЫ КАРТОЧКИ, КОТОРЫЕ КОЛОРВЕЙ КРАСИТ, ПОИМЁННО (O-44 п.2).
//
// Владелец: «в MATERIAL SLOTS есть слот THREAD но в COLOURWAYS этого слота нету». Строки спеки
// доезжали до модели ОДНОЙ дорогой — секцией «уже на карточке — не повторяй», то есть как запрет, а
// не как список того, что красить; правило 9 при этом просило «cloth slots from bom», и модель
// честно красила одни ткани. Этот список называет слоты карточки ТЕМИ ЖЕ ИМЕНАМИ, по складке
// которых designVerifyColourways привязывает цвет: слот, названный моделью иначе, отвалился бы на
// проверке и приехал бы счётчиком SlotColoursUnbound, а не цветом.
//
// ПЕРЕЧИСЛЯЮТСЯ ВСЕ СТРОКИ СПЕКИ — ровно то, что человек видит таблицей MATERIAL SLOTS (там строки
// не фильтруются). Какая из них несёт цвет, решает правило 9 («colour-bearing — cloth, lining,
// thread, hardware, trims»): упаковку по имени модель отличает сама, а фильтр по секции здесь был бы
// вторым, молчаливым мнением о том, что красить.
//
// ПОРЯДОК — ОДИН ДЕТЕРМИНИРОВАННЫЙ ПРИОРИТЕТ НА СПИСОК И НА ПРАВИЛО 9 (ревью O-44): главные ткани,
// нитка, остальные рулонные, всё прочее; внутри ступени — порядок карточки. Правило 9 красит не
// больше восьми слотов «в этом порядке», поэтому порядок списка и решает, что будет окрашено, — и
// нитка обязана стоять внутри восьми. Прежний порядок (все ткани, за ними нитка) этого не обещал:
// девять рулонных строк выталкивали нитку за восьмёрку, двадцать — за сам список.
//
//   - ГЛАВНЫЕ ТКАНИ — рулонные строки с назначением main (0265), не больше
//     designConstructionMaxColourwaySlots − 1: восьмое место оставлено нитке, и главные сверх семи
//     уходят к остальным рулонным. Карточка, где main не отмечена ни у одной строки (назначение
//     необязательно, у старых карточек его нет), главной считает ПЕРВУЮ рулонную строку: «главные
//     ткани первыми» обязано значить что-то и там.
//   - НИТКА — все строки секции thread.
//   - ОСТАЛЬНЫЕ РУЛОННЫЕ — подклад, дублерин, утеплитель, ткани без main.
//   - ПРОЧЕЕ — фурнитура, отделка, ярлыки, упаковка.
//
// ТОЛЬКО ИМЕНА: строка на слот, без состава и цвета — те уже едут секцией «уже на карточке», и
// дважды платить за них незачем. ИМЯ — В МЕРЕ РАЗБОРА (designSlotName): длинное имя модель видит
// ровно таким, каким разбор прочтёт её эхо, и привязка идёт по той же складке (designSlotKey). Дубли
// по этой складке схлопываются — первая строка карточки остаётся: привязка всё равно одна.
func designSlotsToColour(card *entity.TechCard) string {
	if card == nil {
		return ""
	}
	const (
		tierMain = iota
		tierThread
		tierRoll
		tierRest
	)
	isMain := func(item entity.TechCardBomItem) bool {
		return entity.IsRollGoodsSection(item.Section) && item.Purpose.Valid &&
			entity.TechCardBomPurpose(item.Purpose.String) == entity.BomPurposeMain
	}
	var sink designConstructionStats
	marked := false
	for _, item := range card.BomItems {
		if isMain(item) && designSlotKey(item.Name) != "" {
			marked = true
			break
		}
	}
	type slot struct {
		name string
		tier int
	}
	seen := make(map[string]struct{}, len(card.BomItems))
	slots := make([]slot, 0, len(card.BomItems))
	mains := 0
	for _, item := range card.BomItems {
		name := designSlotName(item.Name, &sink)
		key := designFoldToken(name)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		tier := tierRest
		switch {
		case entity.IsRollGoodsSection(item.Section):
			tier = tierRoll
			if (isMain(item) || (!marked && mains == 0)) && mains < designConstructionMaxColourwaySlots-1 {
				tier = tierMain
				mains++
			}
		case item.Section == entity.BomSectionThread:
			tier = tierThread
		}
		slots = append(slots, slot{name: name, tier: tier})
	}
	sort.SliceStable(slots, func(i, j int) bool { return slots[i].tier < slots[j].tier })

	var b strings.Builder
	for i, s := range slots {
		if i == designConstructionMaxSlotsToColour {
			b.WriteString("- (+" + strconv.Itoa(len(slots)-i) + " more slots on the card, not listed)\n")
			break
		}
		b.WriteString("- " + s.name + "\n")
	}
	return b.String()
}

// designSlotName — ИМЯ СЛОТА В ЕДИНСТВЕННОЙ МЕРЕ (ревью O-44, MINOR): одна строка, не больше
// designConstructionMaxNameRunes рун с маркером обрезки, не больше 255 байт. В этой мере имя уходит
// в промпт (список «Slots to colour»), в ней же разбор читает эхо модели, и по складке этой меры
// (designSlotKey) цвет привязывается к строке. Раньше список слал имя до 200 рун, а разбор резал эхо
// на 60 — и цвет строки с именем в 61 знак отваливался на привязке: обрезанное эхо не складывалось с
// полным именем. Мера одна на три места, поэтому разойтись им негде.
func designSlotName(name string, stats *designConstructionStats) string {
	return designBoundedBytes(
		designBoundedRunes(designOneLine(name), designConstructionMaxNameRunes, stats),
		designConstructionMaxVarchar255, stats)
}

// designSlotKey — КЛЮЧ ПРИВЯЗКИ ЦВЕТА К СЛОТУ: складка имени в мере designSlotName. Для имени в
// пределах меры это та же складка, что и была (резать нечего, а пробелов складка не видит).
func designSlotKey(name string) string {
	var sink designConstructionStats
	return designFoldToken(designSlotName(name, &sink))
}

// ─────────────────────────── разбор ответа ───────────────────────────

// designConstructionStats — ЧТО РАЗБОР ИСПРАВИЛ И ЧТО ВЫБРОСИЛ.
//
// Считается, чтобы быть НАПЕЧАТАННЫМ ОДНОЙ СТРОКОЙ ЛОГА в хендлере (дисциплина разбора тех-карты):
// счётчик без строки лога — это статистика, которую никто не видит, а строка без счётчика — это
// «что-то пошло не так» без числа. Дрейф написания, приведённый молча (регистр, пробел, дефис),
// сюда НЕ попадает: он утопил бы настоящие коэрции.
type designConstructionStats struct {
	AspectsCustom  int // ключ не из словаря — принят как самодельный
	AspectsDropped int // пустой ключ или пустой текст
	// AspectsAbsent — ТЕКСТ АСПЕКТА ОПИСЫВАЛ ОТСУТСТВИЕ («No visible closures…», «none», «n/a») и
	// строка выброшена (O-32, D-33). Отдельно от AspectsDropped: пустая строка — брак формы, а
	// отсутствие — модель ответила на ключ, который правило 11 велит пропустить; растущее число
	// здесь — счёт к промпту, а не к разбору.
	AspectsAbsent   int
	CalloutsDropped int // строка без слов
	BomDropped      int // строка без имени
	MissingDropped  int
	// FlatDetailsDropped — деталь для отдельного рисунка БЕЗ ИМЕНИ или с именем-отсутствием
	// («none», «no separate drawing needed»): подпись слота — единственное, без чего строку нельзя
	// ни принять, ни отвергнуть, а «none» — форма отказа отвечать, не имя детали (O-33).
	FlatDetailsDropped int
	EnumsUnset         int // секция/назначение/вид не узнаны — строка сохранена, токен пуст
	MaterialIDs        int // предложенный артикул обнулён (каталог не показывали)
	Truncated          int // строка обрезана потолком (рун — у TEXT, байтов — у VARCHAR)
	OverLimit          int // строки, не влезшие в потолок списка
	Deduped            int
	// PairsCleared — токен, законный сам по себе, но НЕЗАКОННЫЙ В ЭТОЙ ПАРЕ, снят со строки
	// (назначение не на рулонном, вид не в своей секции). Считается отдельно от EnumsUnset:
	// «слово не узнано» чинит промпт, «пара невозможна» чинит вопрос, который мы задали.
	PairsCleared int
	// NonScalars — на месте скаляра приехал объект или список. Раньше такой токен брался
	// ДОСЛОВНО, и технологу предлагалось значение `{"top":"tank","bottom":"none"}`.
	NonScalars int
	// FieldsDropped — поле (или элемент списка) не разобралось по типу и выброшено ПООДИНОЧКЕ.
	// До круга 19 любое несовпадение типа роняло json.Unmarshal целиком, то есть весь оплаченный
	// ответ ради одного «aspects": "none"».
	FieldsDropped int

	// ─── КРУГ 20 ───

	// CalloutsUnasked — СКОЛЬКО ВЫНОСОК МОДЕЛЬ ПРИСЛАЛА, ХОТЯ ПРОМПТ ИХ БОЛЬШЕ НЕ ПРОСИТ (B-13).
	//
	// ⚠ ЭТО НЕ CalloutsDropped, И РАЗНИЦА НЕСУЩАЯ. `CalloutsDropped` считает строку БЕЗ СЛОВ —
	// брак формы. Здесь строка ЦЕЛА, ПРИНЯТА и уедет на провод (поле 6 живо ради повтора старых
	// прогонов); посчитана она потому, что «модель отвечает на вопрос, которого ей не задавали» —
	// это факт ПРО ПРОМПТ, а узнать его можно только из лога. Ноль здесь — доказательство, что
	// правило 3 сработало; растущее число — счёт за выходные токены, которых никто не читает.
	//
	// ⚠ И ИМЕННО ПОЭТОМУ ОН НЕ ВХОДИТ В Coerced(). Строка ПРИНЯТА — коэрцировать было нечего, а
	// Warn с текстом «was coerced» на добровольной выноске врал бы про собственный ответ.
	CalloutsUnasked int
	// ColourCodesUnset — предложенный код цвета НЕ УЗНАН словарём и обнулён. Строка колорвея
	// остаётся (её можно подтвердить, выбрав код руками) — та же граница, что у designBomEnum:
	// человеку нужнее предложение без кода, чем отсутствие предложения.
	ColourCodesUnset int
	// SlotColoursUnbound — цвет назван для слота, которого нет НИ В ЭТОМ ОТВЕТЕ, НИ НА КАРТОЧКЕ.
	// Рецепт колорвея ключуется по строке спеки; цвет несуществующего слота некуда положить, и
	// нарисованный он обещал бы человеку строку, которую подтверждение молча пропустит.
	SlotColoursUnbound int
	// ColourwaysDropped — колорвей без имени и без единого привязанного цвета. Подтверждать нечего:
	// продукт требует имени или хотя бы одного цвета, чтобы отличаться от соседнего.
	ColourwaysDropped int
	// ColoursDropped — цвет палитры предложения (T45) без подписи и без пантона: плашку hex нельзя
	// ни проверить, ни сохранить (палитра требует одно из двух). Колорвей остаётся.
	ColoursDropped int
	// ColourFamiliesProposed — СЕМЕЙСТВО ПРЕДЛОЖЕНО СЕРВЕРОМ (T45): модель не назвала узнаваемого
	// кода, и код словаря взят ближайшим к hex главного цвета (entity.NearestColourFamily).
	// ⚠ НЕ ПОТЕРЯ И НЕ ПОПРАВКА — предложение сверх ответа, как CalloutsUnasked, поэтому в Coerced()
	// не входит: Warn «was coerced» на ответе, которому мы ДОБАВИЛИ код, был бы неправдой.
	ColourFamiliesProposed int
	// BomEstDropped — ОЦЕНКА РАСХОДА СНЯТА СО СТРОКИ, А САМА СТРОКА ОСТАЛАСЬ (B-16). Модель пишет
	// «about 2», «1,6», «1.5-2 m» — это не десятичное число, и положить его в DECIMAL(12,3) нельзя.
	//
	// ⚠ СНИМАЕТСЯ ЗНАЧЕНИЕ, А НЕ СТРОКА — та же граница, что у designBomEnum: технологу нужнее слот
	// без оценки, чем отсутствие слота, которую модель увидела верно и оценила словами. Отдельный
	// счётчик потому, что «модель отвечает не числом» чинится ПРОМПТОМ, а не разбором, и узнать
	// это можно только из лога.
	BomEstDropped int
	// UnitsUnset — единица названа, но словарём НЕ УЗНАНА («yd», «yards»), и отдана пустой. Отдельно
	// от EnumsUnset: та считает секцию/назначение/вид — токены, которые промпт перечисляет как
	// закрытый список; единица же приезжает в ту же графу, что и оценка, и её потеря означает
	// «число есть, а в чём — неизвестно», то есть клиент покажет семейное умолчание серым.
	UnitsUnset int
}

// Coerced говорит, было ли ХОТЬ ЧТО-ТО поправлено или выброшено: уровень строки лога решается по
// нему, и строка эта дословно называется «was coerced».
//
// ⚠ ИМЯ, СОСТАВ И СТРОКА ЛОГА ОБЯЗАНЫ СОВПАДАТЬ, И ОДНАЖДЫ ОНИ РАЗОШЛИСЬ. Функция звалась `Any()`
// («было ли хоть что-нибудь»), и под это имя в неё попал CalloutsUnasked — счётчик того, что мы
// ПРИНЯЛИ И СОХРАНИЛИ. Модель, добровольно приславшая одну выноску, поднимала Warn «черновик
// коэрцирован» на ответе, в котором коэрцировать было нечего. Здесь считаются ТОЛЬКО потери и
// поправки; «модель ответила на незаданный вопрос» — факт про промпт, он печатается той же строкой
// (callouts_unasked) и виден на уровне Info, где ему и место.
func (s designConstructionStats) Coerced() bool {
	return s.AspectsCustom+s.AspectsDropped+s.AspectsAbsent+s.CalloutsDropped+s.BomDropped+s.MissingDropped+
		s.FlatDetailsDropped+
		s.EnumsUnset+s.MaterialIDs+s.Truncated+s.OverLimit+s.Deduped+
		s.PairsCleared+s.NonScalars+s.FieldsDropped+
		s.ColourCodesUnset+s.SlotColoursUnbound+s.ColourwaysDropped+s.ColoursDropped+
		s.BomEstDropped+s.UnitsUnset > 0
}

// designLoose — строка, принимающая ТРИ формы, в которых модели пишут скаляр: строку, число и
// null. Тот же приём и тот же довод, что у techcardanalysis.stringList: это дрейф ФОРМЫ, а не
// ложь о карточке, и ронять из-за него весь оплаченный прогон значило бы применить к форме
// наказание, придуманное для содержания.
//
// ⚠ ЧИСЛО ЗДЕСЬ НЕ ГИПОТЕТИЧЕСКОЕ: `material_id` в НАШЕМ СОБСТВЕННОМ каноническом JSON — int64,
// а protojson пишет int64 СТРОКОЙ. Одна и та же величина приезжает числом от модели и строкой с
// повтора, и жёсткий тип отверг бы ровно один из двух путей.
//
// ⚠ ОБЪЕКТ И СПИСОК — НЕ СКАЛЯР, И ДОСЛОВНО ОНИ БОЛЬШЕ НЕ БЕРУТСЯ. Ревью круга 19: прежняя ветка
// «что-то ещё скалярное — берётся как написано» брала ЛЮБОЙ нескалярный токен байтами, и модель,
// ответившая `"silhouette": {"top":"tank","bottom":"none"}`, предлагала технологу эту строку в
// поле силуэта — со скобками и кавычками, как значение. Терпимость к форме кончается там, где
// принятое перестаёт быть текстом, который человек согласится вписать в карточку: такой токен
// читается как ПУСТО и считается (NonScalars), потому что «модель отвечает объектами» — это факт
// про промпт, и узнать его можно только из лога.
type designLoose struct {
	v string
	// nonScalar помнит, что на месте скаляра приехала структура. Флаг живёт на значении, а не в
	// статистике, потому что разбор поля и его подсчёт происходят в разных местах: json.Unmarshal
	// счётчика не видит, а читающая сторона видит.
	nonScalar bool
}

func (l *designLoose) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		*l = designLoose{}
		return nil
	}
	if trimmed[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*l = designLoose{v: str}
		return nil
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		*l = designLoose{nonScalar: true}
		return nil
	}
	// Число или bool — берётся как написано.
	*l = designLoose{v: trimmed}
	return nil
}

func (l designLoose) String() string { return strings.TrimSpace(l.v) }

// designRawDecimal — СКАЛЯР, У КОТОРОГО ЕСТЬ ЧЕТВЁРТАЯ ЗАКОННАЯ ФОРМА: ОБЪЕКТ `{"value":"1.6"}`.
//
// ⚠ ЭТА ФОРМА — НЕ ДРЕЙФ МОДЕЛИ, А НАШ СОБСТВЕННЫЙ КАНОНИЧЕСКИЙ JSON, И БЕЗ НЕЁ КРУГОВОЙ ОБХОД НЕ
// ДЕРЖИТСЯ. `google.type.Decimal` — обычное сообщение с одним полем `value`, а не член семьи
// google.protobuf.*, поэтому у protojson для него НЕТ спецотображения в голый скаляр: писатель
// (designConstructionMarshal) кладёт `"est_usage": {"value": "1.6"}`, и голый designLoose прочитал
// бы это как «на месте скаляра приехала структура» — то есть идемпотентный повтор ТЕРЯЛ БЫ оценку у
// каждой строки и ещё считал бы свою потерю дрейфом модели (NonScalars).
//
// Ровно та же асимметрия «писатель против читателя», что однажды сломала подпись дайджеста: пара
// пишется одной стороной и разбирается другой, и молчит она только у той, которая пишет.
//
// Объект БЕЗ ключа `value` (и любой другой не-скаляр) остаётся нескаляром: терпимость кончается
// там, где принятое перестаёт быть числом.
type designRawDecimal struct {
	designLoose
}

func (d *designRawDecimal) UnmarshalJSON(b []byte) error {
	if trimmed := strings.TrimSpace(string(b)); strings.HasPrefix(trimmed, "{") {
		var wrap struct {
			Value *designLoose `json:"value"`
		}
		if err := json.Unmarshal(b, &wrap); err == nil && wrap.Value != nil && !wrap.Value.nonScalar {
			d.designLoose = *wrap.Value
			return nil
		}
		d.designLoose = designLoose{nonScalar: true}
		return nil
	}
	return d.designLoose.UnmarshalJSON(b)
}

// designTake читает скаляр и СЧИТАЕТ выброшенную структуру. Одна дверь на все чтения: пропустив
// её в одном месте, мы получили бы поле, про которое лог молчит.
func designTake(l designLoose, stats *designConstructionStats) string {
	if l.nonScalar {
		stats.NonScalars++
	}
	return l.String()
}

// ─── КЛЮЧИ ОТВЕТА ───

// designConstructionValueKeys — СЕМЬ КЛЮЧЕЙ, КОТОРЫМ ЕСТЬ КУДА ЛЕЧЬ. Присутствие хотя бы одного из
// них и есть признак «это ответ по схеме»; `missing` в семёрку не входит намеренно — это читаемый
// совет, а не значение поля, и ответ из одного совета не отвечает на заданный вопрос.
// ⚠ `colourways` В СЕМЁРКУ ДОБАВЛЕН, И ОТ ЭТОГО ОНА СТАЛА ВОСЬМЁРКОЙ. Ключ отвечает тому же
// условию, что и остальные семь: ему ЕСТЬ КУДА ЛЕЧЬ (блок предложений на студии, а оттуда —
// продукт). Ответ, содержательный одним лишь списком колорвеев, — законный и оплаченный ответ, и
// без этой строки он уходил бы в `invalid_output`.
//
// ⚠ `callouts` ИЗ СПИСКА НЕ УБРАН, ХОТЯ ПРОМПТ ИХ БОЛЬШЕ НЕ ПРОСИТ (B-13). Список отвечает на
// вопрос «это ответ по нашей схеме», а не «это то, что мы просили»: сохранённый до круга 20 прогон,
// у которого содержательными оказались одни выноски, обязан читаться обратно на повторе.
//
// `flat_details` (O-33) — ДЕВЯТЫЙ, по тому же условию: ему есть куда лечь (DETAIL-слоты флэта).
var designConstructionValueKeys = []string{
	"silhouette", "fabric", "fit", "concept", "aspects", "callouts", "bom", "colourways",
	"flat_details",
}

// designField читает ОДИН необязательный ключ, и НЕУДАЧА ОДНОГО КЛЮЧА НЕ РОНЯЕТ ОСТАЛЬНЫЕ.
//
// ⚠ ЭТО И ЕСТЬ ПОЧИНКА «ТРЕТЬЕГО ИСХОДА» (ревью круга 19). Разбор шёл одним json.Unmarshal в
// struct, поэтому ЛЮБОЕ несовпадение типа — `"aspects": "none"`, `"bom": {}` — роняло весь
// оплаченный ответ, включая шесть полей, которые приехали безупречно. Граница «коэрция против
// отказа» объявлена по форме ОТВЕТА ЦЕЛИКОМ, а не по форме каждого поля; поле, которое не
// разобралось, — это ровно та строка, которую разбор и обязан выбросить поодиночке.
func designField[T any](fields map[string]json.RawMessage, key string, stats *designConstructionStats) T {
	var out T
	raw, ok := fields[key]
	if !ok {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		stats.FieldsDropped++
		var zero T
		return zero
	}
	return out
}

// designListField — то же для списка, и ПОЭЛЕМЕНТНО: одна кривая строка спеки не уносит с собой
// четырнадцать целых. Отсутствующий ключ, `null` и пустой список читаются одинаково — «нечего
// перебирать»; отличать их нужно ровно в одном месте (проверка формы), и оно стоит выше.
func designListField[T any](fields map[string]json.RawMessage, key string, stats *designConstructionStats) []T {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		stats.FieldsDropped++
		return nil
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		var v T
		if err := json.Unmarshal(it, &v); err != nil {
			stats.FieldsDropped++
			continue
		}
		out = append(out, v)
	}
	return out
}

type designRawAspect struct {
	Key  designLoose `json:"key"`
	Text designLoose `json:"text"`
}

// designRawFlatDetail — ОДНА ДЕТАЛЬ ДЛЯ ОТДЕЛЬНОГО РИСУНКА, как её пишет модель (O-33).
type designRawFlatDetail struct {
	Name designLoose `json:"name"`
	Note designLoose `json:"note"`
}

type designRawCallout struct {
	Feature    designLoose `json:"feature"`
	Details    designLoose `json:"details"`
	Dimensions designLoose `json:"dimensions"`
}

type designRawBomLine struct {
	Section     designLoose `json:"section"`
	Purpose     designLoose `json:"purpose"`
	Kind        designLoose `json:"kind"`
	Name        designLoose `json:"name"`
	Composition designLoose `json:"composition"`
	Colour      designLoose `json:"colour"`
	// COLOR И COLOUR — ОДНО ПОЛЕ, ДВА НАПИСАНИЯ. Промпт просит британское, модель, обученная на
	// американских датасетах, регулярно пишет американское; принять одно значило бы терять цвет
	// у каждой второй строки по орфографии.
	ColorUS designLoose `json:"color"`
	Pantone designLoose `json:"pantone"`
	// MaterialID читается, чтобы БЫТЬ ОБНУЛЁННЫМ ГРОМКО (счётчик статистики), а не выброшенным
	// молча: «модель придумывает артикулы» — это про промпт, и узнать это можно только из лога.
	MaterialID designLoose `json:"material_id"`
	// ОЦЕНКА РАСХОДА И ЕЁ ЕДИНИЦА (B-16). designLoose, а не float64, и это не перестраховка:
	// модель пишет число то числом (`1.6`), то строкой (`"1.6 m"`), а НАШ СОБСТВЕННЫЙ канонический
	// JSON, который тот же разбор читает на идемпотентном повторе, пишет google.type.Decimal
	// СТРОКОЙ ВСЕГДА. Объявив здесь float64, мы уронили бы поле на первом же повторе.
	EstUsage designRawDecimal `json:"est_usage"`
	Unit     designLoose      `json:"unit"`
}

// designLooseList — СПИСОК, ТЕРПЯЩИЙ НЕ-СПИСОК НА СВОЁМ МЕСТЕ.
//
// Тот же приём и тот же довод, что у designLoose: `"slots": {}` или `"slots": "black"` — это дрейф
// ФОРМЫ вложенного поля, и ронять из-за него ВЕСЬ колорвей (а вместе с ним имя, код и пантон,
// которые приехали безупречно) значило бы применить к форме наказание, придуманное для содержания.
// Читается как «слотов не названо»; колорвей, у которого не осталось ни имени, ни слотов, дальше
// выбрасывается своим собственным правилом и попадает в ColourwaysDropped.
type designLooseList []json.RawMessage

func (l *designLooseList) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" || trimmed[0] != '[' {
		*l = nil
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		*l = nil
		return nil
	}
	*l = items
	return nil
}

// designRawColourway — ОДНО ПРЕДЛОЖЕНИЕ КОЛОРВЕЯ, КАК ЕГО ПИШЕТ МОДЕЛЬ.
//
// COLOR_CODE И COLOUR_CODE — ОДИН КЛЮЧ, ДВА НАПИСАНИЯ, ровно как colour/color у строки спеки: наш
// собственный канонический JSON пишет `color_code` (так поле названо в схеме — колонка словаря
// называется `color_code`), а промпт, написанный по-британски, регулярно получает `colour_code`.
// Приняв одно, мы теряли бы код у каждого второго ответа по орфографии.
//
// COLOURS И COLORS — ТО ЖЕ САМОЕ (T45): палитра, которую схема называет по-британски, а модель
// регулярно пишет по-американски. Верхние pantone / hex — главный цвет в форме до T45: их пишет
// модель, которая схему палитры не прочла, и они же лежат в каноне прогонов, отвеченных до T45.
type designRawColourway struct {
	Name       designLoose     `json:"name"`
	ColorCode  designLoose     `json:"color_code"`
	ColourCode designLoose     `json:"colour_code"`
	Pantone    designLoose     `json:"pantone"`
	Hex        designLoose     `json:"hex"`
	Colours    designLooseList `json:"colours"`
	ColorsUS   designLooseList `json:"colors"`
	Slots      designLooseList `json:"slots"`
}

// designRawColour — один цвет палитры предложения (T45). Подпись приезжает под именем схемы
// (`label`) или словами цвета, какими модель подписывает слоты (`colour` / `color`), — то же
// терпение к орфографии, что у слота. `pantone_system` схема не спрашивает (книга пишется в коде:
// «19-4005 TCX»), но наш канон пишет его ключом ColorwayColour, и повтор обязан его прочесть.
type designRawColour struct {
	Label         designLoose `json:"label"`
	Colour        designLoose `json:"colour"`
	ColorUS       designLoose `json:"color"`
	Pantone       designLoose `json:"pantone"`
	PantoneSystem designLoose `json:"pantone_system"`
	Hex           designLoose `json:"hex"`
}

type designRawSlotColour struct {
	Slot    designLoose `json:"slot"`
	Pantone designLoose `json:"pantone"`
	Hex     designLoose `json:"hex"`
	Colour  designLoose `json:"colour"`
	ColorUS designLoose `json:"color"`
}

// parseConstructionDraft — ЧИСТАЯ ФУНКЦИЯ: текст модели → проверенный черновик.
//
// Не ходит ни в стор, ни в кэш, ничего не логирует и не знает про прогон — ровно поэтому её можно
// накрыть табличными пробами, а хендлер остаётся одной веткой.
//
// ⚠ ДВА ИСХОДА, КОТОРЫЕ РОНЯЮТ ВЕСЬ ОТВЕТ, И БОЛЬШЕ НИКАКИХ:
//  1. finish_reason == "length" — ответ обрезан потолком токенов. Половина черновика неотличима
//     от полного: человек увидел бы четыре группы и решил, что остального модель не заметила.
//  2. в теле нет JSON-объекта, или в объекте НЕТ НИ ОДНОГО из семи ключей, которым есть куда лечь.
//
// ⚠ «КЛЮЧ ЕСТЬ» СПРАШИВАЕТСЯ У КАРТЫ КЛЮЧЕЙ, А НЕ У УКАЗАТЕЛЯ ПОСЛЕ РАЗБОРА, И ЭТО ПОЧИНКА, А НЕ
// СТИЛЬ. Раньше присутствие ключа читалось по «указатель не nil», но json.Unmarshal кладёт nil в
// указатель и на `"silhouette": null` — то есть ИДЕАЛЬНО ОФОРМЛЕННЫЙ ответ, где модель обычным для
// себя способом сказала «тут ничего», уходил в `invalid_output` ОПЛАЧЕННЫМ. Карта сырых кусков
// отвечает на вопрос, который здесь и задаётся: КЛЮЧ НАПИСАН ИЛИ НЕТ. «Написан и пуст» — законный
// и полезный ответ, «не написан вовсе» — не наша форма.
func parseConstructionDraft(raw, finishReason string) (*pb_common.DesignConstructionDraft, designConstructionStats, error) {
	return parseConstructionDraftTracing(raw, finishReason, nil)
}

// parseConstructionDraftTracing — тот же ЖИВОЙ разбор, с наблюдателем за выброшенными отсутствиями
// (designAbsenceTrace). Хендлер зовёт эту форму, чтобы назвать выброшенное в логе; всё прочее —
// короткую. Обе — designParseLive: это ответ модели, и смысловые сторожа включены.
func parseConstructionDraftTracing(
	raw, finishReason string, onAbsent designAbsenceTrace,
) (*pb_common.DesignConstructionDraft, designConstructionStats, error) {
	var stats designConstructionStats

	if strings.EqualFold(strings.TrimSpace(finishReason), "length") {
		return nil, stats, fmt.Errorf("the construction draft was cut by the token ceiling (finish_reason=%q)", finishReason)
	}

	js := designExtractJSONObject(raw)
	if js == "" {
		return nil, stats, fmt.Errorf("no JSON object in the model output (%q)", aiBoundedText(raw, 200))
	}
	out, err := designParseConstructionObject(js, designParseLive, &stats, onAbsent)
	return out, stats, err
}

// designParseConstructionObject — разбор УЖЕ ВЫДЕЛЕННОГО объекта. Отдельная функция ради второго
// входа: повтор читает НАШ СОБСТВЕННЫЙ канонический JSON и не имеет права терпеть вокруг него прозу
// (см. designConstructionDraftFromRun), а живой ответ модели — обязан. Режим (designParseMode)
// включает или выключает смысловые сторожа; форма проверяется в обоих.
func designParseConstructionObject(
	js string, mode designParseMode, stats *designConstructionStats, onAbsent designAbsenceTrace,
) (*pb_common.DesignConstructionDraft, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &fields); err != nil {
		return nil, fmt.Errorf("the model output is not a construction draft: %v", err)
	}
	found := false
	for _, k := range designConstructionValueKeys {
		if _, ok := fields[k]; ok {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("the model output carries none of the construction draft keys")
	}

	out := &pb_common.DesignConstructionDraft{
		Silhouette: designBoundedRunes(designScalarField(fields, "silhouette", stats), designConstructionMaxSilhouetteRunes, stats),
		Fabric:     designBoundedRunes(designScalarField(fields, "fabric", stats), designConstructionMaxFabricRunes, stats),
		Concept:    designBoundedRunes(designScalarField(fields, "concept", stats), designConstructionMaxConceptRunes, stats),
		Fit:        designConstructionFitByFold[designFoldToken(designScalarField(fields, "fit", stats))],
	}

	// ─── АСПЕКТЫ ───
	seenAspect := make(map[string]struct{})
	for _, a := range designListField[designRawAspect](fields, "aspects", stats) {
		key := designTake(a.Key, stats)
		text := designBoundedRunes(designTake(a.Text, stats), designConstructionMaxAspectRunes, stats)
		// ТЕКСТ ИЗ ОДНОЙ ПУНКТУАЦИИ («—», «-») — ПУСТОЙ: складка без единой буквы или цифры.
		if key == "" || text == "" || designFoldToken(text) == "" {
			stats.AspectsDropped++
			continue
		}
		fold := designFoldToken(key)
		canon, standard := designConstructionAspectByFold[fold]
		// ОТСУТСТВИЕ — НЕ АСПЕКТ (O-32, D-33), И ТОЛЬКО У ЖИВОГО ОТВЕТА (ревью 26.09, MAJOR 1):
		// канон повтора читается той же формой, но БЕЗ смысловых сторожей — иначе один и тот же
		// client_request_id отдавал бы разное число аспектов до и после выката. Выбрасывается ДО
		// дедупа и ДО потолка списка: «no closures» не имеет права занять одно из десяти мест
		// настоящей детали. Наблюдателю — ТОЛЬКО КЛЮЧ, канонический либо обрезанный: текст аспекта
		// выведен из слов человека на доске, и в лог он не едет. Ключ едет в предикат ради правила
		// (в): «no visible closures; …» под fastening — отсутствие, под extraDetails — описание.
		if mode == designParseLive && designIsAbsentAspect(key, text) {
			stats.AspectsAbsent++
			if onAbsent != nil {
				if standard {
					onAbsent(canon)
				} else {
					onAbsent(aiBoundedText(key, designConstructionMaxTraceKeyRunes))
				}
			}
			continue
		}
		custom := false
		if standard {
			key = canon
		} else {
			// САМОДЕЛЬНЫЙ КЛЮЧ ПРИНИМАЕТСЯ, А НЕ ОТВЕРГАЕТСЯ: редактор аспектов принимает такие
			// от человека, и запрет ровно того же от модели был бы правилом про автора, а не
			// про данные. Обрезается ПО БАЙТАМ КОЛОНКИ (detail_key varchar(64)) — потолок в
			// рунах пропускал бы 64 кириллические руны, то есть 128 байт, и MySQL ответил бы
			// сырым 1406.
			key = designBoundedBytes(key, designConstructionMaxVarchar64, stats)
			custom = true
		}
		if _, dup := seenAspect[fold]; dup {
			stats.Deduped++
			continue
		}
		seenAspect[fold] = struct{}{}
		if len(out.Aspects) >= designConstructionMaxAspects {
			stats.OverLimit++
			continue
		}
		// ⚠ СЧИТАЕТСЯ ПРИНЯТАЯ СТРОКА, А НЕ УВИДЕННАЯ. До круга 19 счётчик стоял выше дедупа и
		// потолка и печатал 40 там, где технологу предложено 10, — то есть лог отвечал на вопрос,
		// которого никто не задавал.
		if custom {
			stats.AspectsCustom++
		}
		out.Aspects = append(out.Aspects, &pb_common.DesignConstructionAspect{Key: key, Text: text})
	}

	// ─── ДЕТАЛИ ДЛЯ ОТДЕЛЬНОГО РИСУНКА (O-33, D-32) ───
	//
	// ЭТО НЕ АСПЕКТЫ, И РАЗНИЦА — ВЕСЬ СМЫСЛ КЛЮЧА. Аспект — факт конструкции словами; деталь для
	// флэта — ответ на ДРУГОЙ вопрос: «что нельзя понять по фронту и спине и надо нарисовать
	// отдельно», и обычный ответ на него — «ничего». Клиент делает DETAIL-слоты ИЗ ЭТОГО списка, а
	// не из аспектов (пока делал из аспектов, у пуловера появлялся «DETAIL · FASTENING»).
	//
	// Имя-заглушка («none», «n/a», «no separate drawing needed») — отказ отвечать, а не имя: модели,
	// которой велели вернуть пустой список, случается вернуть список из одного «none». Имя
	// проверяется ТОЛЬКО правилом голой заглушки (designIsAbsentFlatDetailName): имя — подпись, и
	// «Without side seams — tubular-knit body» — законное имя детали. И только у живого ответа, по
	// тому же доводу, что у аспектов. Записка не обязательна — имя есть подпись слота, и без него
	// строку нельзя ни принять, ни отвергнуть.
	seenFlat := make(map[string]struct{})
	for _, d := range designListField[designRawFlatDetail](fields, "flat_details", stats) {
		name := designBoundedRunes(designTake(d.Name, stats), designConstructionMaxFlatDetailNameRunes, stats)
		note := designBoundedRunes(designTake(d.Note, stats), designConstructionMaxFlatDetailNoteRunes, stats)
		fold := designFoldToken(name)
		if fold == "" || (mode == designParseLive && designIsAbsentFlatDetailName(name)) {
			stats.FlatDetailsDropped++
			continue
		}
		if _, dup := seenFlat[fold]; dup {
			stats.Deduped++
			continue
		}
		seenFlat[fold] = struct{}{}
		if len(out.FlatDetails) >= designConstructionMaxFlatDetails {
			stats.OverLimit++
			continue
		}
		out.FlatDetails = append(out.FlatDetails, &pb_common.DesignFlatDetail{Name: name, Note: note})
	}

	// ─── ВЫНОСКИ ───
	//
	// ⚠ РАЗБОР ЖИВ, ХОТЯ ПРОМПТ ВЫНОСОК БОЛЬШЕ НЕ ПРОСИТ (B-13), И ЭТО ТРЕБОВАНИЕ, А НЕ ЗАБЫВЧИВОСТЬ.
	// Идемпотентный повтор читает СОХРАНЁННЫЙ канонический JSON тем же самым разбором; выбросив
	// ключ, мы сломали бы второе нажатие на КАЖДОМ прогоне, отвеченном до этой волны, — и сломали
	// бы навсегда, потому что перезвонить модели по тому же ключу идемпотентности нельзя.
	//
	// Строки, которые модель шлёт по своей воле, ПРИНИМАЮТСЯ и считаются (CalloutsUnasked): клиент
	// их не рисует, а лог говорит, работает ли правило 3.
	seenCallout := make(map[string]struct{})
	for _, c := range designListField[designRawCallout](fields, "callouts", stats) {
		// `feature` уезжает в tech_card_callout.part (varchar(255)), `dimensions` — в одноимённую
		// varchar(255); описание — в TEXT, и там потолок смысловой.
		feature := designBoundedBytes(designTake(c.Feature, stats), designConstructionMaxVarchar255, stats)
		details := designBoundedRunes(designTake(c.Details, stats), designConstructionMaxTextRunes, stats)
		dims := designBoundedBytes(designTake(c.Dimensions, stats), designConstructionMaxVarchar255, stats)
		if feature == "" && details == "" {
			// РАЗМЕР БЕЗ СЛОВ — НЕ СТРОКА. «12 мм» без того, ЧТО двенадцать миллиметров, нельзя
			// ни принять, ни осмысленно отвергнуть.
			stats.CalloutsDropped++
			continue
		}
		fold := designFoldToken(feature + "|" + details)
		if _, dup := seenCallout[fold]; dup {
			stats.Deduped++
			continue
		}
		seenCallout[fold] = struct{}{}
		if len(out.Callouts) >= designConstructionMaxCallouts {
			stats.OverLimit++
			continue
		}
		out.Callouts = append(out.Callouts, &pb_common.DesignConstructionCallout{
			Feature: feature, Details: details, Dimensions: dims,
		})
		stats.CalloutsUnasked++
	}

	// ─── СПЕЦИФИКАЦИЯ ───
	seenBom := make(map[string]struct{})
	for _, l := range designListField[designRawBomLine](fields, "bom", stats) {
		// РУНЫ (правило 7), ЗАТЕМ БАЙТЫ (колонка): второй потолок после первого недостижим, но он —
		// сторож колонки, а не смысла, и стоит на своём месте.
		name := designBoundedBytes(
			designBoundedRunes(designTake(l.Name, stats), designConstructionMaxNameRunes, stats),
			designConstructionMaxVarchar255, stats)
		if name == "" {
			// СТРОКА СПЕКИ БЕЗ ИМЕНИ НЕ СОХРАНЯЕТСЯ ВОВСЕ (свободная строка обязана нести имя),
			// поэтому её нечего и предлагать.
			stats.BomDropped++
			continue
		}
		fold := designFoldToken(name)
		if _, dup := seenBom[fold]; dup {
			stats.Deduped++
			continue
		}
		seenBom[fold] = struct{}{}
		if len(out.Bom) >= designConstructionMaxBom {
			stats.OverLimit++
			continue
		}
		colour := designTake(l.Colour, stats)
		if colour == "" {
			colour = designTake(l.ColorUS, stats)
		}
		section := designBomEnum(designTake(l.Section, stats), designBomSectionByFold, stats)
		purpose := designBomEnum(designTake(l.Purpose, stats), designBomPurposeByFold, stats)
		kind := designBomEnum(designTake(l.Kind, stats), designBomKindByFold, stats)
		purpose, kind = designPairBomTokens(section, purpose, kind, stats)
		line := &pb_common.DesignConstructionBomLine{
			Name: name,
			Composition: designBoundedBytes(
				designBoundedRunes(designTake(l.Composition, stats), designConstructionMaxCompositionRunes, stats),
				designConstructionMaxVarchar255, stats),
			Colour: designBoundedBytes(
				designBoundedRunes(colour, designConstructionMaxColourRunes, stats),
				designConstructionMaxVarchar255, stats),
			// PANTONE — varchar(64) (0363), и СВОЕГО СТОРОЖА В DTO У НЕГО НЕТ ВОВСЕ: длинная
			// строка доезжала бы до MySQL и возвращалась сырым 1406, не назвав ни строки, ни поля.
			Pantone: designBoundedBytes(
				designBoundedRunes(designTake(l.Pantone, stats), designConstructionMaxPantoneRunes, stats),
				designConstructionMaxVarchar64, stats),
			Section: section,
			Purpose: purpose,
			Kind:    kind,
			// ОЦЕНКА РАСХОДА И ЕЁ ЕДИНИЦА (B-16). Обе НЕОБЯЗАТЕЛЬНЫ и обе снимаются поодиночке:
			// число без единицы читается семейным умолчанием клиента, единица без числа — пустой
			// графой. Уронить из-за них строку было бы наказанием, придуманным для содержания.
			EstUsage: designBoundedDecimal(designTake(l.EstUsage.designLoose, stats), stats),
			Unit:     designUnitToken(designTake(l.Unit, stats), stats),
		}
		// ⚠ АРТИКУЛ ОБНУЛЯЕТСЯ ВСЕГДА, И ЭТО ФАЗА, А НЕ ЗАБЫВЧИВОСТЬ: каталог в промпт не уезжает
		// (фаза 4), значит подтвердить предложенный id нечем. Строка, выглядящая связанной и
		// оценённой, но указывающая на чужой артикул, — ошибка себестоимости с ценником; строка
		// без связи — законная и полезная строка спеки.
		if v := designTake(l.MaterialID, stats); v != "" && v != "0" {
			stats.MaterialIDs++
		}
		out.Bom = append(out.Bom, line)
	}

	// ─── ЧТО СТОИТ ПРИКОЛОТЬ ───
	seenMissing := make(map[string]struct{})
	for _, m := range designListField[designLoose](fields, "missing", stats) {
		text := designBoundedRunes(designTake(m, stats), designConstructionMaxMissingRunes, stats)
		if text == "" {
			stats.MissingDropped++
			continue
		}
		fold := designFoldToken(text)
		if _, dup := seenMissing[fold]; dup {
			stats.Deduped++
			continue
		}
		seenMissing[fold] = struct{}{}
		if len(out.Missing) >= designConstructionMaxMissing {
			stats.OverLimit++
			continue
		}
		out.Missing = append(out.Missing, text)
	}

	// ─── КОЛОРВЕИ (B-25) ───
	//
	// ⚠ ЗДЕСЬ ТОЛЬКО ФОРМА: потолки, границы колонок, дедуп, шестнадцатеричный цвет. КОД СЛОВАРЯ И
	// ПРИВЯЗКА СЛОТОВ ПРОВЕРЯЮТСЯ ОТДЕЛЬНЫМ ШАГОМ (designVerifyColourways), И РАЗДЕЛЕНИЕ ЭТО
	// НЕСУЩЕЕ. Этот же разбор читает СОХРАНЁННЫЙ канонический JSON на идемпотентном повторе — там
	// ни словаря, ни карточки под рукой нет и быть не должно. Сложив проверку сюда, мы получили бы
	// повтор, который сверяет вчерашний оплаченный ответ с СЕГОДНЯШНИМ словарём: архивированный за
	// ночь цвет молча обнулял бы код, а переименованная строка спеки — уносила бы цвета слотов.
	// Прогон отвечен один раз и навсегда.
	seenColourway := make(map[string]struct{})
	for _, c := range designListField[designRawColourway](fields, "colourways", stats) {
		// ИМЯ — ПОДПИСЬ: потолок в РУНАХ (смысловой, 64), затем в БАЙТАХ (колонка
		// tech_card_colorway.dev_name, varchar(255)). Порядок именно такой: рунный потолок строже
		// для латиницы, байтовый — для кириллицы, и нужны оба.
		name := designBoundedBytes(
			designBoundedRunes(designTake(c.Name, stats), designConstructionMaxColourwayNameRunes, stats),
			designConstructionMaxVarchar255, stats)
		code := designTake(c.ColorCode, stats)
		if code == "" {
			code = designTake(c.ColourCode, stats)
		}
		cw := &pb_common.DesignColourwayProposal{
			Name: name,
			// КОД ЕДЕТ СЫРЫМ И ПРОВЕРЯЕТСЯ ШАГОМ ВЫШЕ ПО МАРШРУТУ. Здесь он лишь обрезан по
			// колонке словаря (varchar(64) с запасом: настоящий код — три знака).
			ColorCode: designBoundedBytes(
				designBoundedRunes(code, designConstructionMaxColourCodeRunes, stats),
				designConstructionMaxVarchar64, stats),
			Pantone: designBoundedBytes(
				designBoundedRunes(designTake(c.Pantone, stats), designConstructionMaxPantoneRunes, stats),
				designConstructionMaxVarchar64, stats),
			Hex: designHexColour(designTake(c.Hex, stats)),
		}
		seenSlot := make(map[string]struct{}, len(c.Slots))
		for _, rawSlot := range c.Slots {
			var s designRawSlotColour
			if err := json.Unmarshal(rawSlot, &s); err != nil {
				stats.FieldsDropped++
				continue
			}
			// ИМЯ СЛОТА — В МЕРЕ designSlotName: той же, в которой его показал промпт и по которой его
			// привяжет проверка. КАНОН ПОВТОРА — ИСКЛЮЧЕНИЕ: проверка могла вернуть слоту ПОЛНОЕ имя
			// строки карточки (designCardSlotFolds), и повтор обязан отдать его таким, каким его
			// получил клиент, — в каноне режет только колонка, и ничего больше (канон, записанный до
			// меры, тоже читается байт в байт).
			var slot string
			if mode == designParseCanonical {
				slot = designBoundedBytes(designTake(s.Slot, stats), designConstructionMaxVarchar255, stats)
			} else {
				slot = designSlotName(designTake(s.Slot, stats), stats)
			}
			fold := designFoldToken(slot)
			if fold == "" {
				// ЦВЕТ БЕЗ СЛОТА НЕКУДА ПОЛОЖИТЬ: строка рецепта ключуется именем строки спеки.
				stats.SlotColoursUnbound++
				continue
			}
			if _, dup := seenSlot[fold]; dup {
				stats.Deduped++
				continue
			}
			seenSlot[fold] = struct{}{}
			if len(cw.Slots) >= designConstructionMaxColourwaySlots {
				stats.OverLimit++
				continue
			}
			colour := designTake(s.Colour, stats)
			if colour == "" {
				colour = designTake(s.ColorUS, stats)
			}
			cw.Slots = append(cw.Slots, &pb_common.DesignColourwaySlotColour{
				Slot: slot,
				Pantone: designBoundedBytes(
					designBoundedRunes(designTake(s.Pantone, stats), designConstructionMaxPantoneRunes, stats),
					designConstructionMaxVarchar64, stats),
				Hex: designHexColour(designTake(s.Hex, stats)),
				Colour: designBoundedBytes(
					designBoundedRunes(colour, designConstructionMaxColourRunes, stats),
					designConstructionMaxVarchar255, stats),
			})
		}
		// ─── ПАЛИТРА (T45) ───
		//
		// ТОЛЬКО ФОРМА, КАК И ВСЁ В ЭТОМ ЦИКЛЕ: потолки, пустые и повторы. Зеркало главного цвета в
		// верхние pantone / hex и предложение семейства — живой шаг (designSettleColourwayPalettes):
		// на повторе палитра читается из канона ровно такой, какой её получил клиент, а канон прогона,
		// отвеченного до T45, палитры не несёт вовсе — и выдумывать её задним числом нельзя.
		rawColours := c.Colours
		if len(rawColours) == 0 {
			rawColours = c.ColorsUS
		}
		seenColour := make(map[string]struct{}, len(rawColours))
		for _, raw := range rawColours {
			var rc designRawColour
			if err := json.Unmarshal(raw, &rc); err != nil {
				stats.FieldsDropped++
				continue
			}
			label := designTake(rc.Label, stats)
			if label == "" {
				label = designTake(rc.Colour, stats)
			}
			if label == "" {
				label = designTake(rc.ColorUS, stats)
			}
			// Подпись и пантон — в РУНАХ: колонки палитры (product_colour, 0375) считают знаки, а
			// не байты, и 40 / 24 знака лежат внутри их 64.
			colour := &pb_common.ColorwayColour{
				Label:         designBoundedRunes(label, designConstructionMaxColourRunes, stats),
				Pantone:       designBoundedRunes(designTake(rc.Pantone, stats), designConstructionMaxPantoneRunes, stats),
				PantoneSystem: designColourBook(designTake(rc.PantoneSystem, stats)),
				Hex:           designHexColour(designTake(rc.Hex, stats)),
			}
			if colour.Pantone == "" {
				colour.PantoneSystem = "" // книга без кода не пишется (entity.NormalizeColorwayPalette)
			}
			if colour.Label == "" && colour.Pantone == "" {
				// ЦВЕТ БЕЗ ИМЕНИ И БЕЗ КОДА — ОДНА ПЛАШКА hex, которую нельзя ни проверить, ни
				// сохранить: палитра требует пантон или подпись.
				stats.ColoursDropped++
				continue
			}
			fold := designFoldToken(colour.Pantone + "|" + colour.Label)
			if _, dup := seenColour[fold]; dup {
				stats.Deduped++
				continue
			}
			seenColour[fold] = struct{}{}
			if len(cw.Colours) >= designConstructionMaxColourwayColours {
				stats.OverLimit++
				continue
			}
			cw.Colours = append(cw.Colours, colour)
		}
		if designColourwayIsEmpty(cw) {
			stats.ColourwaysDropped++
			continue
		}
		// ДЕДУП ПО СЛОЖЕННОМУ ИМЕНИ: два «Black / Bone» в одном ответе — это одно предложение,
		// напечатанное дважды, и подтвердив оба, человек завёл бы два одинаковых колорвея (с T45 —
		// с двумя разными SKU-токенами: занятым код словаря больше не бывает).
		fold := designFoldToken(cw.Name + "|" + cw.ColorCode)
		if _, dup := seenColourway[fold]; dup {
			stats.Deduped++
			continue
		}
		seenColourway[fold] = struct{}{}
		if len(out.Colourways) >= designConstructionMaxColourways {
			stats.OverLimit++
			continue
		}
		out.Colourways = append(out.Colourways, cw)
	}

	return out, nil
}

// designColourwayIsEmpty — «ПОДТВЕРЖДАТЬ НЕЧЕГО».
//
// Правило дизайна дословно: колорвей без имени И без слотов выбрасывается. Именно эта пара, а не
// «пусто по всем полям»: имя без цветов — законный колорвей (цвета доставят на вкладке), цвета без
// имени — тоже (сервер подпишет его «colourway N»). Пустое И то, и другое — строка, которая не
// отличается от соседней ничем.
//
// T45: палитра — тоже содержание. Предложение из одних цветов подписывается «colourway N» и
// подтверждается так же, как безымянное со слотами. Считаются только цвета, которые модель
// НАЗВАЛА списком: главный цвет, выведенный из верхнего пантона (designSettleColourwayPalettes),
// появляется после этой проверки и пустую строку не спасает — ровно как до T45.
func designColourwayIsEmpty(c *pb_common.DesignColourwayProposal) bool {
	return c.GetName() == "" && len(c.GetSlots()) == 0 && len(c.GetColours()) == 0
}

// designColourBook — книга Pantone из канона повтора: как записана, верхним регистром, не длиннее
// колонки (8 знаков). Схема ответа её не спрашивает; значение, не похожее на книгу, читается пустым.
func designColourBook(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" || utf8.RuneCountInString(s) > entity.ColorwayColourSystemMaxRunes {
		return ""
	}
	return s
}

// designHexColour — ЭКРАННОЕ ПРИБЛИЖЕНИЕ ЦВЕТА ИЛИ ПУСТО, ТРЕТЬЕГО НЕ ДАНО.
//
// ⚠ ПРОВЕРКА, А НЕ ОБРЕЗКА, И ЭТО РАЗНЫЕ ВЕЩИ. `dev_hex` — varchar(7), то есть «#RRGGBB» ровно;
// обрезав «black» до семи знаков, мы положили бы в колонку слово и нарисовали бы человеку плашку
// цвета, которого никто не называл. Не шестнадцатеричное — это ОТСУТСТВИЕ приближения, и пустая
// строка говорит именно это; пантон рядом при этом остаётся, и клиент рисует плашку по нему
// (findPantone) — то есть по КОДУ, который человек может проверить, а не по выдумке.
//
// Сокращённая запись (#RGB) НЕ ПРИНИМАЕТСЯ: разворачивать её значило бы называть цвет, который
// модель не написала, а колонка всё равно ждёт семь знаков.
func designHexColour(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 7 || s[0] != '#' {
		return ""
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		hex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !hex {
			return ""
		}
	}
	return s
}

// designVerifyColourways — ВТОРОЙ, НЕЧИСТЫЙ ПО ВХОДАМ ШАГ: ответ сверяется С НАШИМИ СОБСТВЕННЫМИ
// ДАННЫМИ — со словарём цвета и со строками спеки.
//
// ⚠ ПОЧЕМУ ЭТО ОТДЕЛЬНАЯ ФУНКЦИЯ, А НЕ ЧАСТЬ РАЗБОРА. Разбор зовётся ДВАЖДЫ: на живом ответе
// модели и на СОХРАНЁННОЙ строке при идемпотентном повторе. Проверка обязана случиться РОВНО ОДИН
// РАЗ — на живом ответе, до записи канонического JSON. Сверка на повторе означала бы, что
// вчерашний оплаченный ответ пересматривается сегодняшним словарём: цвет, архивированный за ночь,
// молча терял бы код, а переименованная строка спеки уносила бы с собой цвета слотов. То, что
// человек уже видел и за что заплачено, менять нельзя — это тот же довод, по которому в
// `output_text` уезжает проверенный канон, а не ответ модели.
//
// ФУНКЦИЯ ОСТАЁТСЯ ЧИСТОЙ: словарь и складки карточки приходят готовыми, стор она не знает.
func designVerifyColourways(
	draft *pb_common.DesignConstructionDraft,
	dict designColourDictionary,
	cardSlots map[string]string,
	stats *designConstructionStats,
) {
	if draft == nil || len(draft.Colourways) == 0 {
		return
	}

	// ЧТО СЧИТАЕТСЯ СУЩЕСТВУЮЩИМ СЛОТОМ: строки спеки ЭТОГО ЖЕ ОТВЕТА плюс строки спеки КАРТОЧКИ.
	// Первое — потому что колорвей и его слоты приезжают одним ответом и человек примет их одним
	// заходом; второе — потому что на карточке со набранной руками спекой предложение обязано
	// лечь на неё, а не потребовать пересоздать слоты.
	//
	// КЛЮЧ — designSlotKey, складка имени в мере разбора (ревью O-44, MINOR): эхо имени длиннее
	// шестидесяти знаков разбор обрезал, и сложить его можно только с так же обрезанным именем
	// строки. ЗНАЧЕНИЕ — имя, которое слот получает на проводе: у строки карточки, чьё имя мера
	// обрезает, — её полное имя (см. designCardSlotFolds), у остальных пусто — имя остаётся, как его
	// написал ответ. Строка карточки перекрывает строку ответа с тем же ключом.
	bound := make(map[string]string, len(cardSlots)+len(draft.Bom))
	for _, line := range draft.Bom {
		if key := designSlotKey(line.GetName()); key != "" {
			bound[key] = ""
		}
	}
	for key, full := range cardSlots {
		bound[key] = full
	}

	// ─── ОДИН КОД — СКОЛЬКО УГОДНО КОЛОРВЕЕВ (T45, 27.09) ───
	//
	// До T45 здесь стоял дедуп «один код — один колорвей»: `product` держал UNIQUE(style_id,
	// color_code), и второе предложение с тем же кодом подтверждалось отказом сервера. С T45 код
	// словаря — лишь СЕМЕЙСТВО (фильтр, сборка), уникален SKU-токен, который сервер чеканит сам
	// (миграция 0376 сняла uniq_product_style_color). «Black / Bone» и «Black / Ivory» — два законных
	// колорвея одного семейства BLK, и снимать код со второго значило бы заставить человека выбирать
	// руками то, что модель выбрала верно.
	kept := draft.Colourways[:0]
	for _, cw := range draft.Colourways {
		// ─── КОД СЛОВАРЯ ───
		if cw.ColorCode != "" {
			if canon, ok := dict[designFoldToken(cw.ColorCode)]; ok {
				cw.ColorCode = canon
			} else {
				// ⚠ СТРОКА ОСТАЁТСЯ, ОБНУЛЯЕТСЯ ТОЛЬКО КОД — та же граница, что у designBomEnum.
				// Человеку нужнее предложение, у которого код надо выбрать самому, чем отсутствие
				// предложения, которое модель составила правильно и подписала своим словом.
				cw.ColorCode = ""
				stats.ColourCodesUnset++
			}
		}

		// ─── ПРИВЯЗКА ЦВЕТОВ К СЛОТАМ ───
		keptSlots := cw.Slots[:0]
		for _, s := range cw.Slots {
			full, ok := bound[designSlotKey(s.GetSlot())]
			if !ok {
				stats.SlotColoursUnbound++
				continue
			}
			if full != "" {
				s.Slot = full
			}
			keptSlots = append(keptSlots, s)
		}
		cw.Slots = keptSlots

		if designColourwayIsEmpty(cw) {
			stats.ColourwaysDropped++
			continue
		}

		kept = append(kept, cw)
	}
	draft.Colourways = kept

	// ─── ПОДПИСЬ БЕЗЫМЯННОМУ ───
	//
	// ⚠ СЧИТАЕТСЯ ПО ПОРЯДКУ В ОТВЕТЕ, А НЕ ПО ЧИСЛУ БЕЗЫМЯННЫХ: «colourway 2» рядом с «Black /
	// Bone» и «colourway 3» читается как «второе и третье предложение», а два подряд «colourway 1»
	// и «colourway 2» на местах 2 и 4 не сказали бы человеку ничего.
	for i, cw := range draft.Colourways {
		if cw.Name == "" {
			cw.Name = "colourway " + strconv.Itoa(i+1)
		}
	}
}

// designSettleColourwayPalettes — ПАЛИТРА И СЕМЕЙСТВО ПРЕДЛОЖЕНИЯ, ТРЕТИЙ ШАГ ЖИВОГО ОТВЕТА (T45).
//
// Зовётся ТАМ ЖЕ И ТАК ЖЕ, как designVerifyColourways, — один раз, на живом ответе, до записи
// канона, с ТЕМ ЖЕ списком цветов, что уехал в промпт. На повторе палитра и код читаются из канона
// ровно такими, какими их получил клиент: вчерашний ответ не пересчитывается сегодняшним словарём.
//
// ЧТО ДЕЛАЕТ, ПО ПОРЯДКУ, ДЛЯ КАЖДОГО ПРЕДЛОЖЕНИЯ:
//
//  1. ЗЕРКАЛО. Палитра названа — верхние pantone / hex становятся её главным цветом (colours[0]):
//     клиент, написанный до T45, читает цвет предложения оттуда, и два источника одного цвета не
//     имеют права разойтись. Палитра не названа, а верхний пантон есть (модель ответила формой до
//     T45) — главный цвет палитры выводится из него: подтверждение шлёт development.colours, и
//     предложение с пантоном не должно приезжать без палитры.
//  2. СЕМЕЙСТВО. Код словаря не узнан или не назван — предлагается ближайший НЕАРХИВНЫЙ цвет
//     словаря к hex главного цвета (entity.NearestColourFamily: OKLab, серые — по светлоте, цветные —
//     прежде всего по тону) —
//     та же мера, которой CreateColorway предлагает семейство колорвею, созданному без кода.
//     Без hex код остаётся пустым: клиент спросит семейство у человека.
func designSettleColourwayPalettes(draft *pb_common.DesignConstructionDraft, colours []entity.Color, stats *designConstructionStats) {
	if draft == nil {
		return
	}
	for _, cw := range draft.Colourways {
		if len(cw.Colours) > 0 {
			cw.Pantone = cw.Colours[0].GetPantone()
			cw.Hex = cw.Colours[0].GetHex()
		} else if cw.Pantone != "" {
			cw.Colours = []*pb_common.ColorwayColour{{Pantone: cw.Pantone, Hex: cw.Hex}}
		}
		if cw.ColorCode != "" {
			continue
		}
		hex := cw.Hex
		if len(cw.Colours) > 0 && cw.Colours[0].GetHex() != "" {
			hex = cw.Colours[0].GetHex()
		}
		if nearest, ok := entity.NearestColourFamily(hex, colours); ok {
			cw.ColorCode = nearest.Code
			stats.ColourFamiliesProposed++
		}
	}
}

// designScalarField — один скалярный ключ ответа: прочитан, посчитан, отдан строкой. Отсутствующий
// ключ и пустая строка дают одно и то же ЗНАЧЕНИЕ; разница между ними нужна ровно один раз — при
// проверке формы — и спрашивается там у карты ключей.
func designScalarField(fields map[string]json.RawMessage, key string, stats *designConstructionStats) string {
	return designTake(designField[designLoose](fields, key, stats), stats)
}

// designPairBomTokens СНИМАЕТ СО СТРОКИ ТОКЕН, ЗАКОННЫЙ САМ ПО СЕБЕ И НЕВОЗМОЖНЫЙ В ЭТОЙ ПАРЕ.
//
// ⚠ РАДИУС ПОРАЖЕНИЯ ЗДЕСЬ — ВСЯ КАРТОЧКА, И ИМЕННО ПОЭТОМУ ЭТО ЧИНИТСЯ В РАЗБОРЕ. Разбор клал
// section / purpose / kind независимо друг от друга, и `{"section":"hardware","purpose":"main",
// "kind":"button"}` проходил: каждый токен есть в своём словаре. Сохранение же требует ПАР —
// назначение только на рулонных (store/techcard/materials.go, rollGoodsSections) и вид только в
// своей домашней секции (validateBomKindSection), — а UpsertTechCard устроен «всё-или-ничего».
// То есть одна предложенная строка спеки отказывала в сохранении ВСЕЙ тех-карты, причём поля,
// которые её сломали, интерфейс предложения даже не рисует: человек не видит, что править.
//
// ⚠ СНИМАЕТСЯ ТОКЕН, А НЕ СТРОКА, И ЭТО ТА ЖЕ ГРАНИЦА, ЧТО У designBomEnum: технологу нужнее
// строка спеки без назначения, чем отсутствие строки, которую модель увидела правильно.
//
// ⚠ ПРАВИЛО НЕ ПЕРЕПИСАНО, А ПОЗВАНО ПО ИМЕНИ (entity.IsRollGoodsSection, entity.IsKindEligibleSection,
// entity.BomKindHomeSection). Вторая копия «какие семьи рулонные» разошлась бы с первой в тот день,
// когда семей станет пять, и разбор снова начал бы предлагать пары, которые сохранение отвергает.
func designPairBomTokens(
	section pb_common.TechCardBomSection,
	purpose pb_common.TechCardBomPurpose,
	kind pb_common.TechCardBomKind,
	stats *designConstructionStats,
) (pb_common.TechCardBomPurpose, pb_common.TechCardBomKind) {
	sec := designEntityBomSection(section)
	if purpose != pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET && !entity.IsRollGoodsSection(sec) {
		purpose = pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET
		stats.PairsCleared++
	}
	if kind != pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET {
		home, known := entity.BomKindHomeSection(designEntityBomKind(kind))
		legal := known && entity.IsKindEligibleSection(sec) &&
			(home == entity.BomKindAnySection || home == sec)
		if !legal {
			kind = pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET
			stats.PairsCleared++
		}
	}
	return purpose, kind
}

// designEntityBomSection / designEntityBomKind — ОДНО И ТО ЖЕ ЗНАЧЕНИЕ ДВУМЯ ПИСЬМАМИ.
//
// Хранимая строка выводится ИЗ ИМЕНИ ЧЛЕНА ENUM'А, а не из второй написанной от руки карты — тем же
// приёмом и по тому же доводу, что designEnumVocabulary выше: карта, переписанная руками, молча
// теряет член, добавленный в другом файле. Тождество «имя члена без префикса, строчными = строка
// колонки» прибито дрейф-пробами (TestBomKindEnumNoDrift, TestBomSectionEnumNoDrift).
//
// Нулевой член — это «не задано», а не значение: он даёт пустую строку, которую ни одна семья не
// признаёт своей, и пара с ним поэтому невозможна по построению.
func designEntityBomSection(v pb_common.TechCardBomSection) entity.TechCardBomSection {
	if v == pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN {
		return ""
	}
	return entity.TechCardBomSection(strings.ToLower(strings.TrimPrefix(
		pb_common.TechCardBomSection_name[int32(v)], "TECH_CARD_BOM_SECTION_")))
}

func designEntityBomKind(v pb_common.TechCardBomKind) entity.TechCardBomKind {
	if v == pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET {
		return ""
	}
	return entity.TechCardBomKind(strings.ToLower(strings.TrimPrefix(
		pb_common.TechCardBomKind_name[int32(v)], "TECH_CARD_BOM_KIND_")))
}

// designBoundedRunes — обрезка по РУНАМ со счётчиком, для колонок TEXT и для читаемых советов, где
// потолок смысловой («одна мысль на строку»). Обрезка МАРКИРУЕТСЯ (тот же довод, что у
// aiBoundedText): оборванная фраза, прочитанная как законченная, — это другая инструкция.
func designBoundedRunes(s string, max int, stats *designConstructionStats) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		stats.Truncated++
	}
	return aiBoundedText(s, max)
}

// designBoundedBytes — обрезка по БАЙТАМ, для колонок VARCHAR(N).
//
// ⚠ БАЙТЫ, ПОТОМУ ЧТО БАЙТЫ СЧИТАЕТ СТОРОЖ, СТОЯЩИЙ ДАЛЬШЕ ПО МАРШРУТУ (internal/dto меряет
// `len()`), И ПОТОМУ ЧТО БАЙТЫ СЧИТАЕТ КОЛОНКА. Потолок в рунах пропускает ~85 кириллических рун
// в varchar(255) сверх предела, и отказ приходит либо полем DTO — то есть отказом в сохранении
// ВСЕЙ карточки, — либо сырым MySQL 1406.
//
// РЕЖЕТСЯ ПО ГРАНИЦЕ РУНЫ: половина многобайтового символа — это невалидный UTF-8, который MySQL
// встретит ошибкой 1366 вместо обрезанного слова. Многоточие-маркер (3 байта) входит В потолок, а
// не сверх него.
func designBoundedBytes(s string, maxBytes int, stats *designConstructionStats) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	stats.Truncated++
	const ellipsis = "…" // 3 байта в UTF-8
	cut := s[:maxBytes-len(ellipsis)]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + ellipsis
}

// designBomEnum узнаёт токен закрытого словаря. Неузнанный — UNSET, а СТРОКА ОСТАЁТСЯ: человеку
// нужнее строка спеки без секции, чем отсутствие строки, которую модель увидела правильно и
// назвала синонимом.
func designBomEnum[E ~int32](token string, byFold map[string]E, stats *designConstructionStats) E {
	var unset E
	if strings.TrimSpace(token) == "" {
		return unset
	}
	if v, ok := byFold[designFoldToken(token)]; ok {
		return v
	}
	stats.EnumsUnset++
	return unset
}

// designBoundedDecimal читает ОЦЕНКУ РАСХОДА (B-16): десятичное число, ≥ 0, с потолком колонки.
//
// ЧТО СНИМАЕТСЯ, А ЧТО ОСТАЁТСЯ. Снимается ЗНАЧЕНИЕ — «about 2», «1,6», «1.5-2 m», отрицательное,
// слишком длинное; СТРОКА СПЕКИ ОСТАЁТСЯ (та же граница, что у designBomEnum: слот без оценки
// полезнее отсутствия слота). Ноль — законный ответ и НЕ снимается: «нисколько» это утверждение.
//
// ⚠ ПОТОЛОК ЖЁСТЧЕ КОЛОНКИ, И НАМЕРЕННО. Колонка DECIMAL(12,3), сторож DTO пропускает всё меньше
// десяти миллионов; здесь потолок миллион, потому что расход НА ОДНО ИЗДЕЛИЕ, выраженный семью
// знаками, — это не оценка, а промах разряда, и принять его значило бы предложить технологу
// «1 250 000 m» с видом достоверного числа. Три знака после точки — ровно столько хранит колонка;
// лишние молча пропали бы при записи, а лишние В ПРЕДЛОЖЕНИИ обещали бы точность, которой нет.
//
// Возвращает nil, когда числа нет: у отсутствующей оценки нет «нулевого» написания, и пустая
// обёртка Decimal{value:""} означала бы «очистить», а не «не сказано».
func designBoundedDecimal(raw string, stats *designConstructionStats) *pb_decimal.Decimal {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		stats.BomEstDropped++
		return nil
	}
	if d.IsNegative() || d.Exponent() < -designEstUsageMaxFrac ||
		d.GreaterThanOrEqual(decimal.NewFromInt(designEstUsageLimit)) {
		stats.BomEstDropped++
		return nil
	}
	return &pb_decimal.Decimal{Value: d.String()}
}

// designUnitToken складывает написание единицы к КОРОТКОМУ имени члена MaterialUnit («m», «pcs»).
//
// Отдаётся строка, а не энум, потому что ложится она в `tech_card_bom_item.unit` — свободный текст
// ВНУТРИ ПОДПИСАННОГО дайджеста MATERIALS. Неузнанное написание («yd») отдаётся ПУСТЫМ и считается:
// записать в подписываемую колонку слово, которого наш словарь не знает, значило бы сдвинуть
// отпечаток строки ради догадки. Пустая единица — законное состояние, клиент покажет семейное
// умолчание серым.
//
// ⚠ УЗНАЁТ entity.NormalizeMaterialUnit, А НЕ СКЛАДКА, И ЭТО ПОЧИНКА С ЦЕНОЙ. Прежняя версия
// смотрела в карту, построенную на designFoldToken, а тот выбрасывает всё, что не IsLetter и не
// IsDigit: «²» (U+00B2) — категория No, не Nd, поэтому fold("m²") давало "m". Модель, ответившая
// «m²» про клеевой прокладочный, получала в предложении «0.45 m»; технолог принимал строку,
// квадратные метры уезжали в подписываемую колонку погонными — и НИ ОДИН счётчик при этом не рос,
// потому что написание считалось УЗНАННЫМ. Записать в подпись ЧУЖОЕ ЗНАКОМОЕ слово хуже, чем
// оставить пусто: пустое видно, а «m» вместо «m2» — нет.
//
// entity.NormalizeMaterialUnit — та же и единственная функция, через которую единицу читает каждый
// её потребитель (pbMaterialUnit, SameMaterialUnit, план материалов, калибровка расхода). Её
// словарь содержит все одиннадцать канонических написаний члена энума и сверх них синонимы, ради
// которых он и заведён («m²», «sqm», «м», «metres», «pc», «шт», «кг»). Проба
// TestConstructionDraftUnitVocabularyIsTheColumnsOwn держит это включение.
//
// ⚠ НО «СТРОГО ШИРЕ ПРЕЖНЕГО» — НЕПРАВДА, И ЗДЕСЬ ЭТО СТОЯЛО НАПИСАННЫМ. Прежняя карта узнавала не
// список написаний, а ВСЁ, ЧТО СКЛАДЫВАЕТСЯ в имя члена: designFoldToken выбрасывает любой знак,
// кроме буквы и цифры, поэтому она принимала и «m.», и «pcs.», и «m 2», и «m^2», и «MaterialUnitM»,
// и «MATERIAL UNIT PCS» — для КАЖДОГО из одиннадцати членов. Новый словарь — конечный список, и
// все эти формы он не узнаёт. Механический прогон по 201 правдоподобному написанию (короткое имя,
// полное имя и camel-форма каждого члена в одиннадцати видах пунктуации и регистра) даёт 168
// написаний, которые прежняя карта принимала, а этот путь оставляет пустыми. Потеря не одна («m.»),
// как сказано в коммите, — потерян ЦЕЛЫЙ КЛАСС.
//
// ⚠ И ЭТО ВСЁ РАВНО ПРАВИЛЬНЫЙ РАЗМЕН, ПРОСТО НАЗВАННЫЙ ЧЕСТНО. Каждая потеря — ПУСТАЯ единица,
// которую видно (клиент рисует семейное умолчание серым) и которую СЧИТАЕТ UnitsUnset, а он с
// круга 20 печатается в логе. Прежняя же карта в обмен на эту широту молча писала «m» вместо «m²»
// в ПОДПИСАННУЮ колонку и не считала ничего. Пустое видно, чужое знакомое слово — нет; поэтому
// сужение оставлено, а универсальное утверждение — убрано: следующий читатель обопрётся на него.
//
// ⚠ ПОЛНОЕ ИМЯ ЧЛЕНА СВЕРЯЕТСЯ ТОЧНЫМ РАВЕНСТВОМ, А НЕ СКЛАДКОЙ. Складка «m²» → «m» вернулась бы
// через чёрный ход: fold("cm²") == "cm" — тот же класс промаха. Полное имя приходит из энума, оно
// без пунктуации по построению, и точное сравнение по верхнему регистру закрывает вопрос.
func designUnitToken(raw string, stats *designConstructionStats) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, ok := entity.NormalizeMaterialUnit(raw); ok {
		return string(u)
	}
	// «MATERIAL_UNIT_M» — написание, которым модель отвечает, когда берёт слово из энума, а не из
	// списка в промпте. Ноль (UNKNOWN) сюда не годится: это «не задано», ответ, а не значение.
	if v, ok := pb_common.MaterialUnit_value[strings.ToUpper(raw)]; ok && v != 0 {
		return strings.ToLower(strings.TrimPrefix(pb_common.MaterialUnit_name[v], "MATERIAL_UNIT_"))
	}
	stats.UnitsUnset++
	return ""
}

// designExtractJSONObject достаёт внешний {...}, снимая markdown-ограду и терпя прозу вокруг.
//
// ПОВТОРЯЕТ ЛОГИКУ extractAnalysisJSON, А НЕ ЗОВЁТ ЕЁ: та неэкспортируема и живёт в чужом пакете.
// Поведение обязано совпадать — расхождение разборов означало бы, что один и тот же ответ модели
// принимает один платный путь и отвергает соседний.
func designExtractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:] // строка с языковым тегом («json»)
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// designConstructionDraftFromRun — ВОССТАНОВЛЕНИЕ ЧЕРНОВИКА ИЗ СОХРАНЁННОЙ СТРОКИ ПРОГОНА.
//
// ⚠ ЭТО ТРЕБОВАНИЕ, А НЕ УДОБСТВО. Идемпотентный повтор отдаёт СОХРАНЁННЫЙ прогон и модель не
// зовёт; без восстановления второе нажатие той же кнопки возвращало бы пустое предложение при
// оплаченном и успешном прогоне. Разбор — ТОТ ЖЕ САМЫЙ, потому что хранится наш собственный
// канонический JSON: второй, «строгий для своих» разбор был бы вторым мнением о той же строке.
//
// nil — законный ответ и обычный: так выглядит прогон, отвеченный ПРОЗОЙ (старый клиент, флага не
// было). Прозе здесь ошибки не полагается — вопрос «структурный ли это прогон» задают именно так.
//
// ⚠ ПРОЗА БОЛЬШЕ НЕ РАЗБИРАЕТСЯ ВОВСЕ, И ЭТО ПОЧИНКА (ревью круга 19). Тот же разбор звался через
// designExtractJSONObject, который берёт от первой `{` до последней `}` и терпит прозу вокруг —
// терпимость, законная для ЖИВОГО ответа модели и НЕЗАКОННАЯ здесь. Прозаический черновик,
// содержащий фигурные скобки («Use a {"fabric": "jersey"} weight.»), давал НЕПУСТОЙ черновик: на
// прогоне, который структурного ответа не просил, человеку показывалось предложение, которого
// никто не делал. Здесь читается наш СОБСТВЕННЫЙ канонический JSON, и он всегда объект целиком,
// поэтому строгость не стоит ничего: тело обязано начинаться `{` и кончаться `}`.
//
// Эта же строгость и есть признак формы, по которому повтор отличает структурный прогон от
// прозаического (см. designReasonShapeMismatch).
func designConstructionDraftFromRun(outputText string) *pb_common.DesignConstructionDraft {
	js := strings.TrimSpace(outputText)
	if !strings.HasPrefix(js, "{") || !strings.HasSuffix(js, "}") {
		return nil
	}
	// ⚠ designParseCanonical: ФОРМА, И ТОЛЬКО ФОРМА. Смысловые сторожа (отсутствия, имена-заглушки)
	// здесь выключены — канон уже прошёл их перед записью, и прочитать сохранённое строже, чем в
	// первый раз, значило бы отдать на повторе не то, что человек видел (ревью 26.09, MAJOR 1).
	var stats designConstructionStats
	draft, err := designParseConstructionObject(js, designParseCanonical, &stats, nil)
	if err != nil {
		return nil
	}
	return draft
}

// designConstructionMarshal — ПИСАТЕЛЬ КАНОНИЧЕСКОГО ЧЕРНОВИКА, И ОН НАМЕРЕННО НЕ ТОТ, ЧТО ПИШЕТ
// `params` и `inputs` (designJSONMarshal).
//
// ⚠ EmitUnpopulated ВКЛЮЧЁН, И БЕЗ НЕГО КРУГОВОЙ ОБХОД НЕ ДЕРЖИТСЯ. Замерено ревью круга 19: ответ,
// у которого содержательным оказался один только список `missing`, сохранялся как `{"missing":[…]}`
// — потому что protojson по умолчанию не пишет пустых полей, — а designConstructionDraftFromRun
// требует ПРИСУТСТВИЯ хотя бы одного из семи ключей и на такой строке возвращал nil. То есть
// УСПЕШНЫЙ ОПЛАЧЕННЫЙ прогон на идемпотентном повторе отдавал пустоту — ровно тот двойной клик,
// ради которого механизм и существует. Системный промпт сам ведёт к этой форме: правило 1 велит
// «оставь поле пустым, назови нехватку в missing».
//
// ЭТО ТА ЖЕ АСИММЕТРИЯ «ПИСАТЕЛЬ ПРОТИВ ЧИТАТЕЛЯ», ЧТО ОДНАЖДЫ СЛОМАЛА ПОДПИСЬ ДАЙДЖЕСТА: правило
// «не хранить выводимое» экономит байты у писателя и молча меняет ответ у читателя. Здесь писатель
// пишет ровно то, чего читатель требует.
//
// ⚠ И ИМЕННО ПОЭТОМУ ЭТО ВТОРАЯ НАСТРОЙКА, А НЕ ПРАВКА ПЕРВОЙ. У `inputs` довод против
// EmitUnpopulated живой и денежный: снимок ограничен 64 KB, и заполнение нулями тратило бы потолок.
// У черновика потолка нет — он едет в output_text (TEXT), — а восемь пустых ключей стоят около
// сотни байт. UseProtoNames обе разделяют: канонический JSON читается обратно тем же разбором, чей
// словарь узнаёт ПОЛНЫЕ имена членов enum'а (TECH_CARD_BOM_SECTION_FABRIC).
var designConstructionMarshal = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

func designMarshalConstructionDraft(d *pb_common.DesignConstructionDraft) ([]byte, error) {
	return designConstructionMarshal.Marshal(d)
}
