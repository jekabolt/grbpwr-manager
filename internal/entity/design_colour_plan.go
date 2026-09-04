package entity

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ЦВЕТОВОЙ ПЛАН КАРТОЧКИ (Feature A) — покрашенные флэты и то, какой цвет какой тканью оказался.
//
// ДВА СЛОВА, ПОТОМУ ЧТО ЭТО ДВЕ ВЕЩИ. КАРТА (DesignColourMap) — один покрашенный вид; ПЛАН
// (DesignColourPlan) — весь документ карточки: карты плюс палитра с назначениями. Прогон замораживает
// ТОЛЬКО карты и `map_hex` каждой ткани, а не план целиком: план — это состояние экрана, которое
// продолжает жить и меняться после запуска, а история обязана говорить то, что было отправлено.
//
// ⚠ ЦВЕТА КАРТЫ — ЯРЛЫКИ, А НЕ ЦВЕТ ИЗДЕЛИЯ. Деталь, залитая #3a7bd5, не станет синей: она наденет
// ту ткань, которую план пришпилил к этому ярлыку. Собственные цвета изделия живут там, где всегда
// жили, — на тканях рецепта.

// MaxDesignColourMaps — потолок карт в плане. Шесть — это ровно столько, сколько у карточки бывает
// силуэтных видов (DesignSilhouetteViews), и седьмой карты не бывает не по бережливости, а потому
// что рисовать её не на чем.
const MaxDesignColourMaps = 6

// ПОТОЛКИ ДОКУМЕНТА — ТРИ, И ОНИ ЗАКРЫВАЮТ ЗАМЕРЕННУЮ ДЫРУ, А НЕ ВКУСОВЩИНУ.
//
// ⚠ ЧТО СЛУЧАЛОСЬ БЕЗ НИХ. Потолок стоял ровно один — шесть карт, — а ни палитре, ни списку
// тканей, ни документу целиком не было потолка вовсе; gRPC принимает 50 МиБ. Один
// SetDesignColourPlan с шестью картами по ~10⁵ образцов клал десятки мегабайт в `maps`/`cloths`, а
// colourPlanByCard сидит ВНУТРИ чтения ленты карточки (store/design/band.go) — значит КАЖДЫЙ
// GetDesignBand этой карточки грузил и перекодировал их, и за grpcMaxSendMsgSize экран студии
// падал с ResourceExhausted НАВСЕГДА. Лечилось только удалением плана или хирургией по базе.
//
// ⚠ ПОЧЕМУ ПОТОЛОК БАЙТАМ, А НЕ ТОЛЬКО СЧЁТЧИКАМ. Ровно тот же довод, что у пути прогона
// (designMaxParamsBytes/designMaxInputsBytes): считать поля вместо байтов значит проверять другое
// число — `words` и `parts` строки не ограничены длиной, и шестнадцать строк по мегабайту прошли
// бы любой счётчик. Отказ отсюда называет ЧИСЛО и поле, в отличие от отказа кодировщика.
const (
	// MaxDesignColourSwatchesPerMap — ярлыков на одной карте. Палитра это ЗАМКНУТОЕ множество
	// красок, которые человек выбрал сам (см. DesignColourSwatch): у изделия деталей десятки, а не
	// тысячи, и сканированная кромка ярлыком не становится по построению.
	MaxDesignColourSwatchesPerMap = 64
	// MaxDesignColourCloths — назначений в плане. Назначение бывает только на покрашенный ярлык,
	// поэтому больше, чем ярлыков на одной карте, их осмысленно не бывает.
	MaxDesignColourCloths = 64
	// MaxDesignColourPlanBytes — ВЕСЬ документ в закодированном виде, тот же потолок, что у снимка
	// входов прогона. План читается на каждой ленте карточки, поэтому его цена — это цена
	// открытия студии, а не цена одной записи.
	MaxDesignColourPlanBytes = 64 << 10
)

// DesignColourSwatch — ОДИН ЯРЛЫК карты: цвет, который человек выбрал сам, и сколько пикселей им
// закрашено.
//
// `Px` — СЧЁТ ПО ТОЧНОМУ СОВПАДЕНИЮ, и это единственный счёт, который здесь что-то значит. И кисть,
// и ведро смешивают на краях, поэтому сырой скан документа содержит сотни промежуточных оттенков,
// которых никто не выбирал; счёт ведётся по ЗАМКНУТОМУ множеству записанных красок, и только
// поэтому сглаженная кромка не может стать тканью.
type DesignColourSwatch struct {
	Hex string `json:"hex"`
	Px  int    `json:"px"`
}

