package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══════════════════════ РЕМЕСЛО ПЛЕЙГРАУНДА ═══════════════════════
//
// ОДИН АБЗАЦ НА ПРОГОН, как у всех остальных родов, и выбирает его ПРЕСЕТ. Пресет — словарь
// сервера: человек нажимает кнопку, а не пишет этот текст, потому что абзац объясняет модели
// УСТРОЙСТВО ВЛОЖЕНИЙ (что обведено, где кроп, чем контур не является), и человек этого устройства
// не знает — оно собирается здесь же, в этом пакете.
//
// ⚠ НИ ОДИН ИЗ ТРЁХ АБЗАЦЕВ НЕ ГОВОРИТ «change nothing else», И ЭТО ЗАМЕР, А НЕ ВКУС. Маска у
// этого семейства моделей мягкая: обещание «остальное не тронуто» она не исполняет, а обещание,
// данное в промпте и не исполненное моделью, читается человеком как поломка нашего кода. Вместо
// него — «keep the rest as close to image 1 as you can», то есть просьба, которую модель
// действительно может выполнить частично, и экран, который показывает результат рядом с
// исходником (WHAT CAME BACK).
//
// ⚠ И ВСЕ ТРИ ГОВОРЯТ ОДНО И ТО ЖЕ ПРО МЕТКУ. Контур И БУКВА нарисованы НА КАРТИНКЕ, значит по
// умолчанию они часть картинки; без явной строки модель перерисовывает их на выход. Строка общая и
// стоит в каждом абзаце, а не «где нужнее»: пресет выбирает человек, и абзац, в котором её забыли,
// стоил бы кадра с красными линиями и белой буквой «A» поперёк вещи.

// freeformOutlineDisclaimer — общая строка про контур. Одна на три ремесла, потому что правда
// одна; две копии разошлись бы при первой правке.
const freeformOutlineDisclaimer = "an outline and its letter are a hint about WHERE, not a mask: do " +
	"not draw them, and do not leave any trace of them in the picture you return"

// freeformCraft — абзац ремесла этого прогона.
//
// `attached` — картинки, которые действительно уехали, в порядке номеров. Он нужен ровно затем,
// чтобы абзац мог сослаться на номер («the hardware shown in image 2»), а не на «ту картинку, где
// фурнитура»: номер — единственное, что у модели и у нас общее.
func freeformCraft(p runParams, attached []refCaption) string {
	ff := p.Freeform
	if ff == nil {
		return ""
	}
	switch ff.Preset {
	case entity.DesignFreeformPresetAddHardware:
		return freeformAddHardwareCraft(ff, attached)
	case entity.DesignFreeformPresetRepaintParts:
		return freeformRepaintCraft(ff, attached)
	// PLAYGROUND phase 2: one paragraph per tile, every picture named by its NUMBER.
	case entity.DesignFreeformPresetTryon:
		return freeformTryonCraft(ff, attached)
	case entity.DesignFreeformPresetFabricExtract:
		return freeformFabricExtractCraft(ff, attached)
	case entity.DesignFreeformPresetGhostMannequin:
		return freeformGhostMannequinCraft(ff, attached)
	case entity.DesignFreeformPresetAddLogo:
		return freeformAddLogoCraft(ff, attached)
	case entity.DesignFreeformPresetVariations:
		return freeformVariationsCraft(ff, attached)
	case entity.DesignFreeformPresetRetouch:
		return freeformRetouchCraft(ff, attached)
	default:
		if len(attached) == 0 {
			// TEXT → IMAGE (tile 11): there is no picture to read, and a paragraph about reading
			// pictures would describe a call that does not exist.
			return "Work from the words above. Return ONE picture."
		}
		return freeformFreeCraft()
	}
}

// freeformReturnOne ends every phase-2 paragraph; the outline line is added only when some
// picture of the run is marked (a paragraph about outlines on an unmarked run is noise).
func freeformReturnOne(b *strings.Builder, ff *freeformParams) {
	if freeformAnyMarked(ff) {
		b.WriteString(freeformOutlineDisclaimer)
		b.WriteString(". ")
	}
	b.WriteString("Return ONE picture.")
}

func freeformAnyMarked(ff *freeformParams) bool {
	for _, it := range ff.Items {
		if len(it.Regions) > 0 {
			return true
		}
	}
	return false
}

