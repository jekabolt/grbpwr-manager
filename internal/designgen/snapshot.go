package designgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Layouts of a run's output — the DesignRunParams.layout dictionary.
const (
	layoutOne     = "one"
	layoutPerView = "per_view"
)

// runParams / runInputs are EXACTLY THE FIELDS THE WORKER NEEDS out of the frozen snapshot, and
// nothing more. They are a second, narrow reader of the same JSON the store reads narrowly — not
// a duplicate of the contract — for the reason the store gives in wave2.go: a full protobuf decode
// here would drag the wire into a package that has no business knowing about it.
//
// ⚠ THE KEYS ARE snake_case BECAUSE THE WRITER USES protojson WITH UseProtoNames: true. Switching
// that writer to the protojson default would turn every field below into a silent zero: no error,
// no picture missing, just a prompt that says less than it should and references that are not
// sent. That is the failure this repo has already met once.
type runParams struct {
	Views              []string      `json:"views"`
	Layout             string        `json:"layout"`
	Colour             *colourRecipe `json:"colour"`
	ExtraInputMediaIDs []int         `json:"extra_input_media_ids"`
	DetailSlotIDs      []int         `json:"detail_slot_ids"`
	FixTarget          string        `json:"fix_target"`
	FixTargets         []string      `json:"fix_targets"`
	// FixSlotIDs — ТРЕТЬЕ ПРАВОПИСАНИЕ СУЖЕНИЯ, и без него сборщик ссылок читал «сужен ли прогон»
	// иначе, чем отбор плит: прогон, сузивший себя слотами, а не видами, выглядел здесь обычным.
	FixSlotIDs []int         `json:"fix_slot_ids"`
	Threed     *threedParams `json:"threed"`
	// Pattern is only meaningful for kind=pattern. A nil pointer is «the run said nothing about the
	// repeat», which is legal — a tile is a tile whether or not anybody has decided how large it
	// will be printed.
	Pattern *patternParams `json:"pattern"`
	// Freeform is the PLAYGROUND's whole ask, and on that kind it is the ONLY source of pictures:
	// no bench, no references, no card. A nil pointer on any other kind is the ordinary state —
	// the door refuses the field to every kind but its own.
	Freeform *freeformParams `json:"freeform"`
	// Image is the per-run engine (DesignRunParams.image, PLAYGROUND phase 2). nil = the
	// deployment's dial, which is every run frozen before the field.
	Image *imageOptions `json:"image"`
	// Extend / Inpaint — PLAYGROUND phase 3 (DesignRunParams.extend = 18 / inpaint = 17). nil on
	// every other kind: the door refuses the blocks to every kind but their own.
	Extend  *extendParams  `json:"extend"`
	Inpaint *inpaintParams `json:"inpaint"`
	// Video — DesignVideoParams (field 19, B-32): the one source picture, the clip length and the
	// slug the door froze. nil on every other kind.
	Video *videoParams `json:"video"`
}

// videoParams — DesignVideoParams: the picture to animate, the duration, the frozen Kling slug.
type videoParams struct {
	SourceMediaID int    `json:"source_media_id"`
	Duration      int    `json:"duration"`
	Model         string `json:"model"`
}

// extendParams — DesignExtendParams: the target proportion of an extend run (the source travels in
// extra_input_media_ids).
type extendParams struct {
	AspectRatio string `json:"aspect_ratio"`
}

// inpaintParams — DesignInpaintParams: the picture to repaint and its painted mask (white = repaint).
type inpaintParams struct {
	SourceMediaID int `json:"source_media_id"`
	MaskMediaID   int `json:"mask_media_id"`
}

// imageOptions — the per-run engine of an OpenRouter image kind. The door validated every value
// against the engine table; the reader only carries them to Job.
type imageOptions struct {
	Model       string `json:"model"`
	Quality     string `json:"quality"`
	AspectRatio string `json:"aspect_ratio"`
	Background  string `json:"background"`
}

// freeformParams / freeformItem / freeformRegion — ТОТ ЖЕ УЗКИЙ ЧИТАТЕЛЬ, ЧТО И ВСЁ ВЫШЕ: ровно
// те поля замороженного снимка, которые нужны сборке задания, и ни одного сверх.
//
// ⚠ КЛЮЧИ — snake_case, ПОТОМУ ЧТО ПИСАТЕЛЬ — protojson С UseProtoNames. Ошибка здесь не падает и
// не логируется: `media_id`, прочитанный как `mediaId`, — это ноль, то есть картинка, которая
// просто не уехала, при полностью успешном прогоне.
type freeformParams struct {
	Preset string         `json:"preset"`
	Items  []freeformItem `json:"items"`
	// Options — the preset's settings (DesignWorkflowOptions, phase 2). nil on every run frozen
	// before the field and on a preset that reads none.
	Options *workflowOptions `json:"options"`
}

// workflowOptions — DesignWorkflowOptions, flat: each preset reads its own fields and the door
// refuses the rest (`option_not_read`). tryon: framing … product_colorway_id; add_logo:
// logo_size; variations: creativity.
type workflowOptions struct {
	Framing           string `json:"framing"`
	Angle             string `json:"angle"`
	SceneMode         string `json:"scene_mode"`
	SceneText         string `json:"scene_text"`
	ModelID           int    `json:"model_id"`
	ProductColorwayID int    `json:"product_colorway_id"`
	LogoSize          string `json:"logo_size"`
	Creativity        int    `json:"creativity"`
}

// freeformItem — ОДНА КАРТИНКА ПЛЕЙГРАУНДА со всем, что человек про неё сказал.
//
// `Texts` и `Regions` ПАРНЫ ПО ИНДЕКСУ, и это единственное правило, по которому слова находят
// своё место на картинке: texts[i] описывает regions[i], а хвостовой текст (i == len(regions))
// описывает картинку целиком. Дверь держит len(texts) ≤ len(regions)+1, поэтому лишнего хвоста не
// бывает; читатель всё равно берёт по индексу, а не по длине.
type freeformItem struct {
	MediaID int              `json:"media_id"`
	Regions []freeformRegion `json:"regions"`
	Texts   []string         `json:"texts"`
	Role    string           `json:"role"`
}

// freeformRegion — размеченная область: многоугольник в долях картинки.
//
// `Kind` читается, но не проверяется здесь: дверь принимает только POLYGON, а снимок, замороженный
// мимо сегодняшней двери, честнее обвести по его точкам, чем выбросить молча.
type freeformRegion struct {
	Kind   string          `json:"kind"`
	Points []freeformPoint `json:"points"`
}

// freeformPoint — доля 0..1. Координата приезжает google.type.Decimal'ом, то есть ОБЪЕКТОМ со
// строкой внутри: `{"x":{"value":"0.35"}}`. Читать её числом нельзя — protojson её так не пишет.
type freeformPoint struct {
	X freeformDecimal `json:"x"`
	Y freeformDecimal `json:"y"`
}

type freeformDecimal struct {
	Value string `json:"value"`
}

// f — доля числом. Непарсящееся значение читается нулём: у двери оно пройти не могло, а падать в
// сборке задания из-за одной координаты значило бы потерять весь прогон.
func (d freeformDecimal) f() float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(d.Value), 64)
	if err != nil {
		return 0
	}
	return v
}

// patternParams is the frozen ask of a repeating-tile run.
//
// ONE FIELD, AND IT IS A NUMBER THE MODEL CAN ACT ON. That is the same argument V-7 already made
// about `design_asset.repeat_mm`: «крупный» and «мелкий» are not instructions, «120 mm» is. It is
// also the number the ASSET built from this tile inherits, so «generated at 120 mm» and «placed at
// 120 mm» are one claim rather than two that drift.
type patternParams struct {
	RepeatMM int `json:"repeat_mm"`
	// Mode — ИЗ ЧЕГО СТРОИТСЯ ПЛИТКА (STEP 3): "" / "image" — из одной фотографии ткани (маршрут,
	// которым замёрз каждый прогон до этого поля), "swatch" — из заявленного цвета, с 0–1
	// референсом ФАКТУРЫ. Имя snake_case по той же причине, что у всего снимка: protojson пишет
	// params с UseProtoNames. Режим меняет ДВА места и больше ничего: число картинок вызова
	// (imageCalls) и абзац ремесла (patternCraft).
	Mode string `json:"mode"`
}

type colourRecipe struct {
	Source        string `json:"source"`
	Code          string `json:"code"`
	Hex           string `json:"hex"`
	Words         string `json:"words"`
	FabricMediaID int    `json:"fabric_media_id"`
	// THE CLOTHS OF THIS GARMENT, one entry per cloth, in the order the person stated them (V-8).
	//
	// ⚠ THE FOUR SCALARS ABOVE ARE NOT SUPERSEDED BY THIS LIST, THEY ARE ITS FIRST MEMBER'S ECHO.
	// The contract (DesignColourRecipe.fabric_media_id) says it in as many words: a new run states
	// its cloths here AND ALSO repeats the first one's texture in `fabric_media_id`, because the
	// order-of-authority paragraph names the governing photograph by its image number and reads it
	// from that scalar. So a ONE-cloth run — the ordinary case, and every run frozen before this
	// field existed — is fully described by the scalars alone, and the render craft must keep
	// composing it from them, byte for byte. This list earns its own words only from the SECOND
	// cloth onwards, where the scalars have nothing left to say.
	Fabrics []fabricUse `json:"fabrics"`
	// THE COLOUR MAPS OF THIS RUN — the painted flats, frozen copies like the cloths.
	//
	// ⚠ EMPTY IS THE ORDINARY RUN, AND THAT IS WHAT MAKES THIS FIELD FREE. Every run frozen before
	// Feature A carries no `colour_maps` at all, so the attach loop below adds nothing, the heading
	// sentence is not emitted and every cloth line is the line it was — the six frozen single-cloth
	// goldens included. The new wording therefore cannot reach an old run by construction rather
	// than by a guard somebody could get wrong.
	ColourMaps []colourMap `json:"colour_maps"`
}

// colourMap is ONE PAINTED VIEW that went out with this run: which picture it is and which drawing
// it is a map OF.
//
// ⚠ TWO FIELDS, AND THE ABSENT ONES ARE THE POINT. The contract's map also carries
// `base_media_id` and a `palette`; neither is read here, and reading them would be this package
// inventing a second opinion. The base is a STALENESS MARK the client compares against the slot —
// it never goes to the provider and the prompt never mentions it — and the palette is the closed
// set of labels the CLIENT verified when it saved the map. The prompt names a colour because a
// cloth pointed at it (`map_hex`), never because a palette listed it: a label nobody assigned to a
// cloth is a label with nothing to say, and printing it would tell the model about a part of the
// garment the person never described.
type colourMap struct {
	MediaID int    `json:"media_id"`
	View    string `json:"view"`
}