// DesignColourMap — ОДИН ПОКРАШЕННЫЙ ВИД: флэт стороны `View`, залитый деталь за деталью плоскими
// цветами.
//
// `BaseMediaId` ХРАНИТСЯ, А НЕ ВЫВОДИТСЯ. Плита в слоте законно сменяется (новый флэт, перерез,
// флэттен), и карта, не умеющая назвать подложку, по которой её рисовали, была бы навсегда
// непроверяемой на устаревание.
type DesignColourMap struct {
	MediaId     int                  `json:"media_id"`
	View        string               `json:"view"`
	BaseMediaId int                  `json:"base_media_id"`
	Palette     []DesignColourSwatch `json:"palette"`
}

// DesignColourCloth — ЧТО ЗНАЧИТ ОДИН ПОКРАШЕННЫЙ ЦВЕТ: ткань, цвет или слова, из которых сделаны
// детали под этим ярлыком.
//
// КЛЮЧ — HEX, А НЕ АССЕТ. Одна ткань законно метит два цвета (тот же джерси в двух колорвеях — это
// две строки), а цвет законно не называет ассета вовсе: плоский цвет либо фраза — полный ответ на
// вопрос «из чего эта деталь». Хотя бы одно из трёх сказать обязаны, иначе строка не говорит
// ничего, а ярлык карты указывает в тишину.
type DesignColourCloth struct {
	Hex       string `json:"hex"`
	AssetId   int    `json:"asset_id"`
	ColourHex string `json:"colour_hex"`
	Words     string `json:"words"`
	Parts     string `json:"parts"`
}

// Stated — сказала ли строка хоть что-нибудь. См. шапку типа: строка, назвавшая один ярлык и
// больше ничего, — это ярлык, указывающий в тишину.
//
// ⚠ `Parts` СЮДА НЕ ВХОДИТ, И ЭТО ПОЧИНКА, А НЕ УЖЕСТОЧЕНИЕ. Контракт говорит буквально «хотя бы
// одно из ТРЁХ» и перечисляет их — asset_id, colour_hex, words (common/design.proto,
// DesignColourCloth), — и шапка этого типа строкой выше говорит то же самое. Код принимал ЧЕТВЁРТОЕ,
// поэтому строка `{hex, parts:"cuffs"}` хранилась и возвращалась как назначение, НЕ НАЗЫВАЮЩЕЕ
// МАТЕРИАЛА ВОВСЕ: «манжеты сделаны из… » — и тишина. `Parts` отвечает на другой вопрос («КАКИЕ
// детали»), а этот метод спрашивает «ИЗ ЧЕГО», и ответом на него имя детали быть не может.
func (c DesignColourCloth) Stated() bool {
	return c.AssetId > 0 || strings.TrimSpace(c.ColourHex) != "" ||
		strings.TrimSpace(c.Words) != ""
}

// DesignColourPlan — строка design_colour_plan: весь план одной карточки.
type DesignColourPlan struct {
	TechCardId int
	Rev        int
	Maps       []DesignColourMap
	Cloths     []DesignColourCloth
	UpdatedBy  string
	UpdatedAt  time.Time
}

// DesignColourPlanSave — ЗАМЕНА ДОКУМЕНТА ЦЕЛИКОМ под compare-and-set по `rev`, ровно та же форма,
// которой SaveEditLayer заменяет штрихи. ExpectedRev == 0 означает «плана ещё нет» и рождает его.
type DesignColourPlanSave struct {
	TechCardId  int
	ExpectedRev int
	Maps        []DesignColourMap
	Cloths      []DesignColourCloth
	Actor       string
}

// ErrDesignColourPlanRevMismatch — CAS по rev цветового плана. Отдельный сентинел, а не
// ErrDesignLayerRevMismatch: у клиента здесь другой экран и другое объяснение («коллега сохранил
// свой план»), а различить их он может только по машинному токену.
var ErrDesignColourPlanRevMismatch = errors.New("design: colour_plan_rev_mismatch")

