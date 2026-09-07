package designgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/shopspring/decimal"
)

// providerNameFalCutout is what lands in design_run_attempt.provider. It names the ROUTE and not
// the vendor, which is why it is not simply «fal»: two different routes of this band now spend
// money at the same provider under two different slugs and two different tariffs, and a history row
// that called them both `fal` would make a bill impossible to reconcile against the runs that
// produced it.
const providerNameFalCutout = "fal_cutout"

// CodeCutoutNoAlpha — ВЫРЕЗ КУПЛЕН И НИЧЕГО НЕ ВЫРЕЗАЛ.
//
// ⚠ ЭТО КОД ДОСТАВЛЕННОЙ ПОПЫТКИ, А НЕ ПРОВАЛЕННОЙ, ровно как CodePatternNotSeamless. Картинка
// получена и оплачена, её кладут в карточку, прогон закрывается `done` — а строка попытки говорит,
// чем именно результат может не быть тем, что просили.
const CodeCutoutNoAlpha = "cutout_no_alpha"

// errCutoutNoAlpha is raised when the picture a background removal delivered has no transparency in
// it at all — every pixel fully opaque, or a format that cannot carry an alpha channel.
//
// IT IS RETURNED BESIDE THE ARTIFACT, NEVER INSTEAD OF IT, and that is the whole shape of this
// failure: the money is spent and the picture may still be perfectly useful to look at, so the
// store files it and the attempt row carries the complaint. Same seam as errPatternNotSeamless.
//
// ⚠ ЧТО ЕЩЁ НЕ СДЕЛАНО, И ЭТО СТОИТ ДЕНЕГ: В classify.go НЕТ ВЕТКИ ДЛЯ ЭТОГО СЕНТИНЕЛА. Пока её
// нет, ошибка проваливается в retryable-умолчание классификатора — прогон будет ПОКУПАТЬ ТОТ ЖЕ
// ОТВЕТ СНОВА до потолка платных попыток, а строка истории скажет `provider_unavailable`, отправив
// человека смотреть статус-страницу провайдера, у которого всё в порядке. Ветка, которую надо
// добавить рядом с errPatternNotSeamless, дословно:
//
//	case errors.Is(err, errCutoutNoAlpha):
//	    return verdict{Retryable: false, Code: CodeCutoutNoAlpha, State: entity.DesignAttemptDelivered}
//
// Она обязана приехать ВМЕСТЕ с проводкой маршрута (Providers.Cutout + forKind), не позже: до
// проводки этот код недостижим, после — достижим на каждом отказавшем вырезе.
var errCutoutNoAlpha = errors.New("designgen: the cut-out came back with nothing cut out")

// falCutoutProvider is the background-removal route (задача 3 — «прозрачные картинки»).
//
// ПОЧЕМУ ЭТО ОТДЕЛЬНЫЙ Provider, А НЕ ПЯТЫЙ РОД НА КАРТИНОЧНОМ МАРШРУТЕ. Флэт, рендер, перекрас и
// паттерн делят один маршрут потому, что делят ОДИН ПЛАТНЫЙ ЭНДПОИНТ и различаются только промптом.
// Вырез не различается промптом — он вообще не посылает слов; у него другой провайдер, другой слаг,
// другой тариф и другая форма ответа. Общий Provider означал бы `switch kind` внутри Execute, то
// есть ту же развилку, только спрятанную от предполётной проверки.
//
// ⚠ И ОН СИНХРОННЫЙ, БЕЗ Collector. Матирование считается секунды и укладывается в один проход
// воркера; пара Submit/Collect существует ради сборок, идущих минуты. Цена выбора названа в
// fal/cutout.go и повторена здесь, потому что читают её отсюда: у выреза НЕТ бесплатного
// возобновления — повтор после успешного сабмита это второй платёж, и поэтому классификация отказов
// этого маршрута (см. errCutoutNoAlpha выше) не косметика.
type falCutoutProvider struct{ c *fal.Client }

// NewFalCutoutProvider wires the background-removal route. A nil client is a disabled route, not a
// panic.
func NewFalCutoutProvider(c *fal.Client) Provider { return falCutoutProvider{c: c} }

