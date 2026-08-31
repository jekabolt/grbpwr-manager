package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// This file is the CRAFT of the flat route: the words that turn "the model drew something" into a
// technical drawing. The owner supplied two reference prompts — one for a multi-view flat sheet,
// one for an enlarged construction-detail callout — and said to adapt them to our case. What is
// adapted is exactly one thing: the LAYOUT paragraph, because our runs do not have three fixed
// views. Views are checkboxes (front, back, side_l, side_r, detail — one to five of them, two
// sides when the garment is asymmetric) and the layout is a run parameter (`one` = every chosen
// view on a single sheet a person later splits; `per_view` = one paid call and one image per
// view). So the layout paragraph is BUILT from the frozen params, while the craft paragraphs
// below are carried through untouched.
//
// ⚠ flatStyle* / flatExcluded* / flatOutput ARE THE OWNER'S WORDING, VERBATIM. They are the
// reference, not a draft: do not tighten, reorder or "improve" them. The tests hold their own
// literal copies precisely so that an improvement here goes red there.
//
// TWO SMALL COHERENCES WORTH KNOWING:
//   - The words say "plain white background" and THE API PARAMETER NOW AGREES: the image route
//     asks for an opaque background (`backgroundFor`, images.go). This paragraph used to explain
//     that the route asked for transparency instead and that the two were coherent anyway. They
//     were — until the default model became one whose catalogue has no `transparent` at all, at
//     which point the request became a 400 on every flat run. The explanation outlived its subject
//     by one wave, which is exactly how a reader "fixes" working code back to broken.
//   - "no text, labels, measurements, callouts" agrees with the product: our callouts are a
//     separate entity drawn OVER the picture, never baked into it.
const (
	// Эталон 1 — silhouette views of a whole garment.
	flatIdentifyGarment = "Automatically identify the garment type and reproduce it exactly: silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, princess seams, darts, pleats, pockets, closures (zippers, buttons, snaps, drawcords), waistband, cuffs, hems and bindings — all true to the reference. Ignore the model, pose, background, lighting and fabric color of the reference; extract only the construction."

	flatStyleGarment = "Style: black vector line art on a plain white background. Uniform, precise lines; heavier weight for outer contours, thin lines for internal design lines; fine dashed lines for topstitching and seam stitching. Garment drawn flat and symmetrical with subtle body-form shaping. No human body, no mannequin, no hanger."

	flatExcludedGarment = "Strictly excluded: color, fills, shading, gradients, shadows, fabric texture or print, logos, text, labels, measurements, callouts, background elements."

	// Эталон 2 — one enlarged construction detail.
	flatIdentifyDetail = "Automatically identify what the detail is (seam, closure, strap, collar, cuff, pocket, hem, binding, hardware, etc.) and reproduce it exactly as constructed: layer order, seam placement, stitch rows, folds, edge finishes, hardware shape and proportions, closure mechanics, and the exact way it attaches to the surrounding panels. Ignore the model, pose, background, lighting, fabric color and texture of the reference; extract only the construction."

	flatStyleDetail = "Style: black vector line art on a plain white background. Heavier weight for outer contours, thin lines for internal design lines, fine dashed lines for topstitching and seam stitching. Flat, technical, true proportions. No human body, no mannequin, no hanger."

	flatExcludedDetail = "Strictly excluded: color, fills, shading, gradients, shadows, fabric texture or print, logos, text, labels, measurements, arrows, background elements."

	// Shared closing paragraph — identical in both of the owner's prompts.
	flatOutput = "Output: high resolution, crisp clean lines, white seamless background, apparel industry technical drawing aesthetic."
)

// flatCraft assembles the craft block for one flat run: intro, identification, the layout
// paragraph built from the frozen params, then the owner's style / exclusions / output verbatim.
//
// `refs` IS THE COUNT OF PICTURES ACTUALLY ATTACHED, handed down from buildJob's resolution —
// not the snapshot's wish list. The intro's grammar hangs on it («the reference image» /
// «images» / no reference at all), and a run whose only reference failed to resolve must get the
// no-reference identification, or the model is told to be faithful to a picture it was never
// shown.
//
// ROUTING BETWEEN THE TWO REFERENCE PROMPTS: a run whose every chosen view is `detail` is a
// detail callout and gets Эталон 2; anything else — silhouette views, or a mix that happens to
// include a detail — gets Эталон 1, with the detail named in the view list as an enlarged
// close-up. The mixed case is not something either reference prompt describes, so the garment
// frame (the more general of the two) carries it.
func flatCraft(p runParams, detailNames []string, refs int) string {
	detail := detailOnlyRun(p.Views)

	identify := flatIdentifyGarment
	style, excluded := flatStyleGarment, flatExcludedGarment
	if detail {
		identify = flatIdentifyDetail
		style, excluded = flatStyleDetail, flatExcludedDetail
	}
	if refs == 0 {
		// The owner's identification paragraphs are written AT a reference image ("all true to
		// the reference", "ignore the model, pose…"). A run launched from words alone has no
		// reference to be true to, and keeping those clauses would tell the model to be faithful
		// to a picture it was never shown.
		identify = flatIdentifyGarmentNoRef
		if detail {
			identify = flatIdentifyDetailNoRef
		}
	}

	paras := []string{
		flatIntro(detail, countDetails(p.Views), refs),
		identify,
		flatLayoutParagraph(p.Views, detailNames, p.Layout, refs),
		style,
		excluded,
		flatOutput,
	}
	return strings.Join(paras, "\n\n")
}