// fabricUse is ONE cloth of the submission: what it looks like and WHICH PART OF THE GARMENT it is
// for. The fields are the frozen copies of DesignFabricUse, never a join: a run's history must
// still read after the shelf asset was renamed, re-coloured or deleted, which is why `asset_id`
// travels beside the copies as provenance and not as something to resolve.
type fabricUse struct {
	AssetID    int    `json:"asset_id"`
	Name       string `json:"name"`
	MediaID    int    `json:"media_id"`
	ColourCode string `json:"colour_code"`
	ColourHex  string `json:"colour_hex"`
	Words      string `json:"words"`
	// WHICH PARTS THIS CLOTH IS FOR, in the human's own words, composed from the marks drawn on the
	// flats. ⚠ EMPTY MEANS THE WHOLE GARMENT — or, among several cloths, the remainder — AND NEVER
	// «unknown»: see the contract. A reader that treated the empty string as missing data would
	// drop the one cloth the person did not have to mark, which is usually the main one.
	Parts string `json:"parts"`
	// The repeat of a PATTERN cloth in whole millimetres on the finished garment; 0 = plain cloth.
	RepeatMM int `json:"repeat_mm"`
	// WHAT THIS CLOTH IS: `fabric` | `pattern`, the shelf asset's kind AT LAUNCH.
	//
	// ⚠ EMPTY IS A FABRIC, AND THAT IS WHAT MAKES THIS FIELD FREE. Every run frozen before round 15
	// carries no `kind` at all, so `clothIsAPattern` is false for all of them and every sentence
	// this package composes about them is the sentence it composed yesterday — the six frozen
	// single-cloth goldens included. The pattern wording therefore cannot reach an old run by
	// construction rather than by a guard somebody could get wrong.
	//
	// ⚠ AND IT IS NOT DERIVABLE FROM `repeat_mm`, WHICH IS WHY IT HAD TO BE PUT ON THE WIRE. That
	// number was the only circumstantial evidence of a pattern, and the round that added this field
	// is the round that took the number off the pattern screen: new tiles carry 0. A reader that
	// kept guessing from the repeat would have started calling every new pattern a plain cloth.
	Kind string `json:"kind"`
	// MapHex — THE FLAT COLOUR THAT MARKS THIS CLOTH'S PARTS on the run's colour maps. '' = this
	// cloth was not placed by painting, which is every run frozen before Feature A.
	//
	// ⚠ IT IS A SECOND WAY OF SAYING WHERE, NOT A REPLACEMENT FOR `Parts`. The two are printed
	// together when both are there («body, sleeves — the parts painted steel blue (#3a7bd5) on the
	// colour map»), because the words are what a person recognises and the colour is what the
	// picture actually shows. A cloth carrying only this is fully placed: the map says where.
	//
	// ⚠ AND `statedCloths` COUNTS IT. A row whose only content is a map label is a cloth somebody
	// deliberately painted onto the garment, and dropping it would drop a whole cloth out of a paid
	// run in silence — the same defect a blank row exists to avoid, read from the other end.
	MapHex string `json:"map_hex"`
}

// clothIsAPattern — эта ткань ПЛИТКА, а не фотография ткани.
//
// ОДИН ЧИТАТЕЛЬ ПОЛЯ НА ВЕСЬ ПАКЕТ, потому что вопрос один, а мест, где он задаётся, три: подпись
// картинки, строка клота и абзац одноклоточного рендера. Три сравнения со строковым литералом
// разошлись бы на первой же опечатке, и разошлись бы молча — «не паттерн» это законный ответ.
func clothIsAPattern(c fabricUse) bool {
	return strings.TrimSpace(c.Kind) == entity.DesignAssetKindPattern
}

type threedParams struct {
	Presentation string `json:"presentation"`
	FitOverride  string `json:"fit_override"`
	// ТЕЛОСЛОЖЕНИЕ — ЕДИНСТВЕННОЕ, ЧТО ЭТОТ ПРОГОН ГОВОРИТ ПРО ТЕЛО СЛОВАМИ (V-15).
	//
	// `model_id` в промпт не едет и ехать ему некуда: он называет СТРОКУ нашей картотеки, а у
	// снимка нет поля ни под имя модели, ни под её мерки — сборка о ней не узнает ничего. Значит
	// без этой строки выбор «на модели» менял в картинке ровно ноль, и «телосложение» было бы
	// вторым таким же органом. Оно СТРОКА, а не enum, потому что словарь телосложений — вопрос
	// формулировок, а enum заморозил бы сегодняшние слова в истории каждого замороженного прогона.
	BodyType string `json:"body_type"`

	// REFERENCE MODE (phase 2): 1..4 media ids, ordered front, back, left, right. Non-empty means
	// the run reads no bench plate at all.
	ReferenceMediaIDs []int `json:"reference_media_ids"`
	// Texture / PBR: '' | on | off (strings, because a proto3 bool cannot say «not stated»);
	// '' = on for texture, off for pbr. Quality: '' | standard | detailed. Follow: '' | photo |
	// shape (not advertised in phase 2).
	Texture string `json:"texture"`
	PBR     string `json:"pbr"`
	Quality string `json:"quality"`
	Follow  string `json:"follow"`
	// SurfaceHint — the person's own words about the surface; surfaceSteer carries them.
	SurfaceHint string `json:"surface_hint"`
}

// runInputs is the frozen input snapshot.
//
// ⚠ `mood` IS ABSENT FROM THIS STRUCT ON PURPOSE, AND ITS ABSENCE IS THE W-15 GUARANTEE. The
// moodboard is the mood, not the prompt: a moodboard picture reaches a model only when a person
// moves it into REFERENCES. A screen can promise that; only a reader that cannot see the field can
// guarantee it. Adding a `Mood` field here would silently undo the requirement, which is why the
// requirement is enforced by a type rather than by a filter somebody could forget to apply.
type runInputs struct {
	GarmentNote string      `json:"garment_note"`
	Fit         string      `json:"fit"`
	Refs        []inputRef  `json:"refs"`
	Slots       []inputSlot `json:"slots"`
}

type inputRef struct {
	MediaID  int            `json:"media_id"`
	Role     string         `json:"role"`
	Note     string         `json:"note"`
	Deleted  bool           `json:"deleted"`
	Callouts []inputCallout `json:"callouts"`
}

type inputCallout struct {
	Text string `json:"text"`
}

type inputSlot struct {
	ViewKey string `json:"view_key"`
	// WHICH detail slot, so `detail_slot_ids` can be resolved to a NAME. The wire has carried this
	// since the snapshot existed (`DesignInputSlot.slot_id`); this reader simply never asked for
	// it, and without it a run that requested two details could only say «draw two details».
	SlotID     int    `json:"slot_id"`
	DetailName string `json:"detail_name"`
	MediaID    int    `json:"media_id"`
}

// requestedDetailNameList names the detail slots this run asked for, ONE ENTRY PER ASKED SLOT and
// in the order they were asked; an entry the snapshot cannot name is empty.
//
// THE LIST IS THE PRIMITIVE, THE JOINED STRING IS A VIEW OF IT. Three different readers need these
// names — the «draw these details» block, the frame labels of a layout paragraph, and the per-call
// suffix of a per_view job — and only the first of them wants them glued together. Deriving the
// other two by splitting a comma-joined string would break on a detail whose own name has a comma.
//
// ПОЗИЦИОННО, И НИКАК ИНАЧЕ. Сервер у двери уже отверг прогон, у которого число элементов
// `detail_slot_ids` не совпало с числом `detail` в `views` (designEffectiveParams), поэтому здесь
// i-й `detail` соответствует i-му идентификатору. Сортировать или дедуплицировать эти списки
// нельзя: порядок `views` — тот же порядок, которым разрезчик подписывает кадры склеенного листа.
//
// ЧТО ДЕЛАЕТ НЕИЗВЕСТНЫЙ ИДЕНТИФИКАТОР. Он НЕ отменяет прогон и НЕ подставляет соседнее имя:
// снимок мог быть записан до того, как слоты попали в него, или деталь удалили вместе со слотом.
// Такая позиция говорит «detail» — ровно столько, сколько известно, — и молчание здесь честнее
// догадки, которая нарисовала бы не ту деталь под правильным именем.
//
// ⚠ ЧИТАЕТСЯ ВЕСЬ in.Slots, ВКЛЮЧАЯ ЗАПИСИ БЕЗ media_id, И ИМЕННО В ЭТОМ ВСЯ ФУНКЦИЯ. Плита с
// картинкой уже названа подписью `slotCaption` («… — detail view (collar)»); слот, который просят
// НАРИСОВАТЬ, картинки не имеет по определению — её как раз и заказывают, — и до этой волны в
// снимок не попадал вовсе. Сервер кладёт его записью без media_id (designNamedEmptyDetailSlots),
// и вот она — единственный источник имени в том единственном случае, ради которого поле завели.
func requestedDetailNameList(p runParams, in runInputs) []string {
	nameOf := make(map[int]string, len(in.Slots))
	for _, s := range in.Slots {
		if s.SlotID > 0 && strings.TrimSpace(s.DetailName) != "" {
			nameOf[s.SlotID] = strings.TrimSpace(s.DetailName)
		}
	}
	names := make([]string, 0, len(p.DetailSlotIDs))
	for _, id := range p.DetailSlotIDs {
		names = append(names, nameOf[id])
	}
	return names
}

// detailNameAt — имя i-й по счёту просимой детали либо пустая строка. Позиция, которой список не
// покрывает, — это унаследованный снимок, записанный до появления поля; см. правило длин у двери.
func detailNameAt(names []string, i int) string {
	if i < 0 || i >= len(names) {
		return ""
	}
	return names[i]
}