func freeformOptions(ff *freeformParams) workflowOptions {
	if ff.Options == nil {
		return workflowOptions{}
	}
	return *ff.Options
}

// freeformTryonCraft — dress the person of the model photo in the product garment(s).
//
// The ask is already first in the prompt, and on tile 1 it is the owner's field «Modify physical
// features & pose» — so the words above may legitimately change the body, the hair and the pose.
// This paragraph therefore fixes only the IDENTITY (face, skin tone) unconditionally, keeps body,
// hair and pose as in the photo UNLESS the words change them, and then says WHAT they wear, the
// framing, the camera and the scene. «Keep … exactly — body and hair» here would contradict the ask
// it follows, and the history could not say which of the two the model honoured (G-02 M-1).
//
// The product picture is a colourway RENDER — a flat, a render or a photo on white — not a worn
// photo, so the paragraph says so (20-PROMPTS D5): a model not told what the picture IS pastes the
// flat on as a sticker, at the flat's scale and in the flat's studio light. Hence four facts, each
// one line: several product pictures are views of ONE garment (not an outfit to layer); it goes on
// at its real scale and drapes; it takes the scene's light; and the result is a PHOTOGRAPH like the
// model photo, because the product picture is the only drawing in the call and would otherwise
// pull the whole frame toward a render. None of these lines says «frame», «camera» or «angle»: an
// auto framing/angle must say nothing about them (TestTryonFRAMING_AND_ANGLE_ARE_WORDS_OR_NOTHING).
func freeformTryonCraft(ff *freeformParams, attached []refCaption) string {
	o := freeformOptions(ff)
	model := freeformImageNumber(ff, attached, entity.DesignFreeformRoleModel)
	var b strings.Builder
	b.WriteString("Keep the identity of the person of ")
	b.WriteString(freeformImageWord(model, "the model photo"))
	b.WriteString(" — the same face and skin tone; their body, hair and pose stay as in the photo " +
		"except where the words above change them. Dress them in the garment of ")
	b.WriteString(freeformImageList(freeformImageNumbers(ff, attached, entity.DesignFreeformRoleProduct),
		"the product picture"))
	b.WriteString(": it is a product picture (a drawing, a render or a photo on white, and several " +
		"pictures are views of the same garment) — put that garment on the person at its real scale, " +
		"draped the way that garment really sits on a body, and reproduce its cut, colour, print and " +
		"seams as they are. Light it with the light of the scene so it belongs to the photograph. ")
	if w := freeformFramingWords(o.Framing); w != "" {
		b.WriteString(w + " ")
	}
	if w := freeformAngleWords(o.Angle); w != "" {
		b.WriteString(w + " ")
	}
	sceneText := oneLine(o.SceneText)
	switch {
	case o.SceneMode == entity.DesignSceneModeReference:
		b.WriteString("The scene is ")
		b.WriteString(freeformImageWord(freeformImageNumber(ff, attached, entity.DesignFreeformRoleScene),
			"the scene picture"))
		b.WriteString(": use it as the background and take its light. ")
		if sceneText != "" {
			b.WriteString("Scene: " + sceneText + ". ")
		}
	case sceneText != "":
		b.WriteString("Scene: " + sceneText + ". ")
	default:
		b.WriteString("Keep the scene of the model photo. ")
	}
	b.WriteString("The result is a photograph with the same lens and realism as the model photo. ")
	freeformReturnOne(&b, ff)
	return b.String()
}

// freeformFramingWords — the tryon framing as a sentence; auto and empty say nothing.
func freeformFramingWords(v string) string {
	switch v {
	case entity.DesignFramingFullBody:
		return "Frame it full-length, head to feet."
	case entity.DesignFramingUpperBody:
		return "Frame it from the waist up."
	case entity.DesignFramingPortrait:
		return "Frame it as a portrait: face and neck."
	case entity.DesignFramingHands:
		return "Frame it as a close-up of the hands."
	case entity.DesignFramingFeet:
		return "Frame it as a close-up of the feet."
	case entity.DesignFramingProductDetail:
		return "Frame it as a close view of the garment as worn."
	}
	return ""
}

// freeformAngleWords — the tryon camera angle as a sentence; auto and empty say nothing.
func freeformAngleWords(v string) string {
	switch v {
	case entity.DesignAngleEyeLevel:
		return "The camera is at eye level."
	case entity.DesignAngleSlightlyAbove:
		return "The camera is slightly above, looking a little down."
	case entity.DesignAngleSlightlyBelow:
		return "The camera is slightly below, looking a little up."
	case entity.DesignAngleLowAngle:
		return "A low angle, from the ground."
	}
	return ""
}

