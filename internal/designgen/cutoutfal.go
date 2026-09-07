package designgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
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
// ⚠ И У НЕГО ЕСТЬ СВОЯ ВЕТКА В classify.go, БЕЗ КОТОРОЙ ОН СТОИЛ БЫ ДЕНЕГ. Сентинел без ветки
// проваливается в retryable-умолчание классификатора: прогон ПОКУПАЛ БЫ ТОТ ЖЕ ОТВЕТ СНОВА до
// потолка платных попыток, а строка истории говорила бы `provider_unavailable` — и отправляла
// человека смотреть статус-страницу провайдера, у которого всё в порядке. Ветка стоит рядом с
// errPatternNotSeamless, потому что это тот же шов: картинка куплена, сохранена и показана, а
// жалоба едет в строке попытки.
var errCutoutNoAlpha = errors.New("designgen: the cut-out came back with nothing cut out")

// CodeCutoutTooLarge — ВЫРЕЗ КУПЛЕН, А ИЗМЕРИТЬ ЕГО НЕЛЬЗЯ: заголовок объявляет больше пикселей,
// чем этот процесс разворачивает.
//
// ⚠ ЭТО ТОЖЕ КОД ДОСТАВЛЕННОЙ ПОПЫТКИ, и по тому же доводу, что у cutout_no_alpha: байты пришли,
// они оплачены, и решение «не смотреть» принято НАМИ. Отдельное слово вместо cutout_no_alpha нужно
// потому, что это ДРУГОЕ утверждение: там прозрачности не нашли, здесь её не искали вовсе — и
// человек, читающий строку, обязан различать «модель вернула непрозрачный кадр» и «кадр слишком
// велик, чтобы его читать».
const CodeCutoutTooLarge = "cutout_too_large"

// errCutoutTooLarge is raised BESIDE the artifact, never instead of it — same seam as
// errCutoutNoAlpha.
//
// ⚠ ЧТО ОН ЗАКРЫВАЕТ, И ЭТО БЫЛА БОМБА ПОСЛЕ ОПЛАТЫ. Проверка альфы декодировала байты ПОСТАВЩИКА
// без единого потолка: транспорт пускает файл до 25 MiB (fal.maxCutoutBytes), а 25 MiB сжатого PNG
// объявляют канву в гигапиксели — и `png.Decode` честно просил бы под неё десятки гигабайт в
// процессе, у которого пол-гигабайта на всё. Умирал бы при этом не только этот прогон, а весь
// воркер, унося чужие оплаченные прогоны из соседних горутин. Потолок читается из ЗАГОЛОВКА
// (png.DecodeConfig), то есть из первых двух десятков байт, и он тот же, которым бакет меряет
// всякую хранимую картинку (bucket.ImageWithinBudget): картинка, которую негде хранить, не должна
// быть и развёрнута.
var errCutoutTooLarge = errors.New("designgen: the cut-out is too large to read")

// falCutoutProvider is the background-removal route (задача 3 — «прозрачные картинки»).
//
// ПОЧЕМУ ЭТО ОТДЕЛЬНЫЙ Provider, А НЕ ПЯТЫЙ РОД НА КАРТИНОЧНОМ МАРШРУТЕ. Флэт, рендер, перекрас и
// паттерн делят один маршрут потому, что делят ОДИН ПЛАТНЫЙ ЭНДПОИНТ и различаются только промптом.
// Вырез не различается промптом — он вообще не посылает слов; у него другой провайдер, другой слаг,
// другой тариф и другая форма ответа. Общий Provider означал бы `switch kind` внутри Execute, то
// есть ту же развилку, только спрятанную от предполётной проверки.
//
// ⚠ И ОН ДВУХПОЛОВИНЧАТЫЙ — Provider + Collector, — ХОТЯ МАТИРОВАНИЕ СЧИТАЕТСЯ СЕКУНДЫ. Пара
// Submit/Collect существует не ради ДЛИТЕЛЬНОСТИ, а ради ТОЧКИ ОПЛАТЫ: сабмит — платёж, сбор —
// бесплатный просмотр, и воркер, умерший между ними, обязан возобновить, а не купить второй раз.
// Синхронная форма, стоявшая здесь раньше, честно называла этот долг вслух: «у выреза нет
// бесплатного возобновления, повтор после успешного сабмита это второй платёж». Секунды сборки
// уменьшают вероятность разрыва и ничего не меняют в его цене — а разрыв бывает не только от
// падения: редеплой в середине прохода это то же самое, и он случается по расписанию.
//
// ЦЕНА РАЗДЕЛЕНИЯ — ОДНА ЛИШНЯЯ СТРОКА ПОПЫТКИ НА ПРОГОН (`accepted` с request_id), и она же
// приносит то, ради чего всё: попытка после `accepted` не тратит круг платного потолка
// (designPaidAttemptsSQL), а повторный сбор того же задания не удваивает списание (заряд ключом на
// provider_request_id).
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