func (p falCutoutProvider) Name() string { return providerNameFalCutout }

func (p falCutoutProvider) Enabled() bool { return p.c != nil && p.c.Enabled() }

// MissingCredential is the sentence the DOOR shows when the route is off — see CredentialNamer. It
// is the same wording fal.ErrNotConfigured carries: a person who reads one and then the other must
// not have to work out that they are the same fact.
//
// ⚠ И ЭТО ТОТ ЖЕ КЛЮЧ, ЧТО У 3D, ХОТЯ МАРШРУТ ДРУГОЙ. Здесь нет второго секрета: FAL_KEY открывает
// оба слага, и назвать в отказе что-нибудь вроде «FAL_MODEL_CUTOUT» значило бы послать владельца
// вписывать модель туда, где не хватает ключа.
func (p falCutoutProvider) MissingCredential() string { return "FAL_KEY is not set" }

// Produces names what this route ASKS FOR and what the pre-flight must therefore be able to store:
// a PNG, because PNG is the only format in this band that carries an alpha channel, and the alpha
// is the entire product.
//
// ⚠ ЭТО НЕ ОБЕЩАНИЕ ПРОВАЙДЕРА. Он вправе прислать JPEG, и тогда прозрачности нет по построению —
// именно этот случай ловит проверка результата ниже, и картинка всё равно будет сохранена под своим
// НАСТОЯЩИМ типом. Перечислять здесь ещё и JPEG было бы хуже: предполётная проверка стала бы
// отказывать всему маршруту на хранилище, которое не умеет тип, который мы не заказывали.
func (p falCutoutProvider) Produces() []string { return []string{ContentTypePNG} }

// SentPrompt is what this route puts in front of the provider: NOTHING — see PromptCarrier.
//
// ⚠ БЕЗ ЭТОГО МЕТОДА design_run.prompt ПОКАЗАЛ БЫ СОСТАВЛЕННЫЙ АБЗАЦ, КОТОРОГО ПРОВАЙДЕР НЕ ВИДЕЛ.
// Матирующая модель принимает одну ссылку на картинку и не имеет текстового поля вовсе; строка
// истории, приписывающая слова уже потраченным деньгам, — не пустая колонка, а ложное свидетельство.
// Ровно этот дефект был измерен на 3D-маршруте.
func (p falCutoutProvider) SentPrompt(Job) string { return "" }