// freeformFabricExtractCraft — the cloth of one picture as a flat tileable swatch.
//
// The swatch is an ASSET, not a picture of cloth (20-PROMPTS D6): it is tiled onto patterns and
// renders later, so what makes it usable is that the print keeps its true colours and SCALE (a
// motif enlarged to fill the frame reads as a different print once tiled) and that at least one
// full repeat sits in the frame (half a repeat cannot tile without a visible seam). «nothing else
// in the picture» replaces the old «no garment shape, no folds, no shadows» list: one positive
// rule covers the hanger, the label and the hand the list forgot.
func freeformFabricExtractCraft(ff *freeformParams, attached []refCaption) string {
	var b strings.Builder
	b.WriteString("From ")
	b.WriteString(freeformImageWord(freeformImageNumber(ff, attached, ""), "the picture"))
	b.WriteString(" extract the fabric named in the words above (the main fabric if they name none) " +
		"as a flat, evenly lit, front-on, seamless tileable swatch: the cloth lies perfectly flat and " +
		"fills the whole frame, nothing else in the picture. Keep the print's true colours, motif and " +
		"scale and the weave or knit as it is; centre at least one full repeat so the swatch tiles " +
		"without a visible seam. ")
	freeformReturnOne(&b, ff)
	return b.String()
}

// freeformGhostMannequinCraft — the garment of one picture as an invisible-body product shot.
//
// The catalogue facts that separate a usable shot from a pretty one (20-PROMPTS D6): the inner
// back neck AND the label (the one detail a ghost shot exists to show), centred with a small margin
// (the site crops to a fixed box, and a garment touching the edge loses a sleeve there), true
// colours (the shot sells the colourway). The closing negatives name what these models actually
// leave in: the mannequin itself, the hanger, a hand at the hem, a drop shadow on the white.
func freeformGhostMannequinCraft(ff *freeformParams, attached []refCaption) string {
	src := freeformImageWord(freeformImageNumber(ff, attached, ""), "the picture")
	var b strings.Builder
	b.WriteString("Recreate the garment of " + src + " (the one the words above name, if they name one) " +
		"as a ghost-mannequin e-commerce shot: its worn 3D shape on an invisible body, the inside of the " +
		"back neck and the label visible, front-on and centred with a small margin, on a pure white " +
		"seamless background, soft studio light, true colours. Keep every seam, print and piece of " +
		"hardware exactly as in " + src + "; no mannequin, hanger, body parts or shadow on the background. ")
	freeformReturnOne(&b, ff)
	return b.String()
}

// freeformLogoWidthCM — the add_logo size as a width on the garment (empty = medium).
func freeformLogoWidthCM(size string) string {
	switch size {
	case entity.DesignLogoSizeSmall:
		return "6"
	case entity.DesignLogoSizeLarge:
		return "16"
	}
	return "10"
}

// freeformAddLogoCraft — the logo of one picture onto the garment of the other.
//
// The logo PNG arrives with its own background, and a model told only «place the logo» pastes that
// background too — a white box on a black tee (20-PROMPTS D6). So the paragraph says HOW it is on
// the cloth (a print or embroidery, i.e. part of the fabric, not a sticker over it), that its
// background is transparent with no box or patch, and that it is not stretched: the width in cm
// is the one size fact, and a model fitting a wide wordmark into a pocket squeezes it instead of
// scaling it.
func freeformAddLogoCraft(ff *freeformParams, attached []refCaption) string {
	o := freeformOptions(ff)
	garment := freeformImageWord(freeformImageNumber(ff, attached, ""), "the garment picture")
	var b strings.Builder
	b.WriteString("Place the logo of ")
	b.WriteString(freeformImageWord(freeformImageNumber(ff, attached, entity.DesignFreeformRoleLogo),
		"the logo picture"))
	b.WriteString(" on the garment of " + garment + " at the place the words above say, about " +
		freeformLogoWidthCM(o.LogoSize) + " cm wide, applied as a print or embroidery on the cloth: its " +
		"background is transparent (no box or patch around it), its exact shape, colours and letterforms " +
		"are kept, it is not stretched, and it follows the folds of the fabric and takes the light of the " +
		"picture. Keep the rest of the picture as close to " + garment + " as you can. ")
	freeformReturnOne(&b, ff)
	return b.String()
}

