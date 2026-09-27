package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ═══════════════════════ ДВЕРЬ ПЛЕЙГРАУНДА (kind=freeform, kind=cutout) ═══════════════════════
//
// ПЛЕЙГРАУНД НЕ ЧИТАЕТ КАРТОЧКУ ВОВСЕ: ни верстака, ни референсов, ни описания изделия, ни
// рецепта. Его вход — ТОЛЬКО те картинки, которые человек положил в `params.freeform.items`, и
// только те слова, которые он написал. Поэтому у него собственный список входов, собственная
// проверка формы и собственная арифметика ссылок — всё в этом файле, чтобы «что уезжает в модель
// у плейграунда» отвечалось в одном месте, а не собиралось по родам в четырёх.
//
// ⚠ ВСЁ ЗДЕСЬ — ДО ДЕНЕГ. StartRun резервирует бюджет дня той же транзакцией, что вставляет
// строку; всякий отказ после неё — занятый резерв и `failed` в оплаченной истории за просьбу,
// которую можно было отклонить бесплатно.

// designFreeformPresets — ЧТО ЭТОТ СЕРВЕР УМЕЕТ В ПЛЕЙГРАУНДЕ, списком для экрана.
//
// ⚠ ЭТО НЕ КОПИЯ СЛОВАРЯ, А ТА ЖЕ ЛЕСТНИЦА, ЧТО У ДВЕРИ, ЗАДАННАЯ ЗАРАНЕЕ. Оба вопроса — «пустят
// ли сюда» — решают одни и те же две проверки: флаг генерации и ворота рода (Produces × Accepts у
// маршрута). Список имён, зашитый здесь отдельно, разошёлся бы с дверью молча и в обе стороны:
// экран рисовал бы кнопку, за которой отказ, или прятал бы работающую.
//
// ПУСТОЙ, НО НЕ nil, И ЭТО ПРО ПРОВОД. Ответ маршалится с EmitUnpopulated, поэтому поле уезжает
// всегда — `[]` значит «сервер про плейграунд знает и говорит, что сейчас нельзя», а отсутствие
// поля значило бы «сервер старый». Клиент читает ПРИСУТСТВИЕ поля, ровно как у has_fabric_render
// и colour_plan; пустой список гасит ячейку на рельсе, а не показывает её сломанной.
//
// ⚠ `cutout` СТОИТ В ЭТОМ ЖЕ СПИСКЕ, ХОТЯ ЭТО ДРУГОЙ РОД ПРОГОНА. Для человека это четвёртая
// кнопка в одном ряду («cut out the background»), и спрашивать её доступность отдельным полем
// значило бы завести на экране второе понятие там, где у него одно. Ворота у неё СВОИ — свой
// провайдер, свой ключ, — поэтому она появляется и исчезает независимо от трёх остальных.
func (s *Server) designFreeformPresets() []string {
	out := []string{}
	if s.designGenerationGate() != nil {
		return out
	}
	if s.designKindGateCheck(entity.DesignRunKindFreeform) == nil {
		out = append(out, entity.FreeformPresets()...)
	}
	if s.designKindGateCheck(entity.DesignRunKindCutout) == nil {
		out = append(out, entity.DesignRunKindCutout)
	}
	return out
}

// designPlaygroundWorkflows — THE TILES THIS SERVER CAN RUN RIGHT NOW (band field 28), in the
// owner's grid order, by the same ladder as the door: the money flag, then each route's own gate.
// `freeform_presets` (band 26) keeps its three keys + cutout for old clients [Codex 10]; new
// capability travels only here. Empty, never nil: present-and-empty is «this server knows the
// playground and says not now», absent is «an old server».
//
// extend_image is never listed in phase 2 (no route). retouch_zone is: its window path is live.
func (s *Server) designPlaygroundWorkflows() []string {
	out := []string{}
	if s.designGenerationGate() != nil {
		return out
	}
	open := map[string]bool{}
	if s.designKindGateCheck(entity.DesignRunKindFreeform) == nil {
		for _, w := range []string{
			entity.DesignWorkflowVirtualTryOn, entity.DesignWorkflowFabricToImage,
			entity.DesignWorkflowGhostMannequin, entity.DesignWorkflowAddLogo,
			entity.DesignWorkflowDesignVariations, entity.DesignWorkflowRetouchZone,
			entity.DesignWorkflowCreateEdit,
		} {
			open[w] = true
		}
	}
	if s.designKindGateCheck(entity.DesignRunKindRecolor) == nil {
		open[entity.DesignWorkflowChangeColor] = true
		open[entity.DesignWorkflowSwapFabrics] = true
	}
	if s.designKindGateCheck(entity.DesignRunKindCutout) == nil {
		open[entity.DesignWorkflowRemoveBackground] = true
	}
	// 3D also needs a reserve number: a wired route without one (fal with a tariff and no units
	// ceiling) is refused by the door (threed_reserve_unbounded), so the tile is not drawn.
	if s.designKindGateCheck(entity.DesignRunKindThreed) == nil && s.designThreedRouteReserveBounded() {
		open[entity.DesignWorkflowImageTo3D] = true
	}
	for _, w := range entity.PlaygroundWorkflows() {
		if open[w] {
			out = append(out, w)
		}
	}
	return out
}

// designImageModels — THE ENGINES THE DOOR ACCEPTS IN params.image (band field 29), off the same
// table it validates and prices with. Empty (present) when the image route is closed or no table
// is wired: the client then draws no picker and sends no `image`.
func (s *Server) designImageModels() []*pb_admin.DesignImageModel {
	out := []*pb_admin.DesignImageModel{}
	if s.designGenerationGate() != nil {
		return out
	}
	if s.designKindGateCheck(entity.DesignRunKindFreeform) != nil &&
		s.designKindGateCheck(entity.DesignRunKindRender) != nil {
		return out
	}
	for _, e := range s.designEngineTable() {
		out = append(out, &pb_admin.DesignImageModel{
			Slug:          e.Slug,
			Label:         e.Label,
			AspectRatios:  append([]string{}, e.Ratios...),
			Qualities:     designEngineTierWords(e),
			IsDefault:     e.IsDefault,
			MaxReferences: int32(e.MaxRefs),
			Backgrounds:   append([]string{}, e.Backgrounds...),
		})
	}
	return out
}

