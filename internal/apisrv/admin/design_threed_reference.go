package admin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ═══ 3D ИЗ НАЗВАННОЙ КАРТИНКИ (PLAYGROUND phase 2, плитка 12 «Image to 3D», бриф B-09) ═════════
//
// `params.threed.reference_media_ids` (поле 8) — 1..4 картинки, которые человек сам назвал видами
// изделия, в порядке front, back, left, right. Непустой список ПЕРЕКЛЮЧАЕТ ИСТОЧНИК: прогон не
// читает ни верстак, ни референсы карточки, двери верстака (`no_fabric_render`, `no_front_render`)
// к нему не относятся, а `source_picture_ids` остаётся пустым — он называет плиты, а плит не было.
// Пустой список — сегодняшний прогон верстака, и ни одна строка этого файла его не касается.
//
// Поля 9..11 — опции сборки (texture / pbr / quality), поле 12 — `follow`, поле 13 — слова о
// поверхности. Пустая строка у каждой опции — сегодняшняя константа (с текстурой, без PBR,
// стандартная геометрия); так читается всякий прогон, замороженный до полей.

// designThreedMaxReferences — потолок названных картинок. Это потолок ПОСТАВЩИКА (Meshy
// multi-image-to-3d и meshy на fal: «1 to 4 images»), а не вкус: пятой картинке некуда ехать, и
// fal без отказа молча берёт первые четыре — то есть покупает модель не того, что человек назвал.
const designThreedMaxReferences = meshy.MaxImages

// Словари опций — дословно те же слова, что у обоих транспортов (fal.Option*, meshy.Option*).
var (
	designThreedSwitchWords  = []string{"", fal.OptionOn, fal.OptionOff}
	designThreedQualityWords = []string{"", fal.QualityStandard, fal.QualityDetailed}
)

// designThreedReferenceMode — прогон назвал свои картинки сам. Одно выражение на дверь; воркер
// задаёт тот же вопрос тому же полю (designgen.threedReferenceMode).
func designThreedReferenceMode(p *pb_common.DesignRunParams) bool {
	return len(p.GetThreed().GetReferenceMediaIds()) > 0
}

// designRunReadsTheCard — designKindReadsTheCard ДЛЯ ЭТОГО ПРОГОНА, а не для рода.
//
// ⚠ 3D В РЕЖИМЕ РЕФЕРЕНСА НЕ ЧИТАЕТ КАРТОЧКУ, и это тот же довод, что у плейграунда: его вход —
// названные картинки целиком. Отбор плит (designSelectBench) и цикл по референсам карточки
// (designAssembleInputs) обязаны спросить ЭТО, иначе снимок запишет как входы плиты и референсы,
// которых модель не видела, а `source_picture_ids` — плиты, из которых ничего не строилось.
func designRunReadsTheCard(kind string, p *pb_common.DesignRunParams) bool {
	if kind == entity.DesignRunKindThreed && designThreedReferenceMode(p) {
		return false
	}
	return designKindReadsTheCard(kind)
}

// designRunReadsTheGarmentNote — designKindReadsTheGarmentNote ДЛЯ ЭТОГО ПРОГОНА (G-02, Fable m-3).
//
// 3D в режиме референса строится из картинок, которые человек назвал сам, — это не обязательно
// изделие карточки (Reuse'нутая картинка чужой вещи — законный вход плитки 12). Описание изделия
// и посадка карточки уезжают в texture_prompt (surface steer), и такой прогон получал «olive shirt,
// spread collar» про вещь, которой на картинке нет. Тот же довод, что у designRunReadsTheCard:
// спрашивать род недостаточно, спрашивать надо прогон.
func designRunReadsTheGarmentNote(kind string, p *pb_common.DesignRunParams) bool {
	if kind == entity.DesignRunKindThreed && designThreedReferenceMode(p) {
		return false
	}
	return designKindReadsTheGarmentNote(kind)
}

