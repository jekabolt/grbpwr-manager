package entity

import (
	"sort"
	"strconv"
	"strings"
)

// Сборочный граф тех-карты: что шаг берёт со стола и что производит.
//
// Этот файл — СПЕЦИФИКАЦИЯ фичи «узлы сборки», а не вспомогательный код. В нём нет ни одного
// обращения к БД, прото или транспорту: только значения, правила и нарушения. Причина не в
// чистоте ради чистоты, а в том, что те же правила обязан повторить клиент (пикер, который
// перестал врать, — это и есть фронтир), и разъехаться две реализации могут только через
// расхождение с этим файлом и с общим набором кейсов `testdata/assembly_cases.json`.
//
// Прототип интерфейса (`operations-configurator.html`, функция `derive()`) НЕ является
// эталоном: он не реализует правило 4, считает арность джойна по длине списка входов и на
// коллизии «ключ узла = ключ детали» ведёт себя обратно правилу 6 — молча съедает второй
// вход. Кейсы берутся отсюда, а не оттуда.
//
// Словарь. УЗЕЛ — именованный результат сборочного шага (`SHELL`, `FRONT-L`). Не строка
// таблицы и не сущность с id: узел определяется операцией, которая его производит. ДЖОЙН —
// шаг с непустым выходным ключом: съедает свои входы, рождает узел. ПОГЛОЩЕНИЕ — джойн, чей
// выходной ключ совпадает с одним из входов-УЗЛОВ (`GARMENT + HEM → GARMENT`): узел сохраняет
// идентичность и получает содержимое. ОБРАБОТКА — шаг с пустым выходным ключом: ничего не
// собирает, его входы остаются доступными следующим шагам. ФРОНТИР на шаге k — то, что
// реально лежит на столе перед шагом k.
//
// Правила, которые держит сервер:
//
//	1. каждый вход шага k лежит на фронтире в k (не съеден раньше, не произведён позже);
//	2. строка детали съедается ровно одним джойном; узел — не более чем одним;
//	3. джойну нужно ≥ 2 РАЗЛИЧНЫХ СУЩЕСТВУЮЩИХ входов (узел из одного входа — это обработка);
//	4. на переходе в RELEASED: ровно один терминальный узел, и каждая объявленная строка
//	   детали в него попадает;
//	5. порядок шагов — линейное продолжение частичного порядка «произвели → потребили»;
//	6. пространство имён едино: выходной ключ не может совпадать с line_key детали карточки;
//	7. внутри входов одного шага дублей нет.
//
// Правила 1, 2 и 5 — одна и та же проверка: один проход живым множеством. Если каждый вход
// жив на своей позиции, порядок автоматически линейное продолжение, а съеденное автоматически
// не переиспользуется. Правила 3, 6, 7 локальны и считаются в том же проходе. Правило 4 —
// отдельное замыкание по листьям, включается только на релизе (AssemblyReleaseCheck).
//
// Чего здесь СОЗНАТЕЛЬНО НЕТ. Шаг без входов допустим: сегодня `piece_line_keys` advisory при
// пустоте, и фича это поведение не меняет (прототип считает пустой список ошибкой — прототип
// неправ). Регистр и стиль ключей не нормализуются: сравнение побайтное, `SHELL` и `Shell` —
// два разных узла. Совпадение имени узла у поглощающих шагов с именем производителя не
// проверяется: имя разрешается по первому производителю и фактом цеха не является.

// AssemblyInputKind различает две природы входа. Классификация сырого ключа выполняется
// ClassifyAssemblyInputs — одной функцией, потому что «ключ, не совпавший ни с одной деталью,
// есть ссылка на узел» это правило графа, а не деталь разбора запроса.
type AssemblyInputKind uint8

const (
	// AssemblyInputPiece — вход есть строка детали кроя, адресуемая по TechCardPiece.LineKey.
	AssemblyInputPiece AssemblyInputKind = iota
	// AssemblyInputUnit — вход есть узел, адресуемый по выходному ключу более раннего шага.
	// Ссылка может быть висячей: существование проверяет правило 1, а не классификация.
	AssemblyInputUnit
)