// designThreedOptions — which DesignThreedParams options the CONFIGURED 3D route honours (band
// field 30), read off the same designgen.ThreedRoute the door refuses with (designRefuseThreedRoute)
// — one value for the band, the door and the reserve (G-02, Codex 3 = Fable m-4).
//
//   - fal meshy family / direct Meshy: texture, quality — and pbr only with DESIGN_THREED_PBR on
//     (its GLB size is unmeasured and the 64 MiB cap fails after the charge, Fable M-3);
//   - the retired hitem3d slug (a FAL_MODEL_3D override): none — its body sends fixed constants;
//   - no route wired, or a route with no reserve number: none.
//
// `follow` is never listed in phase 2 (the door refuses it: option_not_read).
func (s *Server) designThreedOptions() []string {
	out := []string{}
	if s.designGenerationGate() != nil || s.designKindGateCheck(entity.DesignRunKindThreed) != nil {
		return out
	}
	if s.designThreedRoute == nil || !s.designThreedRouteReserveBounded() {
		return out
	}
	return append(out, s.designThreedRoute.Options...)
}

// designRefuseMalformedFreeform — ФОРМА ПРОСЬБЫ ПЛЕЙГРАУНДА, и спрашивается она С ГОВОРЯЩЕГО.
//
// ⚠ ГРАНИЦА ТА ЖЕ, ЧТО У designRefuseMalformedColourMaps, И ДОВОД ТОТ ЖЕ. Словари (пресеты, роли)
// законно растут и законно сужаются, а параметры родителя заморожены: проверка УНАСЛЕДОВАННОГО
// значения сделала бы старый прогон неперезапускаемым навсегда — в тот день, когда пресет
// переименуют. Молчащий реран противоречить не может, поэтому `spoken == nil` — это ноль проверок
// и ноль отказов.
//
// ⚠ ЧТО ЗДЕСЬ НЕ ПРОВЕРЯЕТСЯ, И ЭТО НАМЕРЕННО: «есть ли хоть одна картинка» и «влезает ли всё это
// в один вызов». Первое — вопрос о РАБОТОСПОСОБНОСТИ прогона (designRefuseUnworkableSources),
// второе — о ДЕНЬГАХ (designRefuseFreeformOverflow), и оба задаются ДЕЙСТВУЮЩИМ параметрам, а не
// сообщению: неработоспособная форма остаётся неработоспособной и на реране.
func designRefuseMalformedFreeform(kind string, spoken *pb_common.DesignRunParams) error {
	ff := spoken.GetFreeform()
	if kind != entity.DesignRunKindFreeform {
		// ⚠ ЧУЖОЙ РОД С ЗАПОЛНЕННЫМ `freeform` — ОТКАЗ, А НЕ МОЛЧАЛИВОЕ ИГНОРИРОВАНИЕ. Поле не
		// читает никто, кроме плейграунда, поэтому «принято» здесь означало бы «размеченные вами
		// области и ваши слова не поедут никуда, и мы вам об этом не скажем» — за деньги.
		if ff != nil && (strings.TrimSpace(ff.GetPreset()) != "" || len(ff.GetItems()) > 0 ||
			designWorkflowOptionsStated(ff.GetOptions()) != "") {
			return designRefusal(codes.InvalidArgument, "freeform_forbidden",
				fmt.Sprintf("params.freeform is the playground's own list of pictures and only a "+
					"freeform run reads it; this is a %s run. Nothing was reserved and nothing was "+
					"charged", kind),
				map[string]string{"kind": kind})
		}
		return nil
	}
	if ff == nil {
		// Молчание проверять не на чем: «плейграунд без единой картинки» — вопрос
		// designRefuseUnworkableSources, и отвечает он там же, где реран.
		return nil
	}
	if !entity.IsFreeformPreset(ff.GetPreset()) {
		return designRefusal(codes.InvalidArgument, "unknown_preset",
			fmt.Sprintf("params.freeform.preset %q is not %s — the preset picks the craft paragraph, "+
				"and the server has no paragraph for that word. Nothing was reserved and nothing was "+
				"charged", ff.GetPreset(), strings.Join(entity.FreeformPresetsAll(), " | ")),
			map[string]string{"preset": ff.GetPreset()})
	}
	if n := len(ff.GetItems()); n > entity.MaxDesignFreeformItems {
		return status.Errorf(codes.InvalidArgument,
			"params.freeform.items names %d pictures; the ceiling is %d",
			n, entity.MaxDesignFreeformItems)
	}
	// ⚠ ОДИН СПИСОК НА ОДИН ФАКТ. `extra_input_media_ids` и `freeform.items` — два написания
	// «картинки этого прогона», и прогон, назвавший картинку в обоих, отправил бы её дважды: один
	// раз подписью плейграунда, второй — «additional reference image». Сборка ссылок читает у
	// плейграунда ТОЛЬКО items (designgen/snapshot.go), так что второй список либо уехал бы
	// молчаливым дублем, либо не уехал вовсе — и оба исхода человек узнал бы по счёту.
	if len(ff.GetItems()) > 0 && len(spoken.GetExtraInputMediaIds()) > 0 {
		return designRefusal(codes.InvalidArgument, "one_list_per_fact",
			fmt.Sprintf("a freeform run states its pictures in params.freeform.items; this one also "+
				"names %d in params.extra_input_media_ids. One list per fact — the playground reads "+
				"items and nothing else. Nothing was reserved and nothing was charged",
				len(spoken.GetExtraInputMediaIds())),
			map[string]string{"extra": strconv.Itoa(len(spoken.GetExtraInputMediaIds()))})
	}
	// ⚠ ОДНА КАРТИНКА — ОДНА ЗАПИСЬ, И ЭТО НЕ АККУРАТНОСТЬ, А ПОТОЛОК ОБЛАСТЕЙ. Потолок в четыре
	// области объявлен НА КАРТИНКУ; та же картинка, названная дважды по две области, обошла бы его
	// молча — а обходится он в производные картинки, то есть в размер платного запроса. Плюс
	// снимок дедуплицирует ссылки, а сборка задания — нет: прогон получил бы ДВЕ обведённые копии
	// с подписями «image 1 with area A…», обе про один и тот же номер.
	seen := make(map[int32]int, len(ff.GetItems()))
	for i, it := range ff.GetItems() {
		where := "params.freeform.items." + strconv.Itoa(i)
		if it.GetMediaId() <= 0 {
			return status.Errorf(codes.InvalidArgument, "%s.media_id must be a media id", where)
		}
		if first, dup := seen[it.GetMediaId()]; dup {
			return designRefusal(codes.InvalidArgument, "duplicate_picture",
				fmt.Sprintf("%s names picture %d, which params.freeform.items.%d already names: one "+
					"picture is one entry, with up to %d marked areas on it. Nothing was reserved and "+
					"nothing was charged", where, it.GetMediaId(), first,
					entity.MaxDesignFreeformRegionsPerItem),
				map[string]string{"media_id": strconv.Itoa(int(it.GetMediaId()))})
		}
		seen[it.GetMediaId()] = i
		if !entity.IsFreeformRole(it.GetRole()) {
			return designRefusal(codes.InvalidArgument, "unknown_role",
				fmt.Sprintf("%s.role %q is not subject | hardware | cloth | model | product | scene | logo "+
					"(empty is «just a picture»). Nothing was reserved and nothing was charged", where, it.GetRole()),
				map[string]string{"role": it.GetRole()})
		}
		if n := len(it.GetRegions()); n > entity.MaxDesignFreeformRegionsPerItem {
			return status.Errorf(codes.InvalidArgument,
				"%s.regions marks %d places; the ceiling is %d per picture",
				where, n, entity.MaxDesignFreeformRegionsPerItem)
		}
		for r, region := range it.GetRegions() {
			if err := designRefuseMalformedRegion(where+".regions."+strconv.Itoa(r), region); err != nil {
				return err
			}
		}
		// ⚠ ПОДПИСЕЙ НЕ БОЛЬШЕ, ЧЕМ ОБЛАСТЕЙ ПЛЮС ОДНА, И ЛИШНЯЯ — ЭТО ПОДПИСЬ КАРТИНКИ ЦЕЛИКОМ.
		// Пара «область i ↔ подпись i» позиционная, и она же строит вложения; список подписей
		// длиннее пары молча потерял бы хвост — то есть слова человека не доехали бы до модели, а
		// экран показывал бы их отправленными.
		if n, max := len(it.GetTexts()), len(it.GetRegions())+1; n > max {
			return status.Errorf(codes.InvalidArgument,
				"%s.texts has %d entries but the picture has %d marked areas: a text belongs to the "+
					"area of the same index, and at most one text describes the whole picture",
				where, n, len(it.GetRegions()))
		}
		for t, text := range it.GetTexts() {
			if n := len([]rune(text)); n > entity.MaxDesignFreeformTextRunes {
				return status.Errorf(codes.InvalidArgument,
					"%s.texts.%d is %d characters; the ceiling is %d",
					where, t, n, entity.MaxDesignFreeformTextRunes)
			}
		}
	}
	return designRefuseMalformedWorkflowOptions(ff.GetOptions())
}

