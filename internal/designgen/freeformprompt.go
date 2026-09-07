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
// ⚠ И ВСЕ ТРИ ГОВОРЯТ ОДНО И ТО ЖЕ ПРО КОНТУР. Контур нарисован НА КАРТИНКЕ, значит по умолчанию
// он часть картинки; без явной строки модель его перерисовывает на выход. Строка общая и стоит в
// каждом абзаце, а не «где нужнее»: пресет выбирает человек, и абзац, в котором её забыли,
// стоил бы кадра с красными линиями поперёк вещи.

// freeformOutlineDisclaimer — общая строка про контур. Одна на три ремесла, потому что правда
// одна; две копии разошлись бы при первой правке.
const freeformOutlineDisclaimer = "an outline is a hint about WHERE, not a mask: do not draw it, " +
	"and do not leave any trace of it in the picture you return"

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
	default:
		return freeformFreeCraft()
	}
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
		match := it.Role == role
		if role == "" {
			match = it.Role != entity.DesignFreeformRoleHardware && it.Role != entity.DesignFreeformRoleCloth
		}
		if !match {
			continue
		}
		if n, ok := numberOf[it.MediaID]; ok {
			return n
		}
	}
	return 0
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