// OperationInput — канонический вход шага. Единственная форма, в которой вход существует
// после разбора запроса: ниже по течению никто не пересобирает список из двух половин и не
// угадывает природу ключа заново.
type OperationInput struct {
	Kind AssemblyInputKind
	Key  string
}

// AssemblyPiece — то и только то, что проходу нужно от объявленной строки детали. Имя нужно
// не для красоты: отчёт правила 4 перечисляет недостигшие детали ПО ИМЕНАМ, потому что
// технолог не знает своих ULID'ов.
type AssemblyPiece struct {
	LineKey string
	Name    string
}

// AssemblyStep — то и только то, что проходу нужно от операции. Ни зоны, ни типа, ни SMV:
// узел ортогонален обеим осям 0306, и подмешивать их сюда значило бы связать спецификацию
// сборки с независимой от неё классификацией шага.
type AssemblyStep struct {
	Inputs         []OperationInput
	OutputUnitKey  string
	OutputUnitName string
}

// AssemblyRule — номер нарушенного правила. Номер, а не строка: сообщение переводит слой
// транспорта (dto строит FieldViolation), а считать отказы по правилам нужно в логах.
type AssemblyRule int

const (
	// AssemblyRuleHygiene — не правило графа, а гигиена полей (имя узла без ключа).
	AssemblyRuleHygiene AssemblyRule = 0
	// AssemblyRuleFrontier — правило 1: вход обязан лежать на фронтире своего шага.
	AssemblyRuleFrontier AssemblyRule = 1
	// AssemblyRuleSingleUse — правило 2: деталь съедается ровно одним джойном, узел — не более
	// чем одним. Второй производитель того же ключа — тоже сюда.
	AssemblyRuleSingleUse AssemblyRule = 2
	// AssemblyRuleJoinArity — правило 3: джойну нужно ≥ 2 различных существующих входа.
	AssemblyRuleJoinArity AssemblyRule = 3
	// AssemblyRuleConverges — правило 4: один терминал, все детали в нём. Только на релизе.
	AssemblyRuleConverges AssemblyRule = 4
	// AssemblyRuleNamespace — правило 6: выходной ключ не может быть ключом детали.
	AssemblyRuleNamespace AssemblyRule = 6
	// AssemblyRuleDuplicateInput — правило 7: дубль ключа внутри входов одного шага.
	AssemblyRuleDuplicateInput AssemblyRule = 7
)

// AssemblyDetail — машинный код ВЕТКИ нарушения.
//
// Существует ради общего файла кейсов. Две ветки правила 1 — «такого входа нет» и «он
// появится только на шаге k» — дают одинаковые координаты {правило, шаг, вход}, поэтому
// кейс, сверяющий только координаты, проходит независимо от того, реализована вторая ветка
// или нет. В прототипе она мертва; порт, повторивший этот дефект, прошёл бы общие кейсы —
// ровно тот класс расхождения, против которого файл и заведён. Русские сообщения для пина не
// годятся: TS-порт не обязан дублировать тексты.
type AssemblyDetail string

const (
	AssemblyDetailShadowName      AssemblyDetail = "shadow-name"
	AssemblyDetailDuplicateInput  AssemblyDetail = "duplicate-input"
	AssemblyDetailUnknownKey      AssemblyDetail = "unknown-key"
	AssemblyDetailProducedLater   AssemblyDetail = "produced-later"
	AssemblyDetailSelfReference   AssemblyDetail = "self-reference"
	AssemblyDetailConsumedEarlier AssemblyDetail = "consumed-earlier"
	AssemblyDetailOffFrontier     AssemblyDetail = "off-frontier"
	AssemblyDetailKeyIsPiece      AssemblyDetail = "key-is-piece"
	AssemblyDetailTooFewInputs    AssemblyDetail = "too-few-inputs"
	AssemblyDetailSecondProducer  AssemblyDetail = "second-producer"
	AssemblyDetailNoTerminal      AssemblyDetail = "no-terminal"
	AssemblyDetailManyTerminals   AssemblyDetail = "many-terminals"
	AssemblyDetailUnreachedPieces AssemblyDetail = "unreached-pieces"
)