// designThreedReferenceMediaIDs — названные картинки как []int, для границы карточки
// (designRefuseForeignMedia).
func designThreedReferenceMediaIDs(p *pb_common.DesignRunParams) []int {
	return designInt32sToInts(p.GetThreed().GetReferenceMediaIds())
}

// designThreedPhase2Stated — назван ли хоть один из новых полей 8..13.
func designThreedPhase2Stated(t *pb_common.DesignThreedParams) bool {
	return len(t.GetReferenceMediaIds()) > 0 || t.GetTexture() != "" || t.GetPbr() != "" ||
		t.GetQuality() != "" || t.GetFollow() != "" || strings.TrimSpace(t.GetSurfaceHint()) != ""
}

// designRefuseMalformedThreedReferences — ФОРМА режима референса и опций сборки, у ГОВОРЯЩЕГО.
//
// Граница та же, что у designRefuseMalformedFreeform, и довод тот же: словари законно меняются, а
// параметры родителя заморожены, поэтому молчащий реран (`spoken == nil`) не проверяется вовсе.
// Все отказы — InvalidArgument и все ДО денег.
func designRefuseMalformedThreedReferences(kind string, spoken *pb_common.DesignRunParams) error {
	t := spoken.GetThreed()
	if kind != entity.DesignRunKindThreed {
		// ⚠ ЧУЖОЙ РОД С ПОЛЯМИ 3D — ОТКАЗ, А НЕ МОЛЧАНИЕ. Их не читает никто, кроме 3D, и «принято»
		// означало бы «ваши картинки и опции не поедут никуда», за деньги. Старые поля блока
		// (presentation, body …) не трогаются: их молча несли и раньше, и отказ сломал бы старого
		// клиента.
		if designThreedPhase2Stated(t) {
			return designRefusal(codes.InvalidArgument, "threed_forbidden",
				fmt.Sprintf("params.threed reference pictures and build options are read only by a "+
					"threed run; this is a %s run. Nothing was reserved and nothing was charged", kind),
				map[string]string{"kind": kind})
		}
		return nil
	}
	if t == nil {
		return nil
	}
	ids := t.GetReferenceMediaIds()
	if n := len(ids); n > designThreedMaxReferences {
		return designRefusal(codes.InvalidArgument, "too_many_pictures",
			fmt.Sprintf("params.threed.reference_media_ids names %d pictures, and a 3D build reads at "+
				"most %d views of one object — front, back, left, right. Remove a picture. Nothing was "+
				"reserved and nothing was charged", n, designThreedMaxReferences),
			map[string]string{
				"pictures": strconv.Itoa(n),
				"ceiling":  strconv.Itoa(designThreedMaxReferences),
			})
	}
	seen := make(map[int32]int, len(ids))
	for i, id := range ids {
		where := "params.threed.reference_media_ids." + strconv.Itoa(i)
		if id <= 0 {
			return status.Errorf(codes.InvalidArgument, "%s must be a media id", where)
		}
		if first, dup := seen[id]; dup {
			// Одна картинка на двух позициях — это ОДИН вид, названный двумя сторонами: fal-маршрут
			// отказал бы уже после резерва, прямой Meshy построил бы модель из двух одинаковых
			// «сторон». Дёшево сказать здесь.
			return designRefusal(codes.InvalidArgument, "duplicate_picture",
				fmt.Sprintf("%s names picture %d, which params.threed.reference_media_ids.%d already "+
					"names: each position is a different side of the garment. Nothing was reserved "+
					"and nothing was charged", where, id, first),
				map[string]string{"media_id": strconv.Itoa(int(id))})
		}
		seen[id] = i
	}
	for _, o := range []struct{ field, value string }{
		{"texture", t.GetTexture()}, {"pbr", t.GetPbr()},
	} {
		if !designThreedWordIn(o.value, designThreedSwitchWords) {
			return designThreedUnknownOption(o.field, o.value, "on | off")
		}
	}
	if !designThreedWordIn(t.GetQuality(), designThreedQualityWords) {
		return designThreedUnknownOption("quality", t.GetQuality(), "standard | detailed")
	}
	// ⚠ PBR БЕЗ ТЕКСТУРЫ — ОПЦИЯ, КОТОРУЮ НИКТО НЕ ПРОЧТЁТ: оба поставщика пишут про enable_pbr
	// «Requires should_texture to be true». Транспорт отказал бы тем же (fal.ErrBadOption), но уже
	// ПОСЛЕ резерва; здесь это бесплатно.
	if t.GetPbr() == fal.OptionOn && t.GetTexture() == fal.OptionOff {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOptionNotRead,
			"params.threed.pbr = on asks for realistic materials on an untextured model; materials "+
				"are part of the texture, so turn texture on or pbr off. Nothing was reserved and "+
				"nothing was charged",
			map[string]string{"field": "pbr"})
	}
	// `follow` не объявлен ни одним маршрутом фазы 2 (у Meshy нет такого параметра — 04-DECISIONS
	// D7), значит принять его значило бы продать опцию, которая ничего не делает.
	if t.GetFollow() != "" {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOptionNotRead,
			fmt.Sprintf("params.threed.follow = %q: no 3D route of this server reads it, so it would "+
				"change nothing about the build. Leave it empty. Nothing was reserved and nothing was "+
				"charged", t.GetFollow()),
			map[string]string{"field": "follow"})
	}
	return nil
}

