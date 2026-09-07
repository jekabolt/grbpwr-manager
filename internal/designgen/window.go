package designgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	xdraw "golang.org/x/image/draw"
)

// ═══════════════ ОКНО ГЕНЕРАЦИИ: КРОП ТУДА, КОМПОЗИТ ОБРАТНО ═══════════════
//
// ЗАЧЕМ ЭТО ВООБЩЕ. Пуговица занимает один-три процента кадра. Модель, которой отдали ВЕСЬ кадр и
// попросили «пришей сюда пуговицу», ПЕРЕСОЗДАЁТ ВЕСЬ КАДР целиком — маска у этого семейства мягкая,
// «остального не трону» она не обещает и не исполняет. То есть ради пуговицы человек получает
// другую фотографию: другое лицо, другие складки, другой свет, и найти на ней свою вещь он уже не
// может. Приём (Ideogram называет его generation window, RESEARCH §Поправки п.5) состоит из трёх
// шагов: КРОП вокруг области с полями → генерация ПО КРОПУ → ВКЛЕЙКА ответа обратно по тем же
// координатам. Первые два шага у нас уже были; без третьего они бесполезны, потому что наружу
// выходил бы кроп вместо кадра.
//
// ⚠ КООРДИНАТЫ ЗАМОРОЖЕНЫ ДО ПЛАТНОГО ВЫЗОВА, И ЭТО ГЛАВНОЕ СВОЙСТВО ЭТОГО ФАЙЛА. Прямоугольник
// вычисляется ОДИН раз при сборке задания (freeformCropRect) и едет вместе с ним; вклейка считает
// его не заново, а читает. Пересчёт «по сегодняшним params» дал бы кадр, склеенный по одним
// координатам и вырезанный по другим, в тот день, когда кто-нибудь поправит арифметику полей.

// CodeWindowNotComposited — ОТВЕТ КУПЛЕН, А ВКЛЕИТЬ ЕГО ОБРАТНО НЕ ВЫШЛО.
//
// ⚠ ЭТО КОД ДОСТАВЛЕННОЙ ПОПЫТКИ, как pattern_not_seamless и cutout_no_alpha: наружу уходит то, что
// пришло от модели, — кроп области. Он полезен (на нём видно, как села фурнитура) и он оплачен, а
// провалить прогон значило бы выбросить купленное. Но кроп — НЕ ТО, что просили, и человек, увидев
// в ленте карточки крупный кусок вместо кадра, обязан найти в строке попытки, почему.
const CodeWindowNotComposited = "window_not_composited"

// errWindowNotComposited is raised beside the artifact, never instead of it.
var errWindowNotComposited = errors.New("designgen: the generated window could not be fitted back into its frame")

// GenerationWindow is what a windowed run froze about itself BEFORE it paid: which picture the
// answer belongs to, and exactly where in it.
type GenerationWindow struct {
	// SourceURL is the stored url of the ORIGINAL full frame. The bytes are read again after the
	// paid call rather than carried across it: a 40-megapixel source held in memory for the whole
	// length of a provider call is the same half-gigabyte the byte ceilings exist to protect.
	SourceURL string
	// Rect is the crop rectangle IN SOURCE PIXELS — the very rectangle that was cut out and sent.
	Rect image.Rectangle
	// Bounds is the source's own rectangle, frozen for one purpose: to REFUSE rather than paste if
	// the picture read back is not the picture that was cropped. A media row is immutable, so this
	// can only disagree through a bug — and a bug that pastes into the wrong coordinates produces a
	// plausible-looking picture with the hardware in the wrong place.
	Bounds image.Rectangle
	// KeepAlpha says the source carried real transparency, so the composite must be a PNG. A cut-out
	// re-encoded as JPEG comes back with a white rectangle where its transparency was.
	KeepAlpha bool
}

// compositeWindowJPEGQuality — качество JPEG композита. Выше, чем у производных (90): производная
// живёт один вызов и её никто не хранит, а это КАДР, который ляжет в ленту карточки и с которого
// будут делать следующие прогоны.
const compositeWindowJPEGQuality = 92