// AssemblyViolation — одно нарушение. Step и Input — координаты для пути поля
// (`operations[i].input_keys[j]`), который построит dto; движок транспортных путей не знает.
type AssemblyViolation struct {
	Rule AssemblyRule
	// Detail — машинный код ветки; см. AssemblyDetail. Именно его пинят общие кейсы.
	Detail AssemblyDetail
	// Step — индекс шага в каноническом порядке; -1 для нарушения уровня карточки.
	Step int
	// Input — индекс внутри входов шага; -1 если нарушение не про конкретный вход.
	Input int
	// Key — ключ, о котором речь: вход, выходной ключ или ключ детали. Пусто, если нарушение
	// не про ключ (теневое имя узла — про имя, и оно живёт в Message).
	Key string
	// Message — человеческая причина по-русски. Пойдёт в FieldViolation как есть.
	Message string
}

// AssemblyUnit — узел, каким его увидел проход.
type AssemblyUnit struct {
	Key  string
	Name string
	// ProducedAt — индекс шага, ПЕРВЫМ произведшего узел. Поглощающие шаги его не меняют:
	// имя узла разрешается по первому производителю, и переезд этой точки означал бы, что имя
	// молча теряется при удалении шага.
	ProducedAt int
	// AbsorbedAt — шаги, вобравшие в узел дополнительное содержимое.
	AbsorbedAt []int
	// Leaves — ключи строк деталей, из которых узел собран, в порядке объявления деталей.
	// Замыкание, а не прямые входы: на нём стоит правило 4.
	Leaves []string
}

// AssemblyResult — итог одного прохода.
type AssemblyResult struct {
	// Frontier — что осталось лежать на столе после последнего шага, в порядке появления.
	// Содержит и узлы, и не съеденные детали.
	Frontier []string
	// Units — узлы по ключу.
	Units map[string]AssemblyUnit
	// ConsumedBy — ключ → индекс шага, который его съел.
	ConsumedBy map[string]int
	// FrontierBefore[i] — фронтир перед шагом i. Это то, что пикер шага i имеет право
	// предлагать; ради него проход и существует.
	FrontierBefore [][]string
	// Violations — нарушения правил 1–3, 6, 7 и гигиены, в порядке шагов.
	Violations []AssemblyViolation
}

// ClassifyAssemblyInputs переводит сырые ключи шага в канонические входы.
//
// Живёт здесь, а не в слое разбора запроса, сознательно: правило «ключ, не совпавший ни с
// одной деталью карточки, есть ссылка на узел» — это правило графа. Разложи его по двум
// слоям, и через год клиент и сервер разойдутся в том, что считать узлом, а разойтись им
// нельзя: пикер обязан предлагать ровно то, что примет запись.
//
// Функция НИЧЕГО не проверяет: висячая ссылка классифицируется как узел и падает на правиле 1
// с внятным сообщением. Классификация, которая заодно отказывает, прятала бы одну ошибку под
// другой.
func ClassifyAssemblyInputs(pieceKeys map[string]bool, rawKeys []string) []OperationInput {
	if len(rawKeys) == 0 {
		return nil
	}
	out := make([]OperationInput, 0, len(rawKeys))
	for _, k := range rawKeys {
		kind := AssemblyInputUnit
		if pieceKeys[k] {
			kind = AssemblyInputPiece
		}
		out = append(out, OperationInput{Kind: kind, Key: k})
	}
	return out
}