// Execute cuts the background out of the run's single picture, synchronously.
func (p falCutoutProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	src, err := cutoutSource(job)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	res, err := p.c.RemoveBackground(ctx, src, &buf)
	if err != nil {
		// «ОПЛАЧЕНО, И НИЧЕГО ИЗ ЭТОГО НЕ ВЫШЛО» ИМЕЕТ ЗДЕСЬ НОСИТЕЛЯ, как на 3D и векторе:
		// транспорт вешает на упавший вызов то, что он списал, когда знал. Без этого деньги
		// терминального отказа исчезают — попытка закрывается с NULL-ценой, дневная книга не видит
		// траты, и никто не может сказать, во что обошлись провалы.
		if out := chargedCutoutOutcome(p.c, err); out != nil {
			return out, err
		}
		return nil, err
	}

	out := &Outcome{RequestID: res.RequestID, Model: res.Model}
	if usd := p.c.CostCutoutUSD(res.BillableUnits); usd.IsPositive() {
		out.Price = decimal.NullDecimal{Decimal: usd, Valid: true}
	}
	if res.UnitsAssumed {
		// ⚠ SAID OUT LOUD, EVERY TIME — same rule as the 3D route. The ledger gets a number either
		// way, because a paid cut-out recorded as free is the worse lie; but «the provider named
		// this» and «we assumed one unit» are different claims, and the second must never harden
		// into the first by being invisible.
		slog.Default().WarnContext(ctx, "cutout: fal reported no billable units; the attempt's price "+
			"is this deployment's own per-request estimate, not the provider's charge",
			slog.Int("run_id", job.RunID), slog.String("request_id", res.RequestID),
			slog.String("price_usd", out.Price.Decimal.String()), slog.String("knob", "FAL_UNIT_USD_CUTOUT"))
	}

	raw := buf.Bytes()
	out.Artifacts = append(out.Artifacts, Artifact{
		Bytes: raw,
		// ⚠ ТИП БЕРЁТСЯ ИЗ БАЙТОВ, А НЕ ИЗ ЗАКАЗА И НЕ ИЗ ЯРЛЫКА ПРОВАЙДЕРА. Бакет всё равно
		// определяет формат по сигнатуре, так что честный ярлык не стоит ничего, а нечестный
		// оставил бы в строке выдачи слово, которому противоречит сам файл.
		ContentType: cutoutContentType(raw),
		// Kind is left EMPTY on purpose: empty means «the kind derived from the run kind», which is
		// exactly right here — a cut-out run produces cut-out pictures and nothing else. The 3D
		// route sets it explicitly only because its two artifacts are of different kinds.
	})

	// ─── DID ANYTHING ACTUALLY GET CUT OUT. The measurement, and the reason it may not fail the
	// run, are in cutoutAlphaVerdict. Here it does one thing: it returns the artifact TOGETHER WITH
	// the complaint — the shape settle() already implements, the same one errPatternNotSeamless uses.
	if v := cutoutAlphaVerdict(raw); !v.hasAlpha {
		// THE FIGURES TRAVEL WITH THE COMPLAINT. «There is no transparency» with nothing beside it
		// is an accusation nobody can check against the picture they are looking at.
		return out, fmt.Errorf("%w: %s; the picture was kept", errCutoutNoAlpha, v.why)
	}
	return out, nil
}

// cutoutSource is the ONE picture this run cuts.
//
// ⚠ РОВНО ОДНА, И ОТКАЗ ЗДЕСЬ БЕСПЛАТНЫЙ. Матирование — операция над КОНКРЕТНЫМ кадром, а не
// композиция из нескольких: получив список, маршрут может либо взять первый (то есть решить за
// человека, какую из его картинок он оплатил), либо порезать все (то есть купить N вырезов там, где
// прогон был оценён в один). Обе развилки — тихие, обе про деньги, и обе исчезают, если список
// не той длины отказывается ДО вызова.
func cutoutSource(job Job) (string, error) {
	switch len(job.References) {
	case 1:
		return job.References[0], nil
	case 0:
		return "", fmt.Errorf("%w: a cut-out needs the picture it cuts — add one picture to this run",
			orimages.ErrBadRequest)
	default:
		return "", fmt.Errorf("%w: a cut-out is one picture, and this run carries %d — narrow it to the "+
			"single picture whose background should go", orimages.ErrBadRequest, len(job.References))
	}
}

// alphaVerdict is what one look at the delivered bytes found. It carries the SENTENCE as well as
// the verdict because the complaint has to name what was measured — «no transparency» reads very
// differently from «the provider sent a JPEG», and only one of them is a configuration problem.
type alphaVerdict struct {
	hasAlpha bool
	why      string
}