// postProcess fits a windowed answer back into its own frame.
//
// ⚠ ОНО СТОИТ ПОСЛЕ ПЛАТНОГО ВЫЗОВА И ДО publish, И ДРУГОГО МЕСТА У НЕГО НЕТ. Раньше — нечего
// вклеивать; позже — кроп уже заминчен строкой медиа и опубликован в ленте, то есть человек увидел
// не то, что просил, а мы получили сироту, которую надо подметать.
//
// ВОЗВРАЩАЕТ ЖАЛОБУ, А НЕ ОТКАЗ. Всё, что здесь может пойти не так, происходит ПОСЛЕ того, как
// деньги ушли, и ни одна из этих причин не делает купленную картинку бесполезной.
func (w *Worker) postProcess(ctx context.Context, job Job, out *Outcome) error {
	if job.Window == nil || out == nil || len(out.Artifacts) == 0 {
		return nil
	}
	if w.objects == nil {
		return fmt.Errorf("%w: this worker has no object store to read the original frame from",
			errWindowNotComposited)
	}
	src, err := freeformFetchImage(ctx, w.objects, job.Window.SourceURL)
	if err != nil {
		return fmt.Errorf("%w: the original frame could not be read back: %v", errWindowNotComposited, err)
	}
	fitted, err := compositeWindow(src, *job.Window, out.Artifacts[0].Bytes)
	if err != nil {
		return fmt.Errorf("%w: %v", errWindowNotComposited, err)
	}
	// ⚠ ЗАМЕНА, А НЕ ДОБАВЛЕНИЕ. Оригинал кропа наружу не идёт: два кадра на прогон, проданный как
	// один, — это ровно то, что запрещает narrowToOneOutput, и человеку тут показывать нечего —
	// кроп есть часть композита.
	out.Artifacts[0] = fitted
	return nil
}

// compositeWindow draws the model's answer back into a copy of the original frame.
func compositeWindow(src image.Image, win GenerationWindow, answer []byte) (Artifact, error) {
	if src.Bounds() != win.Bounds {
		// ⚠ НЕ «ПОДГОНИМ МАСШТАБОМ». Замороженный прямоугольник назван в пикселях ЭТОГО кадра;
		// кадр другого размера значит, что читается другая картинка, и вклейка положила бы
		// фурнитуру в правдоподобное, но чужое место.
		return Artifact{}, fmt.Errorf("the frame read back is %v, and the window was frozen against %v",
			src.Bounds(), win.Bounds)
	}
	rect := win.Rect.Intersect(src.Bounds())
	if rect.Empty() {
		return Artifact{}, fmt.Errorf("the window %v lies outside the frame %v", win.Rect, src.Bounds())
	}
	got, err := freeformDecode(answer)
	if err != nil {
		return Artifact{}, fmt.Errorf("the answer is not a readable picture: %w", err)
	}

	// КОПИЯ ИСХОДНИКА, А НЕ ЕГО ЖЕ БУФЕР: src может быть общим с чем угодно выше по стеку, а
	// рисование по чужой памяти — это дефект, который проявляется где-то ещё.
	dst := image.NewNRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	// Src, А НЕ Over: ответ ЗАМЕЩАЕТ окно, а не ложится поверх него полупрозрачно. Смешение с тем,
	// что было под ним, дало бы призрак старой пуговицы под новой.
	xdraw.CatmullRom.Scale(dst, rect, got, got.Bounds(), xdraw.Src, nil)

	var buf bytes.Buffer
	if win.KeepAlpha {
		if err := png.Encode(&buf, dst); err != nil {
			return Artifact{}, err
		}
		return Artifact{Bytes: buf.Bytes(), ContentType: ContentTypePNG}, nil
	}
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: compositeWindowJPEGQuality}); err != nil {
		return Artifact{}, err
	}
	return Artifact{Bytes: buf.Bytes(), ContentType: ContentTypeJPEG}, nil
}

// freeformWindowPlan — БЕРЁТ ЛИ ЭТОТ ПРОГОН ОКНО ГЕНЕРАЦИИ, и если да, то какое.
//
// ТРИ УСЛОВИЯ, И КАЖДОЕ ИЗ НИХ — ОТДЕЛЬНАЯ ПРИЧИНА:
//
//   - ПРЕСЕТ `add_hardware`. Только у него просьба локальна по построению: предмет сажают В
//     КОНКРЕТНОЕ МЕСТО. «Перекрась обведённое» действует на область, которая бывает половиной вещи,
//     а `free` — это чужие слова, и что они значат про кадр, мы не знаем.
//   - РОВНО ОДНА ОБЛАСТЬ НА ВЕСЬ ПРОГОН. Два окна — это два платных вызова и два композита, то
//     есть другая арифметика денег у двери; одна просьба про две области честнее уезжает целым
//     кадром.
//   - ОБЛАСТЬ НА КАРТИНКЕ-ПРЕДМЕТЕ. Область, размеченная на самой фурнитуре, говорит «вот эта
//     часть пряжки», а не «вот сюда»; окно по ней вклеило бы ответ в фотографию фурнитуры.
func freeformWindowPlan(p runParams) *freeformWindow {
	ff := p.Freeform
	if ff == nil || ff.Preset != entity.DesignFreeformPresetAddHardware {
		return nil
	}
	var plan *freeformWindow
	for _, it := range ff.Items {
		if len(it.Regions) == 0 {
			continue
		}
		if plan != nil || len(it.Regions) != 1 || it.MediaID <= 0 {
			return nil
		}
		if it.Role == entity.DesignFreeformRoleHardware || it.Role == entity.DesignFreeformRoleCloth {
			return nil
		}
		plan = &freeformWindow{MediaID: it.MediaID, Region: it.Regions[0], Text: freeformTextForRegion(it, 0)}
	}
	return plan
}