// AssemblyOperationOrder — ЕДИНСТВЕННАЯ функция порядка шагов. Возвращает индексы операций в
// каноническом порядке.
//
// Зачем она нужна отдельно. Валидация идёт над payload'ом, где порядок задан позицией в
// массиве, а чтение из БД сортирует по `operation_number IS NULL, operation_number,
// display_order`. На легаси-карточке с NULL или неканоническим номером это ДВЕ РАЗНЫЕ
// последовательности, и первый же серверный читатель фронтира (блоки, наряд по подсборкам,
// SMV подсборки) разошёлся бы с тем, что валидация утвердила: разметка, принятая сервером,
// оказалась бы невалидной для его же чтения. Поэтому порядок для фронтира берётся только
// отсюда, и ни один читатель не сортирует сам.
//
// Вторая половина этой защиты живёт на записи: карточка со сборочными фактами обязана нести
// `operation_number`, согласованный с порядком массива (канонизация приводит его к (i+1)*10).
// Тогда обе последовательности совпадают по построению, а эта функция остаётся страховкой для
// всего, что было записано раньше.
func AssemblyOperationOrder(ops []TechCardOperation) []int {
	idx := make([]int, len(ops))
	for i := range ops {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := ops[idx[a]].OperationNumber, ops[idx[b]].OperationNumber
		// Строка без номера идёт после нумерованных — тот же порядок, что в читающем запросе
		// (`operation_number IS NULL` первым выражением ORDER BY). Внутри одинаковых —
		// стабильно по исходной позиции, то есть по display_order, каким его вернул стор.
		if x.Valid != y.Valid {
			return x.Valid
		}
		if x.Valid && x.Int32 != y.Int32 {
			return x.Int32 < y.Int32
		}
		return false
	})
	return idx
}