// designRefuseMalformedWorkflowOptions — the VOCABULARY of params.freeform.options, asked of the
// speaker only (the same boundary as presets and roles: a vocabulary may narrow, and a frozen
// run must stay rerunnable). Which preset reads which field is a question of the effective params
// (designRefuseUnworkableFreeform, `option_not_read`).
func designRefuseMalformedWorkflowOptions(o *pb_common.DesignWorkflowOptions) error {
	if o == nil {
		return nil
	}
	bad := func(field, value, allowed string) error {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeUnknownOption,
			fmt.Sprintf("params.freeform.options.%s %q is not %s. Nothing was reserved and nothing "+
				"was charged", field, value, allowed),
			map[string]string{"field": field, "value": value})
	}
	switch {
	case !entity.IsDesignFraming(o.GetFraming()):
		return bad("framing", o.GetFraming(),
			"auto | full_body | upper_body | portrait | hands | feet | product_detail")
	case !entity.IsDesignAngle(o.GetAngle()):
		return bad("angle", o.GetAngle(), "auto | eye_level | slightly_above | slightly_below | low_angle")
	case !entity.IsDesignSceneMode(o.GetSceneMode()):
		return bad("scene_mode", o.GetSceneMode(), "edit | reference")
	case !entity.IsDesignLogoSize(o.GetLogoSize()):
		return bad("logo_size", o.GetLogoSize(), "small | medium | large")
	case o.GetCreativity() < 0 || o.GetCreativity() > entity.MaxDesignCreativity:
		return bad("creativity", strconv.Itoa(int(o.GetCreativity())),
			"0.."+strconv.Itoa(entity.MaxDesignCreativity))
	case o.GetModelId() < 0:
		return bad("model_id", strconv.Itoa(int(o.GetModelId())), "a model id")
	case o.GetProductColorwayId() < 0:
		return bad("product_colorway_id", strconv.Itoa(int(o.GetProductColorwayId())), "a colourway id")
	}
	if n := len([]rune(o.GetSceneText())); n > entity.MaxDesignFreeformTextRunes {
		return status.Errorf(codes.InvalidArgument,
			"params.freeform.options.scene_text is %d characters; the ceiling is %d",
			n, entity.MaxDesignFreeformTextRunes)
	}
	return nil
}

// designWorkflowOptionsStated — the first non-zero field of options, in declaration order (empty =
// nothing stated).
func designWorkflowOptionsStated(o *pb_common.DesignWorkflowOptions) string {
	for _, f := range designWorkflowOptionFields(o) {
		if f.stated {
			return f.name
		}
	}
	return ""
}

type designWorkflowOptionField struct {
	name   string
	stated bool
}

func designWorkflowOptionFields(o *pb_common.DesignWorkflowOptions) []designWorkflowOptionField {
	return []designWorkflowOptionField{
		{"framing", o.GetFraming() != ""},
		{"angle", o.GetAngle() != ""},
		{"scene_mode", o.GetSceneMode() != ""},
		{"scene_text", strings.TrimSpace(o.GetSceneText()) != ""},
		{"model_id", o.GetModelId() != 0},
		{"product_colorway_id", o.GetProductColorwayId() != 0},
		{"logo_size", o.GetLogoSize() != ""},
		{"creativity", o.GetCreativity() != 0},
	}
}

// designPresetReads — which options fields each preset reads; every other stated field is
// `option_not_read`. A preset absent here reads none.
var designPresetReads = map[string]map[string]bool{
	entity.DesignFreeformPresetTryon: {
		"framing": true, "angle": true, "scene_mode": true, "scene_text": true,
		"model_id": true, "product_colorway_id": true,
	},
	entity.DesignFreeformPresetAddLogo:    {"logo_size": true},
	entity.DesignFreeformPresetVariations: {"creativity": true},
}

// designPresetRoles — the picture roles each preset reads. A role outside the set would be sent
// with a caption the preset's craft never mentions, so it is refused (`unknown_role`).
var designPresetRoles = map[string]map[string]bool{
	entity.DesignFreeformPresetFree:         designOldFreeformRoles,
	entity.DesignFreeformPresetAddHardware:  designOldFreeformRoles,
	entity.DesignFreeformPresetRepaintParts: designOldFreeformRoles,
	entity.DesignFreeformPresetTryon: {
		entity.DesignFreeformRoleModel: true, entity.DesignFreeformRoleProduct: true,
		entity.DesignFreeformRoleScene: true,
	},
	entity.DesignFreeformPresetFabricExtract:  designSubjectRoles,
	entity.DesignFreeformPresetGhostMannequin: designSubjectRoles,
	entity.DesignFreeformPresetVariations:     designSubjectRoles,
	entity.DesignFreeformPresetRetouch:        designSubjectRoles,
	entity.DesignFreeformPresetAddLogo: {
		"": true, entity.DesignFreeformRoleSubject: true, entity.DesignFreeformRoleLogo: true,
	},
}