// Execute SUBMITS the run's single picture for matting and returns at once with the request id.
//
// ЭТО ПОЛОВИНА, НА КОТОРОЙ УХОДЯТ ДЕНЬГИ. Всё, что она может сделать полезного после сабмита, — это
// назвать id, потому что именно он превращает следующий проход из второй покупки в бесплатный
// просмотр. Цены здесь нет и быть не может: fal сообщает списание на ЗАБОРЕ РЕЗУЛЬТАТА, а ноль в
// колонке сказал бы, что вырез был бесплатным.
func (p falCutoutProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	src, err := cutoutSource(job)
	if err != nil {
		return nil, err
	}
	id, err := p.c.SubmitCutout(ctx, src)
	if err != nil {
		// ⚠ И ЗДЕСЬ ТОЖЕ БЫВАЮТ ДЕНЬГИ. Сабмит, принятый и не назвавший id, — оплачен: транспорт
		// вешает на такой отказ то, что он списал, когда знал, и без этого носителя трата исчезает.
		if out := chargedCutoutOutcome(p.c, err); out != nil {
			return out, err
		}
		return nil, err
	}
	return &Outcome{RequestID: id, Model: p.c.ModelCutout(), Pending: true}, nil
}

// Collect is the FREE half: the wait, the download and the one question this route exists to
// answer — did anything actually get cut out.
//
// ⚠ ПРОВЕРКА АЛЬФЫ ЖИВЁТ ИМЕННО ЗДЕСЬ, А НЕ В Execute, И ЭТО НЕ ПЕРЕЕЗД РАДИ ПЕРЕЕЗДА: судить о
// СОДЕРЖИМОМ можно только там, где содержимое есть. На сабмите его нет вовсе.
func (p falCutoutProvider) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}

	var buf bytes.Buffer
	res, err := p.c.CollectCutout(ctx, requestID, &buf)
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
	//
	// ДВЕ ЖАЛОБЫ, А НЕ ОДНА, И РАЗЛИЧИЕ НЕСУЩЕЕ: «прозрачности нет» — это ИЗМЕРЕНИЕ, «слишком
	// велик» — это ОТКАЗ МЕРИТЬ. Свернув их в одно слово, строка истории утверждала бы про кадр
	// то, чего никто не смотрел.
	switch v := cutoutAlphaVerdict(raw); {
	case v.tooLarge:
		return out, fmt.Errorf("%w: %s; the picture was kept", errCutoutTooLarge, v.why)
	case !v.hasAlpha:
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
	// tooLarge — ЗАГОЛОВОК ОБЪЯВИЛ БОЛЬШЕ, ЧЕМ ЭТОТ ПРОЦЕСС РАЗВОРАЧИВАЕТ, и растр не трогали
	// вовсе. Третье состояние, а не `hasAlpha=false`: «прозрачности нет» и «мы не смотрели» — два
	// разных утверждения об одном оплаченном кадре, и у них разные коды.
	tooLarge bool
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
// ⚠ ЗАГОЛОВОК ЧИТАЕТСЯ РАНЬШЕ РАСТРА, И ЭТО НЕ ПОРЯДОК СТРОК, А ГРАНИЦА ПАМЯТИ. Довод целиком — у
// errCutoutTooLarge: байты пришли от поставщика, их до 25 MiB, и сжатый PNG объявляет канву какого
// угодно размера. png.DecodeConfig стоит два десятка байт и отвечает на вопрос «стоит ли вообще
// звать декодер» ДО того, как декодер попросит гигабайты.
func cutoutAlphaVerdict(raw []byte) alphaVerdict {
	if len(raw) == 0 {
		return alphaVerdict{why: "the provider delivered no bytes"}
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		// A FORMAT THAT CANNOT CARRY ALPHA IS THE SAME COMPLAINT, and it is the likeliest one: a
		// JPEG answer to a background removal is a picture with the background painted in, and the
		// run asked for `output_format: png` explicitly.
		return alphaVerdict{why: fmt.Sprintf(
			"the answer is not a readable PNG (%s, %d bytes), so it carries no alpha channel at all",
			cutoutContentType(raw), len(raw))}
	}
	if !bucket.ImageWithinBudget(cfg.Width, cfg.Height) {
		side, pixels := bucket.ImageBudgetCeilings()
		return alphaVerdict{tooLarge: true, why: fmt.Sprintf(
			"its header declares %d×%d pixels — past the %d px side and the %d pixel ceiling every "+
				"stored picture is held to — so nothing was decoded and the alpha was never looked at",
			cfg.Width, cfg.Height, side, pixels)}
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		// A HEADER THAT READ AND A RASTER THAT DID NOT: a truncated or corrupt body. Same sentence
		// as the unreadable header above — from where a person stands, both are «this is not a PNG
		// we could look into».
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
		// ⚠ ИДЕНТИФИКАТОР ЗАПРОСА ЕДЕТ ИМЕННО ОТСЮДА. На упавшем сборе воркер подставит тот id, по
		// которому собирал, но упасть можно и на сабмите — а там id снаружи вызова не существует
		// вовсе, и списание в счёте fal иначе не с чем сопоставить.
		RequestID: ce.RequestID,
		Model:     ce.Model,
		Price:     decimal.NullDecimal{Decimal: usd, Valid: true},
	}
}