// freeformVariationsCraft — a variation of one design; creativity 0..3 sets how far it may go.
//
// Every level ends with the same presentation sentence (20-PROMPTS D7): the creativity step moves
// the DESIGN, never the picture. Without it a variation of a flat comes back as a photo on a
// street, and the two can no longer be compared side by side — which is the whole point of the
// tile. The sentence names the source by number, like the level sentence before it.
func freeformVariationsCraft(ff *freeformParams, attached []refCaption) string {
	src := freeformImageWord(freeformImageNumber(ff, attached, ""), "the picture")
	var b strings.Builder
	switch freeformOptions(ff).Creativity {
	case 0:
		b.WriteString("Make a faithful variation of the design in " + src + ": keep its silhouette, " +
			"proportions and colours, and change only what the words above ask. ")
	case 1:
		b.WriteString("Make a variation of the design in " + src + ": keep its silhouette and palette, " +
			"and reinterpret its details. ")
	case 2:
		b.WriteString("Make a variation of the design in " + src + ": keep the garment type and its " +
			"mood, and be free with the cut, the length and the materials. ")
	default:
		b.WriteString("Use " + src + " only as inspiration for a new design. ")
	}
	b.WriteString("Show it the same way as " + src + " — the same kind of picture (a drawing, a render " +
		"or a photo), the same view and the same background — so the two read side by side. ")
	freeformReturnOne(&b, ff)
	return b.String()
}

// freeformRetouchCraft — change ONE marked zone of ONE picture.
//
// Phase 2 is a crop-and-paste window, not a mask (window.go): under a window the model sees only
// the crop around the zone and the rest of the frame is restored pixel for pixel by our
// composite; without one the model is asked, and may only partly comply.
func freeformRetouchCraft(ff *freeformParams, attached []refCaption) string {
	area := freeformAreaLetter(0)
	var b strings.Builder
	if w := freeformWindowNumber(attached); w > 0 {
		b.WriteString("Image " + strconv.Itoa(w) + " is a CLOSE CROP around area " + area + " of a larger " +
			"photograph, not an object of its own; its caption says where in the crop the area is. " +
			"Change only inside area " + area + ", as the words say; leave everything outside it pixel " +
			"for pixel as it is. Return the SAME CROP, at the same framing and the same size: it is " +
			"going to be fitted straight back into the photograph it was cut from. ")
		// NO OUTLINE DISCLAIMER HERE (G-02, Fable m-6): the crop carries no outline and no letter —
		// the area is located only by the caption's percentages — so a sentence about outlines
		// would describe a picture the model was not given.
		b.WriteString("Return ONE picture.")
		return b.String()
	}
	src := freeformImageWord(freeformImageNumber(ff, attached, ""), "the picture")
	b.WriteString("Change only area " + area + " on " + src + ", as the words say. Everything " +
		"outside the marked area stays pixel-identical to " + src + ": the same garment, the same " +
		"background, the same light. ")
	b.WriteString(freeformOutlineDisclaimer)
	b.WriteString(". Return ONE picture.")
	return b.String()
}

// freeformFreeCraft — «делай по словам».
//
// ⚠ АБЗАЦ КОРОТКИЙ, И ЭТО ЕГО СОДЕРЖАНИЕ. Пресет `free` существует ровно затем, чтобы сервер НЕ
// добавлял к просьбе человека собственной эстетики: всё, что здесь сказано, — как читать
// вложения и сколько картинок вернуть. Любая вторая фраза про свет, стиль или фон была бы
// инструкцией, которой человек не давал, и она стояла бы ПОСЛЕ его слов, то есть перевешивала их.
func freeformFreeCraft() string {
	return "Work from the words above and from the pictures given with them. " +
		"The pictures are numbered in the reference list; where an area of a picture is marked, " +
		"the marked copy and the close crop of that area follow it under their own numbers. " +
		freeformOutlineDisclaimer + ". Return ONE picture."
}

