package entity

// --- ИКОННЫЕ ПОЛЯ ШАГА (0435, SPEC-B1) -------------------------------------------------------------
//
// Два закрытых словаря, на которых держится иконка шага: КОНСТРУКЦИЯ ШВА (seam_kind) и ВАРИАНТ
// РАБОТЫ (work_variant). Оба живут в Go, а не в таблице-каталоге и не в CHECK'е: новый токен всегда
// означает новый рисунок, то есть деплой клиента, и таблица ничего не покупает, а REGEXP CHECK
// стоил бы копии tech_card_operation на каждый новый токен. Колонки рождаются NULL; сервер НИКОГДА
// не выводит и не дописывает ни одно из двух значений — их пишет человек.
//
// НЕ ПУТАТЬ С TechCardSeamKind (tech_card_seam.go). Тот — ФОРМА шва на лекалах (edge, partial,
// composite, surface, closure): где он лежит, а не как сшит. Здесь — КОНСТРУКЦИЯ шва внутри класса
// seam_class (bound_open, lapped_closed_a, moscow_hem …). Поэтому у словаря нет своего типа: имя
// TechCardSeamKind занято другим смыслом, и второй тип с похожим именем был бы ловушкой.

// SeamKindTokens — закрытый список конструкций шва, 38 токенов, в порядке классов. Каждый токен
// принадлежит ровно одному классу (SeamKindClass) и имеет свой рисунок в листе иконок
// (sheet/full.html, одобрен владельцем 26.10). Список ТОЛЬКО РАСТЁТ: токен может уже лежать в
// подписанной карточке и в её дайджесте, поэтому переименование или удаление — это потерянная
// конструкция. Тест entity пинит его целиком.
var SeamKindTokens = []string{
	// bs_bound — окантовка; binding_style задан видом (SeamKindBindingStyle).
	"bound_tape", "bound_open", "bound_closed", "bound_frame",
	// ss_plain — стачные и их обработки срезов.
	"seam_edge", "seam_one_side", "seam_open", "open_topstitched",
	"piping_lapped", "piping_faced", "piping_cord",
	"hong_kong", "overcast_plain", "safety_516", "taped",
	// ss_french — французский.
	"french",
	// ls_lapped — настрочные.
	"lapped_raw", "lapped_one_closed", "topstitch_closed", "bonded",
	// ls_flat_felled — запошивочные.
	"lapped_closed_a", "lapped_closed_b", "felled", "locked",
	// ef_faced — обтачные.
	"faced_split", "faced_kant", "faced_bias_closed", "faced_bias_double",
	// ef_hem_turned — подгибка с закрытым срезом.
	"moscow_hem", "hem_closed", "blind_hem", "casing", "cover_hem",
	// ef_hem_raw — подгибка/обработка с открытым срезом.
	"hem_raw", "hem_zigzag", "edge_overlock",
	// fs_flat — плоский.
	"flatlock",
	// os_topstitch — только отделочная строчка (правило F11 требует topstitch_mode, и для защипов это верно).
	"tucks",
}

// SeamKindClass — класс шва, которому принадлежит конструкция. Правило записи: при заданном
// seam_kind seam_class шага обязан быть ЯВНЫМ и равным SeamKindClass[kind] (иначе needs_seam_class);
// наследование умолчания карточки не засчитывается, потому что умолчание может виду противоречить.
var SeamKindClass = map[string]TechCardSeamClass{
	"bound_tape": SeamClassBound, "bound_open": SeamClassBound, "bound_closed": SeamClassBound, "bound_frame": SeamClassBound,

	"seam_edge": "ss_plain", "seam_one_side": "ss_plain", "seam_open": "ss_plain", "open_topstitched": "ss_plain",
	"piping_lapped": "ss_plain", "piping_faced": "ss_plain", "piping_cord": "ss_plain",
	"hong_kong": "ss_plain", "overcast_plain": "ss_plain", "safety_516": "ss_plain", "taped": "ss_plain",

	"french": "ss_french",

	"lapped_raw": "ls_lapped", "lapped_one_closed": "ls_lapped", "topstitch_closed": "ls_lapped", "bonded": "ls_lapped",

	"lapped_closed_a": "ls_flat_felled", "lapped_closed_b": "ls_flat_felled", "felled": "ls_flat_felled", "locked": "ls_flat_felled",

	"faced_split": "ef_faced", "faced_kant": "ef_faced", "faced_bias_closed": "ef_faced", "faced_bias_double": "ef_faced",

	"moscow_hem": "ef_hem_turned", "hem_closed": "ef_hem_turned", "blind_hem": "ef_hem_turned", "casing": "ef_hem_turned", "cover_hem": "ef_hem_turned",

	"hem_raw": "ef_hem_raw", "hem_zigzag": "ef_hem_raw", "edge_overlock": "ef_hem_raw",

	"flatlock": "fs_flat",

	"tucks": SeamClassTopstitch,
}