var (
	designOldFreeformRoles = map[string]bool{
		"": true, entity.DesignFreeformRoleSubject: true, entity.DesignFreeformRoleHardware: true,
		entity.DesignFreeformRoleCloth: true,
	}
	designSubjectRoles = map[string]bool{"": true, entity.DesignFreeformRoleSubject: true}
)

// designMaxTryonProducts — how many garments one try-on dresses the person in.
const designMaxTryonProducts = 4

// designRefuseUnworkableFreeform — THE SHAPE EACH PRESET NEEDS, on EFFECTIVE params (a rerun
// inherits the shape it repeats). Every refusal names what fixes it; all of them are before money.
//
//	free                                 0..8 pictures; none only with words (`words_required`)
//	add_hardware, repaint_parts          as before this wave
//	tryon                                ≥1 model, 1..4 products, one scene iff scene_mode=reference
//	fabric_extract, ghost_mannequin,
//	variations                           exactly one picture, no marked area
//	add_logo                             one picture + one logo
//	retouch                              one picture, one marked area, words for it
func designRefuseUnworkableFreeform(ask string, params *pb_common.DesignRunParams) error {
	ff := params.GetFreeform()
	preset := ff.GetPreset()
	items := ff.GetItems()

	// Roles first: the counts below are counts of roles.
	if allowed, ok := designPresetRoles[preset]; ok {
		for i, it := range items {
			if !allowed[it.GetRole()] {
				return designRefusal(codes.InvalidArgument, "unknown_role",
					fmt.Sprintf("params.freeform.items.%d.role %q is not a role «%s» reads (%s). Nothing "+
						"was reserved and nothing was charged", i, it.GetRole(), preset,
						designRoleList(allowed)),
					map[string]string{"role": it.GetRole(), "preset": preset})
			}
		}
	}
	// Options the preset does not read would travel frozen and change nothing.
	reads := designPresetReads[preset]
	for _, f := range designWorkflowOptionFields(ff.GetOptions()) {
		if f.stated && !reads[f.name] {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOptionNotRead,
				fmt.Sprintf("params.freeform.options.%s is not read by «%s»; leave it empty. Nothing was "+
					"reserved and nothing was charged", f.name, preset),
				map[string]string{"field": f.name, "preset": preset})
		}
	}

	byRole := map[string]int{}
	regions := 0
	for _, it := range items {
		byRole[it.GetRole()]++
		regions += len(it.GetRegions())
	}
	roleRequired := func(role, why string) error {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeRoleRequired,
			fmt.Sprintf("«%s» needs %s: mark it with role=%s in params.freeform.items. Nothing was "+
				"reserved and nothing was charged", preset, why, role),
			map[string]string{"role": role, "preset": preset})
	}
	onePicture := func(named int, what string) error {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOneSourcePicture,
			fmt.Sprintf("«%s» works on exactly one %s, and this run names %d. Nothing was reserved and "+
				"nothing was charged", preset, what, named),
			map[string]string{"named": strconv.Itoa(named), "preset": preset})
	}

	switch preset {
	case entity.DesignFreeformPresetFree:
		// TEXT → IMAGE: no picture is legal when there are words to draw from.
		if len(items) == 0 && strings.TrimSpace(ask) == "" {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeWordsRequired,
				"a playground run with no picture draws from your words alone, and there are none: "+
					"write what to make, or add a picture. Nothing was reserved and nothing was charged", nil)
		}
	case entity.DesignFreeformPresetTryon:
		if byRole[entity.DesignFreeformRoleModel] == 0 {
			return roleRequired(entity.DesignFreeformRoleModel, "the photo of the person to dress")
		}
		products := byRole[entity.DesignFreeformRoleProduct]
		if products == 0 {
			return roleRequired(entity.DesignFreeformRoleProduct, "the garment to dress them in")
		}
		if products > designMaxTryonProducts {
			return designRefusal(codes.InvalidArgument, "too_many_pictures",
				fmt.Sprintf("a try-on dresses the person in at most %d garments, and this run names %d. "+
					"Nothing was reserved and nothing was charged", designMaxTryonProducts, products),
				map[string]string{"role": entity.DesignFreeformRoleProduct,
					"ceiling": strconv.Itoa(designMaxTryonProducts), "named": strconv.Itoa(products)})
		}
		scenes := byRole[entity.DesignFreeformRoleScene]
		if ff.GetOptions().GetSceneMode() == entity.DesignSceneModeReference {
			if scenes == 0 {
				return roleRequired(entity.DesignFreeformRoleScene, "the scene picture (scene_mode=reference)")
			}
			if scenes > 1 {
				return onePicture(scenes, "scene picture")
			}
		} else if scenes > 0 {
			// A scene picture is read only in reference mode; sent otherwise it is a picture the
			// craft never names.
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOptionNotRead,
				"a role=scene picture is read only with options.scene_mode=reference; set it, or "+
					"remove the scene picture. Nothing was reserved and nothing was charged",
				map[string]string{"field": "scene_mode", "preset": preset})
		}
	case entity.DesignFreeformPresetFabricExtract, entity.DesignFreeformPresetGhostMannequin,
		entity.DesignFreeformPresetVariations:
		if len(items) != 1 {
			return onePicture(len(items), "picture")
		}
		if regions > 0 {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOneRegion,
				fmt.Sprintf("«%s» reads the whole picture; clear the marked areas. Nothing was reserved "+
					"and nothing was charged", preset),
				map[string]string{"regions": strconv.Itoa(regions), "preset": preset, "ceiling": "0"})
		}
	case entity.DesignFreeformPresetAddLogo:
		logos := byRole[entity.DesignFreeformRoleLogo]
		if logos == 0 {
			return roleRequired(entity.DesignFreeformRoleLogo, "the logo picture")
		}
		if logos > 1 {
			return onePicture(logos, "logo")
		}
		if base := len(items) - logos; base != 1 {
			return onePicture(base, "garment picture besides the logo")
		}
	case entity.DesignFreeformPresetRetouch:
		if len(items) != 1 {
			return onePicture(len(items), "picture")
		}
		it := items[0]
		if n := len(it.GetRegions()); n != 1 {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOneRegion,
				fmt.Sprintf("«retouch» changes exactly one marked area, and this picture has %d: mark one. "+
					"Nothing was reserved and nothing was charged", n),
				map[string]string{"regions": strconv.Itoa(n), "preset": preset, "ceiling": "1"})
		}
		said := strings.TrimSpace(ask) != ""
		if texts := it.GetTexts(); len(texts) > 0 && strings.TrimSpace(texts[0]) != "" {
			said = true
		}
		if !said {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeWordsRequired,
				"«retouch» needs to be told what to do in the marked area: write it on the area or in "+
					"the request. Nothing was reserved and nothing was charged", nil)
		}
	default:
		// add_hardware, repaint_parts: the rules of the first playground wave.
		if len(items) == 0 {
			return designRefusal(codes.InvalidArgument, "no_source_picture",
				"the playground works on the pictures you put in it: name them in "+
					"params.freeform.items. Nothing was reserved and nothing was charged", nil)
		}
		// ⚠ ПРЕСЕТ ADD_HARDWARE ТРЕБУЕТ ОБЕИХ ПОЛОВИН, И ЭТО НЕ ПЕДАНТИЗМ: его абзац ремесла
		// дословно говорит «возьми фурнитуру с картинки N и посади её в обведённую область
		// картинки 1». Без картинки фурнитуры брать нечего, без области — сажать некуда, и в обоих
		// случаях модель вернёт правдоподобный кадр, по которому в истории не отличить исполненную
		// просьбу от неисполненной. Деньги при этом списаны.
		if preset == entity.DesignFreeformPresetAddHardware {
			hardware, marked := 0, 0
			for _, it := range items {
				if it.GetRole() == entity.DesignFreeformRoleHardware {
					hardware++
					continue
				}
				// ОБЛАСТЬ ИЩЕТСЯ НА ЛЮБОЙ НЕ-ФУРНИТУРНОЙ КАРТИНКЕ, А НЕ ТОЛЬКО НА role=subject:
				// роль пустая законна («просто картинка»), и требовать её проставленной значило бы
				// отказывать за неназванное имя там, где человек уже показал пальцем.
				if len(it.GetRegions()) > 0 {
					marked++
				}
			}
			if hardware == 0 {
				return designRefusal(codes.InvalidArgument, "hardware_picture_required",
					"«add hardware» puts the hardware from one picture onto another: mark the picture "+
						"of the hardware with role=hardware in params.freeform.items. Nothing was "+
						"reserved and nothing was charged", nil)
			}
			if marked == 0 {
				return designRefusal(codes.InvalidArgument, "mark_the_area",
					"«add hardware» needs the place it goes: outline an area on the picture the "+
						"hardware is added to. Nothing was reserved and nothing was charged", nil)
			}
		}
		// `repaint_parts` БЕЗ ОБЛАСТИ ЗАКОНЕН, и это сказано вслух, чтобы никто не «дочинил» его
		// симметрично соседу: перекрасить всю вещь — обычная просьба, и абзац ремесла умеет её
		// («repaint the whole garment»).
	}
	return nil
}