// freeformAddHardwareCraft — «посади фурнитуру с одной картинки в обведённое место другой».
//
// ⚠ ЭТО САМАЯ ХРУПКАЯ ИЗ ТРЁХ ПРОСЬБ, И АБЗАЦ ЧЕСТНО НАЗЫВАЕТ, ЧТО ИМЕННО ДОЛЖНО СОВПАСТЬ:
// масштаб, перспектива и свет. Без этих трёх слов модель сажает предмет «правильно» по форме и
// неправильно по кадру — пряжка в анфас на боковом снимке, — а человек видит просто плохой
// результат и не знает, что попросить иначе.
func freeformAddHardwareCraft(ff *freeformParams, attached []refCaption) string {
	if n := freeformWindowNumber(attached); n > 0 {
		return freeformWindowedHardwareCraft(ff, attached, n)
	}
	subject := freeformImageNumber(ff, attached, "")
	hardware := freeformImageNumber(ff, attached, entity.DesignFreeformRoleHardware)
	var b strings.Builder
	b.WriteString("Take the hardware shown in ")
	b.WriteString(freeformImageWord(hardware, "the picture of the hardware"))
	b.WriteString(" and put it onto ")
	b.WriteString(freeformImageWord(subject, "the garment picture"))
	b.WriteString(" inside the outlined area. Match its scale, its perspective and the light of the " +
		"picture it lands in, and let it sit on the garment the way that piece of hardware really " +
		"would — with its own shadow and its own reflections. Keep the rest of the picture as close " +
		"to the original as you can: the same garment, the same pose, the same framing, the same " +
		"background and the same light. ")
	b.WriteString(freeformOutlineDisclaimer)
	b.WriteString(". Return ONE picture.")
	return b.String()
}

// freeformWindowNumber — номер картинки-окна в вызове, или 0.
//
// Читается по ФЛАГУ, поставленному при сборке списка, а не по подписи и не по позиции: см.
// refCaption.IsWindow.
func freeformWindowNumber(attached []refCaption) int {
	for i, rc := range attached {
		if rc.IsWindow {
			return i + 1
		}
	}
	return 0
}

// freeformWindowedHardwareCraft — ремесло ОКОННОГО прогона.
//
// ⚠ ОНО ОБЯЗАНО СКАЗАТЬ, ЧТО ПЕРВАЯ КАРТИНКА — КРОП, И ЭТО НЕ ВЕЖЛИВОСТЬ. Модель, которой дали
// крупный кусок ткани с петлёй и не сказали, что это кусок, читает его как самостоятельный предмет
// и отвечает натюрмортом на новом фоне — а мы этот ответ вклеим в кадр, и в кадре появится
// прямоугольник чужого фона.
//
// И ОНО НЕ ПРОСИТ «сохрани остальное»: остального модель не видит вовсе, а сохраняет его наш
// композит — буквально, пиксель в пиксель. Просьба, которую нельзя не выполнить, — лишние слова
// перед той, которую выполнить можно.
func freeformWindowedHardwareCraft(ff *freeformParams, attached []refCaption, window int) string {
	hardware := freeformImageNumber(ff, attached, entity.DesignFreeformRoleHardware)
	var b strings.Builder
	b.WriteString("Image ")
	b.WriteString(strconv.Itoa(window))
	b.WriteString(" is a CLOSE CROP of a garment — a small part of a larger photograph, not an " +
		"object of its own. Take the hardware shown in ")
	b.WriteString(freeformImageWord(hardware, "the picture of the hardware"))
	b.WriteString(" and put it into the outlined area of that crop. Match its scale, its perspective " +
		"and the light of the crop it lands in, and let it sit on the garment the way that piece of " +
		"hardware really would — with its own shadow and its own reflections. Return the SAME CROP, " +
		"at the same framing and the same size, with the hardware now on it: it is going to be fitted " +
		"straight back into the photograph it was cut from. ")
	b.WriteString(freeformOutlineDisclaimer)
	b.WriteString(". Return ONE picture.")
	return b.String()
}