// ValidSeamKinds — множество для валидатора; строится из SeamKindTokens, чтобы два не разошлись.
var ValidSeamKinds = tokenSet[string](SeamKindTokens)

// SeamKindBindingStyle — как сложена бейка у каждого вида класса bs_bound. Вид ОКАНТОВКИ задаёт
// binding_style целиком, отдельного выбора сложения у технолога нет (решение владельца): при
// bound_tape / bound_open / bound_closed binding_style обязан быть raw / single_fold / double_fold,
// при bound_frame — ПУСТЫМ (рамка не член enum'а сложения, её несёт сам вид). Иначе отказ
// `operations[N].binding_style` с кодом contradicts_seam_kind. Ключи — ровно виды класса bs_bound;
// пустое значение значит «binding_style должен быть не задан».
var SeamKindBindingStyle = map[string]TechCardBindingStyle{
	"bound_tape":   "raw",
	"bound_open":   "single_fold",
	"bound_closed": "double_fold",
	"bound_frame":  "",
}

// SeamKindTokenMaxLen is the longest a seam_kind token may be: the column is VARCHAR(32).
const SeamKindTokenMaxLen = 32

// WorkVariants — закрытая карта ВАРИАНТОВ ИСПОЛНЕНИЯ внутри одной работы (operation_work.token).
// Правила записи: вариант без работы — отказ; вариант, которого у ЭТОЙ работы нет (в том числе у
// работы без вариантов вовсе), — отказ `operations[N].work_variant` с кодом not_for_work.
//
// ВАРИАНТ НИКОГДА НЕ МЕНЯЕТ ГЛАГОЛ: если действие оператора другое, это другая работа, а не вариант.
// Поэтому здесь нет, например, потайной молнии (она — zipper_application = invisible) и пришивной
// кнопки (attach_method = sew): исполнение, у которого уже есть поле шага, вариантом не дублируется.
//
// Первые пять работ существуют с 0329; остальные семь сеет 0436 (operation_work_icon_catalog).
// Карта ТОЛЬКО РАСТЁТ — вариант может лежать в подписанной карточке и в её дайджесте.
var WorkVariants = map[string][]string{
	"set_zip":          {"open_end", "two_way", "magnetic", "faced"},
	"snap_press_stud":  {"ring", "spring_s"},
	"eyelet_grommet":   {"small", "washer"},
	"attach_elastic":   {"casing", "exposed", "gripper", "picot", "buttonhole", "loop"},
	"fuse":             {"full", "partial", "edge_tape", "tack"},
	"attach_hook":      {"eye", "trouser_bar"},
	"attach_hook_loop": {"tape", "dot"},
	"enclose_magnet":   {"pocket", "hem"},
	"insert_cord":      {"round", "flat"},
	"pleat":            {"knife", "box"},
	"pocket_welt":      {"single", "double"},
	"attach_patch":     {"plain", "embroidered"},
}

// WorkVariantTokenMaxLen is the longest a work_variant token may be: the column is VARCHAR(24).
const WorkVariantTokenMaxLen = 24

// IsWorkVariant reports whether variant is a declared execution of work.
func IsWorkVariant(work, variant string) bool {
	for _, v := range WorkVariants[work] {
		if v == variant {
			return true
		}
	}
	return false
}

// ButtonHoleCounts — законные значения числа отверстий пуговицы (шаг и артикул). 0 на проводе =
// «не указано» и хранится NULL; сервер его не выводит.
var ButtonHoleCounts = map[int32]bool{2: true, 4: true}

// ButtonAttachPatternsNeedFourHoles — рисунки пришивания, которые с двумя отверстиями невозможны:
// с двумя отверстиями пришивают одной перемычкой (parallel) или «другое» + note. Противоречие внутри
// шага — жёсткий отказ `operations[N].fastening.attach_pattern` с кодом needs_four_holes.
var ButtonAttachPatternsNeedFourHoles = map[TechCardButtonAttachPattern]bool{
	"cross_x": true,
	"square":  true,
	"u_shape": true,
}