// designRefuseThreedRerunReferenceSwap — A 3D RERUN BUILDS THE SAME OBJECT FROM THE SAME VIEWS
// (G-02 M-2 = Codex 1; the unhonoured half of Codex S-02 correction 2).
//
// designRefuseFreeformRerunPictureSwap guards the playground only; a 3D rerun could swap its named
// pictures and keep `rerun_of`: a new model filed under the old run's number. Money is right,
// provenance is not — exactly the defect the freeform guard closes.
//
// ⚠ THE LISTS ARE COMPARED IN ORDER, NOT AS SETS, and that is the difference from the playground.
// There the order is a caption number; here it is the VIEW CLAIM — position 0 is the front, 1 the
// back, 2 left, 3 right (designgen ReferenceViews). [88,91] → [91,88] turns the garment round, so it
// is a different build. Switching bench mode ↔ reference mode (an empty list on one side) is a
// change too: a different source altogether.
//
// Asked of the SPEAKER: a silent rerun inherits the parent's params wholesale and cannot differ.
// A spoken rerun REPLACES params wholesale (designEffectiveParams), so a spoken block that omits
// `threed` IS a bench-mode rerun, and it is compared as one.
func designRefuseThreedRerunReferenceSwap(kind string, spoken *pb_common.DesignRunParams,
	parentID int, parentParams []byte) error {
	if kind != entity.DesignRunKindThreed || spoken == nil || parentID <= 0 {
		return nil
	}
	inherited := &pb_common.DesignRunParams{}
	if len(parentParams) > 0 {
		if err := designUnmarshalJSON(parentParams, inherited); err != nil {
			return status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored parameters do not parse", parentID)
		}
	}
	was, now := designThreedReferenceMediaIDs(inherited), designThreedReferenceMediaIDs(spoken)
	same := len(was) == len(now)
	for i := 0; same && i < len(was); i++ {
		same = was[i] == now[i]
	}
	if same {
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
	return designRefusal(codes.InvalidArgument, "rerun_changes_pictures",
		fmt.Sprintf("a rerun repeats the run it points at: run %d built its 3D model from picture(s) %s "+
			"(front, back, left, right in that order), and this one names %s. For a 3D build the order is "+
			"which side each picture shows, so neither the pictures nor their order may change — start a "+
			"new run instead of a rerun. Nothing was reserved and nothing was charged",
			parentID, designJoinIDsOrBench(was), designJoinIDsOrBench(now)),
		map[string]string{
			"rerun_of": strconv.Itoa(parentID),
			"added":    designJoinIDs(designSortedMissing(childSet, parentSet)),
			"dropped":  designJoinIDs(designSortedMissing(parentSet, childSet)),
			"was":      designJoinIDs(was),
			"now":      designJoinIDs(now),
		})
}

// designJoinIDsOrBench — designJoinIDs for a 3D sentence: an empty list is the render bench.
func designJoinIDsOrBench(ids []int) string {
	if len(ids) == 0 {
		return "none (the render bench)"
	}
	return designJoinIDs(ids)
}

func designThreedWordIn(v string, words []string) bool {
	for _, w := range words {
		if v == w {
			return true
		}
	}
	return false
}

func designThreedUnknownOption(field, value, allowed string) error {
	return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeUnknownOption,
		fmt.Sprintf("params.threed.%s %q is not %s (empty keeps the default). Nothing was reserved "+
			"and nothing was charged", field, value, allowed),
		map[string]string{"field": field, "value": value})
}