// freeformRepaintCraft — «перекрась обведённое».
//
// ⚠ БЕЗ ЕДИНОЙ ОБЛАСТИ ПРОСЬБА ЗАКОННА И ЗНАЧИТ «ВСЮ ВЕЩЬ», и это сказано отдельной ветвью, а не
// оставлено на «модель догадается»: абзац, говорящий «repaint only the outlined areas» там, где
// областей нет, — это инструкция, которую невозможно исполнить, и модель ответит на неё чем
// угодно.
func freeformRepaintCraft(ff *freeformParams, attached []refCaption) string {
	subject := freeformImageNumber(ff, attached, "")
	cloth := freeformImageNumber(ff, attached, entity.DesignFreeformRoleCloth)
	marked := false
	for _, it := range ff.Items {
		if len(it.Regions) > 0 {
			marked = true
			break
		}
	}
	var b strings.Builder
	if marked {
		b.WriteString("Repaint ONLY the outlined areas of ")
		b.WriteString(freeformImageWord(subject, "the garment picture"))
		b.WriteString(". Everything outside those areas stays exactly as it is. ")
	} else {
		b.WriteString("Repaint the whole garment in ")
		b.WriteString(freeformImageWord(subject, "the garment picture"))
		b.WriteString(". ")
	}
	if cloth > 0 {
		b.WriteString("Use the cloth shown in image ")
		b.WriteString(strconv.Itoa(cloth))
		b.WriteString(" — its colour, its weave and its finish. ")
	} else {
		b.WriteString("Use the colour described above. ")
	}
	b.WriteString("Keep the cut, the seams, the folds and the shadows of the garment exactly as they " +
		"are: this is the same garment in a different cloth, not a new one. ")
	b.WriteString(freeformOutlineDisclaimer)
	b.WriteString(". Return ONE picture.")
	return b.String()
}

// freeformImageWord — «image N» или, если такой картинки в вызове нет, честное описание словами.
//
// ⚠ НОМЕР, КОТОРОГО НЕТ, НЕ ПЕЧАТАЕТСЯ. Картинка могла не пережить резолв медиа (строку удалили
// между снимком и проходом); «put the hardware from image 2» при двух уехавших картинках — это
// указание в пустоту, и модель выполнит его наугад. Слова вместо номера теряют точность и не
// теряют правды.
func freeformImageWord(number int, fallback string) string {
	if number <= 0 {
		return fallback
	}
	return "image " + strconv.Itoa(number)
}

// freeformImageNumber — номер ПЕРВОЙ картинки этой роли в вызове, или 0.
//
// Пустая роль означает «первая картинка, которая не фурнитура и не ткань» — то есть та, НАД
// которой работают. Роль ставит человек, и ставит не всегда: `free` не спрашивает её вовсе,
// поэтому «предмет» здесь выводится вычитанием, а не требованием.
func freeformImageNumber(ff *freeformParams, attached []refCaption, role string) int {
	numberOf := make(map[int]int, len(attached))
	for i, rc := range attached {
		if rc.MediaID > 0 {
			if _, dup := numberOf[rc.MediaID]; !dup {
				numberOf[rc.MediaID] = i + 1
			}
		}
	}
	for _, it := range ff.Items {
		if !freeformRoleMatches(it.Role, role) {
			continue
		}
		if n, ok := numberOf[it.MediaID]; ok {
			return n
		}
	}
	return 0
}

// freeformRoleMatches — an empty want asks for «the picture being worked on»: any role that is not a
// supporting one (hardware, cloth, and the phase-2 model / product / scene / logo).
func freeformRoleMatches(itemRole, want string) bool {
	if want != "" {
		return itemRole == want
	}
	switch itemRole {
	case entity.DesignFreeformRoleHardware, entity.DesignFreeformRoleCloth,
		entity.DesignFreeformRoleModel, entity.DesignFreeformRoleProduct,
		entity.DesignFreeformRoleScene, entity.DesignFreeformRoleLogo:
		return false
	}
	return true
}

// freeformImageNumbers — the number of EVERY attached picture of this role, in items order.
func freeformImageNumbers(ff *freeformParams, attached []refCaption, role string) []int {
	numberOf := make(map[int]int, len(attached))
	for i, rc := range attached {
		if rc.MediaID > 0 {
			if _, dup := numberOf[rc.MediaID]; !dup {
				numberOf[rc.MediaID] = i + 1
			}
		}
	}
	var out []int
	for _, it := range ff.Items {
		if !freeformRoleMatches(it.Role, role) {
			continue
		}
		if n, ok := numberOf[it.MediaID]; ok {
			out = append(out, n)
		}
	}
	return out
}