// Validate — ФОРМА ДОКУМЕНТА, ПРОВЕРЕННАЯ ДО ЗАПИСИ И В ОДНОМ МЕСТЕ.
//
// ⚠ ЭТО ЖИВЁТ ЗДЕСЬ, А НЕ В CHECK-ОГРАНИЧЕНИЯХ, И НЕ ПО ВКУСУ. ADD CONSTRAINT … CHECK копирует
// таблицу целиком и проверяется против ВСЕЙ истории — в этом проекте это уже однажды остановило
// старт прода. Плюс отказ отсюда называет человеку ПОЛЕ, а 3819 назвал бы имя ограничения.
//
// ЧТО ИМЕННО ПРОВЕРЯЕТСЯ И ПОЧЕМУ ИМЕННО ЭТО:
//
//   - ВИД ИЗ СПИСКА СИЛУЭТА — карта рисуется по флэту стороны, и вид, которого у силуэта нет, это
//     карта ни к чему; промпт назвал бы её словом, которого не знает;
//   - ОДНА КАРТА НА ВИД — две карты одного вида это два несогласных ответа на один вопрос, и
//     выбирать между ними пришлось бы порядку в массиве;
//   - HEX СТРОГО #rrggbb В НИЖНЕМ РЕГИСТРЕ — ярлык это КЛЮЧ, по которому ткань находит свои
//     детали, и «#3A7BD5» против «#3a7bd5» разошлись бы молча, оставив ткань без деталей;
//   - ЧЁРНЫЙ И БЕЛЫЙ НЕ ЯРЛЫКИ — это краска линии и бумага; принять их значило бы позволить
//     назначить ткань на обводку чертежа;
//   - НАЗНАЧЕНИЕ ТОЛЬКО НА ЦВЕТ, КОТОРЫЙ КТО-ТО ПОКРАСИЛ — назначение на цвет, которого нет ни на
//     одной карте, переживёт перекраску, его удалившую, и будет утверждать про деталь, которой нет.
func (s DesignColourPlanSave) Validate() error {
	if s.TechCardId <= 0 {
		return fmt.Errorf("%w: tech_card_id %d", ErrDesignInvalidArgument, s.TechCardId)
	}
	if len(s.Maps) > MaxDesignColourMaps {
		return fmt.Errorf("%w: %d colour maps, the ceiling is %d",
			ErrDesignInvalidArgument, len(s.Maps), MaxDesignColourMaps)
	}
	if len(s.Cloths) > MaxDesignColourCloths {
		return fmt.Errorf("%w: %d cloth assignments, the ceiling is %d",
			ErrDesignInvalidArgument, len(s.Cloths), MaxDesignColourCloths)
	}
	// ПАЛИТРА СОБИРАЕТСЯ ЗДЕСЬ ЖЕ, А НЕ ВТОРЫМ ПРОХОДОМ: множество законных ярлыков — это ровно то,
	// что проверка карт уже перебрала, и второй сборщик того же множества разошёлся бы с первым в
	// первый же день, когда правят одно.
	painted := make(map[string]struct{})
	views := make(map[string]struct{}, len(s.Maps))
	pictures := make(map[int]int, len(s.Maps))
	for i, m := range s.Maps {
		if !IsDesignSilhouetteView(m.View) {
			return fmt.Errorf("%w: maps.%d.view %q is not a silhouette view",
				ErrDesignInvalidArgument, i, m.View)
		}
		if _, dup := views[m.View]; dup {
			return fmt.Errorf("%w: maps.%d names view %q a second time; one map per view",
				ErrDesignInvalidArgument, i, m.View)
		}
		views[m.View] = struct{}{}
		if m.MediaId <= 0 {
			return fmt.Errorf("%w: maps.%d.media_id %d — a map is a picture",
				ErrDesignInvalidArgument, i, m.MediaId)
		}
		if m.BaseMediaId <= 0 {
			return fmt.Errorf("%w: maps.%d.base_media_id %d — a map must name the flat it was painted over",
				ErrDesignInvalidArgument, i, m.BaseMediaId)
		}
		// ⚠ ОДНА КАРТИНКА — ОДНА КАРТА, И ЭТО НЕ ТО ЖЕ САМОЕ, ЧТО «ОДНА КАРТА НА ВИД». Дубль вида
		// отвергался с первого дня, дубль КАРТИНКИ не отвергался ничем: две карты на один media_id
		// давали одну картинку, объявленную картой двух разных видов. В рецепте прогона это
		// доезжало до модели предложением «Images 3 and 3 are colour maps of the front and back
		// drawings» — на оплаченном вызове (замер ревью).
		if at, dup := pictures[m.MediaId]; dup {
			return fmt.Errorf("%w: maps.%d names picture %d, which maps.%d already is; one picture is one map",
				ErrDesignInvalidArgument, i, m.MediaId, at)
		}
		pictures[m.MediaId] = i
		if len(m.Palette) > MaxDesignColourSwatchesPerMap {
			return fmt.Errorf("%w: maps.%d.palette holds %d labels, the ceiling is %d",
				ErrDesignInvalidArgument, i, len(m.Palette), MaxDesignColourSwatchesPerMap)
		}
		for j, sw := range m.Palette {
			if !IsDesignColourMapHex(sw.Hex) {
				return fmt.Errorf("%w: maps.%d.palette.%d.hex %q is not a lower-case #rrggbb label",
					ErrDesignInvalidArgument, i, j, sw.Hex)
			}
			if sw.Px < 0 {
				return fmt.Errorf("%w: maps.%d.palette.%d.px %d",
					ErrDesignInvalidArgument, i, j, sw.Px)
			}
			painted[sw.Hex] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(s.Cloths))
	for i, c := range s.Cloths {
		if !IsDesignColourMapHex(c.Hex) {
			return fmt.Errorf("%w: cloths.%d.hex %q is not a lower-case #rrggbb label",
				ErrDesignInvalidArgument, i, c.Hex)
		}
		if _, dup := seen[c.Hex]; dup {
			return fmt.Errorf("%w: cloths.%d states %s a second time; the hex is the key",
				ErrDesignInvalidArgument, i, c.Hex)
		}
		seen[c.Hex] = struct{}{}
		if _, ok := painted[c.Hex]; !ok {
			return fmt.Errorf("%w: cloths.%d.hex %s appears on no colour map of this plan",
				ErrDesignInvalidArgument, i, c.Hex)
		}
		if c.AssetId < 0 {
			return fmt.Errorf("%w: cloths.%d.asset_id %d", ErrDesignInvalidArgument, i, c.AssetId)
		}
		if !c.Stated() {
			return fmt.Errorf("%w: cloths.%d states %s and nothing else — say what that colour is "+
				"with a texture, a colour or words", ErrDesignInvalidArgument, i, c.Hex)
		}
	}
	// ─── ПОТОЛОК ДОКУМЕНТА ЦЕЛИКОМ, ПОСЛЕДНИМ ───
	//
	// ПОСЛЕДНИМ, ПОТОМУ ЧТО ЭТО ЕДИНСТВЕННАЯ ПРОВЕРКА, КОТОРАЯ СТОИТ РАБОТЫ: счётчики выше режут
	// очевидное даром, а сюда доходит только документ правильной формы, у которого остались длинные
	// строки. Кодируется ровно то, что уедет в колонки (см. store/design/colour_plan.go), поэтому
	// названное здесь число — то самое, которое потом читает каждая лента карточки.
	size, err := colourPlanEncodedSize(s.Maps, s.Cloths)
	if err != nil {
		return fmt.Errorf("%w: the colour plan does not encode: %s", ErrDesignInvalidArgument, err)
	}
	if size > MaxDesignColourPlanBytes {
		return fmt.Errorf("%w: the colour plan encodes to %d bytes, the ceiling is %d — this plan "+
			"is read again on every card band, so an oversized one makes the card unreadable rather "+
			"than merely large", ErrDesignInvalidArgument, size, MaxDesignColourPlanBytes)
	}
	return nil
}

// colourPlanEncodedSize — сколько байт займут ОБЕ JSON-колонки плана вместе.
//
// ОДИН СЧЁТЧИК, ПОТОМУ ЧТО ЧИСЛО ОДНО: стор кодирует ровно эти два документа ровно этим
// кодировщиком, и второй способ померить то же самое разошёлся бы с первым молча.
func colourPlanEncodedSize(maps []DesignColourMap, cloths []DesignColourCloth) (int, error) {
	m, err := json.Marshal(maps)
	if err != nil {
		return 0, err
	}
	c, err := json.Marshal(cloths)
	if err != nil {
		return 0, err
	}
	return len(m) + len(c), nil
}

// IsDesignColourMapHex — ГОДИТСЯ ЛИ СТРОКА В ЯРЛЫК КАРТЫ: строго `#rrggbb` в нижнем регистре, и
// ни чёрный, ни белый.
//
// ОДИН ЧИТАТЕЛЬ НА ВСЮ ПОЛОСУ, потому что вопрос один, а мест, где он задаётся, три: проверка
// плана, дверь прогона и строка промпта. Три сравнения с регуляркой разошлись бы на первой же
// правке, и разошлись бы молча — «не ярлык» это законный ответ.
//
// ⚠ РЕГИСТР ЗДЕСЬ НЕ КОСМЕТИКА, А КЛЮЧ. Ярлык сравнивают со строкой ткани (`map_hex`), и «#3A7BD5»
// против «#3a7bd5» — это ткань, потерявшая свои детали, без единого сообщения об ошибке.
// Нормализовать молча было бы хуже отказа: клиент, приславший верхний регистр, продолжал бы
// присылать его и в `map_hex`, где нормализовать уже некому — то поле замерзает в истории.
func IsDesignColourMapHex(v string) bool {
	if len(v) != 7 || v[0] != '#' {
		return false
	}
	for i := 1; i < 7; i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	// Краска линии и бумага — не ярлыки: см. шапку.
	return v != "#000000" && v != "#ffffff"
}
