package designgen

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
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
}

type colourRecipe struct {
	Source        string `json:"source"`
	Code          string `json:"code"`
	Hex           string `json:"hex"`
	Words         string `json:"words"`
	FabricMediaID int    `json:"fabric_media_id"`
}

type threedParams struct {
	Presentation string `json:"presentation"`
	FitOverride  string `json:"fit_override"`
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
// EVERY ENTRY HAS WORDS, EVEN THE UNANNOTATED ONES. A picture that goes to the model with no
// caption line shifts the numbering of every caption after it — that silent shift is the defect
// this type exists to close, so «no words» is itself said in words («reference image»).
func referenceList(p runParams, in runInputs) []refCaption {
	slots := append([]inputSlot(nil), in.Slots...)
	sort.SliceStable(slots, func(i, j int) bool {
		return viewRank(slots[i].ViewKey) < viewRank(slots[j].ViewKey)
	})

	seen := map[int]int{}
	var out []refCaption
	add := func(id int, caption string) {
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
			return
		}
		seen[id] = len(out)
		out = append(out, refCaption{MediaID: id, Caption: caption})
	}
	for _, s := range slots {
		add(s.MediaID, slotCaption(s))
	}
	for _, r := range in.Refs {
		// A reference whose media row is gone is remembered by the snapshot but cannot be fetched
		// by anyone; sending its id would produce a 404 at the provider, mid-paid-call.
		if r.Deleted {
			continue
		}
		add(r.MediaID, refEntryCaption(r))
	}
	for _, id := range p.ExtraInputMediaIDs {
		add(id, "additional reference image")
	}
	if p.Colour != nil {
		// ⚠ THE CAPTION SAYS MATERIAL, NOT COLOUR, AND THE CHANGE IS LOAD-BEARING. It read «fabric
		// swatch for the colour» while a recipe could only be ONE of photo / picker / words. Now
		// that all three may be given at once, that caption contradicts the order of precedence the
		// render craft states two blocks later: the photograph governs the MATERIAL and loses the
		// colour to the picker. A caption and a rule that disagree about the same picture is worse
		// than either alone — the model gets to choose which of our two sentences it believes.
		add(p.Colour.FabricMediaID, "fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here")
	}
	return out
}

// referenceMediaIDs is the picture half of referenceList — kept as a name because half the band's
// comments point at it, and DERIVED from the list rather than built beside it: two builders of
// «the run's pictures, in order» is how captions and urls came to disagree in the first place.
func referenceMediaIDs(p runParams, in runInputs) []int {
	list := referenceList(p, in)
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
	}
	return b.String()
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
	list := referenceList(p, in)
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
			job.References = append(job.References, u)
			attached = append(attached, rc)
		}
	}
	job.Prompt = composePrompt(run, p, in, attached)
	return job, nil
}