func designRoleList(allowed map[string]bool) string {
	out := make([]string, 0, len(allowed))
	for r := range allowed {
		if r == "" {
			r = "empty"
		}
		out = append(out, r)
	}
	sort.Strings(out)
	return strings.Join(out, " | ")
}

// designWorkflowOf — the PLAYGROUND tile a run belongs to: entity.DesignWorkflowOf over the kind
// and the params, with «a cloth that carries a picture» read the way the feed's SQL reads it
// (colour.fabrics[*].media_id; the scalar echo does not count).
func designWorkflowOf(kind string, p *pb_common.DesignRunParams) string {
	return entity.DesignWorkflowOf(kind, p.GetFreeform().GetPreset(), designAnyClothWithPicture(p.GetColour()))
}

// designRefuseRerunChangesWorkflow — A RERUN REPEATS THE RUN IT POINTS AT [Codex 2]: a spoken
// rerun that lands on a different tile (free → tryon, a recolour that gains a cloth picture) is a
// new run filed under an old one's number. A silent rerun inherits everything and cannot differ.
func designRefuseRerunChangesWorkflow(kind string, spoken *pb_common.DesignRunParams,
	parentID int, parentParams []byte) error {
	if spoken == nil || parentID <= 0 {
		return nil
	}
	inherited := &pb_common.DesignRunParams{}
	if len(parentParams) > 0 {
		if err := designUnmarshalJSON(parentParams, inherited); err != nil {
			return status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored parameters do not parse", parentID)
		}
	}
	was, now := designWorkflowOf(kind, inherited), designWorkflowOf(kind, spoken)
	if was == now {
		return nil
	}
	return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeRerunChangesWorkflow,
		fmt.Sprintf("a rerun repeats the run it points at: run %d was %q and this one would be %q — "+
			"start a new run instead of a rerun. Nothing was reserved and nothing was charged",
			parentID, was, now),
		map[string]string{"rerun_of": strconv.Itoa(parentID), "was": was, "now": now})
}

// designRefuseMalformedRegion — ОДНА РАЗМЕЧЕННАЯ ОБЛАСТЬ.
//
// ⚠ ТОЛЬКО POLYGON, И ЭТО НЕ УЖЕСТОЧЕНИЕ РАДИ ПОРЯДКА. Область плейграунда работает ровно двумя
// способами: контуром на размеченной копии и bbox'ом кропа (designgen/freeform_derive.go). PIN —
// это точка, у неё нет площади; DIM — размерная линия; ARC — дуга. Ни у одного из них нет того,
// что оба потребителя читают, поэтому «принято» означало бы кроп нулевой площади в углу кадра —
// за деньги и молча.
//
// Координаты проверяются designUnitInterval, тем же выражением, что и кроп кадра: доли 0..1,
// обычная запись, потолок знаков после запятой. Второе правописание «доли картинки» разошлось бы
// с первым в первый же день.
func designRefuseMalformedRegion(where string, region *pb_common.TechCardAnnotation) error {
	if region == nil {
		return status.Errorf(codes.InvalidArgument, "%s is empty", where)
	}
	if region.GetKind() != pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON {
		return designRefusal(codes.InvalidArgument, "region_not_a_polygon",
			fmt.Sprintf("%s is %s; a marked area is a POLYGON — the server outlines it on a copy of "+
				"the picture and crops it, and neither can be done with a point or a line. Nothing "+
				"was reserved and nothing was charged", where, region.GetKind().String()),
			map[string]string{"kind": region.GetKind().String()})
	}
	points := region.GetPoints()
	if len(points) < designFreeformRegionMinPoints || len(points) > designFreeformRegionMaxPoints {
		return status.Errorf(codes.InvalidArgument,
			"%s.points has %d points; a marked area is %d..%d points",
			where, len(points), designFreeformRegionMinPoints, designFreeformRegionMaxPoints)
	}
	xs := make([]decimal.Decimal, 0, len(points))
	ys := make([]decimal.Decimal, 0, len(points))
	for i, p := range points {
		x, err := designUnitInterval(fmt.Sprintf("%s.points.%d.x", where, i), p.GetX())
		if err != nil {
			return err
		}
		y, err := designUnitInterval(fmt.Sprintf("%s.points.%d.y", where, i), p.GetY())
		if err != nil {
			return err
		}
		xs = append(xs, x)
		ys = append(ys, y)
	}
	return designRefuseDegenerateRegion(where, xs, ys)
}