// ─────────────────────────── the configured route (G-02, Codex 3 + 4, Fable M-3) ───────────────────────────

// SetDesignThreedRoute wires the configured 3D route (app.go, beside SetDesignEngines).
func (s *Server) SetDesignThreedRoute(r designgen.ThreedRoute) { s.designThreedRoute = &r }

// designThreedRouteReserveBounded — false only when a route IS wired and has no number to reserve
// (fal with FAL_UNIT_USD and no FAL_UNITS_CEILING_3D). The band then lists no image_to_3d and the
// door refuses every 3D run in words.
func (s *Server) designThreedRouteReserveBounded() bool {
	return s.designThreedRoute == nil || s.designThreedRoute.Unbounded() == ""
}

// designThreedNonDefault — the build options this run states with a value that CHANGES the build
// (texture off, pbr on, quality detailed), in band order. A value equal to the route's own constant
// (texture on, pbr off, quality standard, or empty) asks for what every route does anyway, so it is
// never refused — only an option the route would DROP is.
func designThreedNonDefault(t *pb_common.DesignThreedParams) []string {
	out := []string{}
	if t.GetTexture() == fal.OptionOff {
		out = append(out, designgen.ThreedOptionTexture)
	}
	if t.GetPbr() == fal.OptionOn {
		out = append(out, designgen.ThreedOptionPBR)
	}
	if t.GetQuality() == fal.QualityDetailed {
		out = append(out, designgen.ThreedOptionQuality)
	}
	return out
}

// designRefuseThreedRoute — THE CONFIGURED ROUTE MUST READ WHAT THE RUN PAYS FOR, AND ITS RESERVE MUST
// HAVE A NUMBER. On EFFECTIVE params, and deliberately so: this is not vocabulary (which narrows
// legally and is asked of the speaker only) but the route's capability, like the kind gate — a
// silent rerun of a run frozen with pbr=on, on a deployment where PBR is off or the model is the
// hitem3d override, would pay for an option the route drops or for the unmeasured GLB. Free: before
// StartRun.
//
//   - a wired route with no reserve number → threed_reserve_unbounded (FailedPrecondition, the
//     setting named);
//   - a non-default option the route does not read → option_not_read (pbr: DESIGN_THREED_PBR is off;
//     texture / quality: the configured model takes no build options, i.e. the hitem3d override);
//   - no route wired → no option is read (fail closed: nothing on the door knows what would travel).
func (s *Server) designRefuseThreedRoute(kind string, params *pb_common.DesignRunParams) error {
	if kind != entity.DesignRunKindThreed {
		return nil
	}
	r := s.designThreedRoute
	if r != nil {
		if why := r.Unbounded(); why != "" {
			return designRefusal(codes.FailedPrecondition, entity.DesignErrorCodeThreedReserveUnbounded,
				"a 3D build cannot be reserved on this deployment: "+why+". Nothing was reserved and "+
					"nothing was charged",
				map[string]string{"provider": r.Provider})
		}
	}
	for _, o := range designThreedNonDefault(params.GetThreed()) {
		if r != nil && r.Honours(o) {
			continue
		}
		why := "the configured 3D model takes no per-run build options, so it would be dropped"
		if o == designgen.ThreedOptionPBR && (r == nil || r.Honours(designgen.ThreedOptionTexture)) {
			why = "realistic materials are off on this server (DESIGN_THREED_PBR) until their model " +
				"size is measured under the 64 MiB cap"
		}
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOptionNotRead,
			fmt.Sprintf("params.threed.%s: %s — leave it empty (see the band's threed_options). "+
				"Nothing was reserved and nothing was charged", o, why),
			map[string]string{"field": o})
	}
	return nil
}