// AssemblySweep — тот самый один проход живым множеством.
//
// Порядок шагов — это порядок среза `steps`; вызывающий обязан подать их уже упорядоченными
// (см. AssemblyOperationOrder). Проход не сортирует сам: молчаливая пересортировка внутри
// сделала бы правило 5 непроверяемым — «порядок неверен» превратилось бы в «порядок исправлен».
func AssemblySweep(pieces []AssemblyPiece, steps []AssemblyStep) AssemblyResult {
	res := AssemblyResult{
		Units:          make(map[string]AssemblyUnit),
		ConsumedBy:     make(map[string]int),
		FrontierBefore: make([][]string, len(steps)),
	}

	// Порядок объявления деталей — им сортируются листья и им же перечисляются сироты, чтобы
	// отчёт правила 4 читался в том же порядке, в каком детали лежат на вкладке.
	pieceOrder := make(map[string]int, len(pieces))
	pieceName := make(map[string]string, len(pieces))
	isPiece := make(map[string]bool, len(pieces))

	// order — все сущности в порядке появления (сперва детали, затем узлы по мере рождения);
	// live — кто из них ещё на столе. Фронтир = order, отфильтрованный по live: так порядок
	// фронтира стабилен и осмыслен, а не зависит от обхода карты.
	order := make([]string, 0, len(pieces)+len(steps))
	live := make(map[string]bool, len(pieces)+len(steps))

	for i, p := range pieces {
		if p.LineKey == "" {
			continue // строка без ключа — забота разбора запроса, не графа
		}
		if _, dup := pieceOrder[p.LineKey]; dup {
			continue // дубль ключа детали ловит разбор запроса; здесь он просто не удваивается
		}
		pieceOrder[p.LineKey] = i
		pieceName[p.LineKey] = p.Name
		isPiece[p.LineKey] = true
		order = append(order, p.LineKey)
		live[p.LineKey] = true
	}

	// leaves — замыкание по строкам деталей. У детали замыкание есть она сама: тогда правило 4
	// не делает исключений для «узла, собранного прямо из деталей».
	leaves := make(map[string][]string, len(pieces)+len(steps))
	for k := range isPiece {
		leaves[k] = []string{k}
	}

	// firstProducer заполняется ДО прохода, чтобы ветка «произведён позже» была достижима.
	// Без него шаг, ссылающийся вперёд, получал бы «такого входа не существует» — диагностику,
	// которая уводит технолога искать опечатку там, где на самом деле переставлены шаги.
	// (Ровно этим болен derive() в прототипе: ветка есть, но недостижима.)
	firstProducer := make(map[string]int, len(steps))
	for i, s := range steps {
		if s.OutputUnitKey == "" {
			continue
		}
		if _, seen := firstProducer[s.OutputUnitKey]; !seen {
			firstProducer[s.OutputUnitKey] = i
		}
	}

	known := func(key string) bool {
		if isPiece[key] {
			return true
		}
		_, ok := res.Units[key]
		return ok
	}

	for i, s := range steps {
		res.FrontierBefore[i] = filterLive(order, live)

		// --- гигиена -----------------------------------------------------------------------
		if s.OutputUnitKey == "" && s.OutputUnitName != "" {
			res.Violations = append(res.Violations, AssemblyViolation{
				Rule: AssemblyRuleHygiene, Detail: AssemblyDetailShadowName, Step: i, Input: -1,
				Message: "unit name “" + s.OutputUnitName + "” is typed in, but there is no key: the step assembles nothing",
			})
		}

		// --- входы: правила 7 и 1 ----------------------------------------------------------
		seen := make(map[string]int, len(s.Inputs))
		// usable — различные существующие входы; на них считается арность джойна (правило 3).
		// Именно различные и именно существующие: иначе «узел из одного входа» обходится
		// дублем или опечаткой, что и происходит в прототипе.
		usable := make(map[string]bool, len(s.Inputs))
		for j, in := range s.Inputs {
			if first, dup := seen[in.Key]; dup {
				res.Violations = append(res.Violations, AssemblyViolation{
					Rule: AssemblyRuleDuplicateInput, Detail: AssemblyDetailDuplicateInput,
					Step: i, Input: j, Key: in.Key,
					// Позиции входов человеку показываются с единицы — как и номера шагов в
					// соседних сообщениях. Смешивать в одном тексте нумерацию с нуля и с
					// единицы значит заставить читателя гадать, какая тут какая.
					Message: "the input repeats within the same step (first time — input " + strconv.Itoa(first+1) + ")",
				})
				continue
			}
			seen[in.Key] = j

			switch {
			case !known(in.Key):
				// Не существует сейчас — но, может быть, появится позже.
				if at, produced := firstProducer[in.Key]; produced && at >= i {
					if at == i {
						res.Violations = append(res.Violations, AssemblyViolation{
							Rule: AssemblyRuleFrontier, Detail: AssemblyDetailSelfReference,
							Step: i, Input: j, Key: in.Key,
							Message: "“" + in.Key + "” is the output of this very step: a unit appears after the step, not before it",
						})
						continue
					}
					res.Violations = append(res.Violations, AssemblyViolation{
						Rule: AssemblyRuleFrontier, Detail: AssemblyDetailProducedLater,
						Step: i, Input: j, Key: in.Key,
						Message: "unit “" + in.Key + "” only appears at step " + humanStep(at) + " — it can't be an input earlier",
					})
					continue
				}
				res.Violations = append(res.Violations, AssemblyViolation{
					Rule: AssemblyRuleFrontier, Detail: AssemblyDetailUnknownKey,
					Step: i, Input: j, Key: in.Key,
					Message: "input “" + in.Key + "” doesn't exist: there is no such piece and no such unit",
				})
			case !live[in.Key]:
				eater, ok := res.ConsumedBy[in.Key]
				if !ok {
					// Недостижимо при целом состоянии: всё, что ушло с фронтира, ушло в чей-то
					// джойн. Ветка оставлена, чтобы будущая правка прохода не потеряла отказ молча.
					res.Violations = append(res.Violations, AssemblyViolation{
						Rule: AssemblyRuleFrontier, Detail: AssemblyDetailOffFrontier,
						Step: i, Input: j, Key: in.Key,
						Message: "input “" + in.Key + "” is no longer on the table",
					})
					continue
				}
				res.Violations = append(res.Violations, AssemblyViolation{
					Rule: AssemblyRuleSingleUse, Detail: AssemblyDetailConsumedEarlier,
					Step: i, Input: j, Key: in.Key,
					Message: "“" + in.Key + "” was already consumed by step " + humanStep(eater) + " and lies inside unit " + eaterUnit(steps, eater),
				})
			default:
				usable[in.Key] = true
			}
		}

		if s.OutputUnitKey == "" {
			// Обработка: входы остаются на столе. Ничего не съедается — в этом вся её суть.
			continue
		}

		// --- выход: правила 6, 3, 2 --------------------------------------------------------
		out := s.OutputUnitKey

		if isPiece[out] {
			// Ключ узла совпал с ключом детали. НЕ поглощение: поглощать можно только узел.
			// В прототипе этот случай считается поглощением, деталь получает чужие листья, а
			// второй вход исчезает без единого сообщения — самый тихий из известных дефектов.
			shown := pieceName[out]
			if shown == "" {
				shown = out
			}
			res.Violations = append(res.Violations, AssemblyViolation{
				Rule: AssemblyRuleNamespace, Detail: AssemblyDetailKeyIsPiece, Step: i, Input: -1, Key: out,
				Message: "unit key “" + out + "” is taken by piece “" + shown + "”: pieces and units share one namespace",
			})
			continue
		}

		if len(usable) < 2 {
			res.Violations = append(res.Violations, AssemblyViolation{
				Rule: AssemblyRuleJoinArity, Detail: AssemblyDetailTooFewInputs, Step: i, Input: -1, Key: out,
				Message: "a unit from a single input is processing, not a unit: a join needs at least two different inputs",
			})
			continue
		}

		prev, exists := res.Units[out]
		// Поглощение — и только оно — когда выходной ключ пришёл СВОИМ ЖЕ входом-узлом.
		absorb := exists && usable[out]

		if exists && !absorb {
			// Совет «возьмите его же входом» уместен, только пока узел ещё на столе: съеденный
			// узел входом взять нельзя, и предлагать это значит послать технолога по кругу.
			advice := ": to keep assembling it, take it as an input of this step"
			if eater, eaten := res.ConsumedBy[out]; eaten {
				advice = " and was already consumed by step " + humanStep(eater) + ": it can't be assembled any further"
			} else if !live[out] {
				advice = ""
			}
			res.Violations = append(res.Violations, AssemblyViolation{
				Rule: AssemblyRuleSingleUse, Detail: AssemblyDetailSecondProducer, Step: i, Input: -1, Key: out,
				Message: "unit “" + out + "” was already produced by step " + humanStep(prev.ProducedAt) + advice,
			})
			// Узел НЕ переписывается: сохранённое замыкание остаётся честным. Прототип здесь
			// затирает узел, и накопленные поглощением листья пропадают — после чего правило 4,
			// стоящее на этом замыкании, начинает лгать.
			continue
		}

		// Съедаем входы. Собственный ключ при поглощении не помечается съеденным: узел не ест
		// сам себя, он остаётся собой и получает содержимое.
		acc := make([]string, 0, len(s.Inputs))
		if absorb {
			acc = append(acc, prev.Leaves...)
		}
		for _, in := range s.Inputs {
			if !usable[in.Key] {
				continue
			}
			live[in.Key] = false
			if in.Key == out {
				continue
			}
			res.ConsumedBy[in.Key] = i
			acc = append(acc, leaves[in.Key]...)
		}
		acc = dedupPieceKeys(acc, pieceOrder)

		if absorb {
			prev.AbsorbedAt = append(prev.AbsorbedAt, i)
			prev.Leaves = acc
			if prev.Name == "" {
				// Первый производитель имени не дал, поглощающий дал — принимаем: иначе имя
				// узла зависело бы от того, на каком шаге его впервые набрали.
				prev.Name = s.OutputUnitName
			}
			res.Units[out] = prev
		} else {
			res.Units[out] = AssemblyUnit{
				Key: out, Name: s.OutputUnitName, ProducedAt: i, Leaves: acc,
			}
			order = append(order, out)
		}
		leaves[out] = acc
		live[out] = true
	}

	res.Frontier = filterLive(order, live)
	return res
}