// designFreeformMinRegionArea — САМАЯ МАЛЕНЬКАЯ ОБЛАСТЬ, КОТОРАЯ ЕЩЁ ЧТО-ТО ЗНАЧИТ, в долях кадра
// в квадрате. 1e-5 — это одна стотысячная площади снимка: на кадре 4000×3000 примерно 120 пикселей,
// то есть квадратик 11×11.
//
// ⚠ ЧИСЛО ВЫБРАНО СО СТОРОНЫ ЛОЖНОГО ОТКАЗА, А НЕ СО СТОРОНЫ КРАСОТЫ. Всё, что мельче, кроп всё
// равно вытянет до 1024 px из десятка пикселей — то есть модель получит мыло вместо места, — а
// человек, обводивший что-то пальцем, физически не рисует области меньше: даже точка касания на
// телефоне это доли процента кадра. Порог ловит вырождение, а не аккуратность.
const designFreeformMinRegionArea = "0.00001"

// designRefuseDegenerateRegion — У МНОГОУГОЛЬНИКА ОБЯЗАНА БЫТЬ ПЛОЩАДЬ.
//
// ⚠ СЧЁТА ТОЧЕК НЕДОСТАТОЧНО, И ЭТО НЕ ПЕДАНТИЗМ, А ДЕНЬГИ. Три ОДИНАКОВЫЕ точки и три точки НА
// ОДНОЙ ПРЯМОЙ проходят все проверки формы: их ровно три, каждая в 0..1, вид — POLYGON. А дальше
// они работают ровно так же, как настоящая область: `add_hardware` считает картинку размеченной и
// открывает ворота (designRefuseUnworkableSources), окно генерации берётся по ним же
// (freeformWindowPlan), кроп получает bbox нулевой высоты или ширины — и модель платно рисует
// пуговицу в полоску шириной в пиксель, растянутую до 1024. Отказ здесь бесплатный; тот же отказ
// у поставщика — нет.
//
// ФОРМУЛА ШНУРКОВ, И ОНА ЖЕ ОТВЕЧАЕТ НА ОБА ВЫРОЖДЕНИЯ СРАЗУ: у совпавших точек площадь ноль, у
// коллинеарных — тоже, и никакого третьего вопроса задавать не надо. Считается в decimal, потому
// что координаты приезжают decimal'ом и потому что порог здесь — сравнение, а не оценка: float
// внёс бы в него собственную ошибку ровно на том масштабе, где стоит порог.
//
// ⚠ ПОВТОР СОСЕДНИХ ТОЧЕК ОТКАЗЫВАЕТСЯ ОТДЕЛЬНО, ХОТЯ ПЛОЩАДЬ ЕГО ЧАСТО ЛОВИТ. Четырёхугольник,
// у которого две соседние точки совпали, — это треугольник, записанный четырьмя точками: площадь у
// него настоящая, а контур везёт мёртвую вершину, которую обводка рисует точкой поверх линии.
// Своё слово вместо «площадь мала» потому, что чинится это иначе — убрать точку, а не растянуть
// область.
func designRefuseDegenerateRegion(where string, xs, ys []decimal.Decimal) error {
	n := len(xs)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		if xs[i].Equal(xs[j]) && ys[i].Equal(ys[j]) {
			return designRefusal(codes.InvalidArgument, "region_degenerate",
				fmt.Sprintf("%s.points.%d repeats point %d — a marked area is a polygon, and a repeated "+
					"corner is a vertex with no edge. Nothing was reserved and nothing was charged",
					where, j, i),
				map[string]string{"where": where, "reason": "repeated_point"})
		}
	}
	// Формула шнурков даёт УДВОЕННУЮ площадь со знаком; знак — это направление обхода, и он к делу
	// не относится.
	twice := decimal.Zero
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		twice = twice.Add(xs[i].Mul(ys[j]).Sub(xs[j].Mul(ys[i])))
	}
	area := twice.Abs().Div(decimal.NewFromInt(2))
	min, err := decimal.NewFromString(designFreeformMinRegionArea)
	if err != nil {
		return status.Error(codes.Internal, "the minimum marked-area size is misconfigured")
	}
	if area.LessThan(min) {
		return designRefusal(codes.InvalidArgument, "region_degenerate",
			fmt.Sprintf("%s encloses %s of the picture, which is not an area a person can point at: "+
				"its corners are the same point or lie on one straight line. The server outlines this "+
				"area on a copy and crops it, and neither can be done with a line. Draw the area again. "+
				"Nothing was reserved and nothing was charged", where, area.String()),
			map[string]string{"where": where, "area": area.String(), "reason": "no_area"})
	}
	return nil
}

// Сколько точек в размеченной области. Три — это треугольник, меньшего многоугольника не бывает;
// двенадцать — потолок того, что человек рисует пальцем и что имеет смысл обводить контуром.
// Прямоугольник, который тянут мышью, приезжает сюда четырьмя точками.
const (
	designFreeformRegionMinPoints = 3
	designFreeformRegionMaxPoints = 12
)