// ─────────────────────────── цена сборки по её опциям ───────────────────────────

// designThreedCeilingUSDFor — резерв ОДНОЙ сборки 3D при этих опциях: самый дорогой из двух
// маршрутов, по тому же доводу, что у designThreedCeilingUSD (дверь не знает, какой включён).
//
// fal: $1.20 обычная, $1.40 «ultra mode» (detailed) — fal.EstimatedRequestUSDForQuality, то есть ТО
// ЖЕ выражение, которым попытка пишет цену без тарифа. Для сборки без текстуры fal не публикует
// меньшего числа, и оно не выдумывается: остаётся $1.20. Meshy: (20 | 30 кредитов + 5 за detailed)
// × $0.02.
//
// С ПУСТЫМИ ОПЦИЯМИ ЭТО В ТОЧНОСТИ designThreedCeilingUSD() — TestTheDefaultThreedCeilingIsToday.
func designThreedCeilingUSDFor(texture, quality string) decimal.Decimal {
	return decimal.Max(fal.EstimatedRequestUSDForQuality("", quality), designMeshyTaskUSDFor(texture, quality))
}

// designMeshyTaskUSDFor — оценка прямого маршрута Meshy при этих опциях ПО КУРСУ ПО УМОЛЧАНИЮ
// (meshy.EstimatedTaskUSD: опубликованные кредиты × $0.02). С опциями по умолчанию — ровно
// designMeshyTaskCeilingUSD (30 кредитов). Настроенный курс (MESHY_CREDIT_USD) читает маршрут
// (designThreedRunEstimate), а не эта статическая нижняя граница.
func designMeshyTaskUSDFor(texture, quality string) decimal.Decimal {
	return meshy.EstimatedTaskUSD(texture, quality)
}

// designThreedRunEstimate — оценка прогона 3D ПО ЕГО ОПЦИЯМ; ok = false для всякого другого рода
// (там отвечает designEstimateFor). Читает ДЕЙСТВУЮЩИЕ параметры: реран платит за то, что повторяет.
//
// ⚠ И ПО ТАРИФУ НАСТРОЕННОГО МАРШРУТА (G-02, Codex 4). Статический потолок выше считает Meshy по
// $0.02 за кредит и fal по опубликованной цене без тарифа, а собирать деньги будет collect по
// НАСТРОЕННОМУ тарифу (MESHY_CREDIT_USD; FAL_UNIT_USD × единицы). Поэтому резерв —
// max(статический потолок, потолок маршрута): никогда не ниже сегодняшнего числа и никогда не ниже
// того, что запишет collect. Маршрут без числа (fal с тарифом и без FAL_UNITS_CEILING_3D) сюда не
// доходит — его отказывает designRefuseThreedRoute до резерва.
func (s *Server) designThreedRunEstimate(kind string, params *pb_common.DesignRunParams, outputs int) (decimal.NullDecimal, bool) {
	if kind != entity.DesignRunKindThreed {
		return decimal.NullDecimal{}, false
	}
	if outputs < 1 {
		outputs = 1
	}
	t := params.GetThreed()
	per := designThreedCeilingUSDFor(t.GetTexture(), t.GetQuality())
	if r := s.designThreedRoute; r != nil {
		if c, ok := r.CeilingUSD(t.GetTexture(), t.GetQuality()); ok {
			per = decimal.Max(per, c)
		}
	}
	return decimal.NullDecimal{Decimal: per.Mul(decimal.NewFromInt(int64(outputs))), Valid: true}, true
}