// cutoutAlphaVerdict answers the only question that makes this route worth its money: is there
// transparency in what came back?
//
// ⚠ ЭТО ИЗМЕРЕНИЕ ПИКСЕЛЕЙ, А НЕ ЧТЕНИЕ ЯРЛЫКА, И РАЗНИЦА ЗДЕСЬ ВСЯ. `content_type: image/png` над
// картинкой с белым фоном — совершенно законный PNG, и именно его возвращает любая модель, которая
// «убрала фон» РИСОВАНИЕМ, а не матированием (RESEARCH §Поправки п.3). Проверка типа пропустила бы
// такой ответ молча, а человек увидел бы белый квадрат вместо выреза и решил бы, что сломан показ.
//
// ЧТО ОНА НЕ ДЕЛАЕТ: не судит о КАЧЕСТВЕ выреза. Ореол, съеденный рукав, обрезанная тень — это глаз
// человека, и никакая арифметика по каналу их не назовёт. Она ловит ровно тот отказ, который виден
// в момент покупки и который иначе обнаружился бы через две недели: прозрачности нет вовсе.
func cutoutAlphaVerdict(raw []byte) alphaVerdict {
	if len(raw) == 0 {
		return alphaVerdict{why: "the provider delivered no bytes"}
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		// A FORMAT THAT CANNOT CARRY ALPHA IS THE SAME COMPLAINT, and it is the likeliest one: a
		// JPEG answer to a background removal is a picture with the background painted in, and the
		// run asked for `output_format: png` explicitly.
		return alphaVerdict{why: fmt.Sprintf(
			"the answer is not a readable PNG (%s, %d bytes), so it carries no alpha channel at all",
			cutoutContentType(raw), len(raw))}
	}
	b := img.Bounds()
	total := b.Dx() * b.Dy()
	if hasTransparentPixel(img) {
		return alphaVerdict{hasAlpha: true}
	}
	return alphaVerdict{why: fmt.Sprintf(
		"every one of the %d pixels of the %d×%d picture is fully opaque", total, b.Dx(), b.Dy())}
}

// hasTransparentPixel reports whether ANY pixel is less than fully opaque.
//
// IT STOPS AT THE FIRST ONE IT FINDS, which is the whole point of asking the question this way
// round: a picture that HAS alpha — the ordinary, successful case — is usually answered by its first
// corner, while only a failure is walked to the end. A count would have to walk every time and would
// then be reported for a case in which it is always zero.
func hasTransparentPixel(img image.Image) bool {
	b := img.Bounds()
	// The fast path is the type png.Decode returns for a truecolour-with-alpha PNG, which is what
	// this route is asking for: reading the stride directly avoids an interface call and a colour
	// conversion per pixel on pictures that can be tens of megapixels.
	if m, ok := img.(*image.NRGBA); ok {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := m.Pix[m.PixOffset(b.Min.X, y):m.PixOffset(b.Max.X, y)]
			for i := 3; i < len(row); i += 4 {
				if row[i] < 0xff {
					return true
				}
			}
		}
		return false
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a < 0xffff {
				return true
			}
		}
	}
	return false
}

// cutoutContentType names a payload by its MAGIC BYTES.
//
// The bucket sniffs the same way and would store the file correctly whatever label it were handed,
// so this exists for the two places a label is read by a person rather than by a decoder: the
// picture row of the run's output, and the sentence of a complaint about a format. An unrecognised
// payload is called a PNG — the type this route asked for — so that it goes through the picture door
// and is refused there BY NAME, rather than being refused here as «unknown» with the bytes thrown
// away.
func cutoutContentType(raw []byte) string {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return ContentTypePNG
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return ContentTypeJPEG
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return ContentTypeWEBP
	default:
		return ContentTypePNG
	}
}

// chargedCutoutOutcome turns a billed failure into the Outcome that carries its money, or nil when
// the provider never said what the call cost.
//
// ⚠ ok = false ЭТО НЕ НОЛЬ. «Никто не смог назвать цену» и «вызов был бесплатным» — два разных
// утверждения об одном прогоне, и NULL — то слово схемы, которое значит первое. Поэтому неоценённый
// провал возвращает nil, а не Outcome с нулём.
func chargedCutoutOutcome(c *fal.Client, err error) *Outcome {
	var ce *fal.ChargedError
	if !errors.As(err, &ce) {
		return nil
	}
	usd := c.CostCutoutUSD(ce.Units)
	if !usd.IsPositive() {
		return nil
	}
	return &Outcome{
		// ⚠ ИДЕНТИФИКАТОР ЗАПРОСА ЕДЕТ ИМЕННО ОТСЮДА, И БОЛЬШЕ ЕМУ ЕХАТЬ НЕОТКУДА. Маршрут
		// синхронный: снаружи вызова id не существует, а строке попытки он нужен ровно в том
		// случае, где деньги ушли, — иначе списание в счёте fal не с чем сопоставить.
		RequestID: ce.RequestID,
		Model:     ce.Model,
		Price:     decimal.NullDecimal{Decimal: usd, Valid: true},
	}
}