// requestedDetailNames is the «draw these details» line: the same list, joined, with the unnameable
// positions saying «detail» — as much as that run ever knew about them.
func requestedDetailNames(p runParams, in runInputs) string {
	list := requestedDetailNameList(p, in)
	if len(list) == 0 {
		return ""
	}
	names := make([]string, 0, len(list))
	for _, n := range list {
		if n == "" {
			n = "detail"
		}
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// parseParams / parseInputs decode LENIENTLY, exactly as the store does: a snapshot that will not
// parse must not stop a paid job from running. What is lost is context in the prompt, not the run.
func parseParams(raw entity.RawJSON) runParams {
	var p runParams
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func parseInputs(raw entity.RawJSON) runInputs {
	var in runInputs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	return in
}

// refCaption is ONE picture of the run TOGETHER WITH the words the prompt says about it.
//
// THE PAIR IS THE POINT. Until this type existed, the list of pictures was built in one place
// (referenceMediaIDs) and the list of captions in another (composePrompt), each with its own
// filters — a reference with no role and no note produced a picture but no caption, a slot
// produced a picture and never had a caption at all — so caption k routinely described picture
// k+something. The model was left to guess which words went with which image, which is exactly
// the owner's complaint («должно быть помечено какое медиа и что на нем»). One list, carrying
// both halves, is what makes the correspondence a construction rather than a hope.
type refCaption struct {
	MediaID int
	Caption string
	// View is the SIDE this picture shows, when the run knows: front | back | side_l | side_r |
	// detail. Empty for every source that is not a bench plate — a reference, an extra upload, a
	// fabric swatch — and empty is a truthful answer there rather than a missing one.
	//
	// IT IS FILLED BY THE SAME add() CALL THAT FILLS THE CAPTION, off the same element, which is
	// what keeps view k, caption k and reference k the same picture by construction. A second walk
	// producing «the views, in order» is exactly how captions and urls once came to disagree.
	View string
	// IsColourMap — ЭТА КАРТИНКА УЕХАЛА ИМЕННО КАРТОЙ ЦВЕТА, а не просто уехала.
	//
	// ⚠ ОДИН ФАКТ, ПОТОМУ ЧТО ЛГАЛИ ДВА РАЗНЫХ ЧТЕНИЯ ОДНОГО ВОПРОСА. Ревью замерило три способа
	// получить платный промпт, утверждающий про карту, которой у модели нет: `map_hex` без единой
	// карты в рецепте; две карты на один media_id («Images 3 and 3»); и карта, которая на самом
	// деле плита верстака — картинка, подписанная одновременно «current state of the garment» и
	// «colour map … those colours LABEL which cloth covers which part». Все три — один вопрос:
	// СТАЛА ЛИ ЭТА КАРТИНКА КАРТОЙ В СПИСКЕ ВЛОЖЕНИЙ. Он задаётся здесь один раз, при сборке
	// списка, и читается промптом (colourMapsSent) — вместо трёх параллельных догадок по номеру.
	//
	// Наличие media в списке ответом НЕ является: плита, названная картой, в списке есть — под
	// подписью плиты.
	IsColourMap bool
	// IsWindow — ЭТА КАРТИНКА И ЕСТЬ ОКНО ГЕНЕРАЦИИ: кроп области, приехавший ВМЕСТО полного кадра.
	//
	// ⚠ ФЛАГ, А НЕ ДОГАДКА ПО ПОДПИСИ ИЛИ ПО ПОЗИЦИИ. Ремесло обязано назвать модели НОМЕР этой
	// картинки («image 2 is a close crop…»), и номер — это позиция в списке; вычислять её поиском
	// по тексту подписи значило бы завести второе чтение того же факта, которое разъедется при
	// первой правке слов. Ставится ровно там же, где картинка кладётся в список.
	IsWindow bool
}

// referenceList is EVERY picture this run is allowed to show a model, in a stable order, each
// with its caption.
//
// FOUR SOURCES, ALL OF THEM CHOSEN BY A PERSON: the bench plates a fix or a render was pointed
// at, the references panel, the extra media dropped into a render, and the colour recipe's fabric
// swatch. The moodboard is not among them and cannot be — see runInputs.
//
// ORDER IS MEANING ON THE 3D ROUTE, where Meshy reads the first url as the front view. Slots
// therefore come first and are sorted front, back, side_l, side_r, detail rather than in whatever
// order the snapshot happens to hold them.
//
// ⚠ AND ON THE FLAT ROUTE THE ORDER IS THE OPPOSITE, DELIBERATELY (J-10). The owner's rule about
// the plates the studio chooses to send along with a flat run — «так же они всегда добавляются в
// конец промпта» — is a statement about the PROMPT, so it has to hold in the one list that numbers
// the prompt's images. It did not: the plate walk ran first on every route, and a flat run with
// `use_flat_slots` numbered the bench plates `image 1…k` AHEAD of the references the operator
// actually brought. The screen that names each plate's number counts it as «after the last
// reference», so screen and prompt disagreed about which picture `image 3` is — and a caption that
// points at the wrong picture is worse than no caption, because the model acts on it.
//
// THE FLIP IS FLAT-ONLY, AND THE REASON IS THE SAME SENTENCE AS ABOVE: on 3D the first url IS the
// front view, so «references first» would hand Meshy somebody's mood photograph as the front of
// the garment. Render, recolor and pattern are untouched for the plainer reason that their
// composed prompts are already frozen in history, and a reordering would renumber every caption of
// every future run of a kind nobody asked to change.
//
// KIND IS A PARAMETER RATHER THAN A FIELD OF runParams BECAUSE THAT IS WHERE IT LIVES: the kind is
// a column of design_run, not one of the frozen params, and inventing a params field for it would
// mint a second, staleable copy of a fact the row already states.
//
// EVERY ENTRY HAS WORDS, EVEN THE UNANNOTATED ONES. A picture that goes to the model with no
// caption line shifts the numbering of every caption after it — that silent shift is the defect
// this type exists to close, so «no words» is itself said in words («reference image»).
func referenceList(kind string, p runParams, in runInputs) []refCaption {
	// ─── ПЛЕЙГРАУНД: СПИСОК ЕСТЬ РОВНО ТО, ЧТО ЧЕЛОВЕК ПОЛОЖИЛ, В ТОМ ЖЕ ПОРЯДКЕ ───
	//
	// ⚠ ВЕТКА ПЕРВОЙ СТРОКОЙ, А НЕ ФИЛЬТРОМ В КОНЦЕ, И ЭТО ТОТ ЖЕ ПРИЁМ, ЧТО У `sourcePictures`
	// перекраса. Ни плиты верстака, ни ссылки карточки, ни ткани рецепта в прогон плейграунда не
	// едут — и не «отсеиваются потом», а не строятся вовсе: отсев по построенному списку молча
	// зависел бы от того, что положил в снимок кто-то другой.
	//
	// НОМЕР КАРТИНКИ — ЭТО ЕЁ МЕСТО В ЭТОМ СПИСКЕ, и он же — номер на экране человека. Порядок
	// items и есть контракт: «image 1» — это items[0], сказано в самом контракте (design.proto).
	if kind == entity.DesignRunKindFreeform {
		return freeformReferences(p)
	}
	// ─── EXTEND (phase 3): THE ONE NAMED PICTURE AND NOTHING ELSE. Like the cut-out it reads no
	// card; unlike the cut-out it is said here, not inherited from the general walk — the plan below
	// is derived from References[0], so «which picture» must not depend on what else a snapshot holds.
	if kind == entity.DesignRunKindExtend {
		var out []refCaption
		for _, id := range p.ExtraInputMediaIDs {
			if id > 0 {
				out = append(out, refCaption{MediaID: id, Caption: "the picture to extend"})
			}
		}
		return out
	}
	// ─── INPAINT (phase 3): THE ONE PICTURE TO RETOUCH. The mask is NOT a reference — it is where to
	// paint, it travels as mask_url — so it joins the media batch in buildJob and never this list.
	if kind == entity.DesignRunKindInpaint {
		if p.Inpaint == nil || p.Inpaint.SourceMediaID <= 0 {
			return nil
		}
		return []refCaption{{MediaID: p.Inpaint.SourceMediaID, Caption: "the picture to retouch"}}
	}
	// ─── VIDEO (B-32): THE ONE PICTURE TO ANIMATE — the first frame. Like the retouch it reads no
	// card: the bench, the references and the cloths are pictures of other things, and Kling reads
	// exactly one image_url.
	if kind == entity.DesignRunKindVideo {
		if p.Video == nil || p.Video.SourceMediaID <= 0 {
			return nil
		}
		return []refCaption{{MediaID: p.Video.SourceMediaID, Caption: "the picture to animate"}}
	}
	slots := append([]inputSlot(nil), in.Slots...)
	sort.SliceStable(slots, func(i, j int) bool {
		return viewRank(slots[i].ViewKey) < viewRank(slots[j].ViewKey)
	})

	seen := map[int]int{}
	var out []refCaption
	add := func(id int, caption, view string) {
		if id <= 0 {
			return
		}
		if at, dup := seen[id]; dup {
			// The same picture named by a second source keeps its FIRST position — order is
			// meaning on the 3D route — but the second source's words are appended rather than
			// dropped: a bench plate that is also a reference with a note must not lose the note
			// to deduplication.
			if caption != "" && !strings.Contains(out[at].Caption, caption) {
				out[at].Caption = out[at].Caption + "; " + caption
			}
			// ⚠ AND IT ADOPTS A SIDE IF IT DOES NOT YET HAVE ONE. The old comment here said the
			// plate walk always runs first, so a deduped picture «is already labelled with the
			// side it stands on» — TRUE UNTIL THE FLAT ROUTE STARTED WALKING REFERENCES FIRST, and
			// false after it: on flat, a picture that is both a plate and a reference entered as a
			// reference (view "") and the plate's own side was then dropped on the floor. A
			// reference never offers a side, so this can only ever fill a blank, never overwrite a
			// stated one — the 3D route, where the first url IS the front view, is untouched by
			// construction.
			if out[at].View == "" && view != "" {
				out[at].View = view
			}
			return
		}
		seen[id] = len(out)
		out = append(out, refCaption{MediaID: id, Caption: caption, View: view})
	}
	addSlots := func() {
		for _, s := range slots {
			add(s.MediaID, slotCaption(s), s.ViewKey)
		}
	}
	addRefs := func() {
		for _, r := range in.Refs {
			// A reference whose media row is gone is remembered by the snapshot but cannot be
			// fetched by anyone; sending its id would produce a 404 at the provider,
			// mid-paid-call.
			if r.Deleted {
				continue
			}
			add(r.MediaID, refEntryCaption(r), "")
		}
	}
	// TWO ORDERS, ONE `add`. Both branches walk the same three sources through the same closure,
	// so the dedup rule, the caption-merging rule and the «first position wins» rule are one
	// implementation rather than two that can drift — which is exactly how captions and urls came
	// to disagree the first time.
	//
	// THE EXTRAS SIT LAST IN BOTH, AND ON THE FLAT ROUTE THAT COSTS NOTHING: designAssembleInputs
	// already folds `extra_input_media_ids` INTO the snapshot's refs before it is frozen
	// (design_run.go), so by the time this walk runs they are ordinary references and this third
	// loop is a dedup no-op that only exists for snapshots frozen before that folding did.
	//
	// ⚠ ВЫБОРОЧНАЯ ПРАВКА ИЗ ПЕРЕВОРОТА ИСКЛЮЧЕНА, И ГРАНИЦА ТА ЖЕ, ЧТО У ОТБОРА ПЛИТ. Гейт K-1
	// («флэт не берёт плиты, пока не попросят») обходится признаком `selective`, поэтому
	// выборочный флэт-фикс плиты НЕСЁТ — и переворот выводил бы вперёд снимок настроения, а плиту,
	// НА КОТОРУЮ ПРОГОН НАВОДИЛИ, нумеровал вторым. Прогон, чей промпт открывается словами «Turn
	// the garment shown in the reference image into a technical flat sketch», получал бы при этом
	// чужую картинку первой — на платном вызове. Признак считается ОДНОЙ функцией на оба читателя
	// (entity.IsDesignSelectiveFix), иначе половины волны разъезжаются.
	selective := entity.IsDesignSelectiveFix(p.FixTarget, p.FixTargets, p.FixSlotIDs)
	if kind == entity.DesignRunKindFlat && !selective {
		addRefs()
		addSlots()
	} else {
		addSlots()
		addRefs()
	}
	for _, id := range p.ExtraInputMediaIDs {
		add(id, "additional reference image", "")
	}
	// ─── THE COLOUR MAPS ───────────────────────────────────────────────────────────────────────
	//
	// ⚠ THE CAPTION IS THE WHOLE REASON THIS PICTURE TRAVELS IN THE RECIPE AND NOT AS AN EXTRA
	// INPUT. An extra input is captioned «additional reference image» — one sentence for every
	// picture nobody else described — and a colour map read under that caption is a DRAWING OF THE
	// GARMENT in improbable colours: a model shown a flat flooded steel blue and told it is a
	// reference will render a steel blue garment. What has to be said, and can only be said here,
	// is that those colours are LABELS.
	//
	// THEY STAND AFTER THE REFERENCES AND BEFORE THE CLOTHS, which is where the cloth list can
	// point at them by number: the heading of that list cites the maps and every cloth line cites
	// its own swatch, and both numbers come off THIS slice, in this order, by construction.
	//
	// A MAP WHOSE MEDIA ROW WENT AWAY IS SIMPLY NOT NUMBERED AND NOT MENTIONED — the same rule as
	// a cloth's picture, enforced in the same place: `attached` is the survivors of buildJob's
	// resolution, and imageNumberOf answers 0 for anything not in it.
	//
	// ⚠ КАРТОЙ СТАНОВИТСЯ ТОЛЬКО КАРТИНКА, КОТОРОЙ В СПИСКЕ ЕЩЁ НЕТ, И ЭТО НЕ МИКРО-ОПТИМИЗАЦИЯ.
	// `add` дедуплицирует по media id и СКЛЕИВАЕТ подписи, поэтому карта, чей media_id совпал с
	// плитой, референсом или второй картой, давала одну картинку с двумя взаимно исключающими
	// подписями: «current state of the garment — front view; colour map of the front flat — … those
	// colours LABEL which cloth covers which part». Замерено ревью на обоих совпадениях. Дверь
	// прогона такие рецепты теперь отвергает до денег (designRefuseMalformedColourMaps,
	// designRefuseColourMapAlsoAnInput); здесь стоит вторая половина того же правила — ОДНА
	// КАРТИНКА ЕСТЬ ОДНА РОЛЬ, — чтобы промпт не мог соврать даже про снимок, замороженный мимо
	// сегодняшней двери.
	if p.Colour != nil {
		for _, m := range p.Colour.ColourMaps {
			if m.MediaID <= 0 {
				continue
			}
			if _, taken := seen[m.MediaID]; taken {
				continue
			}
			at := len(out)
			add(m.MediaID, colourMapCaption(m.View), m.View)
			if at < len(out) {
				out[at].IsColourMap = true
			}
		}
	}
	// ─── THE CLOTHS ────────────────────────────────────────────────────────────────────────────
	//
	// TWO BRANCHES, AND THE SPLIT IS THE SAME ONE renderFabricSection MAKES, for the same reason:
	// a run of one cloth must attach exactly what it attached before this wave, byte for byte,
	// because the composed prompt is written into the history and a caption that shifted would
	// renumber every image reference after it in every future single-cloth run.
	//
	// ⚠ WITHOUT THE LOOP BELOW THE MULTI-CLOTH FEATURE IS HALF DEAD, AND SILENTLY SO. `fabrics`
	// reached the prompt while only `colour.fabric_media_id` — the FIRST cloth's texture, echoed by
	// the client — reached the provider, so a two-cloth submission sent one photograph and the
	// second cloth's line honestly reported «no photograph of this cloth was sent». Nothing lied;
	// the person simply did not get the thing they asked for. Measured: media 9 and 10 stated,
	// attachments [1 2 9].
	cloths := statedCloths(p.Colour)
	switch {
	case len(cloths) < 2:
		if p.Colour != nil {
			// ⚠ THE CAPTION SAYS MATERIAL, NOT COLOUR, AND THE CHANGE IS LOAD-BEARING. It read
			// «fabric swatch for the colour» while a recipe could only be ONE of photo / picker /
			// words. Now that all three may be given at once, that caption contradicts the order of
			// precedence the render craft states two blocks later: the photograph governs the
			// MATERIAL and loses the colour to the picker. A caption and a rule that disagree about
			// the same picture is worse than either alone — the model gets to choose which of our
			// two sentences it believes.
			// ⚠ A PATTERN TILE IS NOT A PHOTOGRAPH OF CLOTH, AND THE OLD CAPTION SAID IT WAS. «read
			// its weave, texture, sheen and drape from here» is an instruction about a piece of
			// material photographed under light; a generated seamless tile has no drape and no
			// sheen to read, and a model told to read them off it invents them. The tile's own
			// caption says what it IS and what to take from it, which is the motif and the colours.
			// Reached only when the frozen cloth states `kind: pattern` — see fabricUse.Kind.
			add(p.Colour.FabricMediaID, clothOneCaption(cloths), "")
		}
	default:
		// ONE ENTRY PER CLOTH, NUMBERED THE WAY THE CLOTH LIST NUMBERS THEM. Both walks are over
		// the same `statedCloths` slice in the same order, so «CLOTH 2» in the caption and «CLOTH
		// 2» in the craft block are the same cloth by construction rather than by coincidence —
		// and the craft block's own «its texture is image N» is resolved against this very list.
		//
		// THE SCALAR IS NOT ADDED IN THIS BRANCH. It is the client's echo of the first cloth, so
		// adding it too would find the id already present and APPEND its caption to cloth 1's —
		// leaving one picture described twice, once as «CLOTH 1» and once as «the material this
		// garment is made of», which is exactly the disagreement the caption above was rewritten to
		// end. A first cloth that somehow carries no texture of its own loses nothing: the scalar
		// it would have echoed is that same absent texture.
		for i, c := range cloths {
			add(c.MediaID, clothCaption(i+1, c), "")
		}
	}
	return out
}

// referenceMediaIDs is the picture half of referenceList — kept as a name because half the band's
// comments point at it, and DERIVED from the list rather than built beside it: two builders of
// «the run's pictures, in order» is how captions and urls came to disagree in the first place.
func referenceMediaIDs(kind string, p runParams, in runInputs) []int {
	list := referenceList(kind, p, in)
	out := make([]int, 0, len(list))
	for _, rc := range list {
		out = append(out, rc.MediaID)
	}
	return out
}

// slotCaption says what a bench plate is: the garment's own current state, and WHICH SIDE of it —
// the «фронт/бэк» mark the owner asked every picture to carry.
func slotCaption(s inputSlot) string {
	c := "current state of the garment — " + captionView(s.ViewKey)
	if name := strings.TrimSpace(s.DetailName); name != "" {
		c += " (" + name + ")"
	}
	return c
}

// refEntryCaption is the words a person put on one reference: the role they gave it, the note
// they wrote, the callouts they pinned. A reference with none of the three still gets words —
// see referenceList on why silence is not allowed.
// oneLine сплющивает человеческий текст в одну строку.
//
// ПОЧЕМУ ЭТО НЕ КОСМЕТИКА. Подписи референсов нумерованы (`- image 3: …`), и номер связывает
// подпись с картинкой. Записка или выноска, содержащая перевод строки, вписала бы в промпт СВОЮ
// строку — в том числе строку вида «- image 2: …», — и модель прочла бы её как подпись соседней
// картинки. Человек, пишущий заметку, не подозревает, что редактирует структуру промпта.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func refEntryCaption(r inputRef) string {
	line := strings.TrimSpace(oneLine(r.Role) + " — " + oneLine(r.Note))
	line = strings.Trim(line, "— ")
	var marks []string
	for _, c := range r.Callouts {
		if t := strings.TrimSpace(c.Text); t != "" {
			marks = append(marks, t)
		}
	}
	if len(marks) > 0 {
		line = strings.TrimSpace(line + " [" + strings.Join(marks, "; ") + "]")
	}
	if line == "" {
		return "reference image"
	}
	return line
}

// colourMapCaption says what a colour map IS, and it is the one sentence that makes the picture
// usable at all.
//
// ⚠ IT SAYS «NOT THE GARMENT'S COLOURS» OUT LOUD, AND THAT CLAUSE IS THE WHOLE CAPTION. Everything
// else in this request is a picture of the thing being made; this one is a picture ABOUT it, and a
// model that misses the distinction renders the labels. The garment's real colours are stated on
// the cloth list, and the caption points at it rather than restating them — two places saying what
// colour the garment is would be the same disagreement the order of authority exists to end.
func colourMapCaption(view string) string {
	return "colour map of the " + viewWord(view) + " flat — the same drawing with each part " +
		"flooded in one flat colour; those colours LABEL which cloth covers which part and are " +
		"not the garment's own colours, which the cloth list states"
}

// viewWord spells a view key as a bare adjective — «front», «left side» — where a caption needs it
// to sit inside a noun phrase («the front flat»).
//
// IT IS NOT captionView, AND THE TWO ARE NOT DUPLICATES. That one answers «what does this picture
// show» and ends in the noun («front view»); this one is the ADJECTIVE half, and «the front view
// flat» is not English. One function per grammatical role, both over the same dictionary.
func viewWord(v string) string {
	switch v {
	case entity.DesignViewFront:
		return "front"
	case entity.DesignViewBack:
		return "back"
	case entity.DesignViewSideL:
		return "left side"
	case entity.DesignViewSideR:
		return "right side"
	case entity.DesignViewThreeQuarterL:
		return "three-quarter from the left"
	case entity.DesignViewThreeQuarterR:
		return "three-quarter from the right"
	case entity.DesignViewDetail:
		return "detail"
	default:
		return v
	}
}

// captionView spells a slot's view key the way a caption reads it.
func captionView(v string) string {
	switch v {
	case entity.DesignViewFront:
		return "front view"
	case entity.DesignViewBack:
		return "back view"
	case entity.DesignViewSideL:
		return "left side view"
	case entity.DesignViewSideR:
		return "right side view"
	case entity.DesignViewThreeQuarterL:
		return "three-quarter view from the left"
	case entity.DesignViewThreeQuarterR:
		return "three-quarter view from the right"
	case entity.DesignViewDetail:
		return "detail view"
	default:
		return v
	}
}

// viewRank orders the silhouette so the front comes first: the four cardinal sides, then the two
// three-quarter views, then details. Anything unnamed sorts last.
func viewRank(v string) int {
	switch v {
	case entity.DesignViewFront:
		return 0
	case entity.DesignViewBack:
		return 1
	case entity.DesignViewSideL:
		return 2
	case entity.DesignViewSideR:
		return 3
	case entity.DesignViewThreeQuarterL:
		return 4
	case entity.DesignViewThreeQuarterR:
		return 5
	case entity.DesignViewDetail:
		return 6
	default:
		return 7
	}
}

// composePrompt turns the frozen snapshot into the words that go to the model.
//
// IT READS THE SNAPSHOT, NEVER THE CARD. The run may be picked up minutes or hours after it was
// launched, and by then the card's description, fit and references may all have moved. A prompt
// assembled from live data would make the history row a lie: it would say what was asked and the
// model would have been told something else.
//
// `attached` IS THE PICTURES ACTUALLY GOING OUT, in the order they go out — the survivors of
// buildJob's media resolution, not the snapshot's wish list. The caption block is numbered off
// this slice, so «image 3» in the words is images[2] on the wire BY CONSTRUCTION: both halves are
// read off the same element of the same slice, in one loop, in one place (buildJob). Composing
// from the pre-resolution list instead would let one unfetchable media row shift every caption
// after it onto the wrong picture — the exact defect the numbering exists to close. The words of
// a reference whose picture did not survive are dropped WITH the picture: a caption describing an
// image the model cannot see is an instruction about nothing.
func composePrompt(run entity.DesignRun, p runParams, in runInputs, attached []refCaption) string {
	var b strings.Builder
	write := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if label != "" {
			b.WriteString(label)
			b.WriteString(":\n")
		}
		b.WriteString(value)
	}

	// РОД, КОТОРЫЙ РИСУЕТ ДЕТАЛИ ПО ПРОСЬБЕ, РОВНО ОДИН НА КАЖДОМ МАРШРУТЕ: флэт и рендер. Он же
	// решает, писать ли блок имён и звать ли крафт — поэтому предикат считается ОДИН РАЗ и здесь.
	drawsDetails := run.Kind == entity.DesignRunKindFlat || renderIsTheKind(run.Kind)
	detailNames := requestedDetailNameList(p, in)

	if run.Ask.Valid {
		write("", run.Ask.String)
	}
	// A FLAT DETAIL RUN DRAWS THE DETAIL, NOT THE GARMENT (owner item 7). WORDS describe the whole
	// garment; under a bare «garment:» label the model takes them as the subject. They still go in —
	// the detail has to be drawn true to its garment — but labelled as context only.
	garmentLabel := "garment"
	if run.Kind == entity.DesignRunKindFlat && detailOnlyRun(p.Views) {
		garmentLabel = flatDetailGarmentLabel
	}
	write(garmentLabel, in.GarmentNote)
	write("fit", in.Fit)
	// КАКИЕ ИМЕННО ДЕТАЛИ ПРОСИЛИ. Без этой строки прогон на две детали говорил модели ровно
	// «нарисуй две детали» — и получал два произвольных крупных плана, потому что `views` несёт
	// ключ вида (`detail`), а ключ вида не различает воротник и карман. Имя берётся из ЗАМОРОЖЕННОГО
	// снимка слота, а не из карточки: деталь могли переименовать после запуска, и старый прогон
	// обязан читаться тем именем, с которым он был отправлен.
	//
	// ⚠ БЛОК ПРИНАДЛЕЖИТ ТОЛЬКО РИСУЮЩИМ РОДАМ, и раньше он стоял ДО switch'а, то есть писался
	// всем. 3D — это сборка в Meshy, а не картинка по этим словам; унаследовав `detail_slot_ids`
	// от родителя-флэта (реран переписывает параметры целиком), он получал в промпт сборки список
	// деталей, который сборке нечего делать. У Meshy при этом есть собственный ErrPromptTooLong,
	// то есть лишние слова там не бесплатны. Вектор перерисовывает утверждённый растр и тоже не
	// рисует ничего по просьбе.
	if drawsDetails {
		write("draw these details", requestedDetailNames(p, in))
	}

	// ─── COLOUR AND WORDS ARE TWO BLOCKS, NOT ONE COMMA-JOINED LINE.
	//
	// They used to be one ("olive, colourway OLV-03, #4a5a3c"), which was harmless while nothing
	// downstream had to tell them apart. The render route now does: the owner allows a fabric
	// photograph, a picked colour and a free description to be given TOGETHER, so the craft block
	// has to rank them — and a rule cannot point at "the stated colour" and "the words" separately
	// if the prompt has already glued them into one sentence. Split here rather than in the render
	// craft, because it is composePrompt that owns the shape of the human context and a second
	// writer of the same fields is how two readers come to disagree.
	//
	// ON A RECOLOUR THE WORDS ARE LABELLED «colour in words» (20-PROMPTS §3.2). Tile 4 sends the
	// Pantone's NAME there («Classic Blue»), and under «fabric in words» a model reads a colour name
	// as a note about the cloth — or, worse, as licence to change the cloth. The render and pattern
	// routes keep «fabric in words»: their crafts name that label in their own text (renderprompt's
	// order of authority), and one kind check here cannot move a label a paragraph points at.
	if c := p.Colour; c != nil {
		write("colour", colourStatement(c))
		wordsLabel := "fabric in words"
		if run.Kind == entity.DesignRunKindRecolor {
			wordsLabel = "colour in words"
		}
		// A hardware run (a zip, a button) is not cloth: «fabric in words» would introduce the item's
		// description as a note about a fabric. hardwareCraft points at «the words above» generically.
		if run.Kind == entity.DesignRunKindPattern && p.Pattern != nil {
			switch p.Pattern.Mode {
			case entity.DesignPatternModeHardware:
				wordsLabel = "item in words"
			case entity.DesignPatternModeLabel:
				wordsLabel = "label in words"
			}
		}
		write(wordsLabel, c.Words)
	}
	if t := p.Threed; t != nil {
		var parts []string
		if t.Presentation != "" {
			parts = append(parts, "presentation "+t.Presentation)
		}
		// ТЕЛО НАЗЫВАЕТСЯ СРАЗУ ЗА ПОДАЧЕЙ, потому что это её уточнение: «on a model» отвечает на
		// «есть ли фигура», телосложение — на «какая». Пустое молчит, и молчание здесь правдиво:
		// контракт говорит «Empty = not stated; the generator picks».
		if t.BodyType != "" {
			parts = append(parts, "body "+t.BodyType)
		}
		if t.FitOverride != "" {
			parts = append(parts, "fit "+t.FitOverride)
		}
		write("turntable", strings.Join(parts, ", "))
	}

	// The references, EACH TIED TO ITS PICTURE BY NUMBER. «image k:» counts through the pictures
	// in the order they are attached (see the contract on `attached` above), so the model — and a
	// person reading the stored prompt — can say which words describe which image instead of
	// guessing. This is W-7's «our prompt: the pictures, the descriptions and the markup», with
	// the binding between the first two finally said out loud.
	var refLines []string
	for i, rc := range attached {
		refLines = append(refLines, "- image "+strconv.Itoa(i+1)+": "+rc.Caption)
	}
	write("references", strings.Join(refLines, "\n"))

	// A fix names what it is fixing. Both spellings are read — the frozen scalar of an older run
	// and the list a new one uses — because the old meaning has to stay readable forever.
	targets := p.FixTargets
	if len(targets) == 0 && p.FixTarget != "" {
		targets = []string{p.FixTarget}
	}
	if len(targets) > 0 {
		write("correct these views", strings.Join(targets, ", "))
	}

	// ─── THE CRAFT, LAST: flatprompt.go on the flat route, renderprompt.go on the render one.
	//
	// LAST IS A DECISION, NOT AN ACCIDENT. The human context above legitimately carries colour
	// words (a colourway recipe), callout texts and free-form notes — and a flat must stay black
	// line art even when they say "olive". Where the two collide, the words closer to the end of
	// the prompt are the ones an image model obeys, so the craft — whose exclusion list IS the
	// non-negotiable half — speaks after every human word, never before. The reverse order would
	// end the prompt on "colour: olive" and hand the collision to the wrong side. It also keeps
	// the owner's own opening literal: "Turn the garment shown in the reference image…" is a
	// sentence written to operate on material already given, and here everything it operates on
	// (the ask, the garment, the fit, the references and their markup) has already been said.
	//
	// ON THE RENDER ROUTE THE SAME POSITION CARRIES A SECOND, SHARPER LOAD. The render craft is
	// where the ORDER OF PRECEDENCE over the fabric is written, and that rule exists precisely to
	// settle a disagreement between things said ABOVE it — the `colour` block, the `fabric in
	// words` block and the swatch's own caption. A rule that arbitrates between earlier sentences
	// has to come after all of them, or it arbitrates over half its subject.
	//
	// ONE CRAFT PER ROUTE, AND NEVER TWO. The flat block is the owner's reference wording for
	// technical drawings; the render block (renderprompt.go) is the craft of a photograph of cloth.
	// They contradict each other on purpose — "black vector line art, strictly excluded: shading,
	// fabric texture" against "photorealistic, the weave must read" — so a run that took both would
	// end on whichever paragraph happened to be written last.
	//
	// 3D IS A MODEL BUILD AND draft_idea NEVER REACHES THE WORKER. Neither is a picture composed by
	// these words, so both keep the bare human context above and take no craft block at all.
	switch {
	case run.Kind == entity.DesignRunKindFlat:
		write("", flatCraft(p, detailNames, len(attached)))
	case renderIsTheKind(run.Kind):
		write("", renderCraft(p, detailNames, attached))
	// ПЕРЕКРАС И ПАТТЕРН — ЕЩЁ ДВА РЕМЕСЛА, И КАЖДОЕ ПРОТИВОРЕЧИТ ОБОИМ СОСЕДНИМ. Рендер СОЧИНЯЕТ
	// сцену, перекрас обязан её НЕ ТРОГАТЬ; флэт рисует чёрную линию на белом, паттерн — сплошное
	// поле цвета без единого поля вокруг. Поэтому абзац ровно один на прогон, как и у первых двух:
	// прогон, взявший два, закончился бы тем, который случайно написан ниже.
	case run.Kind == entity.DesignRunKindRecolor:
		write("", recolorCraft(p))
	case run.Kind == entity.DesignRunKindPattern:
		pp := patternParams{}
		if p.Pattern != nil {
			pp = *p.Pattern
		}
		// СКОЛЬКО КАРТИНОК РЕАЛЬНО УЕЗЖАЕТ — из `attached`, а не из снимка: абзац свотча говорит
		// модели либо «фактура — с картинки», либо «картинки нет, сделай гладкую ткань», и сказать
		// первое о картинке, которая не пережила резолв, значило бы дать указание ни о чём.
		write("", patternCraft(pp, len(attached)))
	// ПЛЕЙГРАУНД — ПЯТОЕ РЕМЕСЛО, И ОНО ПРОТИВОРЕЧИТ ВСЕМ ЧЕТЫРЁМ ОСТАЛЬНЫМ РОВНО ТЕМ, ЧЕГО НЕ
	// ГОВОРИТ. Ни «чёрная линия на белом», ни «фотореалистично», ни «верни тот же кадр»: человек
	// сказал сам, а абзац объясняет модели ТОЛЬКО устройство вложений — что обведено, где кроп и
	// что контур это метка, а не часть вещи. Место то же самое, последнее, и по тому же доводу:
	// слова ближе к концу промпта — те, которым модель подчиняется.
	case run.Kind == entity.DesignRunKindFreeform:
		write("", freeformCraft(p, attached))
	}
	return b.String()
}

// surfaceSteer is the words a 3D route sends, and the ONLY words it sends: what the SURFACE of
// this garment is made of and how the turntable is presented.
//
// ⚠ WHAT IS ABSENT IS THE POINT OF THE FUNCTION. The ask, the garment note, the fit and the
// numbered reference captions are all deliberately left out. `texture_prompt` is a hint to the
// TEXTURING stage, which paints a surface it cannot locate: told «crossed straps on the back» it
// stamps straps wherever it happens to be painting, and told «- image 3: right side view» it is
// handed a numbering protocol it has no images to attach to. The shape of the garment is carried
// by the plates — four approved pictures — and the only thing words can add is what the cloth is.
//
// IT IS COMPOSED FROM THE FROZEN SNAPSHOT, LIKE EVERY OTHER WORD THIS PACKAGE SENDS, so a run that
// waits an hour in the queue still says what it was launched with.
//
// THE CLOTH LIST STARTS AT THE SECOND CLOTH, and the first one's absence is the rule rather than an
// omission. The scalars just written ARE cloth one — the contract on colourRecipe.Fabrics says the
// client repeats the first cloth's colour into `code`/`hex` and its words into `words` — so a loop
// over the whole list said «colourway BLK» and «matte heavy jersey» twice in a row, measured on
// this function's own two-cloth fixture. The composed prompt can afford that repetition because it
// LABELS it (renderClothFirstIsTheScalar); a hint of a few dozen words cannot, and an unlabelled
// repetition inside a hint reads as two cloths that happen to match.
//
// WHAT THAT GIVES UP IS CLOTH ONE'S `parts`, DELIBERATELY. The scalars are the garment's stated
// surface and the lines below them are the EXCEPTIONS to it — which is exactly how `parts` reads on
// the remaining cloths, whose contract calls an empty `parts` «the whole garment, or the remainder».
// Naming cloth one's region as well would state the same surface twice, once as the base and once
// as a region, and leave the texturing stage to reconcile them.
//
// ⚠ THE CEILING IS ENFORCED HERE, BY CONSTRUCTION, AND THE PREVIOUS ARGUMENT FOR NOT ENFORCING IT
// WAS MEASURABLY WRONG. It read: «nothing is trimmed here, meshy.Submit refuses above
// meshy.MaxTexturePrompt locally… affordable now that the steer is a colour phrase and a cloth
// line». The steer is not bounded by being short in the ordinary case. Nothing in this band bounds
// `colour.words`, `fabrics[].words`, `fabrics[].parts` or the number of cloths: a 660-character
// `colour.words` composes a 703-rune steer, and an eight-cloth recipe composes 1262, against a
// ceiling of 600. Both outcomes were TERMINAL — the direct meshy route refuses locally, the fal
// meshy route takes a 422 — so 3D was permanently dead for that colourway, with an error naming
// `texture_prompt`, a field nobody on the bench has ever heard of.
//
// WHY BOUNDING IS RIGHT HERE AND CUTTING WAS WRONG BEFORE, WHICH IS NOT THE SAME QUESTION. The band
// rule «a ceiling refuses, it does not trim quietly» protects an ORDER a person gave: a trimmed
// order is a claim nobody made. This is not an order. It is a hint this package composes ITSELF,
// for one field, out of a snapshot whose order travels elsewhere in full — as Job.Prompt, and as
// four approved plates. And the trim is not quiet in the way the old one was: the recorded prompt
// column is now filled from what the route actually sends (PromptCarrier), so the bounded text is
// the text a person reads in the run panel. The old textureSteer cut the whole prompt and stored
// the UNCUT one; that is what made its cut a lie rather than a bound.
func surfaceSteer(ctx context.Context, p runParams) string {
	var parts []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	if c := p.Colour; c != nil {
		add(colourStatement(c))
		add(oneLine(c.Words))
		if cloths := statedCloths(c); len(cloths) > 1 {
			for _, f := range cloths[1:] {
				add(clothSteerLine(f))
			}
		}
	}
	if t := p.Threed; t != nil {
		// PRESENTATION AND NOTHING ELSE OUT OF THE 3D PARAMS. `air` and `model` change what the
		// surface has to look like — cloth hanging on nothing reads differently from cloth on a
		// body. Body type and the fit override do not: they are the SHAPE of the thing, which the
		// plates already carry, and asking a texturing stage for a body is asking it for the one
		// thing it cannot give.
		//
		// ⚠ AN UNSTATED PRESENTATION SAYS NOTHING, NOT «presentation». The contract calls the empty
		// value «not stated; the generator picks», and the bare label would be a word the texturing
		// stage has to interpret — the one thing a hint must never be.
		if pres := oneLine(t.Presentation); pres != "" {
			add("presentation " + pres)
		}
		// The person's own surface words (phase 2). Last, so the same bound applies to them.
		if hint := oneLine(t.SurfaceHint); hint != "" {
			add("surface: " + hint)
		}
	}
	steer, dropped := joinSteer(parts)
	if dropped > 0 {
		// LOUD, EVERY TIME, because the alternative to a loud bound is a silent one. The run still
		// goes out — a hint one phrase short still describes the same cloth — but «the provider was
		// told less than the run says» is a fact an operator has to be able to find, and the knob
		// that ends it is a shorter colour description on the colourway.
		slog.Default().WarnContext(ctx, "3D: the surface steer reached the provider's ceiling and "+
			"the tail of it was left off",
			slog.Int("ceiling_runes", steerCeiling), slog.Int("sent_runes", len([]rune(steer))),
			slog.Int("parts_lost", dropped))
	}
	return steer
}

// maxTexturePrompt — the fal-hosted Meshy family's texture_prompt ceiling, in runes: a longer prompt
// is answered with a 422.
const maxTexturePrompt = 600

// steerCeiling is the number of runes a surface hint may carry, and it is the PROVIDER'S number
// rather than a taste of ours: the meshy family reached through fal answers a longer one with a 422,
// a terminal refusal, so this is the one place that can keep it unreachable.
const steerCeiling = maxTexturePrompt

// steerMinPhrase is how much room a part needs before it is worth sending in part. Below it the
// remainder is not a phrase but a fragment — «matte heavy jer» — and a fragment in a hint is worse
// than the absence of one, because the texturing stage has no way to know it was cut.
const steerMinPhrase = 24

// joinSteer joins the parts with «; » and stops at steerCeiling, answering with how many parts did
// not travel whole.
//
// AT MOST ONE PART IS EVER CUT, AND IT IS THE LAST THING WRITTEN. A cut means the ceiling has been
// reached, so there is nothing to put after it; skipping a long part to fit a short one behind it
// would silently reorder a list whose order is its priority.
func joinSteer(parts []string) (string, int) {
	var b strings.Builder
	n := 0
	for i, s := range parts {
		sep := 0
		if n > 0 {
			sep = 2
		}
		r := []rune(s)
		if n+sep+len(r) <= steerCeiling {
			if sep > 0 {
				b.WriteString("; ")
			}
			b.WriteString(s)
			n += sep + len(r)
			continue
		}
		if room := steerCeiling - n - sep; room >= steerMinPhrase {
			// THE ROOM AND THE RESULT ARE BOTH MEASURED, because a part whose only space sits near
			// its start leaves a one-word stub inside a perfectly roomy budget — «a», where the
			// rule above promises a phrase.
			if cut := cutAtWord(r, room); len([]rune(cut)) >= steerMinPhrase {
				if sep > 0 {
					b.WriteString("; ")
				}
				b.WriteString(cut)
			}
		}
		return b.String(), len(parts) - i
	}
	return b.String(), 0
}

// cutAtWord takes at most `room` runes and gives back the last WHOLE word inside them.
//
// ⚠ NO WHITESPACE IN THE PREFIX MEANS NOTHING COMES BACK, AND THAT IS THE CONTRACT RATHER THAN AN
// EDGE CASE. `colour.words` is free text: a person who pastes 660 characters without a space —
// a url, a pasted hex list, a language that does not space its words — used to have it sliced at
// the rune budget, and what reached the texturing stage was A TOKEN THAT DOES NOT EXIST, invented
// by us, indistinguishable to the provider from a word the person actually wrote. A hint that
// says nothing is honest; a hint that says a word nobody typed is not. So the part is dropped
// whole, joinSteer counts it as lost, and surfaceSteer warns.
func cutAtWord(r []rune, room int) string {
	if room <= 0 {
		return ""
	}
	if len(r) <= room {
		return strings.TrimSpace(string(r))
	}
	cut := string(r[:room])
	at := strings.LastIndexAny(cut, " \t")
	if at <= 0 {
		return ""
	}
	// A phrase must not end on the punctuation that was joining it to the words that were dropped.
	return strings.TrimRight(strings.TrimSpace(cut[:at]), " ,;:-—")
}

// clothSteerLine is ONE cloth of a multi-cloth run BEYOND THE FIRST, in as few words as still
// identify it: which parts it is for, what colour it is and what it looks like. Cloth one never
// reaches here — the scalars are already its echo; see surfaceSteer.
//
// THE PARTS COME FIRST BECAUSE THEY ARE WHAT MAKES THE REST ADDRESSABLE. «contrast rib, red» tells
// the texturing stage nothing it can act on; «cuffs and collar: contrast rib, red» does. An empty
// `parts` is legal and means the whole garment (or the remainder) — see the contract on fabricUse —
// so it is left off rather than called unknown.
func clothSteerLine(f fabricUse) string {
	var bits []string
	for _, s := range []string{oneLine(f.Name), colourPhrase(f.ColourCode, f.ColourHex), oneLine(f.Words)} {
		if s = strings.TrimSpace(s); s != "" {
			bits = append(bits, s)
		}
	}
	body := strings.Join(bits, ", ")
	if body == "" {
		return ""
	}
	if parts := oneLine(f.Parts); parts != "" {
		return parts + ": " + body
	}
	return body
}

// viewPrompt is the per-call instruction on the per_view route, where each paid call is made for
// one named side and the model must be told which.
func viewPrompt(base, view string) string {
	if strings.TrimSpace(view) == "" {
		return base
	}
	return strings.TrimSpace(base + "\n\nview:\n" + view)
}

// viewCallLabels — ЧТО ИМЕННО ДОПИСЫВАЕТСЯ К БАЗОВОМУ ПРОМПТУ НА КАЖДОМ ПЛАТНОМ ВЫЗОВЕ per_view,
// позиционно по views.
//
// ЧТО БЫЛО СЛОМАНО, И ЭТО САМЫЙ ДОРОГОЙ ИЗ ДЕФЕКТОВ ВОЛНЫ. `per_view` — умолчание формы, то есть
// основной маршрут. Прогон на две детали давал ДВА вызова, у которых промпт совпадал ПОБАЙТОВО:
// дописывался ключ вида (`detail`), а ключ вида не различает воротник и карман. Два списания за
// два одинаковых запроса, и какая из вернувшихся картинок воротник — не знал никто.
//
// СЧЁТЧИК ИДЁТ ПО `detail` В views, А НЕ ПО ИНДЕКСУ КАДРА, потому что позиционное соответствие
// объявлено именно так: i-й `detail` в views ↔ i-й адрес в detail_slot_ids. Кадры силуэта в этом
// счёте не участвуют, и «detail, front, detail» обязан отдать первой детали первое имя, а второй —
// второе, а не первое и третье.
//
// НЕИЗВЕСТНОЕ ИМЯ ОСТАВЛЯЕТ ГОЛЫЙ КЛЮЧ ВИДА — то же молчание, что и в блоке «draw these details»:
// это ровно столько, сколько знает старый снимок, и меньше лжи, чем догадка.
func viewCallLabels(views, detailNames []string) []string {
	out := make([]string, 0, len(views))
	seen := 0
	for _, v := range views {
		label := v
		if v == entity.DesignViewDetail {
			if n := detailNameAt(detailNames, seen); n != "" {
				label = v + " — " + n
			}
			seen++
		}
		out = append(out, label)
	}
	return out
}

// buildJob assembles everything a provider needs out of one claimed run, resolving media ids into
// the public urls the provider will fetch itself.
//
// URLS RATHER THAN BYTES, DELIBERATELY. Our design pictures already live in a public bucket, so
// the provider downloads them directly and nothing passes through this process — which has half a
// gigabyte of RAM and a base64 image is the thing most likely to end it.
// ⚠ `objects` НУЖЕН РОВНО ОДНОМУ РОДУ, И ОН ОБЯЗАТЕЛЕН ИМЕННО ТАМ. Плейграунд с размеченной
// областью отправляет модели ПРОИЗВОДНЫЕ картинки — обведённую копию и кроп, — а сделать их можно
// только из БАЙТОВ исходника (freeform_derive.go). Прогон без хранилища не «уедет чуть беднее»: он
// уедет с областями, которых модель не увидит, и человек заплатит за кадр, в котором его разметка
// не участвовала. Поэтому nil здесь — ошибка сборки, а не тихая деградация; всем прочим родам
// хранилище не нужно вовсе, и они принимают nil.
func buildJob(ctx context.Context, media mediaResolver, objects objectFetcher, run entity.DesignRun, quality string) (Job, error) {
	return buildJobWith(ctx, media, objects, run, quality, EngineTable(""))
}

// buildJobWith is buildJob with the deployment's engine table (Worker.engines: Config.Engines, B-13),
// which resolves a frozen params.image into the job's engine fields.
func buildJobWith(ctx context.Context, media mediaResolver, objects objectFetcher, run entity.DesignRun, quality string, engines []Engine) (Job, error) {
	p := parseParams(run.Params)
	in := parseInputs(run.Inputs)

	job := Job{
		RunID:      run.Id,
		TechCardID: run.TechCardId,
		Kind:       run.Kind,
		Views:      p.Views,
		Layout:     p.Layout,
		// РЕЗОЛВИТСЯ ЗДЕСЬ, А НЕ У ВЫЗОВА, потому что имя живёт в ЗАМОРОЖЕННОМ снимке, а снимок
		// дальше этой функции не едет. Провайдеру достаётся уже разрешённый позиционный список.
		DetailNames: requestedDetailNameList(p, in),
		Outputs:     run.RequestedOutputs,
		Quality:     quality,
	}
	// РЕЖИМ ПАТТЕРНА ЕДЕТ В ЗАДАНИЕ, ПОТОМУ ЧТО ЕГО ЧИТАЕТ ДЕНЕЖНАЯ ГРАНИЦА (imageCalls), а снимок
	// дальше этой функции не едет. Нет блока pattern — пустой режим, то есть сегодняшний маршрут.
	if p.Pattern != nil {
		job.PatternMode = p.Pattern.Mode
	}
	// The preset travels for the money boundary: imageCalls lets a zero-picture `free` run through.
	if p.Freeform != nil {
		job.FreeformPreset = p.Freeform.Preset
	}
	// The per-run engine (phase 2). No block = today's configured slug and QualityFor's word.
	applyImageOptions(&job, p.Image, engines)
	// The 3D build options travel for the route AND for the money: the fal collect books a detailed
	// build at its own tier (B-09).
	if run.Kind == entity.DesignRunKindThreed {
		o := threedOptionsOf(p)
		job.ThreedTexture, job.ThreedPBR, job.ThreedQuality = o.Texture, o.PBR, o.Quality
		job.ThreedSurfaceHint = threedSurfaceHintOf(p)
		if run.PriceEstimate.Valid {
			n := run.RequestedOutputs
			if n < 1 {
				n = 1
			}
			job.ThreedReservedUSD = decimal.NullDecimal{
				Decimal: run.PriceEstimate.Decimal.Div(decimal.NewFromInt(int64(n))), Valid: true,
			}
		}
	}

	if (run.Kind == entity.DesignRunKindExtend || run.Kind == entity.DesignRunKindInpaint ||
		run.Kind == entity.DesignRunKindVideo) && run.PriceEstimate.Valid {
		job.RouteReservedUSD = run.PriceEstimate
	}
	// The video run's frozen ask (B-32): the slug the door froze and the length. The aspect ratio is
	// read off the SOURCE PICTURE's media row below, once it is resolved — it is a property of the
	// picture, not of the params.
	if run.Kind == entity.DesignRunKindVideo && p.Video != nil {
		job.VideoModel = strings.TrimSpace(p.Video.Model)
		job.VideoDuration = p.Video.Duration
	}

	// ─── RESOLUTION FIRST, WORDS SECOND. The prompt's caption block is numbered off the pictures
	// that actually attach, so the media has to be resolved BEFORE the prompt is composed. Both
	// halves of each pair — the url and the caption — are appended by the SAME iteration of the
	// SAME loop, off the SAME element: that, and nothing subtler, is what guarantees that caption
	// k describes references[k-1]. TestCaptionNumberKIsImageNumberK holds the guarantee.
	list := referenceList(run.Kind, p, in)
	// ⚠ ТКАНИ ОТБИРАЮТСЯ ИЗ ПОЛНОГО СПИСКА И ДО ЕГО СУЖЕНИЯ. sourcePictures оставляет только
	// названные фотографии — то есть выбрасывает и плитку, — поэтому «сначала сузить, потом искать
	// ткани» дало бы пустоту всегда, и починка J-31 была бы зелёной и мёртвой.
	var cloths []refCaption
	switch run.Kind {
	case entity.DesignRunKindRecolor:
		// ПЕРЕКРАС ДЕЙСТВУЕТ НА НАЗВАННЫЕ СНИМКИ — и, с J-31, ОДЕВАЕТ их в названную ткань.
		// Фотографии остаются в References (по ним считаются платные вызовы и requested_outputs);
		// ткань уезжает отдельным списком и попадает ВТОРОЙ КАРТИНКОЙ В КАЖДЫЙ вызов, см. images.go.
		cloths = clothPictures(list, p)
		list = sourcePictures(list, p)
	case entity.DesignRunKindPattern:
		// ПАТТЕРН ДЕЙСТВУЕТ НА ОДНУ НАЗВАННУЮ КАРТИНКУ, И ТОЛЬКО НА НЕЁ. Довод целиком в
		// source_inputs.go: плитку, собранную из двух лоскутов, невозможно состыковать саму с
		// собой. Тканей у этого рода нет по построению — images.go отказывает всему, что не ровно
		// одна ссылка. У СВОТЧА (STEP 3) названная картинка — фактура, и их ноль или одна: тот же
		// отбор, другое число у денежной границы.
		list = sourcePictures(list, p)
	}
	if run.Kind == entity.DesignRunKindThreed {
		// У СБОРКИ 3D КАРТИНКИ — ЭТО ЕЁ ПЛИТЫ, И ТОЛЬКО ОНИ (V-14). Довод целиком в
		// threed_inputs.go: Meshy читает КАЖДУЮ присланную картинку как ВИД одного предмета и
		// принимает их 1..4, поэтому референс карточки здесь либо убивает прогон отказом по числу,
		// либо — что тише и дороже — сам становится «видом», и модель строится по чужой одежде.
		list = threedPicturesOf(list, in, p)
	}
	var attached []refCaption
	// The mask of a retouch rides the SAME media batch as the picture (one moment in time: a row that
	// vanished between two reads would give a job whose picture resolved and whose mask did not).
	maskID, maskURL := 0, ""
	if run.Kind == entity.DesignRunKindInpaint && p.Inpaint != nil {
		maskID = p.Inpaint.MaskMediaID
	}
	if len(list) > 0 || len(cloths) > 0 {
		// ОДИН ПОХОД В МЕДИА НА ОБА СПИСКА. Два запроса были бы двумя моментами времени: строка,
		// исчезнувшая между ними, отдала бы задание, у которого ткань разрешилась, а фотография нет.
		ids := make([]int, 0, len(list)+len(cloths))
		for _, rc := range list {
			ids = append(ids, rc.MediaID)
		}
		for _, rc := range cloths {
			ids = append(ids, rc.MediaID)
		}
		if maskID > 0 {
			ids = append(ids, maskID)
		}
		byID, err := media.GetMediaByIds(ctx, ids)
		if err != nil {
			return Job{}, fmt.Errorf("failed to resolve the input media of design run %d: %w", run.Id, err)
		}
		if m, ok := byID[maskID]; ok && maskID > 0 {
			maskURL = strings.TrimSpace(m.FullSizeMediaURL)
		}
		// THE CLIP'S SHAPE IS THE PICTURE'S SHAPE (B-32): Kling takes 16:9 | 9:16 | 1:1 and would
		// otherwise default to 16:9, cropping a portrait render to a landscape. The nearest of the
		// three, from the media row's stored full-size dimensions; a row that states none (a legacy
		// 0×0) sends no ratio and the provider's default stands.
		if run.Kind == entity.DesignRunKindVideo && p.Video != nil {
			if m, ok := byID[p.Video.SourceMediaID]; ok {
				job.VideoAspectRatio = nearestVideoAspect(m.FullSizeWidth, m.FullSizeHeight)
			}
		}
		resolve := func(rc refCaption) (string, bool) {
			m, ok := byID[rc.MediaID]
			if !ok {
				// The row went away between the snapshot and the pass. Skipping is right: the id
				// cannot be fetched by the provider either, and refusing the whole run over one
				// missing reference would throw away a job whose remaining inputs are intact. The
				// caption is skipped WITH the picture — see composePrompt on why.
				return "", false
			}
			u := strings.TrimSpace(m.FullSizeMediaURL)
			if u == "" {
				return "", false
			}
			return u, true
		}
		for _, rc := range list {
			u, ok := resolve(rc)
			if !ok {
				continue
			}
			// THE THREE HALVES MOVE TOGETHER, IN ONE APPEND, off one element — the url, the side
			// it shows, and the caption that describes it. That, and nothing subtler, is what makes
			// References[k], ReferenceViews[k] and caption k+1 the same picture.
			job.References = append(job.References, u)
			job.ReferenceViews = append(job.ReferenceViews, rc.View)
			attached = append(attached, rc)
		}
		var attachedCloths []refCaption
		for _, rc := range cloths {
			u, ok := resolve(rc)
			if !ok {
				continue
			}
			job.ClothReferences = append(job.ClothReferences, u)
			attachedCloths = append(attachedCloths, rc)
		}
		if run.Kind == entity.DesignRunKindRecolor {
			attached = recolorAttached(len(job.References), attachedCloths)
		}
	}
	// ─── ПРОИЗВОДНЫЕ ПЛЕЙГРАУНДА: ОБВЕДЁННАЯ КОПИЯ И КРОП ОБЛАСТИ ───
	//
	// ⚠ СТОИТ МЕЖДУ РЕЗОЛВОМ И ПРОМПТОМ, И ДРУГОГО МЕСТА У НЕГО НЕТ. Производные — это НАСТОЯЩИЕ
	// картинки вызова, они занимают номера в том же счёте, что и остальные, и подписи к ним обязаны
	// попасть в тот же блок «references». Собранные позже, они уехали бы к модели без единого
	// слова о том, что это такое, — то есть обведённая копия читалась бы как ещё одна фотография
	// вещи с красными линиями НА НЕЙ.
	//
	// СТРОКИ МЕДИА НЕ МИНТУЮТСЯ: производные едут data-URI и живут ровно один вызов. Минт дал бы
	// сирот при каждом отказе и компенсацию, которую пришлось бы писать; а воспроизвести их можно
	// в любой момент — области заморожены в params, исходник адресуется media_id.
	if run.Kind == entity.DesignRunKindFreeform {
		// ─── ПРЕДПОСЫЛКИ ПРЕСЕТА ПЕРЕСПРАШИВАЮТСЯ У ВЫЖИВШИХ КАРТИНОК, И ЭТО НЕ ПОВТОР ДВЕРИ ───
		//
		// Стоит ПЕРЕД окном и перед производными: обе они уже перестраивают `attached`, а вопрос
		// здесь про то, что действительно доехало.
		if err := freeformPrerequisitesSurvived(p, attached); err != nil {
			return Job{}, err
		}
		// ─── ОКНО ГЕНЕРАЦИИ. Если этот прогон берёт окно (add_hardware по одной области), кадр
		// целиком к модели НЕ ЕДЕТ ВОВСЕ: она увидит только кроп, а кадром ответ станет после
		// вызова, вклейкой по замороженным координатам. Полный кадр рядом с кропом сделал бы всю
		// затею бессмысленной — модель ответила бы на него, то есть пересоздала бы фотографию
		// ради пуговицы.
		if plan := freeformWindowPlan(p); plan != nil {
			d, win, err := deriveFreeformWindow(ctx, objects, *plan, &job, attached)
			if err != nil {
				return Job{}, err
			}
			if win != nil {
				attached = d
				job.Window = win
				// ⚠ THE CROP DECIDES THE SHAPE, NOT params.image.aspect_ratio (G-02, Codex 6). The
				// answer is scaled straight into the frozen rectangle (compositeWindow), so a stated
				// ratio would buy a picture of another shape and squeeze it into the crop. The door
				// refuses an explicit ratio on a windowed run; this is the second lock, for a run
				// frozen before that door: no ratio is sent, and the provider answers the crop.
				job.AspectRatio = ""
			}
		}
		derived, err := deriveFreeform(ctx, objects, p, attached, job.References)
		if err != nil {
			return Job{}, err
		}
		for _, d := range derived {
			job.References = append(job.References, d.dataURI)
			job.ReferenceViews = append(job.ReferenceViews, "")
			// MediaID НУЛЕВОЙ НАМЕРЕННО: у этой картинки нет строки media и не должно быть.
			// Единственный читатель id в подписях — renderCraft (imageNumberOf), а плейграунд
			// берёт другое ремесло, так что ноль здесь ни на что не может указать неверно.
			attached = append(attached, refCaption{Caption: d.caption})
		}
	}
	// ─── EXTEND (phase 3): THE PLAN IS FROZEN HERE, BEFORE THE MONEY. The source is decoded once to
	// learn its size, the canvas and the per-side expansion are computed, the 3 MP cap applied — and
	// a refusal here (the picture is gone, too small, or the target adds nothing) is free and
	// terminal: buildJob runs before StartAttempt.
	if run.Kind == entity.DesignRunKindExtend {
		if err := deriveExtendPlan(ctx, objects, p, &job); err != nil {
			return Job{}, err
		}
	}
	// ─── INPAINT (phase 3): the picture and its mask are read, the mask checked (same size, something
	// painted), both cut to the same padded crop — before the money, so every refusal is free.
	if run.Kind == entity.DesignRunKindInpaint {
		if err := deriveInpaintPlan(ctx, objects, maskURL, &job); err != nil {
			return Job{}, err
		}
	}
	switch run.Kind {
	case entity.DesignRunKindInpaint, entity.DesignRunKindVideo:
		// ⚠ composePrompt IS BYPASSED FOR THESE KINDS, ON PURPOSE. The fill model takes plain words
		// about the painted zone, Kling takes plain words about the motion; a craft paragraph or a
		// caption block would be text about pictures they are not shown. The prompt is the ask,
		// verbatim — and the history column records exactly this (recordedPrompt → job.Prompt).
		job.Prompt = strings.TrimSpace(run.Ask.String)
	default:
		job.Prompt = composePrompt(run, p, in, attached)
	}
	// COMPOSED FOR EVERY KIND, USED BY ONE. Deriving it here rather than inside the 3D route keeps
	// every word this package sends coming out of the same reader of the same frozen snapshot; a
	// route that composed its own text would be a second composer to keep in step.
	job.SurfaceSteer = surfaceSteer(ctx, p)
	return job, nil
}

// errFreeformSourceGone — КАРТИНКА, БЕЗ КОТОРОЙ ЭТОТ ПРЕСЕТ НЕ ИСПОЛНИМ, НЕ ДОЕХАЛА, И ОТКАЗ
// БЕСПЛАТНЫЙ.
//
// ⚠ ТЕРМИНАЛЬНЫЙ. Строку медиа удалили; следующий проход соберёт то же задание из того же
// замороженного снимка и снова её не найдёт — то есть повтор покупает пять одинаковых отказов.
var errFreeformSourceGone = errors.New("designgen: a picture this playground run needs is gone")

// freeformPrerequisitesSurvived ПЕРЕСПРАШИВАЕТ ПРЕДПОСЫЛКИ ПРЕСЕТА У КАРТИНОК, КОТОРЫЕ ДЕЙСТВИТЕЛЬНО
// ДОЕХАЛИ.
//
// ⚠ ЭТО НЕ ВТОРАЯ КОПИЯ ДВЕРИ, А ТОТ ЖЕ ВОПРОС ДРУГОМУ МНОЖЕСТВУ, И РАСХОЖДЕНИЕ СТОИЛО ДЕНЕГ.
// Дверь (designRefuseUnworkableSources) спрашивает ПАРАМЕТРЫ: «названа ли картинка фурнитуры,
// размечена ли область». Здесь спрашиваются ВЫЖИВШИЕ строки медиа — те, что пережили резолв. Между
// дверью и проходом стоит время: картинку законно удаляют, и `resolve` пропускает пропавшую
// МОЛЧА — правильно для рода, где каждая ссылка сама по себе, и разрушительно для пресета, у
// которого ссылки СВЯЗАНЫ. Без этой проверки `add_hardware`, потерявший фотографию фурнитуры,
// уезжал провайдеру абзацем «возьми фурнитуру с картинки …» БЕЗ ЕДИНОЙ картинки фурнитуры: модель
// отвечает правдоподобным кадром, деньги списаны, а в истории такой прогон неотличим от честного.
//
// ⚠ И ОТКАЗ ЗДЕСЬ БЕСПЛАТНЫЙ РОВНО ПОТОМУ, ЧТО ОН ЗДЕСЬ. buildJob зовётся до StartAttempt — то есть
// до движения денег. Тот же отказ на один шаг позже был бы отказом ПОСЛЕ покупки.
//
// ЧТО НЕ ПРОВЕРЯЕТСЯ: потеря ОДНОЙ картинки из нескольких у пресетов `free` и `repaint_parts`. Там
// ссылки не связаны — «работай по словам и по этим картинкам», — и деградация честно видна в
// подписях. Отказывать за неё значило бы ронять исполнимый прогон.
func freeformPrerequisitesSurvived(p runParams, attached []refCaption) error {
	ff := p.Freeform
	if ff == nil {
		return nil
	}
	// PHASE 2: only `free` may run with no picture (text → image). Every other preset works ON a
	// picture, so a frozen run that names none — the door refuses it — is refused here too, free.
	if len(ff.Items) == 0 {
		if ff.Preset == entity.DesignFreeformPresetFree || ff.Preset == "" {
			return nil
		}
		return fmt.Errorf("%w: «%s» works on a picture, and this run names none", errFreeformSourceGone, ff.Preset)
	}
	alive := make(map[int]struct{}, len(attached))
	for _, rc := range attached {
		if rc.MediaID > 0 {
			alive[rc.MediaID] = struct{}{}
		}
	}
	named, survived := 0, 0
	hardware, marked := 0, 0
	survivedAs := map[string]int{}
	for _, it := range ff.Items {
		if it.MediaID <= 0 {
			continue
		}
		named++
		if _, ok := alive[it.MediaID]; !ok {
			continue
		}
		survived++
		survivedAs[it.Role]++
		if it.Role == entity.DesignFreeformRoleHardware {
			hardware++
			continue
		}
		// ОБЛАСТЬ ИЩЕТСЯ НА ЛЮБОЙ НЕ-ФУРНИТУРНОЙ КАРТИНКЕ, дословно как у двери: пустая роль
		// законна («просто картинка»), и требовать её проставленной значило бы отказывать за
		// неназванное имя там, где человек уже показал пальцем.
		if len(it.Regions) > 0 {
			marked++
		}
	}
	// НИ ОДНА НЕ ДОЕХАЛА — это уже не плейграунд, а платная просьба «нарисуй по словам», которую
	// дверь отклонила бы как `no_source_picture`.
	if named > 0 && survived == 0 {
		return fmt.Errorf("%w: this run names %d picture(s) and not one of them could be read any "+
			"more — a playground run works ON the pictures put into it, and there are none left",
			errFreeformSourceGone, named)
	}
	// PHASE 2: presets whose pictures are LINKED — each one is named by number in the craft — need
	// every named role to survive; the rest (one picture each) are covered by the check above.
	gone := func(what string) error {
		return fmt.Errorf("%w: «%s» needs %s, and it is no longer there", errFreeformSourceGone, ff.Preset, what)
	}
	switch ff.Preset {
	case entity.DesignFreeformPresetTryon:
		if survivedAs[entity.DesignFreeformRoleModel] == 0 {
			return gone("the model photo")
		}
		if survivedAs[entity.DesignFreeformRoleProduct] == 0 {
			return gone("the garment picture")
		}
		if ff.Options != nil && ff.Options.SceneMode == entity.DesignSceneModeReference &&
			survivedAs[entity.DesignFreeformRoleScene] == 0 {
			return gone("the scene picture")
		}
		return nil
	case entity.DesignFreeformPresetAddLogo:
		if survivedAs[entity.DesignFreeformRoleLogo] == 0 {
			return gone("the logo picture")
		}
		if survived-survivedAs[entity.DesignFreeformRoleLogo] == 0 {
			return gone("the garment picture")
		}
		return nil
	}
	// retouch / fabric_extract / ghost_mannequin / variations name exactly one picture (the door),
	// so «it did not survive» is the named > 0 && survived == 0 check above, and «it names none» is
	// the empty-items check at the top.
	if ff.Preset != entity.DesignFreeformPresetAddHardware {
		return nil
	}
	if hardware == 0 {
		return fmt.Errorf("%w: «add hardware» puts the hardware from one picture onto another, and "+
			"the picture marked role=hardware is no longer there", errFreeformSourceGone)
	}
	if marked == 0 {
		return fmt.Errorf("%w: «add hardware» needs the place it goes, and the picture carrying the "+
			"outlined area is no longer there", errFreeformSourceGone)
	}
	return nil
}

// recolorAttached is WHAT ONE RECOLOUR CALL SHOWS THE MODEL, which is not what the job carries.
//
// ═══ THE ONLY PLACE IN THIS PACKAGE WHERE CAPTIONS ARE NOT NUMBERED OFF `References` ════════════
//
// Every other route makes ONE call holding ALL of `References`, so «- image k» counting through
// that slice is the truth. A recolour makes N calls holding ONE photograph each (imageCalls), and
// numbering the captions over N photographs described a call that never happens: with three frames
// the prompt said «- image 1 … - image 3» while every call carried exactly one picture, and the
// model was handed a numbering protocol with nothing to attach it to. That inaccuracy predates
// J-31 and is fixed by the same edit that creates the reason to care: with a cloth attached, the
// numbers stop being decorative and start pointing — «the garment made of the cloth in image 2».
//
// SO THE CAPTION BLOCK DESCRIBES THE SHAPE OF ONE CALL: the photograph first, then the cloths, in
// the order imageCalls appends them. It is the same list for all N calls, which is honest, because
// all N calls have the same shape and the same prompt.
//
// ⚠ THE PHOTOGRAPH'S LINE IS SYNTHETIC AND CARRIES NO media_id, DELIBERATELY. There is no single
// photograph to name — there are N, one per call — so naming any of them would be a lie about the
// other N-1. `imageNumberOf` is never asked about a recolour (only renderCraft asks, and a recolour
// takes recolorCraft), so a zero id here cannot point anything at the wrong picture.
//
// ⚠ THE CAPTION ASSERTS NO PERSON AND NO PHOTOGRAPH (20-PROMPTS D4, review MAJOR 1). Tiles 4/5
// recolour flats and renders too; a caption promising «the real photograph … the same person»
// under a craft that says «a flat drawing or a render» is two contradictory descriptions of one
// picture, and a model resolves that by photorealising the flat or inventing a wearer. So the
// lighting, the person and the pose are kept only «that are present».
func recolorAttached(photos int, cloths []refCaption) []refCaption {
	if photos == 0 {
		// Nothing to recolour: imageCalls refuses this job before any money moves. Whatever the
		// caption block says here reaches nobody, so it says only what is true.
		return cloths
	}
	out := make([]refCaption, 0, 1+len(cloths))
	out = append(out, refCaption{
		Caption: "the source picture being recoloured — return this same picture, preserving its " +
			"presentation, crop and background, plus any lighting, person and pose that are present",
	})
	return append(out, cloths...)
}

// clothOneCaption is the caption of the ONE cloth of an ordinary single-cloth run — the picture the
// client echoes into `colour.fabric_media_id`.
//
// ⚠ THE FABRIC WORDING IS RETURNED BYTE FOR BYTE WHENEVER THE CLOTH IS NOT A PATTERN, and that is
// the load-bearing half of this function. Every run frozen before `kind` existed reaches here with
// `cloths` empty or with a cloth whose kind is "" — both answer «not a pattern» — so the caption of
// every single-cloth run that has ever gone out is the caption it went out with. The new sentence
// can only be reached by a run that STATED `kind: pattern`, which no frozen run does.
func clothOneCaption(cloths []fabricUse) string {
	const fabric = "fabric photograph — the material this garment is made of: read its weave, " +
		"texture, sheen and drape from here"
	if len(cloths) != 1 || !clothIsAPattern(cloths[0]) {
		return fabric
	}
	name := ""
	if n := oneLine(cloths[0].Name); n != "" {
		name = " «" + n + "»"
	}
	return "pattern tile" + name + " — a seamless repeat tile of the print this garment is made in: " +
		"read the motif and its colours from here; it is not a photograph of the garment's cloth"
}

// clothCaption is the caption of cloth N of a MULTI-cloth run, numbered exactly as the craft block
// numbers it — both walks are over the same statedCloths slice, in the same order.
func clothCaption(n int, c fabricUse) string {
	if !clothIsAPattern(c) {
		return "fabric photograph — CLOTH " + strconv.Itoa(n) + clothCaptionName(c) +
			": the material of the parts this cloth is used on, read its weave, texture, sheen and drape from here"
	}
	return "pattern tile — CLOTH " + strconv.Itoa(n) + clothCaptionName(c) +
		": a seamless repeat tile of the print the parts this cloth is used on are made in; " +
		"read the motif and its colours from here, it is not a photograph of cloth"
}

// clothCaptionName is the « — contrast rib» half of a cloth's attachment caption, or nothing when
// the cloth was never named.
//
// IT IS A SEPARATE FUNCTION SO THE UNNAMED CASE CANNOT PRODUCE A DANGLING DASH. A caption reading
// «CLOTH 2 — : the material of…» would be read by a model as an empty name rather than as an absent
// one, and by a human as a bug.
func clothCaptionName(c fabricUse) string {
	if name := oneLine(c.Name); name != "" {
		return " — " + name
	}
	return ""
}