// freeformImageList — «image 2», «images 2 and 3», «images 2, 3 and 4», or the fallback words.
func freeformImageList(numbers []int, fallback string) string {
	switch len(numbers) {
	case 0:
		return fallback
	case 1:
		return "image " + strconv.Itoa(numbers[0])
	}
	words := make([]string, len(numbers))
	for i, n := range numbers {
		words[i] = strconv.Itoa(n)
	}
	return "images " + strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// freeformReferences — СПИСОК КАРТИНОК ПЛЕЙГРАУНДА, в порядке items, каждая со своей подписью.
//
// ⚠ ПОДПИСЬ ЕСТЬ У КАЖДОЙ, ДАЖЕ У БЕЗМОЛВНОЙ. Картинка без строки в блоке «references» сдвигает
// нумерацию всех следующих подписей — тот самый дефект, ради которого refCaption и существует, —
// поэтому «человек ничего про неё не сказал» тоже говорится словами.
//
// РОЛЬ ПЕЧАТАЕТСЯ ПЕРВОЙ, потому что она отвечает на вопрос «чем эта картинка является в просьбе»
// («the hardware», «the cloth»), а слова человека — на вопрос «что с ней делать». Роль пустая
// молчит: выдуманная роль соврала бы модели.
func freeformReferences(p runParams) []refCaption {
	if p.Freeform == nil {
		return nil
	}
	out := make([]refCaption, 0, len(p.Freeform.Items))
	seen := make(map[int]struct{}, len(p.Freeform.Items))
	for _, it := range p.Freeform.Items {
		if it.MediaID <= 0 {
			continue
		}
		if _, dup := seen[it.MediaID]; dup {
			// Одна картинка — одно место в списке и один номер. Дубль дал бы два номера у одного
			// адреса, а подписи после него поехали бы на единицу.
			continue
		}
		seen[it.MediaID] = struct{}{}
		out = append(out, refCaption{MediaID: it.MediaID, Caption: freeformItemCaption(it)})
	}
	return out
}

// freeformItemCaption — подпись одной картинки плейграунда.
//
// СЛОВА БЕРУТСЯ ТОЛЬКО ХВОСТОВЫЕ — те, что описывают картинку ЦЕЛИКОМ. Слова про области уезжают
// с кропами этих областей (freeform_derive.go), где они и осмысленны; повторённые здесь, они
// описывали бы кадр, на котором область — одна сотая площади.
func freeformItemCaption(it freeformItem) string {
	var parts []string
	switch it.Role {
	case entity.DesignFreeformRoleHardware:
		parts = append(parts, "the hardware")
	case entity.DesignFreeformRoleCloth:
		parts = append(parts, "the cloth")
	case entity.DesignFreeformRoleSubject:
		parts = append(parts, "the picture being worked on")
	case entity.DesignFreeformRoleModel:
		parts = append(parts, "the model — keep this person's identity")
	case entity.DesignFreeformRoleProduct:
		parts = append(parts, "the garment to put on them")
	case entity.DesignFreeformRoleScene:
		parts = append(parts, "the scene")
	case entity.DesignFreeformRoleLogo:
		parts = append(parts, "the logo (PNG, keep exact)")
	}
	if whole := freeformWholePictureText(it); whole != "" {
		parts = append(parts, oneLine(whole))
	}
	if len(it.Regions) > 0 {
		parts = append(parts, freeformMarkedNote(len(it.Regions)))
	}
	if len(parts) == 0 {
		return "reference image"
	}
	return strings.Join(parts, " — ")
}

// freeformWholePictureText — хвостовая подпись картинки: та, которой не досталось области.
func freeformWholePictureText(it freeformItem) string {
	if len(it.Texts) > len(it.Regions) {
		return strings.TrimSpace(it.Texts[len(it.Regions)])
	}
	return ""
}

// freeformMarkedNote — «на этой картинке отмечены места», числом и буквами.
//
// ⚠ СКАЗАНО У САМОЙ КАРТИНКИ, А НЕ ТОЛЬКО У ЕЁ КОПИИ. Копия с контурами стоит в списке ПОЗЖЕ, и
// модель, читающая подписи по порядку, узнала бы про разметку только дойдя до неё; строка здесь
// связывает оригинал с его копией по номеру заранее.
func freeformMarkedNote(n int) string {
	if n == 1 {
		return "area A is marked on it; the marked copy and a close crop follow"
	}
	letters := make([]string, 0, n)
	for i := 0; i < n; i++ {
		letters = append(letters, freeformAreaLetter(i))
	}
	return "areas " + strings.Join(letters, ", ") + " are marked on it; the marked copy and close " +
		"crops follow"
}