// AssemblyReleaseCheck — правило 4. Отдельно от прохода, потому что включается только на
// переходе в RELEASED: на черновике полуразмеченная карточка законна и живёт сколько угодно.
//
// Условие включения — карточка несёт хотя бы один ПРОИЗВОДЯЩИЙ ШАГ (а не хотя бы один
// состоявшийся узел): иначе карточка, где единственный джойн отвергнут правилом 6, тихо
// проваливалась бы мимо проверки как «неразмеченная».
func AssemblyReleaseCheck(pieces []AssemblyPiece, steps []AssemblyStep, res AssemblyResult) []AssemblyViolation {
	marked := false
	for _, s := range steps {
		if s.OutputUnitKey != "" {
			marked = true
			break
		}
	}
	if !marked {
		return nil // сегодняшняя карточка: узлов нет, релиз идёт как раньше
	}

	var out []AssemblyViolation

	terminals := make([]string, 0, 2)
	for _, k := range res.Frontier {
		if _, isUnit := res.Units[k]; isUnit {
			terminals = append(terminals, k)
		}
	}
	switch len(terminals) {
	case 1:
	case 0:
		out = append(out, AssemblyViolation{
			Rule: AssemblyRuleConverges, Detail: AssemblyDetailNoTerminal, Step: -1, Input: -1,
			Message: "the assembly doesn't converge: not a single finished unit at the end",
		})
	default:
		out = append(out, AssemblyViolation{
			Rule: AssemblyRuleConverges, Detail: AssemblyDetailManyTerminals, Step: -1, Input: -1,
			Message: "there must be exactly one terminal unit, and there are " + strconv.Itoa(len(terminals)) + ": " + strings.Join(terminals, ", "),
		})
	}

	// Достижимость считается по замыканию терминала, а не по «ключ где-то упомянут»: деталь,
	// которую трогает только обработка, упомянута, но ни в один узел не попадает. Прототип
	// проверяет именно упоминание и такую деталь пропускает.
	//
	// Список сирот выдаётся ТОЛЬКО при ровно одном терминале. При двух терминалах ни одна
	// деталь формально не достигает «изделия», и перечислить их все значило бы утопить
	// настоящую причину («терминала два») в списке из сорока имён.
	if len(terminals) != 1 {
		return out
	}
	reached := make(map[string]bool, len(pieces))
	for _, leaf := range res.Units[terminals[0]].Leaves {
		reached[leaf] = true
	}

	var orphans []string
	for _, p := range pieces {
		if p.LineKey == "" || reached[p.LineKey] {
			continue
		}
		name := p.Name
		if name == "" {
			name = p.LineKey
		}
		orphans = append(orphans, name)
	}
	if len(orphans) > 0 {
		out = append(out, AssemblyViolation{
			Rule: AssemblyRuleConverges, Detail: AssemblyDetailUnreachedPieces, Step: -1, Input: -1,
			Message: "don't make it into the finished garment: " + strings.Join(orphans, ", "),
		})
	}
	return out
}