// The no-reference identification variants: the same construction vocabulary as the owner's
// paragraphs, minus every clause that points at a reference image.
const (
	flatIdentifyGarmentNoRef = "Reproduce its construction exactly and explicitly: silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, princess seams, darts, pleats, pockets, closures (zippers, buttons, snaps, drawcords), waistband, cuffs, hems and bindings — all true to the description above."

	flatIdentifyDetailNoRef = "Reproduce it exactly as constructed: layer order, seam placement, stitch rows, folds, edge finishes, hardware shape and proportions, closure mechanics, and the exact way it attaches to the surrounding panels."
)

// flatIntro is the opening sentence, aimed at what the run actually has: the reference image, the
// reference images, or — when a person launched from words alone — the description above it.
//
// `details` IS THE COUNT, NOT A FLAG, because "the construction detail" is a promise of exactly one
// and a run may legitimately ask for several — the very confusion the layout paragraph used to make
// on its own.
func flatIntro(detail bool, details, refs int) string {
	subject, sketch := "the garment", "professional fashion technical flat sketch (CAD-style tech pack drawing)"
	if detail {
		subject, sketch = "the construction detail", "professional fashion technical detail sketch (CAD-style tech pack callout drawing)"
		if details > 1 {
			subject, sketch = "the construction details", "professional fashion technical detail sheet (CAD-style tech pack callout drawings)"
		}
	}
	switch {
	case refs == 1:
		return "Turn " + subject + " shown in the reference image into a " + sketch + "."
	case refs > 1:
		return "Turn " + subject + " shown in the reference images into a " + sketch + "."
	default:
		return "Draw " + subject + " described above as a " + sketch + "."
	}
}

// flatLayoutParagraph is THE paragraph the owner's prompt hard-codes and our runs cannot: which
// views, how many, and on how many canvases all come from the frozen params.
//
// ⚠ LEFT-TO-RIGHT ORDER IS params.views ORDER, NOT THE CANONICAL viewRank ORDER, AND THAT IS
// LOAD-BEARING. When a `one` sheet lands, the store's compositeViewsOf records p.Views VERBATIM
// as "what is glued into this image", and the splitter labels the cut frames from that record.
// The words that told the model what to draw left-to-right and the record that tells the splitter
// what it is looking at must be the SAME list in the SAME order — a prompt that re-sorted the
// views would produce sheets whose split frames are systematically mislabeled. (per_view has the
// same property through ghostViewOf: requested views are handed out by ordinal.)
//
// ⚠ ЭТАЛОН 2 ОТДАЁТСЯ ТОЛЬКО ОДНОЙ ДЕТАЛИ, И ЭТО ИСПРАВЛЕНИЕ, А НЕ ОГРАНИЧЕНИЕ. Его абзац говорит
// «a single enlarged view of the detail» дословно, и отдавался он ЛЮБОМУ прогону, у которого все
// виды — детали. Прогон на две детали получал промпт, где один абзац просит нарисовать воротник И
// карман, а следующий — «одну увеличенную деталь»: модель обязана была выбрать, какому из двух
// предложений подчиниться. Параллельно стор записывал в composite_views ["detail","detail"], и
// человеку предлагалось разрезать пополам лист, на котором один воротник.
//
// ПРИ ДВУХ И БОЛЕЕ ДЕТАЛЯХ РАБОТАЕТ ОБЩИЙ РЯД-АБЗАЦ, А КАДРЫ НАЗЫВАЮТСЯ ИМЕНАМИ (displayViews):
// «left to right: DETAIL — collar …, DETAIL — patch pocket …». Ключ вида здесь не годится по той
// же причине, по которой он не годится нигде в этой волне: он одинаков у обеих деталей.
func flatLayoutParagraph(views, detailNames []string, layout string, refs int) string {
	if singleDetailCanvas(views) {
		// Эталон 2's layout paragraph, verbatim — ONE detail is a single enlarged view by nature,
		// whatever the run's layout says. Only the "same angle as the reference" clause depends
		// on a reference existing.
		if refs > 0 {
			return "Layout: a single enlarged view of the detail, isolated and centered on the canvas, shown from the same angle as the reference. Include just enough of the surrounding garment panel or edge to make the construction readable, with the fragment ending in clean straight edges."
		}
		return "Layout: a single enlarged view of the detail, isolated and centered on the canvas. Include just enough of the surrounding garment panel or edge to make the construction readable, with the fragment ending in clean straight edges."
	}

	names := displayViews(views, detailNames)

	switch {
	case len(views) == 0:
		// A snapshot that names no views (a broken or ancient one) still deserves a flat, not a
		// canvas spec that asks for a row of one.
		return "Layout: the garment drawn once, isolated and centered on the canvas."
	case len(views) == 1:
		// ⚠ ONE VIEW MUST NOT SAY "side by side": a row-of-views spec with a single member asks
		// the model for a canvas holding one small drawing in a row, which is exactly the waste
		// the checkboxes exist to avoid.
		return "Layout: a single view — " + names[0] + " — the garment drawn once, isolated and centered on the canvas."
	case layout == layoutPerView:
		// Case ②: every chosen view is its own image and its own paid call. The base prompt names
		// the whole set so scale stays consistent across the series; viewPrompt then appends the
		// one view this particular call is for.
		return "Layout: one drawing out of a set of " + countWord(len(views)) +
			", each view of the same garment as its own image — " + strings.Join(names, ", ") +
			", one view per image. This image shows exactly one of those views, the garment isolated and centered on the canvas, equal scale and true proportions across the whole set."
	default:
		// Case ③: `one` (and anything unspecified, which the whole band reads as one sheet) —
		// every chosen view on a single horizontal canvas, a person splits it afterwards.
		return "Layout: " + countWord(len(views)) +
			" views on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced — left to right: " +
			strings.Join(names, ", ") + "."
	}
}