// freeformWindow is the plan itself: which picture, which area, and the words said about it.
type freeformWindow struct {
	MediaID int
	Region  freeformRegion
	Text    string
}

// deriveFreeformWindow ЗАМЕНЯЕТ ПОЛНЫЙ КАДР ПРЕДМЕТА ЕГО КРОПОМ и морозит координаты вклейки.
//
// ⚠ ИМЕННО ЗАМЕНЯЕТ. Отправить и кадр, и кроп значило бы отдать модели ровно ту картинку, ради
// невидения которой окно и заведено: получив полный кадр, она отвечает полным кадром, и вклеивать
// обратно станет нечего — а платить придётся за пересозданную фотографию.
//
// ОТКАЗ ЗДЕСЬ ХОРОНИТ ПРОГОН БЕСПЛАТНО, как и остальная сборка задания: buildJob зовётся до
// StartAttempt. Молчаливая деградация («не вышло окно — пошлём кадром») стоила бы полной цены и
// дала бы в ленте пересозданную фотографию, неотличимую в истории от честной.
func deriveFreeformWindow(ctx context.Context, objects objectFetcher, plan freeformWindow,
	job *Job, attached []refCaption) ([]refCaption, *GenerationWindow, error) {
	at := -1
	for i, rc := range attached {
		if rc.MediaID == plan.MediaID {
			at = i
			break
		}
	}
	if at < 0 || at >= len(job.References) {
		// Картинка не пережила резолв медиа: строку удалили между снимком и проходом. Окна нет,
		// и молчание здесь честно — обычная сборка тоже её не увидит и ремесло скажет словами.
		return attached, nil, nil
	}
	if objects == nil {
		return nil, nil, fmt.Errorf("designgen: a windowed freeform run needs the picture it crops, " +
			"and this worker has no object store to read it from")
	}
	srcURL := job.References[at]
	src, err := freeformFetchImage(ctx, objects, srcURL)
	if err != nil {
		return nil, nil, fmt.Errorf("designgen: cannot read the picture a generation window crops: %w", err)
	}
	keepAlpha := freeformSourceHasAlpha(src)
	rect := freeformCropRect(src.Bounds(), plan.Region)
	if rect.Empty() {
		return nil, nil, fmt.Errorf("designgen: the marked area lies outside its picture")
	}
	crop, err := freeformWithinBudget("the generation window", func(side int) (string, error) {
		return freeformCrop(src, plan.Region, keepAlpha, side)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("designgen: cannot crop the generation window: %w", err)
	}

	// ─── ТРИ ПАРАЛЛЕЛЬНЫХ СПИСКА РЕЖУТСЯ ОДНИМ ДВИЖЕНИЕМ. Ссылка, вид и подпись под номером k
	// описывают одну картинку; удалить кадр из одного списка и забыть про другой значит сдвинуть
	// все следующие подписи на единицу — то есть указать модели на соседнюю картинку.
	job.References = append(job.References[:at], job.References[at+1:]...)
	if at < len(job.ReferenceViews) {
		job.ReferenceViews = append(job.ReferenceViews[:at], job.ReferenceViews[at+1:]...)
	}
	attached = append(attached[:at], attached[at+1:]...)

	job.References = append(job.References, crop)
	job.ReferenceViews = append(job.ReferenceViews, "")
	attached = append(attached, refCaption{Caption: freeformWindowCaption(plan.Text), IsWindow: true})

	return attached, &GenerationWindow{
		SourceURL: srcURL,
		Rect:      rect,
		Bounds:    src.Bounds(),
		KeepAlpha: keepAlpha,
	}, nil
}

// freeformWindowCaption — подпись окна. Она обязана сказать, что это КРОП, а не отдельная вещь:
// без этого модель читает крупный кусок ткани с петлёй как самостоятельный предмет и отвечает
// натюрмортом.
func freeformWindowCaption(text string) string {
	c := "a close crop of the garment, around the place where the hardware goes"
	if text != "" {
		c += " — «" + oneLine(text) + "»"
	}
	return c
}