// --- мелочи ------------------------------------------------------------------------------

func filterLive(order []string, live map[string]bool) []string {
	out := make([]string, 0, len(order))
	for _, k := range order {
		if live[k] {
			out = append(out, k)
		}
	}
	return out
}

// dedupPieceKeys приводит замыкание к порядку объявления деталей; дедуп — чисто защитный.
//
// При живом правиле 2 повторов не бывает по построению: замыкания живых сущностей дизъюнктны,
// потому что каждая строка детали уходит ровно в один джойн. При поглощении собственные листья
// узла приходят ТОЛЬКО через prev.Leaves — свой же ключ пропускается до накопления. То есть
// рабочая часть функции — сортировка, а дедуп стоит на случай будущей правки прохода.
// (Формулировка важна: этот файл портируется построчно, и портер, поверивший неверному
// комментарию, «починит» код под него.)
func dedupPieceKeys(keys []string, pieceOrder map[string]int) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.SliceStable(out, func(a, b int) bool { return pieceOrder[out[a]] < pieceOrder[out[b]] })
	return out
}

// humanStep переводит индекс в номер шага, каким его видит технолог (с единицы).
func humanStep(i int) string { return strconv.Itoa(i + 1) }

// eaterUnit — имя узла, в который ушёл вход; для сообщения правила 2.
func eaterUnit(steps []AssemblyStep, at int) string {
	if at < 0 || at >= len(steps) || steps[at].OutputUnitKey == "" {
		return "?"
	}
	return steps[at].OutputUnitKey
}