// ─── ДВА ВОПРОСА О ДЕТАЛЯХ, И ОНИ РАЗНЫЕ. Один предикат отвечал на оба, и на втором врал.
//
// detailOnlyRun — «прогон РИСУЕТ ТОЛЬКО ДЕТАЛИ», и это выбор ЭТАЛОНА: словарь конструкции
// («identify what the detail is: seam, closure, strap…») и стиль детальной прорисовки. Он верен и
// для двух деталей: лист из двух крупных планов — всё ещё не силуэт изделия, и Эталон 1, который
// велел бы воспроизвести горловину, рукава и пояс, был бы там противоречием, а не улучшением.
//
// singleDetailCanvas — «на холсте РОВНО ОДИН кадр, и это деталь», и это выбор РАСКЛАДКИ: только
// такому прогону принадлежит абзац «a single enlarged view of the detail».
//
// Слить их обратно в одно имя нельзя ни в какую сторону: одно даст двум деталям силуэтный эталон,
// другое — двум деталям обещание одного кадра. Обе ошибки уже были сделаны, вторая — этой волной.
func detailOnlyRun(views []string) bool {
	if len(views) == 0 {
		return false
	}
	for _, v := range views {
		if v != entity.DesignViewDetail {
			return false
		}
	}
	return true
}

func singleDetailCanvas(views []string) bool {
	return len(views) == 1 && views[0] == entity.DesignViewDetail
}

// countDetails — сколько кадров-деталей просит прогон.
func countDetails(views []string) int {
	n := 0
	for _, v := range views {
		if v == entity.DesignViewDetail {
			n++
		}
	}
	return n
}

// displayViews spells the frames of one canvas, IN params.views ORDER, giving each detail frame the
// NAME of the slot it was asked for. The i-th `detail` takes the i-th name — the same positional
// correspondence the whole feature rests on — and an unnamed one keeps the bare view spelling.
func displayViews(views, detailNames []string) []string {
	out := make([]string, 0, len(views))
	seen := 0
	for _, v := range views {
		if v != entity.DesignViewDetail {
			out = append(out, displayView(v))
			continue
		}
		out = append(out, displayDetail(detailNameAt(detailNames, seen)))
		seen++
	}
	return out
}

// displayView spells a view key the way a drawing instruction says it. The raw keys survive on
// the per_view route (viewPrompt appends them per call), so the spellings here stay close enough
// to be read as the same words.
func displayView(v string) string {
	switch v {
	case entity.DesignViewFront:
		return "FRONT"
	case entity.DesignViewBack:
		return "BACK"
	case entity.DesignViewSideL:
		return "SIDE LEFT"
	case entity.DesignViewSideR:
		return "SIDE RIGHT"
	case entity.DesignViewDetail:
		return displayDetail("")
	default:
		return v
	}
}

// displayDetail spells one detail frame. The name goes BEFORE the parenthetical so that a row of
// them reads as a list of different things rather than a repetition of the same word.
func displayDetail(name string) string {
	if name == "" {
		return "DETAIL (one enlarged construction close-up)"
	}
	return "DETAIL — " + name + " (one enlarged construction close-up)"
}

// countWord spells the small counts a layout sentence uses; anything larger falls back to digits.
func countWord(n int) string {
	switch n {
	case 2:
		return "two"
	case 3:
		return "three"
	case 4:
		return "four"
	case 5:
		return "five"
	}
	return strconv.Itoa(n)
}
