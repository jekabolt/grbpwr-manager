package designgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
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
	Threed             *threedParams `json:"threed"`
	// Pattern is only meaningful for kind=pattern. A nil pointer is «the run said nothing about the
	// repeat», which is legal — a tile is a tile whether or not anybody has decided how large it
	// will be printed.
	Pattern *patternParams `json:"pattern"`
}

// patternParams is the frozen ask of a repeating-tile run.
//
// ONE FIELD, AND IT IS A NUMBER THE MODEL CAN ACT ON. That is the same argument V-7 already made
// about `design_asset.repeat_mm`: «крупный» and «мелкий» are not instructions, «120 mm» is. It is
// also the number the ASSET built from this tile inherits, so «generated at 120 mm» and «placed at
// 120 mm» are one claim rather than two that drift.
type patternParams struct {
	RepeatMM int `json:"repeat_mm"`
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
// the garment. Render, recolor, pattern and vector are untouched for the plainer reason that their
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
			// AND IT KEEPS ITS FIRST VIEW, for the same reason the position is kept: the plate
			// walk runs first, so a picture that is both a plate and a reference is already
			// labelled with the side it stands on, and the second source has no side to offer.
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
	if kind == entity.DesignRunKindFlat {
		addRefs()
		addSlots()
	} else {
		addSlots()
		addRefs()
	}
	for _, id := range p.ExtraInputMediaIDs {
		add(id, "additional reference image", "")
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
			add(p.Colour.FabricMediaID, "fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here", "")
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
			add(c.MediaID, "fabric photograph — CLOTH "+strconv.Itoa(i+1)+clothCaptionName(c)+
				": the material of the parts this cloth is used on, read its weave, texture, sheen and drape from here", "")
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
	case entity.DesignViewDetail:
		return "detail view"
	default:
		return v
	}
}

// viewRank orders the silhouette so the front comes first. Anything unnamed sorts last.
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
	case entity.DesignViewDetail:
		return 4
	default:
		return 5
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
	write("garment", in.GarmentNote)
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
	if c := p.Colour; c != nil {
		write("colour", colourStatement(c))
		write("fabric in words", c.Words)
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
	// 3D IS A MESHY BUILD, VECTOR REDRAWS AN APPROVED RASTER, AND draft_idea NEVER REACHES THE
	// WORKER. None of the three is a picture composed by these words, so all three keep the bare
	// human context above and take no craft block at all.
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
		write("", patternCraft(pp))
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

// steerCeiling is the number of runes a surface hint may carry, and it is the PROVIDER'S number
// rather than a taste of ours: meshy.Submit refuses above it locally, and the meshy family reached
// through fal answers a longer one with a 422. Both refusals are terminal, so this is the one place
// that can keep them unreachable.
const steerCeiling = meshy.MaxTexturePrompt

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
func buildJob(ctx context.Context, media mediaResolver, run entity.DesignRun, quality string) (Job, error) {
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

	// ─── RESOLUTION FIRST, WORDS SECOND. The prompt's caption block is numbered off the pictures
	// that actually attach, so the media has to be resolved BEFORE the prompt is composed. Both
	// halves of each pair — the url and the caption — are appended by the SAME iteration of the
	// SAME loop, off the SAME element: that, and nothing subtler, is what guarantees that caption
	// k describes references[k-1]. TestCaptionNumberKIsImageNumberK holds the guarantee.
	list := referenceList(run.Kind, p, in)
	switch run.Kind {
	case entity.DesignRunKindRecolor, entity.DesignRunKindPattern:
		// ПЕРЕКРАС И ПАТТЕРН ДЕЙСТВУЮТ НА НАЗВАННЫЕ СНИМКИ, И ТОЛЬКО НА НИХ. Довод целиком в
		// source_inputs.go: вторая картинка в вызове превращает «верни ТОТ ЖЕ кадр» в «сочини
		// похожий», а плитку, собранную из двух лоскутов, невозможно состыковать саму с собой.
		list = sourcePictures(list, p)
	}
	if run.Kind == entity.DesignRunKindThreed {
		// У СБОРКИ 3D КАРТИНКИ — ЭТО ЕЁ ПЛИТЫ, И ТОЛЬКО ОНИ (V-14). Довод целиком в
		// threed_inputs.go: Meshy читает КАЖДУЮ присланную картинку как ВИД одного предмета и
		// принимает их 1..4, поэтому референс карточки здесь либо убивает прогон отказом по числу,
		// либо — что тише и дороже — сам становится «видом», и модель строится по чужой одежде.
		list = threedPictures(list, in)
	}
	var attached []refCaption
	if len(list) > 0 {
		ids := make([]int, 0, len(list))
		for _, rc := range list {
			ids = append(ids, rc.MediaID)
		}
		byID, err := media.GetMediaByIds(ctx, ids)
		if err != nil {
			return Job{}, fmt.Errorf("failed to resolve the input media of design run %d: %w", run.Id, err)
		}
		for _, rc := range list {
			m, ok := byID[rc.MediaID]
			if !ok {
				// The row went away between the snapshot and the pass. Skipping is right: the id
				// cannot be fetched by the provider either, and refusing the whole run over one
				// missing reference would throw away a job whose remaining inputs are intact. The
				// caption is skipped WITH the picture — see composePrompt on why.
				continue
			}
			u := strings.TrimSpace(m.FullSizeMediaURL)
			if u == "" {
				continue
			}
			// THE THREE HALVES MOVE TOGETHER, IN ONE APPEND, off one element — the url, the side
			// it shows, and the caption that describes it. That, and nothing subtler, is what makes
			// References[k], ReferenceViews[k] and caption k+1 the same picture.
			job.References = append(job.References, u)
			job.ReferenceViews = append(job.ReferenceViews, rc.View)
			attached = append(attached, rc)
		}
	}
	job.Prompt = composePrompt(run, p, in, attached)
	// COMPOSED FOR EVERY KIND, USED BY ONE. Deriving it here rather than inside the 3D route keeps
	// every word this package sends coming out of the same reader of the same frozen snapshot; a
	// route that composed its own text would be a second composer to keep in step.
	job.SurfaceSteer = surfaceSteer(ctx, p)
	return job, nil
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