// designFreeformItemMediaIDs — картинки плейграунда, в порядке их номеров. Один читатель у
// границы карточки, второй — у сужения ссылок на реране.
func designFreeformItemMediaIDs(params *pb_common.DesignRunParams) []int {
	items := params.GetFreeform().GetItems()
	out := make([]int, 0, len(items))
	for _, it := range items {
		if id := int(it.GetMediaId()); id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// designRefuseFreeformRerunPictureSwap — РЕРАН ПОВТОРЯЕТ ПРОГОН, А НЕ ЗАВОДИТ НОВЫЙ ПОД ЧУЖИМ
// НОМЕРОМ.
//
// ⚠ ЧТО ЭТО ЗАКРЫВАЕТ, ПО ШАГАМ, И ЭТО ПРО СВИДЕТЕЛЬСТВО, А НЕ ПРО ДЕНЬГИ. У плейграунда ссылки
// снимка не СУЖАЮТСЯ до названных, как у перекраса и паттерна, а ПЕРЕСОБИРАЮТСЯ из `params`
// (designRunInputs) — потому что запись плейграунда несёт ещё и выноски, а реран вправе разметить
// те же картинки иначе. Довод верен ровно наполовину: он объясняет, почему меняться могут ОБЛАСТИ,
// и ничего не говорит про то, почему могли меняться КАРТИНКИ. А они могли: реран прогона,
// показавшего модели снимок 11, с `params.freeform.items=[88]` уезжал с картинкой 88 — и оставался
// в истории с `rerun_of = <тот прогон>`, то есть утверждал повтор того, что никогда не повторял.
// Строка истории — это то, чем карточка доказывает своё происхождение; строка, показывающая на
// родителя, с которым у неё нет ни одного общего входа, доказывает неправду.
//
// ⚠ СРАВНИВАЮТСЯ МНОЖЕСТВА, А НЕ СПИСКИ, И ЭТО НЕ СНИСХОДИТЕЛЬНОСТЬ. Порядок картинок — это их
// НОМЕРА в промпте; переставить их и переразметить области — законная правка просьбы, ровно та, ради
// которой реран вообще принимает `params`. Незаконно только одно: показать модели ДРУГИЕ картинки.
//
// ⚠ СПРАШИВАЕТСЯ С ГОВОРЯЩЕГО. Молчащий реран наследует параметры родителя целиком, значит
// множества совпадают по построению и проверять нечего. (Phase 2: an empty list is a legal
// text → image run, so it is compared like any other set — see below.)
func designRefuseFreeformRerunPictureSwap(kind string, spoken *pb_common.DesignRunParams,
	parentID int, parentParams []byte) error {
	if kind != entity.DesignRunKindFreeform || spoken == nil || parentID <= 0 {
		return nil
	}
	now := designFreeformItemMediaIDs(spoken)
	if len(now) == 0 && spoken.GetFreeform() == nil {
		// No playground block at all: somebody else's refusal (the preset table).
		return nil
	}
	inherited := &pb_common.DesignRunParams{}
	if len(parentParams) > 0 {
		if err := designUnmarshalJSON(parentParams, inherited); err != nil {
			return status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored parameters do not parse", parentID)
		}
	}
	was := designFreeformItemMediaIDs(inherited)
	// ⚠ PHASE 2: AN EMPTY LIST IS A LEGAL RUN, NOT A REFUSED ONE. `free` with words and no picture
	// is text → image, so «no pictures» is now a picture set like any other: a words-only rerun of
	// a pictured run drops them all, and a pictured rerun of a words-only run adds them — both a new
	// run under an old number. Two empty sets are the same set.
	if len(was) == 0 && len(now) == 0 {
		return nil
	}
	parentSet := make(map[int]struct{}, len(was))
	for _, id := range was {
		parentSet[id] = struct{}{}
	}
	childSet := make(map[int]struct{}, len(now))
	for _, id := range now {
		childSet[id] = struct{}{}
	}
	added := designSortedMissing(childSet, parentSet)
	dropped := designSortedMissing(parentSet, childSet)
	if len(added) == 0 && len(dropped) == 0 {
		return nil
	}
	return designRefusal(codes.InvalidArgument, "rerun_changes_pictures",
		fmt.Sprintf("a rerun repeats the run it points at: run %d worked on picture(s) %s, and this "+
			"one names %s. Areas, words and the order of the pictures may change; the pictures "+
			"themselves may not — start a new run instead of a rerun. Nothing was reserved and "+
			"nothing was charged",
			parentID, designJoinIDsOrNone(was), designJoinIDsOrNone(now)),
		map[string]string{
			"rerun_of": strconv.Itoa(parentID),
			"added":    designJoinIDs(added),
			"dropped":  designJoinIDs(dropped),
		})
}

// designSortedMissing — члены a, которых нет в b, по возрастанию: отказ обязан быть одинаковым при
// одинаковом запросе, а обход map таковым не бывает.
func designSortedMissing(a, b map[int]struct{}) []int {
	out := make([]int, 0, len(a))
	for id := range a {
		if _, ok := b[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out
}

// designJoinIDsOrNone — designJoinIDs for a sentence: a words-only run names «none».
func designJoinIDsOrNone(ids []int) string {
	if len(ids) == 0 {
		return "none"
	}
	return designJoinIDs(ids)
}

func designJoinIDs(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, ", ")
}

// designRefuseFreeformOverflow — ВЛЕЗАЕТ ЛИ ЭТОТ ПРОГОН В ОДИН ВЫЗОВ, посчитано ДО денег.
//
// ⚠ КАРТИНОК УЕЗЖАЕТ БОЛЬШЕ, ЧЕМ ЧЕЛОВЕК ПОЛОЖИЛ, И В ЭТОМ ВСЯ ПРОВЕРКА. Каждая размеченная
// область рождает КРОП (отдельная картинка), а каждая картинка, на которой есть хоть одна область,
// рождает ещё и РАЗМЕЧЕННУЮ КОПИЮ с контурами. Восемь картинок с четырьмя областями каждая — это
// 8 + 32 + 8 = 48 ссылок при потолке провайдера 16, и без этой арифметики отказ пришёл бы от
// поставщика — после резерва, а на асинхронном маршруте и после списания.
//
// ЧИСЛО БЕРЁТСЯ У ПОСТАВЩИКА (orimages.MaxInputReferences), а не переписывается сюда: копия
// разошлась бы молча и в ту сторону, в которую дороже. Формула ОБЯЗАНА совпадать с тем, что
// действительно собирает designgen (buildJob → deriveFreeform); проба держит обе половины рядом.
func designRefuseFreeformOverflow(kind string, params *pb_common.DesignRunParams) error {
	if kind != entity.DesignRunKindFreeform {
		return nil
	}
	pictures, regions, marked := designFreeformImageCounts(params)
	total := pictures + regions + marked
	if total <= orimages.MaxInputReferences {
		return nil
	}
	return designRefusal(codes.InvalidArgument, "too_many_pictures",
		fmt.Sprintf("this run would send %d images in one call — %d pictures, %d close crops of the "+
			"marked areas and %d copies with the outlines drawn on them — and the provider takes at "+
			"most %d. Remove a picture or a marked area. Nothing was reserved and nothing was charged",
			total, pictures, regions, marked, orimages.MaxInputReferences),
		map[string]string{
			"images":   strconv.Itoa(total),
			"pictures": strconv.Itoa(pictures),
			"crops":    strconv.Itoa(regions),
			"outlined": strconv.Itoa(marked),
			"ceiling":  strconv.Itoa(orimages.MaxInputReferences),
		})
}

// designFreeformRefs — ВХОДЫ ПЛЕЙГРАУНДА В СНИМКЕ: одна запись на картинку, с областями,
// замороженными готовым сообщением.
//
// ⚠ ОБЛАСТЬ ЗАМЕРЗАЕТ ВЫНОСКОЙ (DesignMoodCallout), А НЕ ПАРОЙ СПИСКОВ. Снимок — это то, что
// ушло в модель, и «область A ↔ слова про область A» обязано читаться из него ОДНИМ элементом:
// два параллельных списка в замороженной строке разъезжаются при первом же чтении вполглаза, и
// история начинает утверждать про картинку то, чего про неё не говорили.
//
// ПОДПИСЬ БЕЗ ОБЛАСТИ СТАНОВИТСЯ ЗАПИСКОЙ (`Note`) — тем же полем, которым говорит записка
// референса: это слова ПРО КАРТИНКУ ЦЕЛИКОМ, и другого места для них в снимке нет.
func designFreeformRefs(params *pb_common.DesignRunParams) []*pb_common.DesignInputRef {
	items := params.GetFreeform().GetItems()
	out := make([]*pb_common.DesignInputRef, 0, len(items))
	seen := make(map[int32]struct{}, len(items))
	for _, it := range items {
		id := it.GetMediaId()
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			// Одна и та же картинка, названная дважды, — одна запись снимка. Сборка ссылок
			// дедуплицирует так же (referenceList.add), значит снимок и вложения совпадают.
			continue
		}
		seen[id] = struct{}{}
		ref := &pb_common.DesignInputRef{MediaId: id, Role: it.GetRole()}
		texts := it.GetTexts()
		for i, region := range it.GetRegions() {
			text := ""
			if i < len(texts) {
				text = texts[i]
			}
			ref.Callouts = append(ref.Callouts, &pb_common.DesignMoodCallout{
				MediaId:    id,
				Text:       text,
				Annotation: region,
			})
		}
		// Хвостовая подпись — та, которой не досталось области.
		if len(texts) > len(it.GetRegions()) {
			ref.Note = texts[len(it.GetRegions())]
		}
		out = append(out, ref)
	}
	return out
}

// designRefuseTryonProductNotColourwayRender — A TRY-ON NAMING A PRODUCT COLOURWAY DRESSES THE
// PERSON IN THAT COLOURWAY'S FABRIC RENDERS (G-02, Codex 10). The owner's tile 1 field is «продукт
// колорвей из наших колорвеев из фабрик рендер»: the product picture comes from the fabric renders
// of one of this card's colourways, and options.product_colorway_id is that colourway — provenance
// the history shows («worn: colourway X»). Accepting any id (999999) beside any card picture would
// file one garment under another colourway's name.
//
// FREE: it reads the band StartDesignRun already loaded (the bench and the whole-card outputs — the
// same pools the client's colourway-render picker draws from, cardPictureGroups), no second read.
// Every role=product item must be a picture of kind `render` of colourway X there (its own
// colorway_id, or a render-bench slot of X). X therefore also names a colourway of THIS card: a
// colourway of another card has no render on this band.
//
// ASKED OF THE SPEAKER (the same boundary as the other provenance words): a silent rerun inherits a
// claim this door verified when it was spoken, with the same pictures (the picture-swap guard), and
// a render that has since left the 60-newest window must not make it unrerunnable. 0 = no colourway
// claimed (the client sends it for a colourway-less render): nothing to verify.
func designRefuseTryonProductNotColourwayRender(kind string, spoken *pb_common.DesignRunParams, band *entity.DesignBand) error {
	ff := spoken.GetFreeform()
	cw := int(ff.GetOptions().GetProductColorwayId())
	if kind != entity.DesignRunKindFreeform || ff.GetPreset() != entity.DesignFreeformPresetTryon || cw <= 0 {
		return nil
	}
	renders := map[int]bool{}
	isRenderOf := func(p *entity.DesignPicture) bool {
		return p != nil && p.Kind == entity.DesignPictureKindRender && p.ColorwayId.Valid && int(p.ColorwayId.Int32) == cw
	}
	if band != nil {
		for i := range band.Outputs {
			if p := &band.Outputs[i].Picture; isRenderOf(p) {
				renders[p.MediaId] = true
			}
		}
		for _, slot := range band.Bench {
			p := slot.Picture
			if p == nil || p.Kind != entity.DesignPictureKindRender {
				continue
			}
			if isRenderOf(p) || (slot.Kind == entity.DesignPictureKindRender && slot.ColorwayId.Valid &&
				int(slot.ColorwayId.Int32) == cw) {
				renders[p.MediaId] = true
			}
		}
	}
	for i, it := range ff.GetItems() {
		if it.GetRole() != entity.DesignFreeformRoleProduct || renders[int(it.GetMediaId())] {
			continue
		}
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeProductNotColorwayRender,
			fmt.Sprintf("params.freeform.items.%d (picture %d) is not a fabric render of colourway %d of this "+
				"card — pick the garment from that colourway's renders, or clear product_colorway_id. "+
				"Nothing was reserved and nothing was charged", i, it.GetMediaId(), cw),
			map[string]string{"product_colorway_id": strconv.Itoa(cw), "media_id": strconv.Itoa(int(it.GetMediaId()))})
	}
	return nil
}

// designRefuseModelPhotoMismatch — A TRY-ON THAT NAMES A MODEL PROFILE DRESSES A PHOTO OF THAT
// MODEL. options.model_id is provenance the history will show («worn by …»); a role=model picture
// that is not one of that profile's photos (its thumbnail or gallery) would file one person's
// picture under another's name. One read, only for tryon with a model id (every other preset
// refuses the field: option_not_read); the media door (designRefuseForeignMedia) is not widened —
// the item passed it first (D2).
func (s *Server) designRefuseModelPhotoMismatch(ctx context.Context, kind string, params *pb_common.DesignRunParams) error {
	ff := params.GetFreeform()
	id := int(ff.GetOptions().GetModelId())
	if kind != entity.DesignRunKindFreeform || ff.GetPreset() != entity.DesignFreeformPresetTryon || id <= 0 {
		return nil
	}
	m, err := s.repo.Models().GetModelById(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeModelNotFound,
				fmt.Sprintf("params.freeform.options.model_id %d is not a model profile. Nothing was "+
					"reserved and nothing was charged", id),
				map[string]string{"model_id": strconv.Itoa(id)})
		}
		return designError(ctx, "failed to read the model profile of a try-on", err, nil)
	}
	allowed := map[int]struct{}{}
	if m.ThumbnailId.Valid && m.ThumbnailId.Int32 > 0 {
		allowed[int(m.ThumbnailId.Int32)] = struct{}{}
	}
	for _, mid := range m.MediaIds {
		allowed[mid] = struct{}{}
	}
	for i, it := range ff.GetItems() {
		if it.GetRole() != entity.DesignFreeformRoleModel {
			continue
		}
		if _, ok := allowed[int(it.GetMediaId())]; !ok {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeModelPhotoMismatch,
				fmt.Sprintf("params.freeform.items.%d (picture %d) is not a photo of model %d — pick one "+
					"of that profile's photos, or clear model_id. Nothing was reserved and nothing was "+
					"charged", i, it.GetMediaId(), id),
				map[string]string{"model_id": strconv.Itoa(id), "media_id": strconv.Itoa(int(it.GetMediaId()))})
		}
	}
	return nil
}
